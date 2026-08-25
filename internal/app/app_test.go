package app_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/cfgstore"
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

// listenPort stands a listener up on a free port so Activate's probe
// succeeds (the collector itself never runs — launchd is stubbed) and
// records it as compy's gRPC port.
func listenPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.GRPCPort = port
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	return port
}

func called(calls [][]string, sub string) bool {
	for _, c := range calls {
		if len(c) > 0 && c[0] == sub {
			return true
		}
	}
	return false
}

func readPlist(t *testing.T) string {
	t.Helper()
	path, err := launchd.PlistPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	return string(data)
}

func TestActivateHappyPath(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	port := listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetVar("mine", "prod", "API_KEY", "s3cret"); err != nil {
		t.Fatal(err)
	}
	// A set value must never win over compy's own port variables.
	if err := a.SetVar("mine", "prod", "COMPY_GRPC_PORT", "9999"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("mine", "prod"); err != nil {
		t.Fatalf("Activate = %v, want nil", err)
	}

	if !called(*calls, "bootstrap") {
		t.Errorf("no bootstrap in %v", *calls)
	}
	if !called(*calls, "kickstart") {
		t.Errorf("no kickstart in %v", *calls)
	}

	plist := readPlist(t)
	if !strings.Contains(plist, "<key>API_KEY</key><string>s3cret</string>") {
		t.Errorf("plist missing the set's variables:\n%s", plist)
	}
	wantPort := "<key>COMPY_GRPC_PORT</key><string>" + strconv.Itoa(port) + "</string>"
	if !strings.Contains(plist, wantPort) {
		t.Errorf("plist missing/overridden %s:\n%s", wantPort, plist)
	}
	if !strings.Contains(plist, "<key>COMPY_HTTP_PORT</key><string>14318</string>") {
		t.Errorf("plist missing COMPY_HTTP_PORT:\n%s", plist)
	}
	wantConfig := filepath.Join(a.Dir, "configs", "mine", "config.yaml")
	if !strings.Contains(plist, "<string>--config</string>") || !strings.Contains(plist, "<string>"+wantConfig+"</string>") {
		t.Errorf("plist args wrong, want a single --config %s:\n%s", wantConfig, plist)
	}
	if strings.Contains(plist, "feature-gates") {
		t.Errorf("plist carries a feature gate; v2 uses none:\n%s", plist)
	}

	if _, err := os.Stat(filepath.Join(a.Dir, "last-good", "settings.json")); err != nil {
		t.Errorf("no last-good snapshot: %v", err)
	}

	name, set, err := a.ActiveConfig()
	if err != nil || name != "mine" || set != "prod" {
		t.Errorf("ActiveConfig() = %q,%q,%v, want mine,prod", name, set, err)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Config != "mine" || st.Set != "prod" || st.Distro != "fake" || st.GRPCPort != port {
		t.Errorf("Status() = %+v", st)
	}
}

func TestActivateValidateFailureNoLaunchctl(t *testing.T) {
	calls := setup(t, "")
	fakeDistro(t, `echo "error decoding 'exporters': unknown type" >&2; exit 1`)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	err = a.Activate("debug", "")
	if err == nil {
		t.Fatal("Activate() = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("Activate() error = %q, want the collector's output", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("launchd.Exec called %v, want no calls on validation failure", *calls)
	}
	if name, _, _ := a.ActiveConfig(); name != "" {
		t.Errorf("ActiveConfig = %q after a failed activation, want unchanged", name)
	}
}

func TestActivateUnknownSetErrors(t *testing.T) {
	calls := setup(t, "")
	fakeDistro(t, "exit 0")

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	err = a.Activate("debug", "nope")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Activate(debug, nope) = %v, want an unknown-set error", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("launchd.Exec called %v, want no calls", *calls)
	}
}

func TestActivateWithoutDistroMentionsDistroCommand(t *testing.T) {
	setup(t, "")

	a, err := app.New()
	if err != nil {
		t.Fatalf("New() = %v, want nil (a distro-less compy must still run)", err)
	}
	err = a.Activate("debug", "")
	if err == nil || !strings.Contains(err.Error(), "compy distro") {
		t.Fatalf("Activate() error = %v, want a `compy distro` hint", err)
	}
}

func TestDeleteActiveConfigErrors(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteConfig("debug"); err == nil {
		t.Fatal("DeleteConfig on the active config = nil, want error")
	}
	if _, _, err := cfgstore.Get(a.Dir, "debug"); err != nil {
		t.Errorf("active config was deleted anyway: %v", err)
	}
}

func TestWriteYAMLReactivatesWhenActive(t *testing.T) {
	calls := setup(t, "")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("other", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if err := a.WriteConfigYAML("other", "exporters: {}\n"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("writing an inactive config re-applied: %v", *calls)
	}

	if err := a.WriteConfigYAML("debug", "receivers: {}\n# edited\n"); err != nil {
		t.Fatal(err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("writing the active config did not re-activate: %v", *calls)
	}
	_, yaml, err := cfgstore.Get(a.Dir, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yaml, "# edited") {
		t.Errorf("yaml not written: %q", yaml)
	}
}

func TestRollbackRestoresAndApplies(t *testing.T) {
	calls := setup(t, "")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}
	// Break the config behind compy's back (a write through the app would
	// re-activate and snapshot the broken config as the new last-good).
	if err := cfgstore.WriteYAML(a.Dir, "debug", "broken: true\n"); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if err := a.Rollback(); err != nil {
		t.Fatalf("Rollback() = %v, want nil", err)
	}
	_, yaml, err := cfgstore.Get(a.Dir, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(yaml, "broken") {
		t.Errorf("config not restored: %q", yaml)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("rollback did not re-apply: %v", *calls)
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

	err = a.Activate("debug", "")
	if err == nil {
		t.Fatal("Activate() = nil, want probe failure")
	}
	if !strings.Contains(err.Error(), "compy rollback") {
		t.Errorf("error = %q, want a rollback hint", err)
	}
	if !strings.Contains(err.Error(), "bind: address already in use") {
		t.Errorf("error = %q, want the log tail", err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("no bootstrap in %v", *calls)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, "last-good", "settings.json")); err == nil {
		t.Error("a failed activation took a last-good snapshot")
	}
}

func TestNewMaterializesDefaults(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	configs, err := a.Configs()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range configs {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		t.Fatal("no shipped configurations materialized")
	}
	for _, c := range configs {
		if c.Name == "debug" && c.Provenance != "shipped" {
			t.Errorf("debug provenance = %q, want shipped", c.Provenance)
		}
	}
}

// writeLegacyTree fabricates a v1 state dir: config/base.yaml, one enabled
// backend fragment, and a v1 settings.json.
func writeLegacyTree(t *testing.T, dir, binPath string, enabled []string, grpcPort int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "config", "backends"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "base.yaml"), []byte("receivers:\n  otlp:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "backends", "sink.yaml"), []byte("exporters:\n  debug/sink:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	on, err := json.Marshal(enabled)
	if err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf(`{"grpc_port":%d,"http_port":14318,"distro":"fake","enabled":%s,"raw_mode":false}`, grpcPort, on)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	distros, err := json.Marshal([]state.Distro{{Name: "fake", Path: binPath}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "distros.json"), distros, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationLegacyBackends(t *testing.T) {
	calls := setup(t, "state = running")
	dir := os.Getenv("COMPY_HOME")

	// The migrated config has to actually come up: give the probe a port
	// that answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	bin := filepath.Join(t.TempDir(), "otelcol")
	script := "#!/bin/sh\nif [ \"$1\" = print-initial-config ]; then echo 'merged: rendered'; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLegacyTree(t, dir, bin, []string{"sink"}, ln.Addr().(*net.TCPAddr).Port)

	if _, err := app.New(); err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}

	_, yaml, err := cfgstore.Get(dir, "migrated")
	if err != nil {
		t.Fatalf("migrated config missing: %v", err)
	}
	if !strings.Contains(yaml, "merged: rendered") {
		t.Errorf("migrated yaml = %q, want the rendered effective config", yaml)
	}
	if _, err := os.Stat(filepath.Join(dir, "legacy-v1", "backends", "sink.yaml")); err != nil {
		t.Errorf("legacy tree not archived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); err == nil {
		t.Error("legacy config/ still present after migration")
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveConfig != "migrated" {
		t.Errorf("ActiveConfig = %q, want migrated (backends were enabled)", s.ActiveConfig)
	}
	if _, _, err := cfgstore.Get(dir, "debug"); err != nil {
		t.Errorf("shipped defaults not materialized after migration: %v", err)
	}
	// The v1 LaunchAgent pointed at the now-archived tree: migration must
	// have repointed it at the migrated configuration.
	if !called(*calls, "bootstrap") || !called(*calls, "kickstart") {
		t.Errorf("migration did not re-apply the collector: %v", *calls)
	}

	// Second run: nothing left to migrate, and nothing blows up.
	if _, err := app.New(); err != nil {
		t.Fatalf("second New() = %v, want nil", err)
	}
}

func TestMigrationFallsBackToBaseYAML(t *testing.T) {
	setup(t, "")
	dir := os.Getenv("COMPY_HOME")
	writeLegacyTree(t, dir, filepath.Join(t.TempDir(), "does-not-exist"), []string{"sink"}, 1)

	if _, err := app.New(); err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	_, yaml, err := cfgstore.Get(dir, "migrated")
	if err != nil {
		t.Fatalf("migrated config missing: %v", err)
	}
	if !strings.Contains(yaml, "otlp") {
		t.Errorf("migrated yaml = %q, want a copy of the old base.yaml", yaml)
	}
}

func TestMigrationWithoutEnabledBackendsStopsTheJob(t *testing.T) {
	calls := setup(t, "")
	dir := os.Getenv("COMPY_HOME")
	writeLegacyTree(t, dir, filepath.Join(t.TempDir(), "unused"), nil, 1)

	if _, err := app.New(); err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveConfig != "" {
		t.Errorf("ActiveConfig = %q, want empty (nothing was enabled)", s.ActiveConfig)
	}
	// Nothing to run: the v1 job must be unloaded, not left crash-looping
	// against the archived config.
	if !called(*calls, "bootout") {
		t.Errorf("stale collector job not stopped: %v", *calls)
	}
	if called(*calls, "bootstrap") {
		t.Errorf("nothing was enabled, yet a job was installed: %v", *calls)
	}
}

// A configuration may bind ports of its own: launchd, not compy's probe
// port, is the authority on whether the collector came up.
func TestActivateProbeFallsBackToLaunchdRunning(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens on compy's configured gRPC port

	setup(t, "state = running")
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
	if err := a.Activate("debug", ""); err != nil {
		t.Fatalf("Activate() = %v, want nil (launchd reports the job running)", err)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, "last-good", "settings.json")); err != nil {
		t.Errorf("no last-good snapshot after a launchd-confirmed start: %v", err)
	}
}
