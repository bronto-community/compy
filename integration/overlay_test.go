//go:build integration

package integration

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bronto-community/compy/internal/collector"
)

// writeOverlay drops compy's telemetry overlay in dir and returns its path.
func writeOverlay(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "telemetry.yaml")
	if err := os.WriteFile(p, []byte(collector.OverlayYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// startCollector runs bin with args and env, killing it when the test ends.
// It returns the captured output buffer so a failure can show it.
func startCollector(t *testing.T, bin string, args []string, env map[string]string) *bytes.Buffer {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start collector: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return &out
}

// scrapeOK polls a metrics endpoint until it answers 200.
func scrapeOK(t *testing.T, port int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/metrics"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// minimalConfig is a config that says nothing about service::telemetry —
// the ordinary case, and the one the overlay exists for.
func minimalConfig(t *testing.T, dir string, httpPort int) string {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	body := "receivers:\n  otlp:\n    protocols:\n      http:\n        endpoint: 127.0.0.1:" +
		strconv.Itoa(httpPort) + "\nexporters:\n  debug: {}\nservice:\n  pipelines:\n" +
		"    traces: {receivers: [otlp], exporters: [debug]}\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestTelemetryOverlayMovesThePort is the load-bearing claim of the whole
// overlay design, checked against a real collector: compy can move the
// collector's own metrics port WITHOUT editing the configuration, by
// passing its block as a second --config source.
//
// The collector's Prometheus reader treats a failed bind as fatal
// (contrib/otelconf metric.go), so on a machine where something already
// owns :8888 the alternative is a collector that refuses to start.
func TestTelemetryOverlayMovesThePort(t *testing.T) {
	bin := otelcolBin(t)
	dir := t.TempDir()

	httpPort, metricsPort := freePort(t), freePort(t)
	cfg := minimalConfig(t, dir, httpPort)
	args := []string{"--config", writeOverlay(t, dir), "--config", cfg}
	env := map[string]string{collector.MetricsPortEnv: strconv.Itoa(metricsPort)}

	if err := collector.Validate(bin, args, env); err != nil {
		t.Fatalf("Validate with the overlay: %v", err)
	}
	out := startCollector(t, bin, args, env)
	if !scrapeOK(t, metricsPort, 15*time.Second) {
		t.Fatalf("no /metrics on the overlay's port %d\noutput:\n%s", metricsPort, out.String())
	}

	// And compy's own scrape reads it, which is the point of moving it.
	h := collector.ScrapePorts([]int{httpPort, metricsPort})
	if !h.Available || h.Port != metricsPort {
		t.Errorf("ScrapePorts = %+v, want the overlay's port %d", h, metricsPort)
	}
}

// TestTelemetryOverlayIsADefaultNotAnOverride locks the argument ORDER.
// confmap merges --config sources with the last one winning, so overlay
// FIRST means a configuration carrying its own service::telemetry keeps it.
// Reverse the order and compy would silently override a deliberate choice —
// which is exactly the "don't touch the user's config" promise, enforced.
func TestTelemetryOverlayIsADefaultNotAnOverride(t *testing.T) {
	bin := otelcolBin(t)
	dir := t.TempDir()

	httpPort, overlayPort, ownPort := freePort(t), freePort(t), freePort(t)
	body, err := os.ReadFile(minimalConfig(t, dir, httpPort))
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, "own-telemetry.yaml")
	block := string(body) + "  telemetry:\n    metrics:\n      readers:\n        - pull:\n" +
		"            exporter:\n              prometheus:\n                host: 127.0.0.1\n" +
		"                port: " + strconv.Itoa(ownPort) + "\n"
	if err := os.WriteFile(own, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"--config", writeOverlay(t, dir), "--config", own}
	env := map[string]string{collector.MetricsPortEnv: strconv.Itoa(overlayPort)}
	out := startCollector(t, bin, args, env)

	if !scrapeOK(t, ownPort, 15*time.Second) {
		t.Fatalf("the config's own telemetry port %d did not win over the overlay's %d\noutput:\n%s",
			ownPort, overlayPort, out.String())
	}
	if scrapeOK(t, overlayPort, time.Second) {
		t.Errorf("the overlay's port %d answered too — it overrode the config instead of defaulting it", overlayPort)
	}
}

// TestTelemetryOverlayDefaultsTo8888 checks the :-fallback: with no
// COMPY_METRICS_PORT in the environment the overlay must land on otelcol's
// documented default, which is what the scrape's blind fallback assumes.
// Skipped when something on this machine already owns :8888 — including a
// real compy install, which is exactly the situation the overlay exists for.
func TestTelemetryOverlayDefaultsTo8888(t *testing.T) {
	bin := otelcolBin(t)
	if !collector.PortFree(8888) {
		t.Skip(":8888 is busy on this machine; the fallback cannot be observed")
	}
	dir := t.TempDir()
	args := []string{"--config", writeOverlay(t, dir), "--config", minimalConfig(t, dir, freePort(t))}
	out := startCollector(t, bin, args, nil)
	if !scrapeOK(t, 8888, 15*time.Second) {
		t.Fatalf("overlay with no env did not land on :8888\noutput:\n%s", out.String())
	}
}
