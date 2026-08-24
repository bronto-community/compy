package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bronto-io/compy/internal/config"
	"github.com/bronto-io/compy/internal/state"
)

// setupDir sets COMPY_HOME to a fresh temp dir and returns the resolved
// state directory.
func setupDir(t *testing.T) string {
	t.Helper()
	t.Setenv("COMPY_HOME", t.TempDir())
	dir, err := state.Dir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestArgsManagedMode(t *testing.T) {
	dir := setupDir(t)
	s := state.Settings{GRPCPort: 14317, HTTPPort: 14318, Enabled: []string{"b", "a"}}
	if _, err := config.EnsureBase(dir, s); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		frag, _ := config.Preset("debug", n, "", "")
		if err := config.WriteBackend(dir, n, frag); err != nil {
			t.Fatal(err)
		}
	}
	args, err := config.Args(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--config", filepath.Join(dir, "config/base.yaml"),
		"--config", filepath.Join(dir, "config/backends/a.yaml"),
		"--config", filepath.Join(dir, "config/backends/b.yaml"),
		"--feature-gates=confmap.enableMergeAppendOption",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("got %v", args)
	}
}

func TestArgsMissingFragmentErrors(t *testing.T) {
	dir := setupDir(t)
	s := state.Settings{GRPCPort: 14317, HTTPPort: 14318, Enabled: []string{"missing"}}
	if _, err := config.EnsureBase(dir, s); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Args(dir, s); err == nil {
		t.Fatal("expected error for missing fragment")
	}
}

func TestArgsRawMode(t *testing.T) {
	dir := setupDir(t)
	s := state.Settings{GRPCPort: 14317, HTTPPort: 14318, RawMode: true}
	args, err := config.Args(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--config", filepath.Join(dir, "config/custom.yaml"),
		"--feature-gates=confmap.enableMergeAppendOption",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("got %v", args)
	}
}

