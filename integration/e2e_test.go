//go:build integration

// Package integration runs compy end-to-end against a real collector
// binary: OTLP in over gRPC/HTTP, routed through the shipped "debug"
// configuration, and out to the collector's stdout. No launchd — the
// collector process is started directly, with the same --config arg and
// environment variables app.Activate would hand to launchd.Install.
package integration

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/collector"
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

// otelcolBin returns OTELCOL_BIN, or skips the test if it isn't set.
func otelcolBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("OTELCOL_BIN")
	if bin == "" {
		t.Skip("OTELCOL_BIN not set; skipping end-to-end test")
	}
	return bin
}

// TestE2E materializes the shipped default configurations into a fresh
// COMPY_HOME, then runs the "debug" configuration exactly as app.Activate
// would hand it to launchd: the config's own --config arg plus the active
// variable set's values (here just compy's port variables) on the process
// environment. It POSTs one OTLP span over HTTP and asserts the debug
// exporter logged it.
func TestE2E(t *testing.T) {
	bin := otelcolBin(t)

	root := t.TempDir()
	if err := cfgstore.MaterializeDefaults(root); err != nil {
		t.Fatalf("MaterializeDefaults: %v", err)
	}

	grpcPort, httpPort := freePort(t), freePort(t)
	env := map[string]string{
		"COMPY_GRPC_PORT": strconv.Itoa(grpcPort),
		"COMPY_HTTP_PORT": strconv.Itoa(httpPort),
	}
	configPath := filepath.Join(cfgstore.Dir(root), "debug", "config.yaml")
	args := []string{"--config", configPath}

	if err := collector.Validate(bin, args, env); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// --set disables the collector's own self-telemetry metrics server
	// (default :8888): a real compy install may already have a collector
	// running on this machine bound to it, and this test has no business
	// with self-telemetry either way.
	runArgs := append(append([]string{}, args...), "--set=service.telemetry.metrics.level=none")
	cmd := exec.Command(bin, runArgs...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
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

	if err := collector.Probe(grpcPort, 10*time.Second); err != nil {
		t.Fatalf("Probe: %v\noutput so far:\n%s", err, out.String())
	}

	url := "http://127.0.0.1:" + strconv.Itoa(httpPort) + "/v1/traces"
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

// TestDefaultsValidate checks that every shipped default configuration
// validates against a real collector binary using only its own
// ${env:...:-default} fallbacks plus compy's port variables — the same env
// a freshly materialized, never-configured default gets. Required-but-
// defaultless vars (e.g. a vendor API key) must not break validate; an
// empty header value is fine for validate.
func TestDefaultsValidate(t *testing.T) {
	bin := otelcolBin(t)

	root := t.TempDir()
	if err := cfgstore.MaterializeDefaults(root); err != nil {
		t.Fatalf("MaterializeDefaults: %v", err)
	}
	infos, err := cfgstore.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) == 0 {
		t.Fatal("no shipped default configurations found")
	}

	env := map[string]string{
		"COMPY_GRPC_PORT": strconv.Itoa(freePort(t)),
		"COMPY_HTTP_PORT": strconv.Itoa(freePort(t)),
	}
	for _, info := range infos {
		t.Run(info.Name, func(t *testing.T) {
			configPath := filepath.Join(cfgstore.Dir(root), info.Name, "config.yaml")
			if err := collector.Validate(bin, []string{"--config", configPath}, env); err != nil {
				t.Fatalf("Validate %s: %v", info.Name, err)
			}
		})
	}
}
