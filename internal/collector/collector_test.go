package collector_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bronto-io/compy/internal/collector"
)

func fakeBin(t *testing.T, script string) string {
	p := filepath.Join(t.TempDir(), "otelcol")
	os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0755)
	return p
}

func TestValidateFailureCarriesOutput(t *testing.T) {
	bin := fakeBin(t, `echo "error decoding 'exporters': unknown type" >&2; exit 1`)
	err := collector.Validate(bin, []string{"--config", "x.yaml"}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateOK(t *testing.T) {
	if err := collector.Validate(fakeBin(t, "exit 0"), nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProbe(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if err := collector.Probe(port, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := collector.Probe(1, 300*time.Millisecond); err == nil {
		t.Fatal("want error")
	}
}

func TestTailLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "collector.log")
	lines := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := collector.TailLog(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := "8\n9\n10\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTailLogMissingFile(t *testing.T) {
	got, err := collector.TailLog(filepath.Join(t.TempDir(), "missing.log"), 3)
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestValidatePassesEnvToCollector(t *testing.T) {
	bin := fakeBin(t, `[ "$API_KEY" = secret ] || { echo "API_KEY not set"; exit 1; }`)
	if err := collector.Validate(bin, nil, map[string]string{"API_KEY": "secret"}); err != nil {
		t.Fatalf("Validate did not pass env through: %v", err)
	}
}
