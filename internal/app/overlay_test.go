package app

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/collector"
)

// TestCollectorArgsOverlayFirst is the whole contract in one assertion. The
// collector deep-merges its --config sources and the LAST one wins, so the
// overlay has to come FIRST: that makes compy's telemetry block a default a
// user's own service::telemetry overrides, instead of an override that
// silently replaces it. Reverse these two and compy starts editing people's
// configurations by the back door.
func TestCollectorArgsOverlayFirst(t *testing.T) {
	a := &App{Dir: t.TempDir()}
	args, err := a.collectorArgs("/configs/mine/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(a.Dir, overlayFile)
	want := []string{"--config", overlay, "--config", "/configs/mine/config.yaml"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("collectorArgs = %v, want %v", args, want)
	}

	data, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatalf("overlay not written: %v", err)
	}
	if string(data) != collector.OverlayYAML {
		t.Errorf("overlay content = %q, want collector.OverlayYAML", data)
	}

	// Rewritten every time, so a compy upgrade that changes the block takes
	// effect on the next activation rather than leaving a stale file behind.
	if err := os.WriteFile(overlay, []byte("stale: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.collectorArgs("/configs/mine/config.yaml"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(overlay); string(data) != collector.OverlayYAML {
		t.Errorf("a stale overlay survived: %q", data)
	}
}

// TestResolveMetricsPort: a busy telemetry port must never take the
// collector down with it — the Prometheus reader's bind failure is fatal —
// so it falls back to 0, "let the OS pick".
//
// The "our own collector already holds it" arm belongs to
// collector.TestPortHeldBy, which can stub the process lookup; here the
// holder is this test process, which is deliberately NOT the collector
// binary being passed in.
func TestResolveMetricsPort(t *testing.T) {
	if got := resolveMetricsPort(0, "/bin/otelcol"); got != 0 {
		t.Errorf("resolveMetricsPort(0) = %d, want 0 (already OS-assigned)", got)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := ln.Addr().(*net.TCPAddr).Port

	if got := resolveMetricsPort(busy, "/no/such/collector"); got != 0 {
		t.Errorf("resolveMetricsPort(busy) = %d, want 0 — a busy port must not reach the collector", got)
	}

	ln.Close()
	if got := resolveMetricsPort(busy, "/no/such/collector"); got != busy {
		t.Errorf("resolveMetricsPort(free) = %d, want %d", got, busy)
	}
}
