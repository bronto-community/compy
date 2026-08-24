package app_test

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/config"
	"github.com/bronto-io/compy/internal/launchd"
	"github.com/bronto-io/compy/internal/state"
)

// setup points COMPY_HOME and HOME at temp dirs (HOME too: launchd.Install
// writes ~/Library/LaunchAgents/...), stubs launchd.Exec, and returns a
// pointer to the recorded launchctl invocations.
func setup(t *testing.T, printOut string) *[][]string {
	t.Helper()
	t.Setenv("COMPY_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	var calls [][]string
	orig := launchd.Exec
	launchd.Exec = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) > 0 && args[0] == "print" {
			return []byte(printOut), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { launchd.Exec = orig })
	return &calls
}

// fakeDistro installs a shell script standing in for the collector binary
// and registers it as the selected distro.
func fakeDistro(t *testing.T, script string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "otelcol")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveDistros([]state.Distro{{Name: "fake", Path: p}}); err != nil {
		t.Fatal(err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.Distro = "fake"
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
}

func called(calls [][]string, sub string) bool {
	for _, c := range calls {
		if len(c) > 0 && c[0] == sub {
			return true
		}
	}
	return false
}

func TestApplyValidateFailureShort(t *testing.T) {
	calls := setup(t, "")
	fakeDistro(t, `echo "error decoding 'exporters': unknown type" >&2; exit 1`)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	err = a.Apply()
	if err == nil {
		t.Fatal("Apply() = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("Apply() error = %q, want collector output", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("launchd.Exec called %v, want no calls on validation failure", *calls)
	}
}

func TestApplyHappyPath(t *testing.T) {
	// The probe dials settings.GRPCPort; stand a listener up there since the
	// collector itself never runs (launchd is stubbed).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")

	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.GRPCPort = port
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(); err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("no bootstrap in %v", *calls)
	}
	if !called(*calls, "kickstart") {
		t.Errorf("no kickstart in %v", *calls)
	}
	// The config came up, so it becomes the new last-good.
	if _, err := os.Stat(filepath.Join(a.Dir, "last-good", "settings.json")); err != nil {
		t.Errorf("no last-good snapshot: %v", err)
	}

	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Distro != "fake" || st.GRPCPort != port {
		t.Errorf("Status() = %+v, want running fake distro on %d", st, port)
	}
}

func TestRollbackRestoresAndApplies(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	calls := setup(t, "")
	fakeDistro(t, "exit 0")

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}

	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.GRPCPort = port
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(a.Dir, "config", "backends", "good.yaml")
	if err := os.WriteFile(good, []byte("# good\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SnapshotLastGood(a.Dir); err != nil {
		t.Fatal(err)
	}

	// Break things after the snapshot.
	if err := os.Remove(good); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Dir, "config", "backends", "bad.yaml"), []byte("# bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := a.Rollback(); err != nil {
		t.Fatalf("Rollback() = %v, want nil", err)
	}
	if _, err := os.Stat(good); err != nil {
		t.Errorf("good.yaml not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, "config", "backends", "bad.yaml")); err == nil {
		t.Error("bad.yaml still present after rollback")
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("rollback did not apply: %v", *calls)
	}
}

func TestNewErrorsWithoutDistroMentionsDistroAdd(t *testing.T) {
	setup(t, "")

	// New() itself must succeed with no distro — otherwise `compy distro add`
	// could never run. The distro-add hint comes from SelectedDistro/Apply.
	a, err := app.New()
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	if _, err := a.SelectedDistro(); err == nil || !strings.Contains(err.Error(), "compy distro add") {
		t.Fatalf("SelectedDistro() error = %v, want mention of `compy distro add`", err)
	}
	if err := a.Apply(); err == nil || !strings.Contains(err.Error(), "compy distro add") {
		t.Fatalf("Apply() error = %v, want mention of `compy distro add`", err)
	}
}

func TestApplyProbeFailureHintsRollback(t *testing.T) {
	// Bind then release a port: nothing listens there, so the probe fails
	// (and we are not at the mercy of whatever holds the default port).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	calls := setup(t, "")
	fakeDistro(t, "exit 0")
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.GRPCPort = port
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.LogPath(), []byte("boom: bind: address already in use\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A pre-existing snapshot must survive a failed apply — otherwise
	// `compy rollback` would restore the config that just failed.
	marker := filepath.Join(a.Dir, "config", "backends", "known-good.yaml")
	if err := os.WriteFile(marker, []byte("# good\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SnapshotLastGood(a.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	err = a.Apply()
	if err == nil {
		t.Fatal("Apply() = nil, want probe failure")
	}
	if !strings.Contains(err.Error(), "compy rollback") {
		t.Errorf("Apply() error = %q, want rollback hint", err)
	}
	if !strings.Contains(err.Error(), "bind: address already in use") {
		t.Errorf("Apply() error = %q, want log tail", err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("no bootstrap in %v", *calls)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, "last-good", "config", "backends", "known-good.yaml")); err != nil {
		t.Errorf("failed apply clobbered the last-good snapshot: %v", err)
	}
}

func TestBackendLifecycle(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	setup(t, "")
	fakeDistro(t, "exit 0")
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.GRPCPort = port
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AddBackend("local-debug", "debug", "", ""); err != nil {
		t.Fatal(err)
	}
	frag, err := a.ReadFragment("local-debug")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag, "debug/local-debug") {
		t.Errorf("fragment = %q, want exporter id debug/local-debug", frag)
	}

	if err := a.SetEnabled("local-debug", true); err != nil {
		t.Fatalf("SetEnabled = %v", err)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Enabled) != 1 || st.Enabled[0] != "local-debug" {
		t.Errorf("Enabled = %v, want [local-debug]", st.Enabled)
	}

	if err := a.RemoveBackend("local-debug"); err != nil {
		t.Fatalf("RemoveBackend = %v", err)
	}
	st, err = a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Enabled) != 0 {
		t.Errorf("Enabled = %v, want empty after remove", st.Enabled)
	}
	if _, err := os.Stat(config.BackendPath(a.Dir, "local-debug")); err == nil {
		t.Error("fragment still on disk after remove")
	}
}

func TestSetEnabledRejectsInvalidBackendName(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	before, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}

	if err := a.SetEnabled("../base", true); err == nil {
		t.Fatal("SetEnabled(\"../base\", true) = nil, want error")
	}

	after, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before.Enabled, after.Enabled) {
		t.Errorf("settings.Enabled changed: before %v, after %v", before.Enabled, after.Enabled)
	}
}

func TestSetRawModeSeedsCustomYAML(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	setup(t, "")
	fakeDistro(t, "exit 0")
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.GRPCPort = port
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetRawMode(true); err != nil {
		t.Fatalf("SetRawMode(true) = %v", err)
	}
	raw, err := a.ReadRaw()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "receivers:") {
		t.Errorf("custom.yaml = %q, want seed from base.yaml", raw)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !st.RawMode {
		t.Error("RawMode not persisted")
	}
}

func TestBackendsReportsKind(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"a-grpc": "otlp-grpc", "b-http": "otlp-http", "c-dbg": "debug"} {
		if err := a.AddBackend(name, kind, "http://x:1", "k"); err != nil {
			t.Fatal(name, err)
		}
	}
	list, err := a.Backends()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range list {
		got[b["name"].(string)] = b["kind"].(string)
	}
	want := map[string]string{"a-grpc": "otlp-grpc", "b-http": "otlp-http", "c-dbg": "debug"}
	for n, k := range want {
		if got[n] != k {
			t.Errorf("%s: kind = %q, want %q", n, got[n], k)
		}
	}
}
