//go:build integration

// Package integration runs compy end-to-end against a real collector
// binary: OTLP in over gRPC-configured HTTP receiver, routed through a
// debug-preset backend, and out to the collector's stdout. No launchd, no
// tray — the collector process is started directly.
package integration

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bronto-io/compy/internal/collector"
	"github.com/bronto-io/compy/internal/config"
	"github.com/bronto-io/compy/internal/state"
)

const otlpTraceJSON = `{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"e2e-span",` +
	`"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",` +
	`"startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`

// freePort asks the OS for an unused port by binding to :0, then releasing
// it. There's a race if something else grabs it before the collector binds,
// but that's true of any "pick a free port" scheme and not worth guarding
// against in a local dev/test tool.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestE2E(t *testing.T) {
	bin := os.Getenv("OTELCOL_BIN")
	if bin == "" {
		t.Skip("OTELCOL_BIN not set; skipping end-to-end test")
	}

	dir := t.TempDir()
	t.Setenv("COMPY_HOME", dir)
	if _, err := state.Dir(); err != nil { // creates config/backends, logs, last-good
		t.Fatalf("state.Dir: %v", err)
	}

	s := state.Settings{
		GRPCPort: freePort(t),
		HTTPPort: freePort(t),
	}
	if _, err := config.EnsureBase(dir, s); err != nil {
		t.Fatalf("EnsureBase: %v", err)
	}
	yaml, err := config.Preset("debug", "sink", "", "")
	if err != nil {
		t.Fatalf("Preset: %v", err)
	}
	if err := config.WriteBackend(dir, "sink", yaml); err != nil {
		t.Fatalf("WriteBackend: %v", err)
	}
	s.Enabled = []string{"sink"}

	args, err := config.Args(dir, s)
	if err != nil {
		t.Fatalf("Args: %v", err)
	}

	if err := collector.Validate(bin, args); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start collector: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	if err := collector.Probe(s.GRPCPort, 10*time.Second); err != nil {
		t.Fatalf("Probe: %v\noutput so far:\n%s", err, out.String())
	}

	url := "http://127.0.0.1:" + strconv.Itoa(s.HTTPPort) + "/v1/traces"
	resp, err := http.Post(url, "application/json", strings.NewReader(otlpTraceJSON))
	if err != nil {
		t.Fatalf("POST /v1/traces: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/traces: status %d", resp.StatusCode)
	}

	// Give the debug exporter a moment to flush its log line, then stop the
	// process and check its combined output for the span name.
	time.Sleep(500 * time.Millisecond)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	if !strings.Contains(out.String(), "e2e-span") {
		t.Fatalf("collector output does not contain span name %q:\n%s", "e2e-span", out.String())
	}
}
