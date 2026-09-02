package collector

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// defaultMetricsPort is where compy puts the collector's own telemetry:
// :18888, otelcol's :8888 plus 10000, the same convention as the OTLP ports.
// Compy's overlay (OverlayYAML) places it there; no compy configuration
// mentions it, which is why the Collector screen names the address on
// screen rather than expecting anyone to find it in yaml.
//
// This is only the BLIND fallback, used when pid detection gave no
// listeners to probe. A moved port — or an OS-assigned one, after a busy
// port fell back — is found the way every other listener is: from the
// collector process itself. Probing compy's own default rather than
// otelcol's is also the safer blind guess, since :8888 on a developer's
// machine is very likely SOMEONE ELSE's collector, and reporting a
// stranger's numbers as ours would be worse than reporting none. A var so
// tests can aim it at a closed port.
var defaultMetricsPort = 18888

// healthTimeout bounds the scrape. It is on the path of a screen the user is
// looking at, so a collector that is wedged must not hold the page.
const healthTimeout = 2 * time.Second

// maxLineBytes caps one sample line; see the scanner buffer in health.
const maxLineBytes = 1 << 20

// maxMetricsBytes caps the scrape. A collector with many exporters emits a
// few hundred KB; anything past this is not our /metrics page.
const maxMetricsBytes = 4 << 20

// Health is the four numbers the Collector screen shows: how much telemetry
// came in, how much went out, how much is waiting, and how much was lost.
// Available is false when nothing answered — a stopped collector is not an
// error, it is dashes on screen.
type Health struct {
	Available bool  `json:"available"`
	Port      int   `json:"port,omitempty"` // the localhost port that answered the scrape
	Received  int64 `json:"received"`
	Exported  int64 `json:"exported"`
	Queue     int64 `json:"queue"`
	Dropped   int64 `json:"dropped"`
}

// ScrapePorts reads the running collector's own metrics: :18888 (compy's
// default) first, then — only if the default did not answer — each of the
// collector's detected listening ports, first success wins. Port records
// which one answered, so the UI can label it. It never returns an error:
// every failure — nothing listening, a timeout, something else on the port —
// means the same thing to the caller, "no numbers".
func ScrapePorts(ports []int) Health {
	// Pid-bound: when detection gave us the collector's own listeners,
	// probe ONLY those (the default first when present) — a blind probe
	// could read some other collector's metrics on this machine. The
	// default is the fallback only when detection is unavailable.
	if len(ports) == 0 {
		if h := health(fmt.Sprintf("http://localhost:%d/metrics", defaultMetricsPort)); h.Available {
			h.Port = defaultMetricsPort
			return h
		}
		return Health{}
	}
	ordered := make([]int, 0, len(ports))
	for _, p := range ports {
		if p == defaultMetricsPort {
			ordered = append([]int{p}, ordered...)
		} else {
			ordered = append(ordered, p)
		}
	}
	for _, p := range ordered {
		if h := health(fmt.Sprintf("http://localhost:%d/metrics", p)); h.Available {
			h.Port = p
			return h
		}
	}
	return Health{}
}

// health is Scrape against an arbitrary URL, so tests can serve a captured
// /metrics page.
//
// The metric names are otelcol's, verified against a running 0.135.0
// (`curl localhost:18888/metrics` with traces, metrics and logs flowing):
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
	// A sample line carrying many labels can pass bufio's 64KB default,
	// which would silently end the scan mid-page.
	scanner.Buffer(nil, maxLineBytes)
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