func TestEnsureBaseIdempotent(t *testing.T) {
	dir := setupDir(t)
	s := state.Settings{GRPCPort: 14317, HTTPPort: 14318}
	path, err := config.EnsureBase(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.EnsureBase(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "modified: true\n" {
		t.Fatalf("EnsureBase overwrote existing file: %q", got)
	}
}

func TestPresetOtlpGRPCContainsEndpointAndHeader(t *testing.T) {
	frag, err := config.Preset("otlp-grpc", "jaeger-local", "collector.example.com:4317", "sekret")
	if err != nil {
		t.Fatal(err)
	}
	s := string(frag)
	if !strings.Contains(s, "collector.example.com:4317") {
		t.Fatalf("missing endpoint in %q", s)
	}
	if !strings.Contains(s, "x-api-key") || !strings.Contains(s, "sekret") {
		t.Fatalf("missing header/key in %q", s)
	}
	if !strings.Contains(s, "otlp/jaeger-local") {
		t.Fatalf("exporter ID not scoped to backend name in %q", s)
	}
}

func TestPresetBronto(t *testing.T) {
	withKey, err := config.Preset("bronto", "b", "https://ingestion.eu.bronto.io/v1/traces", "sekret")
	if err != nil {
		t.Fatal(err)
	}
	s := string(withKey)
	if !strings.Contains(s, "X-BRONTO-API-KEY") || !strings.Contains(s, "sekret") {
		t.Fatalf("missing bronto auth header in %q", s)
	}

	noKey, err := config.Preset("bronto", "b", "https://ingestion.eu.bronto.io/v1/traces", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(noKey), "headers") {
		t.Fatalf("expected no headers block when apiKey is empty, got %q", noKey)
	}
}

func TestPresetDifferentNamesSameKindDoNotCollide(t *testing.T) {
	a, err := config.Preset("otlp-grpc", "a", "127.0.0.1:1", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := config.Preset("otlp-grpc", "b", "127.0.0.1:2", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(a), "otlp/a") || !strings.Contains(string(b), "otlp/b") {
		t.Fatalf("expected distinct component IDs, got %q and %q", a, b)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	dir := setupDir(t)
	s := state.Settings{GRPCPort: 14317, HTTPPort: 14318, Enabled: []string{"a"}}
	if _, err := config.EnsureBase(dir, s); err != nil {
		t.Fatal(err)
	}
	frag, _ := config.Preset("debug", "a", "", "")
	if err := config.WriteBackend(dir, "a", frag); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	origBase, _ := os.ReadFile(filepath.Join(dir, "config", "base.yaml"))
	origFrag, _ := os.ReadFile(config.BackendPath(dir, "a"))
	origSettings, _ := os.ReadFile(filepath.Join(dir, "settings.json"))

	if err := config.SnapshotLastGood(dir); err != nil {
		t.Fatal(err)
	}

	// mutate everything
	if err := os.WriteFile(filepath.Join(dir, "config", "base.yaml"), []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.DeleteBackend(dir, "a"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := config.RestoreLastGood(dir); err != nil {
		t.Fatal(err)
	}

	gotBase, err := os.ReadFile(filepath.Join(dir, "config", "base.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBase) != string(origBase) {
		t.Fatalf("base.yaml not restored: got %q want %q", gotBase, origBase)
	}
	gotFrag, err := os.ReadFile(config.BackendPath(dir, "a"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotFrag) != string(origFrag) {
		t.Fatalf("backend fragment not restored: got %q want %q", gotFrag, origFrag)
	}
	gotSettings, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSettings) != string(origSettings) {
		t.Fatalf("settings.json not restored: got %q want %q", gotSettings, origSettings)
	}
}

func TestRestoreLastGoodFreshInstallErrorsWithoutDeleting(t *testing.T) {
	dir := setupDir(t) // state.Dir() pre-creates an empty last-good/
	s := state.Settings{GRPCPort: 14317, HTTPPort: 14318, Enabled: []string{"a"}}
	if _, err := config.EnsureBase(dir, s); err != nil {
		t.Fatal(err)
	}
	frag, _ := config.Preset("debug", "a", "", "")
	if err := config.WriteBackend(dir, "a", frag); err != nil {
		t.Fatal(err)
	}

	if err := config.RestoreLastGood(dir); err == nil {
		t.Fatal("RestoreLastGood() = nil on fresh install, want error")
	}

	if _, err := os.Stat(filepath.Join(dir, "config", "base.yaml")); err != nil {
		t.Errorf("base.yaml deleted by RestoreLastGood on fresh install: %v", err)
	}
	if _, err := os.Stat(config.BackendPath(dir, "a")); err != nil {
		t.Errorf("backend fragment deleted by RestoreLastGood on fresh install: %v", err)
	}
}

func TestWriteBackendRejectsBadName(t *testing.T) {
	dir := setupDir(t)
	if err := config.WriteBackend(dir, "Bad Name!", []byte("exporters:\n  nop:\n")); err == nil {
		t.Fatal("expected error for invalid backend name")
	}
}

func TestDeleteBackendRejectsPathTraversal(t *testing.T) {
	dir := setupDir(t)
	// plant a sentinel file outside config/backends/ that traversal would
	// otherwise be able to reach and delete.
	sentinel := filepath.Join(dir, "config", "sentinel.yaml")
	if err := os.WriteFile(sentinel, []byte("do-not-delete\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := config.DeleteBackend(dir, "../sentinel"); err == nil {
		t.Fatal("expected error for path-traversal backend name")
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel file was affected by traversal attempt: %v", err)
	}
}

func TestValidateAgainstRealCollector(t *testing.T) {
	bin := os.Getenv("OTELCOL_BIN")
	if bin == "" {
		t.Skip("OTELCOL_BIN not set")
	}
	dir := setupDir(t)
	s := state.Settings{GRPCPort: 14317, HTTPPort: 14318, Enabled: []string{"a", "b"}}
	if _, err := config.EnsureBase(dir, s); err != nil {
		t.Fatal(err)
	}
	fragA, _ := config.Preset("otlp-grpc", "a", "127.0.0.1:5317", "")
	if err := config.WriteBackend(dir, "a", fragA); err != nil {
		t.Fatal(err)
	}
	fragB, _ := config.Preset("debug", "b", "", "")
	if err := config.WriteBackend(dir, "b", fragB); err != nil {
		t.Fatal(err)
	}
	args, err := config.Args(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, append([]string{"validate"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("validate failed: %v\n%s", err, out)
	}
}
