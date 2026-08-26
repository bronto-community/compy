package collector

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// sample is trimmed from a real otelcol 0.135.0 /metrics page. Two shapes
// appear on purpose: the default telemetry config renders counters WITHOUT
// the "_total" suffix, an explicitly configured prometheus reader renders
// them WITH it, and compy must read both.
const sample = `# HELP otelcol_receiver_accepted_spans Number of spans successfully pushed into the pipeline.
# TYPE otelcol_receiver_accepted_spans counter
otelcol_receiver_accepted_spans{receiver="otlp",service_name="otelcol",transport="http"} 7
otelcol_receiver_accepted_metric_points_total{receiver="otlp",service_name="otelcol"} 4
otelcol_receiver_accepted_log_records_total{receiver="otlp",service_name="otelcol"} 1
otelcol_receiver_refused_spans{receiver="otlp",service_name="otelcol"} 2
otelcol_exporter_sent_spans{exporter="otlphttp",service_name="otelcol"} 5
otelcol_exporter_sent_metric_points_total{exporter="otlphttp",service_name="otelcol"} 3
otelcol_exporter_send_failed_log_records_total{exporter="otlphttp",service_name="otelcol"} 1
# HELP otelcol_exporter_queue_size Current size of the retry queue (in batches). [alpha]
# TYPE otelcol_exporter_queue_size gauge
otelcol_exporter_queue_size{data_type="logs",exporter="otlphttp",service_name="otelcol"} 6
otelcol_exporter_queue_size{data_type="traces",exporter="otlphttp",service_name="otelcol"} 2
otelcol_exporter_queue_capacity{data_type="logs",exporter="otlphttp",service_name="otelcol"} 1000
otelcol_exporter_queue_batch_send_size_bucket{exporter="otlphttp",service_name="otelcol",le="10"} 99
otelcol_exporter_queue_batch_send_size_sum{exporter="otlphttp",service_name="otelcol"} 99
otelcol_process_uptime_seconds_total{service_name="otelcol"} 1560.4
target_info{service_name="otelcol",service_version="0.135.0"} 1
`

func TestHealthReadsTheCollectorsOwnMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sample))
	}))
	defer srv.Close()

	h := health(srv.URL)
	if !h.Available {
		t.Fatal("Available = false, want true")
	}
	// received: accepted spans + metric points + log records.
	if h.Received != 12 {
		t.Errorf("Received = %d, want 12", h.Received)
	}
	// exported: sent spans + metric points.
	if h.Exported != 8 {
		t.Errorf("Exported = %d, want 8", h.Exported)
	}
	// queue: only otelcol_exporter_queue_size, summed over exporters and
	// signals — never queue_capacity or the batch-size histogram.
	if h.Queue != 8 {
		t.Errorf("Queue = %d, want 8", h.Queue)
	}
	// dropped: refused at the receiver + failed at the exporter.
	if h.Dropped != 3 {
		t.Errorf("Dropped = %d, want 3", h.Dropped)
	}
}

// A stopped collector has nothing listening on :8888. That is not an error
// to report, it is "no numbers": the strip shows dashes.
func TestHealthUnavailableWhenNothingAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	if h := health(url); h.Available {
		t.Errorf("Available = true for an unreachable endpoint: %+v", h)
	}

	// A non-200 (something else on the port) is no better than silence.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer other.Close()
	if h := health(other.URL); h.Available {
		t.Errorf("Available = true for a %d response: %+v", http.StatusNotFound, h)
	}
}

// closedPort returns a localhost port with nothing listening on it.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// srvPort digs the port out of an httptest server URL.
func srvPort(t *testing.T, url string) int {
	t.Helper()
	p, err := strconv.Atoi(url[strings.LastIndexByte(url, ':')+1:])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ScrapePorts falls back to the detected listening ports when the default
// telemetry port does not answer — a config that moves service::telemetry
// off :8888 still gets its numbers — and records which port answered.
func TestScrapePortsFallsBackToDetectedPorts(t *testing.T) {
	origDefault := defaultMetricsPort
	defer func() { defaultMetricsPort = origDefault }()
	defaultMetricsPort = closedPort(t) // this machine's real :8888 is not the test's business

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sample))
	}))
	defer srv.Close()
	port := srvPort(t, srv.URL)

	// A dead port before the live one: first success wins, not first try.
	h := ScrapePorts([]int{closedPort(t), port})
	if !h.Available || h.Port != port {
		t.Fatalf("Available=%v Port=%d, want true/%d", h.Available, h.Port, port)
	}
	if h.Received != 12 {
		t.Errorf("Received = %d, want 12 (the real page was parsed)", h.Received)
	}

	// The default port answering wins without touching the detected list.
	defaultMetricsPort = port
	if h := ScrapePorts([]int{closedPort(t)}); !h.Available || h.Port != port {
		t.Errorf("default port: Available=%v Port=%d, want true/%d", h.Available, h.Port, port)
	}

	// Nothing answering anywhere: no numbers, no port claim.
	defaultMetricsPort = closedPort(t)
	if h := ScrapePorts([]int{closedPort(t)}); h.Available || h.Port != 0 {
		t.Errorf("nothing listening: got %+v, want zero Health", h)
	}
}

func TestHealthIgnoresJunkSamples(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("# comment only\n" +
			"otelcol_receiver_accepted_spans{receiver=\"otlp\"} NaN\n" +
			"otelcol_receiver_accepted_spans{receiver=\"otlp\"} +Inf\n" +
			"otelcol_receiver_accepted_spans_broken\n" +
			"otelcol_receiver_accepted_spans{receiver=\"otlp\"} 3 1700000000000\n"))
	}))
	defer srv.Close()

	h := health(srv.URL)
	if !h.Available || h.Received != 3 {
		t.Errorf("health = %+v, want available with Received=3 (NaN/Inf/malformed skipped, timestamp ignored)", h)
	}
}
