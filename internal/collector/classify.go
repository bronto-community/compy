package collector

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// classifyTimeout bounds one port probe. Localhost either answers or resets
// immediately; the timeout only guards a listener that accepts and says
// nothing.
const classifyTimeout = 2 * time.Second

// TelemetryPort is the port the collector's own telemetry defaults to
// (otelcol's :8888 — see MetricsURL). Conformance verdicts exclude it from a
// config's "actual" OTLP candidates.
func TelemetryPort() int { return defaultMetricsPort }

// IsHTTPPort reports whether 127.0.0.1:port speaks HTTP/1.1 — how adopt
// tells a config's otlp/http listener from its grpc one, since detection
// yields bare port numbers.
//
// Verified against the real otelcol-compy (0.159.0) running both receivers:
// the OTLP/HTTP server answers a plain GET / with a normal HTTP/1.1 404
// (405 on /v1/traces), while the gRPC listener replies with a raw HTTP/2
// SETTINGS frame that net/http rejects as `malformed HTTP response
// "\x00\x00\x06\x04..."`. Any well-formed response counts, whatever the
// status; any transport error counts as not-HTTP.
func IsHTTPPort(port int) bool {
	client := &http.Client{Timeout: classifyTimeout}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return false
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	return true
}
