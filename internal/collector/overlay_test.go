package collector

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

// TestOverlayYAMLShape locks the two things the overlay must carry: the
// reader form otelcol 0.159 actually accepts (the older
// `metrics: {address: …}` is gone, and `readers: []` is rejected), and the
// env reference rather than a baked port. The real collector's verdict on
// this text lives in integration/.
func TestOverlayYAMLShape(t *testing.T) {
	for _, want := range []string{
		"service:", "telemetry:", "metrics:", "readers:", "- pull:", "prometheus:",
		"host: 127.0.0.1",
		"port: ${env:" + MetricsPortEnv + ":-8888}",
	} {
		if !strings.Contains(OverlayYAML, want) {
			t.Errorf("overlay missing %q:\n%s", want, OverlayYAML)
		}
	}
	// It must read as compy's, not as something the user should edit.
	if !strings.HasPrefix(OverlayYAML, "# Written by compy.") {
		t.Errorf("overlay does not say who wrote it:\n%s", OverlayYAML)
	}
	// The blind scrape fallback and the overlay's own fallback have to be
	// the same number, or a config that never sets the env var serves its
	// metrics somewhere the scrape does not look.
	if !strings.Contains(OverlayYAML, fmt.Sprintf(":-%d}", defaultMetricsPort)) {
		t.Errorf("overlay fallback disagrees with defaultMetricsPort (%d):\n%s", defaultMetricsPort, OverlayYAML)
	}
}

func TestPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if PortFree(port) {
		t.Errorf("PortFree(%d) = true while it is bound", port)
	}
	ln.Close()
	if !PortFree(port) {
		t.Errorf("PortFree(%d) = false after the listener closed", port)
	}
}

// TestPortHeldBy is the "that's just my own collector" question: a busy
// telemetry port must not push the next activation onto an OS-assigned one
// when the process holding it is the collector we are about to replace.
func TestPortHeldBy(t *testing.T) {
	origLsof, origPs := lsofOnPort, psExe
	t.Cleanup(func() { lsofOnPort, psExe = origLsof, origPs })

	exes := map[int]string{101: "/opt/compy/otelcol-compy", 202: "/usr/bin/something-else"}
	psExe = func(pid int) ([]byte, error) {
		exe, ok := exes[pid]
		if !ok {
			return nil, fmt.Errorf("no such pid")
		}
		return []byte(exe + "\n"), nil
	}

	for _, tc := range []struct {
		name, lsof, binary string
		want               bool
	}{
		{"held by our collector", "p101\n", "/opt/compy/otelcol-compy", true},
		{"held by a stranger", "p202\n", "/opt/compy/otelcol-compy", false},
		{"one of several is ours", "p202\np101\n", "/opt/compy/otelcol-compy", true},
		{"nothing listening", "", "/opt/compy/otelcol-compy", false},
		{"unreadable pid line", "pnope\n", "/opt/compy/otelcol-compy", false},
		{"pid we cannot inspect", "p999\n", "/opt/compy/otelcol-compy", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lsofOnPort = func(int) ([]byte, error) { return []byte(tc.lsof), nil }
			if got := PortHeldBy(8888, tc.binary); got != tc.want {
				t.Errorf("PortHeldBy = %v, want %v", got, tc.want)
			}
		})
	}

	// lsof missing or failing: the cautious answer is false — an
	// unnecessary fallback to an OS-assigned port costs a stable scrape
	// target, while a wrong true costs a collector that will not start.
	lsofOnPort = func(int) ([]byte, error) { return nil, fmt.Errorf("lsof: not found") }
	if PortHeldBy(8888, "/opt/compy/otelcol-compy") {
		t.Error("PortHeldBy = true with no detection available")
	}
}
