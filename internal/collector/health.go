package collector

import (
	"bufio"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MetricsURL is the collector's own telemetry endpoint. otelcol exposes it
// on localhost:8888 with no configuration at all — verified against
// otelcol 0.135.0, whose startup fails with "binding address localhost:8888
// for Prometheus exporter" when the port is taken, for a config with no
// service::telemetry section — so compy's shipped configurations say nothing
// about it and this address is simply where the numbers are. The Collector
// screen names it on screen for the same reason.
const MetricsURL = "http://localhost:8888/metrics"

// healthTimeout bounds the scrape. It is on the path of a screen the user is
// looking at, so a collector that is wedged must not hold the page.
const healthTimeout = 2 * time.Second

// maxMetricsBytes caps the scrape. A collector with many exporters emits a
// few hundred KB; anything past this is not our /metrics page.
const maxMetricsBytes = 4 << 20

// Health is the four numbers the Collector screen shows: how much telemetry
// came in, how much went out, how much is waiting, and how much was lost.
// Available is false when nothing answered — a stopped collector is not an
// error, it is dashes on screen.
type Health struct {
	Available bool  `json:"available"`
	Received  int64 `json:"received"`
	Exported  int64 `json:"exported"`
	Queue     int64 `json:"queue"`
	Dropped   int64 `json:"dropped"`
}

// Scrape reads the running collector's own metrics. It never returns an
// error: every failure — nothing listening, a timeout, something else on the
// port — means the same thing to the caller, "no numbers".
func Scrape() Health { return health(MetricsURL) }

// health is Scrape against an arbitrary URL, so tests can serve a captured
// /metrics page.
//
// The metric names are otelcol's, verified against a running 0.135.0
// (`curl localhost:8888/metrics` with traces, metrics and logs flowing):
//
//	received  otelcol_receiver_accepted_{spans,metric_points,log_records}
//	exported  otelcol_exporter_sent_{spans,metric_points,log_records}
//	queue     otelcol_exporter_queue_size          (gauge, per exporter+signal)
//	dropped   otelcol_receiver_refused_*, otelcol_exporter_send_failed_*,
//	          otelcol_exporter_enqueue_failed_*
//
// Counters render with a "_total" suffix under an explicitly configured
// prometheus reader and without one under otelcol's default telemetry
// config, so the suffix is trimmed before matching. Every signal is summed
// into one number: the strip counts telemetry, not spans-versus-logs.
func health(url string) Health {
	client := &http.Client{Timeout: healthTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return Health{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{}
	}

	h := Health{Available: true}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxMetricsBytes))
	for scanner.Scan() {
		name, value, ok := parseSample(scanner.Text())
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(name, "otelcol_receiver_accepted_"):
			h.Received += value
		case strings.HasPrefix(name, "otelcol_exporter_sent_"):
			h.Exported += value
		case name == "otelcol_exporter_queue_size":
			h.Queue += value
		case strings.HasPrefix(name, "otelcol_receiver_refused_"),
			strings.HasPrefix(name, "otelcol_exporter_send_failed_"),
			strings.HasPrefix(name, "otelcol_exporter_enqueue_failed_"):
			h.Dropped += value
		}
	}
	return h
}

// parseSample pulls the metric name and value out of one Prometheus text
// line ("name{labels} value" or "name value", with an optional trailing
// timestamp), trimming a "_total" suffix. Comments, blank lines, and
// anything that does not parse as a finite number are skipped.
func parseSample(line string) (string, int64, bool) {
	if line == "" || line[0] == '#' {
		return "", 0, false
	}
	name, rest := line, ""
	if open := strings.IndexByte(line, '{'); open >= 0 {
		close := strings.LastIndexByte(line, '}')
		if close < open {
			return "", 0, false
		}
		name, rest = line[:open], line[close+1:]
	} else if sp := strings.IndexByte(line, ' '); sp >= 0 {
		name, rest = line[:sp], line[sp+1:]
	} else {
		return "", 0, false
	}
	rest = strings.TrimSpace(rest)
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp] // drop the optional timestamp
	}
	v, err := strconv.ParseFloat(rest, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return "", 0, false
	}
	return strings.TrimSuffix(name, "_total"), int64(v), true
}
