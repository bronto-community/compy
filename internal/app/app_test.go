package app_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/collector"
	"github.com/bronto-community/compy/internal/distro"
	"github.com/bronto-community/compy/internal/envvars"
	"github.com/bronto-community/compy/internal/launchd"
	"github.com/bronto-community/compy/internal/state"
)

// setup points COMPY_HOME and HOME at temp dirs (HOME too: launchd.Install
// writes ~/Library/LaunchAgents/...), stubs launchd.Exec, and returns a
// pointer to the recorded launchctl invocations.
func setup(t *testing.T, printOut string) *[][]string {
	return setupStaged(t, printOut)
}

// setupStaged is setup with a scripted sequence of `launchctl print`
// outputs: the nth print call answers stages[n], the last stage repeating
// once the script runs out. Activate consults launchd after every probe —
// launchd, not the probe, is the authority on "up" — so a test whose
// scenario changes over time (running during the initial activation, down
// at the failing one, back up after the restore) stages the answers in
// activation order.
func setupStaged(t *testing.T, stages ...string) *[][]string {
	t.Helper()
	t.Setenv("COMPY_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	var calls [][]string
	prints := 0
	orig := launchd.Exec
	launchd.Exec = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) > 0 && args[0] == "print" {
			i := min(prints, len(stages)-1)
			prints++
			return []byte(stages[i]), nil
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
// records it as compy's gRPC port. closeListener stops it again, which is
// how a test makes the next activation fail its probe.
func listenPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	listeners[port] = ln

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

// listeners maps a port handed out by listenPort to its listener, so
// closeListener can stop it. Tests run in one process and never share a
// port, so a plain map is enough.
var listeners = map[int]net.Listener{}

func closeListener(t *testing.T, port int) {
	t.Helper()
	ln, ok := listeners[port]
	if !ok {
		t.Fatalf("no listener recorded for port %d", port)
	}
	ln.Close()
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

	// A configuration proven to have started is the setup a later failure
	// comes back to, so success is exactly when the snapshot is taken.
	if _, err := os.Stat(filepath.Join(a.Dir, "last-good", "settings.json")); err != nil {
		t.Errorf("no last-good snapshot after a successful activation: %v", err)
	}

	name, set, err := a.ActiveConfig()
	if err != nil || name != "mine" || set != "prod" {
		t.Errorf("ActiveConfig() = %q,%q,%v, want mine,prod", name, set, err)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Config != "mine" || st.Preset != "prod" || st.Distro != "fake" || st.GRPCPort != port {
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

func TestActivateUnknownPresetErrors(t *testing.T) {
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

func TestValidateConfig(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ValidateConfig("debug"); err != nil {
		t.Fatalf("ValidateConfig(debug) = %v, want nil", err)
	}
}

func TestValidateConfigFailureReturnsCollectorOutput(t *testing.T) {
	setup(t, "")
	fakeDistro(t, `echo "error decoding 'exporters': unknown type" >&2; exit 1`)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	err = a.ValidateConfig("debug")
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("ValidateConfig(debug) = %v, want the collector's output", err)
	}
}

// TestValidateConfigValidatesAnyConfig proves ValidateConfig checks name's
// own config, not the active one: it validates an inactive configuration
// while a different one is active.
func TestValidateConfigValidatesAnyConfig(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("other", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.ValidateConfig("other"); err != nil {
		t.Fatalf("ValidateConfig(other) = %v, want nil (validates any config, not just the active one)", err)
	}
}

// contribDef returns the shipped contrib definition (the out-of-the-box
// default collector).
func contribDef(t *testing.T) distro.Def {
	t.Helper()
	for _, d := range distro.Defs() {
		if d.Name == app.DefaultDistro {
			return d
		}
	}
	t.Fatalf("no shipped definition named %q", app.DefaultDistro)
	return distro.Def{}
}

// A fresh home never picked a distro: the first operation that needs a
// collector binary reaches for contrib automatically and downloads it — no
// `compy distro use` step. The fetch is stubbed; the test asserts the
// contrib release URL was requested.
func TestFreshHomeDefaultsToContrib(t *testing.T) {
	setup(t, "")

	a, err := app.New()
	if err != nil {
		t.Fatalf("New() = %v, want nil (a distro-less compy must still run)", err)
	}
	var fetched string
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		fetched = url
		return nil, 0, errors.New("stub: tests never download a real release")
	}
	if _, err := a.EnsureDistro("", nil); err == nil {
		t.Fatal("EnsureDistro() = nil, want the stub fetch error")
	}
	if !strings.Contains(fetched, "otelcol-contrib") {
		t.Fatalf("fetched %q, want the contrib release URL", fetched)
	}

	// The settings screen shows contrib as the in-use default, not a
	// nothing-selected state — without persisting anything.
	rows, err := a.Distros()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rows, func(r map[string]any) bool { return r["name"] == app.DefaultDistro })
	if i < 0 || rows[i]["selected"] != true {
		t.Fatalf("Distros() contrib row = %v, want selected", rows)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Distro != "" {
		t.Fatalf("settings.Distro = %q, want still empty (default is implicit, not persisted)", s.Distro)
	}
}

// `compy use debug` on a fresh home runs on contrib once it is installed —
// activation resolves the implicit default end to end. The binary is
// pre-placed at contrib's install path so nothing downloads.
func TestFreshHomeActivateRunsOnContrib(t *testing.T) {
	setup(t, "state = running")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	def := contribDef(t)
	dir := filepath.Join(a.Dir, "distros", def.Name+"-"+def.Version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, def.Binary), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatalf("Activate(debug) on a fresh home = %v, want nil", err)
	}
}

func TestDeleteActiveConfigErrors(t *testing.T) {
	setup(t, "state = running") // the initial Activate must find the job up
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
	calls := setup(t, "state = running")
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

func TestResetReactivatesWhenActive(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	_, shipped, err := cfgstore.Get(a.Dir, "debug")
	if err != nil {
		t.Fatal(err)
	}

	// A modified builtin that is NOT active resets without touching launchd.
	if err := a.WriteConfigYAML("otlp", "receivers: {}\n# edited\n"); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := a.Reset("otlp"); err != nil {
		t.Fatalf("Reset inactive builtin: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("resetting an inactive config re-applied: %v", *calls)
	}

	// The active one resets AND re-activates, like Resync does.
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteConfigYAML("debug", "receivers: {}\n# edited\n"); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := a.Reset("debug"); err != nil {
		t.Fatalf("Reset active builtin: %v", err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("resetting the active config did not re-activate: %v", *calls)
	}
	info, yaml, err := cfgstore.Get(a.Dir, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if yaml != shipped || info.Modified {
		t.Errorf("after Reset: modified=%v yaml=%q, want the shipped default back", info.Modified, yaml)
	}
}

func TestRenameConfigUpdatesSettingsAndRecent(t *testing.T) {
	// Running during the initial activation (Activate only succeeds when
	// launchd confirms), stopped by rename time: no re-apply expected.
	calls := setupStaged(t, "state = running", "")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("mine", ""); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if err := a.RenameConfig("mine", "renamed"); err != nil {
		t.Fatalf("RenameConfig: %v", err)
	}
	// Running() itself calls `launchctl print`; what must NOT happen is a
	// re-apply (bootstrap/kickstart) while the collector is stopped.
	if called(*calls, "bootstrap") || called(*calls, "kickstart") {
		t.Errorf("renaming while the collector is stopped re-applied: %v", *calls)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveConfig != "renamed" {
		t.Errorf("ActiveConfig = %q, want renamed", s.ActiveConfig)
	}
	if slices.Contains(s.Recent, "mine") || !slices.Contains(s.Recent, "renamed") {
		t.Errorf("Recent = %v, want the rename followed", s.Recent)
	}
	if _, _, err := cfgstore.Get(a.Dir, "renamed"); err != nil {
		t.Errorf("renamed config missing: %v", err)
	}
	if _, _, err := cfgstore.Get(a.Dir, "mine"); err == nil {
		t.Error("old name still resolves after rename")
	}
}

func TestRenameRunningConfigReapplies(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("mine", ""); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if err := a.RenameConfig("mine", "renamed"); err != nil {
		t.Fatalf("RenameConfig: %v", err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("renaming the running config did not re-apply: %v", *calls)
	}
	plist := readPlist(t)
	want := filepath.Join(a.Dir, "configs", "renamed", "config.yaml")
	if !strings.Contains(plist, "<string>"+want+"</string>") {
		t.Errorf("plist still points at the old path:\n%s", plist)
	}
}

func TestReplacePresetReactivatesWhenActivePreset(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("other", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetVar("other", "prod", "K", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	// Replacing a preset on an inactive config must not re-apply.
	if err := a.ReplacePreset("other", "prod", map[string]string{"K": "v2"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("replacing a preset on an inactive config re-applied: %v", *calls)
	}

	// Give "debug" a preset and activate it so it becomes the active config
	// AND active preset together.
	if err := a.SetVar("debug", "prod", "K", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", "prod"); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if err := a.ReplacePreset("debug", "prod", map[string]string{"K": "v2"}); err != nil {
		t.Fatalf("ReplacePreset: %v", err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("replacing the active preset did not re-activate: %v", *calls)
	}
	info, _, err := cfgstore.Get(a.Dir, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.Presets["prod"]["K"] != "v2" {
		t.Errorf("Presets = %+v, want K=v2", info.Meta.Presets)
	}

	*calls = nil
	// Replacing a *different, non-active* preset on the active config must
	// not re-apply.
	if err := a.ReplacePreset("debug", "staging", map[string]string{"K": "v1"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("replacing a non-active preset on the active config re-applied: %v", *calls)
	}
}

func TestUpdateConfigMetaRemoteURL(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}

	url := "https://example.com/c.yaml"
	if err := a.UpdateConfigMeta("mine", &url); err != nil {
		t.Fatalf("UpdateConfigMeta: %v", err)
	}
	info, _, err := cfgstore.Get(a.Dir, "mine")
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.RemoteURL != url {
		t.Errorf("RemoteURL = %q, want %q", info.Meta.RemoteURL, url)
	}

	// nil leaves it alone; "" clears it.
	if err := a.UpdateConfigMeta("mine", nil); err != nil {
		t.Fatalf("UpdateConfigMeta(nil): %v", err)
	}
	info, _, _ = cfgstore.Get(a.Dir, "mine")
	if info.Meta.RemoteURL != url {
		t.Errorf("RemoteURL = %q after a nil update, want unchanged", info.Meta.RemoteURL)
	}
	empty := ""
	if err := a.UpdateConfigMeta("mine", &empty); err != nil {
		t.Fatalf("UpdateConfigMeta(clear): %v", err)
	}
	info, _, _ = cfgstore.Get(a.Dir, "mine")
	if info.Meta.RemoteURL != "" {
		t.Errorf("RemoteURL = %q, want cleared", info.Meta.RemoteURL)
	}
}

func TestUpdateConfigMetaReactivatesWhenActive(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	url := "https://example.com/c.yaml"
	if err := a.UpdateConfigMeta("debug", &url); err != nil {
		t.Fatalf("UpdateConfigMeta: %v", err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("updating the active config's meta did not re-activate: %v", *calls)
	}
}

func TestGetPutSettings(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}

	s, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.GRPCPort != 14317 || s.HTTPPort != 14318 {
		t.Fatalf("GetSettings() = %+v, want default ports", s)
	}

	grpc := 5000
	if err := a.PutSettings(&grpc, nil, nil); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	s, err = a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.GRPCPort != 5000 || s.HTTPPort != 14318 {
		t.Errorf("GetSettings() after partial PutSettings = %+v", s)
	}

	bad := 70000
	if err := a.PutSettings(&bad, nil, nil); err == nil {
		t.Fatal("PutSettings with out-of-range port: want error, got nil")
	}
	s, err = a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.GRPCPort != 5000 {
		t.Errorf("GRPCPort = %d after rejected update, want unchanged 5000", s.GRPCPort)
	}

	zero := 0
	if err := a.PutSettings(nil, &zero, nil); err == nil {
		t.Fatal("PutSettings with port 0: want error, got nil")
	}

	// Protocol: exactly grpc, http/protobuf, http/json; anything else is the
	// caller's mistake, and the stored value round-trips.
	if s.EffectiveProtocol() != "http/protobuf" {
		t.Errorf("default EffectiveProtocol() = %q, want http/protobuf", s.EffectiveProtocol())
	}
	for _, p := range []string{"grpc", "http/protobuf", "http/json"} {
		p := p
		if err := a.PutSettings(nil, nil, &p); err != nil {
			t.Fatalf("PutSettings(protocol=%q): %v", p, err)
		}
		s, err = a.GetSettings()
		if err != nil {
			t.Fatal(err)
		}
		if s.Protocol != p {
			t.Errorf("Protocol after set = %q, want %q", s.Protocol, p)
		}
	}
	for _, p := range []string{"", "http", "HTTP/PROTOBUF", "grpc "} {
		p := p
		err := a.PutSettings(nil, nil, &p)
		if err == nil || !state.IsBadRequest(err) {
			t.Errorf("PutSettings(protocol=%q) = %v, want a BadRequest", p, err)
		}
	}
	s, err = a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Protocol != "http/json" {
		t.Errorf("Protocol = %q after rejected updates, want unchanged http/json", s.Protocol)
	}
}

func TestEnvInfo(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	vars, script, err := a.EnvInfo()
	if err != nil {
		t.Fatal(err)
	}
	if vars["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:14318" {
		t.Errorf("vars = %+v", vars)
	}
	if !strings.Contains(script, "export OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:14318'") {
		t.Errorf("script = %q", script)
	}
}

func TestEnsureDistro(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnsureDistro("fake", nil); err != nil {
		t.Fatalf("EnsureDistro(fake): %v", err)
	}
	if _, err := a.EnsureDistro("no-such-distro", nil); err == nil {
		t.Fatal("EnsureDistro(no-such-distro): want error, got nil")
	}
}

// TestDistrosUserEntry covers the "user_entry" field Distros() rows carry:
// false for a shipped definition that's merely been downloaded to its
// default path (no distros.json entry — DELETE would 400), true once that
// name is overridden with SetDistroPath (a real distros.json entry).
func TestDistrosUserEntry(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}

	def := distro.Defs()[0] // "core"
	binDir := filepath.Join(a.Dir, "distros", def.Name+"-"+def.Version)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, def.Binary)
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	findRow := func(rows []map[string]any, name string) map[string]any {
		for _, r := range rows {
			if r["name"] == name {
				return r
			}
		}
		t.Fatalf("no row named %q in %v", name, rows)
		return nil
	}

	rows, err := a.Distros()
	if err != nil {
		t.Fatal(err)
	}
	row := findRow(rows, def.Name)
	if row["downloaded"] != true {
		t.Fatalf("downloaded = %v, want true (binary present at default path)", row["downloaded"])
	}
	if row["user_entry"] != false {
		t.Fatalf("user_entry = %v, want false (downloaded but not overridden)", row["user_entry"])
	}

	if _, err := a.SetDistroPath(def.Name, binPath); err != nil {
		t.Fatalf("SetDistroPath: %v", err)
	}
	rows, err = a.Distros()
	if err != nil {
		t.Fatal(err)
	}
	row = findRow(rows, def.Name)
	if row["user_entry"] != true {
		t.Fatalf("user_entry = %v after SetDistroPath, want true", row["user_entry"])
	}
}

func TestAddDistroWarning(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if w := a.AddDistroWarning("core"); w == "" || !strings.Contains(w, "core") || !strings.Contains(w, "overrides") {
		t.Errorf("AddDistroWarning(core) = %q, want an override warning", w)
	}
	if w := a.AddDistroWarning("brand-new-name"); w != "" {
		t.Errorf("AddDistroWarning(brand-new-name) = %q, want empty", w)
	}
}

// TestSetDistroPath covers registering a brand-new distro path, updating an
// existing entry's path in place (no duplicate), the shipped-definition
// override warning, and the invalid-name/invalid-path error cases.
func TestSetDistroPath(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	bin1 := filepath.Join(t.TempDir(), "otelcol1")
	if err := os.WriteFile(bin1, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin2 := filepath.Join(t.TempDir(), "otelcol2")
	if err := os.WriteFile(bin2, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// "core" is a shipped distro.Defs() name: overriding it warns.
	warning, err := a.SetDistroPath("core", bin1)
	if err != nil {
		t.Fatalf("SetDistroPath(core, bin1): %v", err)
	}
	if warning == "" || !strings.Contains(warning, "core") {
		t.Fatalf("warning = %q, want an override warning naming core", warning)
	}
	distros, err := state.LoadDistros()
	if err != nil {
		t.Fatal(err)
	}
	if len(distros) != 1 || distros[0].Name != "core" || distros[0].Path != bin1 {
		t.Fatalf("distros = %+v, want a single core entry pointing at bin1", distros)
	}

	// Updating the same name's path replaces it in place, no duplicate.
	if _, err := a.SetDistroPath("core", bin2); err != nil {
		t.Fatalf("SetDistroPath(core, bin2): %v", err)
	}
	distros, err = state.LoadDistros()
	if err != nil {
		t.Fatal(err)
	}
	if len(distros) != 1 || distros[0].Path != bin2 {
		t.Fatalf("distros = %+v, want the single core entry updated to bin2", distros)
	}

	// A brand-new name: no warning, becomes the default (none selected yet).
	warning, err = a.SetDistroPath("brand-new", bin1)
	if err != nil {
		t.Fatalf("SetDistroPath(brand-new): %v", err)
	}
	if warning != "" {
		t.Fatalf("warning = %q, want empty for a brand-new name", warning)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Distro != "core" {
		t.Fatalf("settings.Distro = %q, want core (already selected by the first SetDistroPath)", s.Distro)
	}

	wantMsg := `invalid distro name "Bad Name!": use lowercase letters, digits, dashes`
	if _, err := a.SetDistroPath("Bad Name!", bin1); err == nil || !state.IsBadRequest(err) || err.Error() != wantMsg {
		t.Fatalf("SetDistroPath with an invalid name: err=%v, want a state.BadRequest-marked error %q", err, wantMsg)
	}
	if _, err := a.SetDistroPath("whatever", filepath.Join(t.TempDir(), "missing")); err == nil || !state.IsBadRequest(err) {
		t.Fatalf("SetDistroPath with a nonexistent path: err=%v, want a state.BadRequest-marked error", err)
	}

	if err := a.AddDistro("Bad Name!", bin1); err == nil || !state.IsBadRequest(err) || err.Error() != wantMsg {
		t.Fatalf("AddDistro with an invalid name: err=%v, want a state.BadRequest-marked error %q", err, wantMsg)
	}
}

// TestRemoveDistro covers removing a plain user entry (reverted:false),
// removing a shipped-definition override (reverted:true), and the two
// state.BadRequest-marked 400 cases: the selected distro, and a pure
// definition name with no user entry.
func TestRemoveDistro(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0") // registers + selects "fake"
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "otelcol")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := a.AddDistro("extra", bin); err != nil {
		t.Fatalf("AddDistro(extra): %v", err)
	}
	reverted, err := a.RemoveDistro("extra")
	if err != nil {
		t.Fatalf("RemoveDistro(extra): %v", err)
	}
	if reverted {
		t.Fatal("RemoveDistro(extra) reverted = true, want false (no shipped definition named extra)")
	}
	distros, err := state.LoadDistros()
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(distros, func(d state.Distro) bool { return d.Name == "extra" }) {
		t.Fatalf("distros = %+v, extra should be gone", distros)
	}

	if err := a.AddDistro("core", bin); err != nil { // "core" is a shipped definition
		t.Fatalf("AddDistro(core): %v", err)
	}
	reverted, err = a.RemoveDistro("core")
	if err != nil {
		t.Fatalf("RemoveDistro(core): %v", err)
	}
	if !reverted {
		t.Fatal("RemoveDistro(core) reverted = false, want true (core is a shipped definition)")
	}

	if _, err := a.RemoveDistro("fake"); err == nil || !state.IsBadRequest(err) {
		t.Fatalf("RemoveDistro(fake) [selected]: err=%v, want a state.BadRequest-marked error", err)
	}
	if _, err := a.RemoveDistro("contrib"); err == nil || !state.IsBadRequest(err) {
		t.Fatalf("RemoveDistro(contrib) [pure definition, no user entry]: err=%v, want a state.BadRequest-marked error", err)
	}
}

func TestRenamePresetApp(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("cfg", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetVar("cfg", "prod", "HOST", "example.com"); err != nil {
		t.Fatal(err)
	}
	if err := a.UsePreset("cfg", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := a.RenamePreset("cfg", "prod", "production"); err != nil {
		t.Fatalf("RenamePreset: %v", err)
	}
	info, _, err := a.Config("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.ActivePreset != "production" || info.Meta.Presets["production"]["HOST"] != "example.com" {
		t.Fatalf("info.Meta = %+v, want the active set renamed with its values intact", info.Meta)
	}

	if err := a.RenamePreset("cfg", "no-such-preset", "x"); err == nil {
		t.Fatal("RenameSet from a nonexistent set: want error, got nil")
	}
}

func TestLog(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.LogPath(), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := a.Log(2)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if got != "two\nthree\n" {
		t.Fatalf("Log(2) = %q, want the last two lines", got)
	}
}

// TestLogStats covers a synthetic zap-shaped log: it must count by the level
// token (2nd tab-separated field, tolerating a space-delimited log), not by
// substring — a message containing the word "error" at level info must not
// count — and a missing log file must report zero, zero, nil.
func TestLogStats(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}

	errs, warns, err := a.LogStats(500)
	if err != nil || errs != 0 || warns != 0 {
		t.Fatalf("LogStats with no log file = %d, %d, %v, want 0, 0, nil", errs, warns, err)
	}

	log := strings.Join([]string{
		"2026-08-25T14:32:01.000+0200\tinfo\tservice@v0.135.0/service.go:1\tstarting",
		"2026-08-25T14:32:02.000+0200\twarn\tservice@v0.135.0/service.go:2\tqueue nearly full",
		"2026-08-25T14:32:03.000+0200\terror\tservice@v0.135.0/service.go:3\texporter send failed",
		"2026-08-25T14:32:04.000+0200\tinfo\tservice@v0.135.0/service.go:4\tan error occurred downstream, ignore",
		"2026-08-25T14:32:05.000+0200 error service@v0.135.0/service.go:5 space-delimited line",
		"",
	}, "\n")
	if err := os.MkdirAll(filepath.Dir(a.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.LogPath(), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}

	errs, warns, err = a.LogStats(500)
	if err != nil {
		t.Fatalf("LogStats: %v", err)
	}
	if errs != 2 || warns != 1 {
		t.Fatalf("LogStats = errors=%d warnings=%d, want 2, 1 (info line mentioning \"error\" must not count)", errs, warns)
	}
}

// TestLogStatsCountsSinceLastStart: the tray's attention icon must reflect
// the CURRENT collector session, not history — a persisted log keeps
// yesterday's errors in the tail window forever, which latched the icon
// permanently. Only lines after the last "Starting otelcol" startup marker
// count; a window with no marker (long-running collector, marker scrolled
// out) still counts in full — TestLogStats above pins that case.
func TestLogStatsCountsSinceLastStart(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	// Real marker shape, verified live against otelcol v0.135.0.
	start := "2026-08-26T16:52:57.549+0200\tinfo\tservice@v0.135.0/service.go:211\tStarting otelcol...\t{\"Version\": \"v0.135.0\"}"
	old := []string{
		"2026-08-25T14:32:02.000+0200\twarn\tservice.go:2\tyesterday's warning",
		"2026-08-25T14:32:03.000+0200\terror\tservice.go:3\tyesterday's failure",
	}

	write := func(lines ...string) {
		if err := os.WriteFile(a.LogPath(), []byte(strings.Join(append(lines, ""), "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Old errors before the marker, clean since: all zero.
	write(old[0], old[1], start)
	if errs, warns, err := a.LogStats(500); err != nil || errs != 0 || warns != 0 {
		t.Fatalf("clean since restart: LogStats = %d, %d, %v, want 0, 0, nil", errs, warns, err)
	}

	// Errors after the marker count; the old ones still don't.
	write(old[0], old[1], start,
		"2026-08-26T16:53:01.000+0200\terror\tservice.go:9\texporter send failed",
		"2026-08-26T16:53:02.000+0200\twarn\tservice.go:9\tqueue nearly full")
	if errs, warns, err := a.LogStats(500); err != nil || errs != 1 || warns != 1 {
		t.Fatalf("errors since restart: LogStats = %d, %d, %v, want 1, 1, nil", errs, warns, err)
	}

	// Two markers: only lines after the LAST one count.
	write(start,
		"2026-08-26T10:00:00.000+0200\terror\tservice.go:9\tfirst session failure",
		start)
	if errs, warns, err := a.LogStats(500); err != nil || errs != 0 || warns != 0 {
		t.Fatalf("after second restart: LogStats = %d, %d, %v, want 0, 0, nil", errs, warns, err)
	}
}

func TestActivateStartupFailureReportsTheLog(t *testing.T) {
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

// TestActivateFailureLeadsWithTheBusyPort: when the log tail shows an
// "address already in use" failure, the busy port — not compy's probe
// port — is the headline; the probe detail and the tail follow.
func TestActivateFailureLeadsWithTheBusyPort(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens: the probe fails
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
	tail := `Error: cannot start pipelines: failed to start "otlp" receiver: listen tcp 127.0.0.1:16317: bind: address already in use` + "\n"
	if err := os.WriteFile(a.LogPath(), []byte(tail), 0o600); err != nil {
		t.Fatal(err)
	}

	err = a.Activate("debug", "")
	if err == nil {
		t.Fatal("Activate() = nil, want a startup failure")
	}
	if !strings.HasPrefix(err.Error(), "port 16317 is already in use by another process") {
		t.Errorf("error = %q, want it to LEAD with the busy port", err)
	}
	if !strings.Contains(err.Error(), "collector did not come up") {
		t.Errorf("error = %q, want the probe detail kept after the headline", err)
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

func TestAddDistroWarnsOnDefinitionNameCollision(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "otelcol")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	addErr := a.AddDistro("core", bin) // "core" is a shipped distro.Defs() name
	w.Close()
	os.Stderr = orig
	out, _ := io.ReadAll(r)

	if addErr != nil {
		t.Fatalf("AddDistro: %v", addErr)
	}
	if !strings.Contains(string(out), "core") || !strings.Contains(string(out), "overrides") {
		t.Errorf("stderr = %q, want a warning that %q overrides the shipped definition", out, "core")
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

func TestMigrationRawModeFallsBackToCustomYAML(t *testing.T) {
	setup(t, "")
	dir := os.Getenv("COMPY_HOME")
	writeLegacyTree(t, dir, filepath.Join(t.TempDir(), "does-not-exist"), nil, 1)
	// raw_mode: true with a custom.yaml the fallback must prefer over base.yaml.
	if err := os.WriteFile(filepath.Join(dir, "config", "custom.yaml"), []byte("custom: marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf(`{"grpc_port":1,"http_port":14318,"distro":"fake","enabled":[],"raw_mode":true}`)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := app.New(); err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	_, yaml, err := cfgstore.Get(dir, "migrated")
	if err != nil {
		t.Fatalf("migrated config missing: %v", err)
	}
	if !strings.Contains(yaml, "custom: marker") {
		t.Errorf("migrated yaml = %q, want a copy of custom.yaml (raw_mode fallback)", yaml)
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
}

// A stale v1 process (the old tray) can recreate config/ AFTER a completed
// migration. Re-running migration then must not touch launchd, settings, the
// migrated config, or the existing archive — observed live 2026-08-25.
func TestMigrationIgnoresStaleLegacyRecreation(t *testing.T) {
	calls := setup(t, "state = running")
	dir := os.Getenv("COMPY_HOME")

	// Completed v2 state: migrated config exists and is active.
	if err := cfgstore.Create(dir, "migrated", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	s, _ := state.LoadSettings()
	s.ActiveConfig = "migrated"
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	// The real archive from the original migration, which must survive.
	if err := os.MkdirAll(filepath.Join(dir, "legacy-v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy-v1", "base.yaml"), []byte("original archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stale leftovers as the old tray recreates them.
	if err := os.MkdirAll(filepath.Join(dir, "config", "backends"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "base.yaml"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := app.New(); err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}

	for _, c := range *calls {
		t.Errorf("launchctl must not be called for stale leftovers, got %v", c)
	}
	s2, _ := state.LoadSettings()
	if s2.ActiveConfig != "migrated" {
		t.Errorf("ActiveConfig = %q, want migrated", s2.ActiveConfig)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "legacy-v1", "base.yaml")); err != nil || string(data) != "original archive" {
		t.Errorf("original archive clobbered: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); !os.IsNotExist(err) {
		t.Errorf("stale config/ tree not archived away: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "legacy-v1.2", "base.yaml")); err != nil || string(data) != "stale" {
		t.Errorf("stale leftovers not archived to unique dir: %q %v", data, err)
	}
}

// Variant of the stale-recreation guard: ActiveConfig may be "" after a
// nothing-was-enabled migration; the existing migrated config is the second
// staleness signal.
func TestMigrationStaleGuardFiresWithoutActiveConfig(t *testing.T) {
	calls := setup(t, "state = running")
	dir := os.Getenv("COMPY_HOME")

	if err := cfgstore.Create(dir, "migrated", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config", "backends"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := app.New(); err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	for _, c := range *calls {
		t.Errorf("launchctl must not be called, got %v", c)
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); !os.IsNotExist(err) {
		t.Errorf("stale tree not archived: %v", err)
	}
}

// TestUserMistakesAreBadRequests locks in that every failure a person can
// cause from the UI or CLI — a bad name, a missing or duplicate config, a
// config the collector rejects, a distro that isn't there — is marked
// state.BadRequest, so the REST layer answers 400. The web UI appends a
// collector log tail only to a 5xx (a real fault of ours); a user mistake
// answered 500 buries its own message under an irrelevant log dump, which
// is exactly what the 2026-08-25 UI feedback reported.
func TestUserMistakesAreBadRequests(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("other", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetVar("mine", "dev", "K", "v"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("mine", "dev"); err != nil {
		t.Fatal(err)
	}
	badBin := filepath.Join(t.TempDir(), "broken")
	if err := os.WriteFile(badBin, []byte("#!/bin/sh\necho 'unknown type: \"nope\"' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetDistroPath("broken", badBin); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	nosuch := "nosuch"
	cases := []struct {
		name string
		fn   func() error
	}{
		{"CreateConfig invalid name", func() error { return a.CreateConfig("Bad Name", "") }},
		{"CreateConfig duplicate", func() error { return a.CreateConfig("mine", "") }},
		{"CreateFromURL invalid name", func() error { return a.CreateFromURL("Bad Name", "http://x/y") }},
		{"CopyConfig invalid dst", func() error { return a.CopyConfig("mine", "Bad Name") }},
		{"CopyConfig existing dst", func() error { return a.CopyConfig("mine", "other") }},
		{"CopyConfig unknown src", func() error { return a.CopyConfig(nosuch, "fresh") }},
		{"DeleteConfig unknown", func() error { return a.DeleteConfig(nosuch) }},
		{"DeleteConfig active", func() error { return a.DeleteConfig("mine") }},
		{"WriteConfigYAML unknown", func() error { return a.WriteConfigYAML(nosuch, "") }},
		{"UpdateConfigMeta unknown config", func() error { return a.UpdateConfigMeta(nosuch, nil) }},
		{"Activate unknown config", func() error { return a.Activate(nosuch, "") }},
		{"Activate unknown preset", func() error { return a.Activate("mine", nosuch) }},
		{"ValidateConfig unknown config", func() error { return a.ValidateConfig(nosuch) }},
		{"Sync non-remote", func() error { return a.Sync("mine") }},
		{"Resync non-remote", func() error { return a.Resync("mine") }},
		{"Reset non-builtin", func() error { return a.Reset("mine") }},
		{"Reset unmodified builtin", func() error { return a.Reset("debug") }},
		{"RenameConfig unknown", func() error { return a.RenameConfig(nosuch, "fresh") }},
		{"RenameConfig existing target", func() error { return a.RenameConfig("mine", "other") }},
		{"RenameConfig invalid target", func() error { return a.RenameConfig("mine", "Bad Name") }},
		{"ReplaceSet unknown config", func() error { return a.ReplacePreset(nosuch, "dev", nil) }},
		{"ReplaceSet invalid set name", func() error { return a.ReplacePreset("mine", "Bad Preset", nil) }},
		{"UseSet unknown set", func() error { return a.UsePreset("other", nosuch) }},
		{"DeleteSet unknown set", func() error { return a.DeletePreset("mine", nosuch) }},
		{"DeletePreset active preset", func() error { return a.DeletePreset("mine", "dev") }},
		{"RenamePreset unknown preset", func() error { return a.RenamePreset("mine", nosuch, "fresh") }},
		{"RenamePreset invalid target", func() error { return a.RenamePreset("mine", "dev", "Bad Preset") }},
		{"SetVar invalid preset name", func() error { return a.SetVar("mine", "Bad Preset", "K", "v") }},
		{"AddDistro duplicate", func() error { return a.AddDistro("fake", missing) }},
		{"AddDistro missing binary", func() error { return a.AddDistro("fresh", missing) }},
		{"UseDistro unknown", func() error { return a.UseDistro(nosuch) }},
		{"EnsureDistro unknown", func() error { _, err := a.EnsureDistro(nosuch, nil); return err }},
		{"PutSettings port out of range", func() error { p := 99999; return a.PutSettings(&p, nil, nil) }},
		{"PutSettings unknown protocol", func() error { p := "http/proto"; return a.PutSettings(nil, nil, &p) }},
	}
	for _, tc := range cases {
		err := tc.fn()
		if err == nil {
			t.Errorf("%s: err = nil, want an error", tc.name)
			continue
		}
		if !state.IsBadRequest(err) {
			t.Errorf("%s: err = %v, want it marked state.BadRequest (400, no collector log tail)", tc.name, err)
		}
	}

	// A configuration the collector itself rejects is the user's YAML being
	// wrong, not our fault: the collector's diagnostics are the whole answer.
	// (The reject is faked by pointing the one collector at the failing
	// binary registered above.)
	sel, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	sel.Distro = "broken"
	if err := state.SaveSettings(sel); err != nil {
		t.Fatal(err)
	}
	if err := a.ValidateConfig("other"); err == nil || !state.IsBadRequest(err) {
		t.Errorf("ValidateConfig with a config the collector rejects: err = %v, want state.BadRequest-marked", err)
	}
	if err := a.Activate("other", ""); err == nil || !state.IsBadRequest(err) {
		t.Errorf("Activate with a config the collector rejects: err = %v, want state.BadRequest-marked", err)
	}
}

// TestBadRequestSurvivesWrapping guards the marker against the commonest way
// to lose it: a caller adding context with fmt.Errorf("%w").
func TestBadRequestSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("activating mine: %w", state.BadRequest(errors.New("boom")))
	if !state.IsBadRequest(wrapped) {
		t.Errorf("IsBadRequest(wrapped) = false, want true")
	}
	if !errors.Is(state.BadRequest(os.ErrNotExist), os.ErrNotExist) {
		t.Errorf("BadRequest must not hide the error it marks from errors.Is")
	}
}

// TestUseDistroReportsApplyFailureWithoutLosingTheSelection covers the
// 2026-08-25 report that a self-added collector "can only be removed":
// selecting one whose binary rejects the active configuration used to
// return the collector's bare diagnostics as a 500 (log tail and all),
// which reads as "picking my own collector failed" even though the default
// had in fact been switched. The default sticks — a global default is not
// hostage to one configuration — and the error says exactly that.
func TestUseDistroReportsApplyFailureWithoutLosingTheSelection(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("mine", ""); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(t.TempDir(), "otelcol")
	if err := os.WriteFile(mine, []byte("#!/bin/sh\necho 'unknown type: \"nope\"' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.AddDistro("mine-own", mine); err != nil {
		t.Fatal(err)
	}

	err = a.UseDistro("mine-own")
	if err == nil {
		t.Fatal("UseDistro = nil, want the apply failure reported")
	}
	if !state.IsBadRequest(err) {
		t.Errorf("UseDistro error = %v, want state.BadRequest-marked (no collector log tail)", err)
	}
	if !strings.Contains(err.Error(), "mine-own") || !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("UseDistro error = %q, want it to name the distro and carry the collector's own diagnostics", err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.Distro != "mine-own" {
		t.Errorf("settings.Distro = %q after a failed apply, want mine-own: the default is the user's choice, not the active config's", s.Distro)
	}
}

// TestGenuineFaultsStay500 pins the other half of the classification: a
// failure that is *ours* — an unwritable state directory, launchctl
// refusing to load the job — must NOT be BadRequest-marked. Without this,
// a future "make more things 400" sweep would quietly turn every server
// fault into a 400 and take the collector-log tail (the only diagnostic the
// UI shows for a real fault) with it.
func TestGenuineFaultsStay500(t *testing.T) {
	t.Run("unwritable config dir", func(t *testing.T) {
		setup(t, "state = running")
		fakeDistro(t, "exit 0")
		a, err := app.New()
		if err != nil {
			t.Fatal(err)
		}
		if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(a.Dir, "configs", "mine")
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })

		err = a.WriteConfigYAML("mine", "receivers: {}\nexporters: {}\n")
		if err == nil {
			t.Fatal("WriteConfigYAML into an unwritable dir = nil, want a write error")
		}
		if state.IsBadRequest(err) {
			t.Errorf("WriteConfigYAML err = %v, marked BadRequest — an unwritable state dir is our fault, not the caller's", err)
		}
	})

	t.Run("launchctl failure on activate", func(t *testing.T) {
		setup(t, "state = running")
		fakeDistro(t, "exit 0")
		orig := launchd.Exec
		launchd.Exec = func(args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "bootstrap" {
				return []byte("Load failed: 5: Input/output error"), errors.New("exit status 5")
			}
			if len(args) > 0 && args[0] == "print" {
				return []byte("state = running"), nil
			}
			return nil, nil
		}
		t.Cleanup(func() { launchd.Exec = orig })

		a, err := app.New()
		if err != nil {
			t.Fatal(err)
		}
		if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
			t.Fatal(err)
		}
		err = a.Activate("mine", "")
		if err == nil {
			t.Fatal("Activate with a failing launchctl = nil, want the bootstrap error")
		}
		if state.IsBadRequest(err) {
			t.Errorf("Activate err = %v, marked BadRequest — launchctl refusing the job is our fault, not the caller's", err)
		}

		// Same fault reached through UseDistro must not be re-labelled either:
		// UseDistro's "does not run with it" wrap only fits a user mistake.
		bin := filepath.Join(t.TempDir(), "otelcol")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := a.AddDistro("second", bin); err != nil {
			t.Fatal(err)
		}
		err = a.UseDistro("second")
		if err == nil {
			t.Fatal("UseDistro with a failing launchctl = nil, want the bootstrap error")
		}
		if state.IsBadRequest(err) {
			t.Errorf("UseDistro err = %v, marked BadRequest — the apply failed on launchctl, not on the user's input", err)
		}
		if strings.Contains(err.Error(), "does not run with it") {
			t.Errorf("UseDistro err = %q: that sentence claims the config is incompatible, which is not what happened", err)
		}
	})
}

// TestActivateStartupFailureRestoresPrevious is the design's failure
// guarantee (docs/design/handoff/README.md, "On failure"): a configuration
// the collector accepts but cannot start puts the previous configuration —
// and its preset — back, and the error names what is still running so the UI
// can say so.
func TestActivateStartupFailureRestoresPrevious(t *testing.T) {
	// Running for the initial activation only; down at the failing
	// activation's check AND after the restore — the restore also died.
	calls := setupStaged(t, "state = running", "")
	fakeDistro(t, "exit 0")
	port := listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetVar("debug", "prod", "K", "v"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", "prod"); err != nil {
		t.Fatalf("Activate(debug, prod): %v", err)
	}
	if err := a.CreateConfig("other", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.LogPath(), []byte("boom: cannot start\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Nothing listens on the probe port from here on, so "other" validates
	// but never comes up.
	closeListener(t, port)
	*calls = nil

	err = a.Activate("other", "")
	if err == nil {
		t.Fatal("Activate(other) = nil, want a startup failure")
	}
	if state.IsBadRequest(err) {
		t.Errorf("startup failure marked BadRequest (400); a collector that won't start is a 500: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot start") {
		t.Errorf("error = %q, want the collector's own log tail", err)
	}
	// launchd reports nothing running after the failure, so the error must
	// not claim otherwise; TestStillRunningOnlyWhenItActuallyIs covers the
	// branch where the restore does come back up.
	var sr interface{ StillRunning() string }
	if errors.As(err, &sr) {
		t.Errorf("error claims %q still running while launchd reports nothing", sr.StillRunning())
	}
	// A restore that was attempted and also died must be said out loud, not
	// left as a silent "cfg-b failed" next to a status naming cfg-a.
	if !strings.Contains(err.Error(), "did not start either") {
		t.Errorf("error = %q, want it to say the restored setup did not start either", err)
	}

	// The previous configuration and preset are the active ones again...
	name, preset, err := a.ActiveConfig()
	if err != nil {
		t.Fatal(err)
	}
	if name != "debug" || preset != "prod" {
		t.Errorf("active = %q/%q after the failure, want debug/prod", name, preset)
	}
	// ...and it is what the LaunchAgent was left pointing at.
	if plist := readPlist(t); !strings.Contains(plist, filepath.Join("configs", "debug", "config.yaml")) {
		t.Errorf("plist still points at the failed config:\n%s", plist)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("the previous configuration was not put back: %v", *calls)
	}
}

// TestActivateValidateFailureChangesNothing: a config the collector rejects
// never reaches launchd, so there is nothing to restore.
func TestActivateValidateFailureChangesNothing(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("other", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}

	// Point the one collector at a binary that rejects everything.
	rejecting := filepath.Join(t.TempDir(), "otelcol")
	if err := os.WriteFile(rejecting, []byte("#!/bin/sh\necho 'unknown type' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetDistroPath("fake", rejecting); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	err = a.Activate("other", "")
	if err == nil || !state.IsBadRequest(err) {
		t.Fatalf("Activate with a rejected config: err = %v, want a BadRequest-marked error", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a rejected config touched launchd: %v", *calls)
	}
	if name, _, _ := a.ActiveConfig(); name != "debug" {
		t.Errorf("active config = %q after a rejected activation, want unchanged debug", name)
	}
}

func TestStopAndStart(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !called(*calls, "bootout") {
		t.Errorf("Stop did not boot the job out: %v", *calls)
	}
	// Stopping records nothing: the active configuration is still named, so
	// the UI can dim it rather than forget it.
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Config != "debug" {
		t.Errorf("Status().Config = %q after Stop, want debug", st.Config)
	}
	// Stopping an already-stopped collector is not an error.
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop (already stopped): %v", err)
	}

	*calls = nil
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("Start did not bring the job back: %v", *calls)
	}
}

// TestDownloadProgressTracksAFetch: the Settings screen POSTs a fetch and
// then polls, so the fetch must return immediately and the progress route
// must go idle → downloading → done (or failed) on its own.
func TestDownloadProgressTracksAFetch(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}

	if got := progressStatus(t, a, "core"); got != "idle" {
		t.Errorf("status before any fetch = %q, want idle", got)
	}

	// A distro nobody has heard of: the failure has to surface through the
	// progress route, since the POST that started it is long gone.
	if err := a.StartFetchDistro("no-such-distro"); err != nil {
		t.Fatalf("StartFetchDistro returned an error instead of starting: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		if status = progressStatus(t, a, "no-such-distro"); status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	p := progressOf(t, a, "no-such-distro")
	if msg, _ := p["error"].(string); !strings.Contains(msg, "no-such-distro") {
		t.Errorf("progress = %v, want an error naming the distro", p)
	}

	// A user-registered binary is already there: fetching it is instantly done.
	fakeDistro(t, "exit 0")
	if err := a.StartFetchDistro("fake"); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		if status = progressStatus(t, a, "fake"); status == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "done" {
		t.Fatalf("status for an already-present binary = %q, want done", status)
	}
	if pct := progressOf(t, a, "fake")["pct"]; pct != 100 {
		t.Errorf("pct = %v when done, want 100", pct)
	}
}

func progressOf(t *testing.T, a *app.App, name string) map[string]any {
	t.Helper()
	p, err := a.DownloadProgress(name)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := p.(map[string]any)
	if !ok {
		t.Fatalf("DownloadProgress(%q) = %T, want a map", name, p)
	}
	return m
}

func progressStatus(t *testing.T, a *app.App, name string) string {
	t.Helper()
	s, _ := progressOf(t, a, name)["status"].(string)
	return s
}

// TestRecencyFollowsActivations: the menu bar orders configurations by when
// they last ran, most recent first, and only successful activations count.
func TestRecencyFollowsActivations(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"debug", "otlp", "bronto", "debug"} {
		if err := a.Activate(name, ""); err != nil {
			t.Fatalf("Activate(%s): %v", name, err)
		}
	}

	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"debug", "bronto", "otlp"}
	if !slices.Equal(st.Recent, want) {
		t.Errorf("Recent = %v, want %v (most recent first, each config once)", st.Recent, want)
	}

	// A failed activation does not enter the list.
	if err := a.Activate("no-such-config", ""); err == nil {
		t.Fatal("Activate(no-such-config) = nil, want an error")
	}
	st, _ = a.Status()
	if !slices.Equal(st.Recent, want) {
		t.Errorf("Recent = %v after a failed activation, want it unchanged %v", st.Recent, want)
	}
}

func TestRecencyIsCapped(t *testing.T) {
	setup(t, "")
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		s.Recent = state.Remember(s.Recent, fmt.Sprintf("cfg-%d", i))
	}
	if len(s.Recent) != 20 {
		t.Fatalf("len(Recent) = %d, want it capped at 20", len(s.Recent))
	}
	if s.Recent[0] != "cfg-24" || s.Recent[19] != "cfg-5" {
		t.Errorf("Recent = %v, want cfg-24 first and cfg-5 last", s.Recent)
	}
}

// TestEditingTheRunningConfigIntoAFailureRestoresIt is the review's
// counter-example. Every reactivate path — WriteConfigYAML, Sync, UseDistro,
// preset writes — persists the user's intent BEFORE re-activating, so a
// snapshot taken inside Activate would capture the already-broken state and
// "restore" it. The snapshot must therefore be of the last setup that
// actually started, taken on success.
func TestEditingTheRunningConfigIntoAFailureRestoresIt(t *testing.T) {
	// Staged launchd prints: #1 the initial activation's up-check
	// (running), #2 reactivateIf's guard (the collector IS running, so the
	// edit re-applies), #3 the failing activation's authority check (not
	// running: the start failed), everything after is the restore coming up.
	setupStaged(t, "state = running", "state = running", "", "state = running")
	fakeDistro(t, "exit 0") // validates anything; nothing ever listens
	port := listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetVar("debug", "prod", "K", "v"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", "prod"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	_, good, err := a.Config("debug")
	if err != nil {
		t.Fatal(err)
	}

	// From here the collector never comes up.
	closeListener(t, port)
	if err := os.WriteFile(a.LogPath(), []byte("boom: cannot start\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	broken := good + "\n# valid yaml the collector accepts but cannot run\n"
	werr := a.WriteConfigYAML("debug", broken)
	if werr == nil {
		t.Fatal("WriteConfigYAML with a start-failing config = nil, want an error")
	}
	if state.IsBadRequest(werr) {
		t.Errorf("startup failure marked BadRequest (400), want a 500: %v", werr)
	}

	_, onDisk, err := a.Config("debug")
	if err != nil {
		t.Fatal(err)
	}
	if onDisk != good {
		t.Errorf("the broken edit survived the restore:\n%q\nwant the pre-edit YAML:\n%q", onDisk, good)
	}

	// The restore put debug · prod back and launchd reports it running, so
	// the error must say so (the not-running side of the honesty gate is
	// TestStillRunningOnlyWhenItActuallyIs).
	var sr interface{ StillRunning() string }
	if !errors.As(werr, &sr) || sr.StillRunning() != "debug · prod" {
		t.Errorf("err = %v, want a still-running claim of %q", werr, "debug · prod")
	}
}

// TestEditingTheStoppedActiveConfigStaysStopped pins reactivateIf's guard:
// editing the active config while the collector is stopped writes the edit
// and leaves the collector stopped — no bootstrap, no error.
func TestEditingTheStoppedActiveConfigStaysStopped(t *testing.T) {
	// Running for the initial activation, stopped when the edit lands.
	calls := setupStaged(t, "state = running", "")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if err := a.WriteConfigYAML("debug", "receivers: {}\n# edited while stopped\n"); err != nil {
		t.Fatalf("WriteConfigYAML while stopped: %v", err)
	}
	if called(*calls, "bootstrap") || called(*calls, "kickstart") {
		t.Errorf("editing the stopped active config started the collector: %v", *calls)
	}
	_, yaml, err := cfgstore.Get(a.Dir, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yaml, "# edited while stopped") {
		t.Errorf("yaml not written: %q", yaml)
	}
}

// TestWriteConfigYAMLNoValidateNeverTouchesTheCollector pins the skip
// mode's whole contract: the yaml lands, the collector binary is never
// asked (the distro is swapped for one that rejects everything), the
// running process is never restarted, and runningStale reports that the
// active running collector kept the previous version.
func TestWriteConfigYAMLNoValidateNeverTouchesTheCollector(t *testing.T) {
	// Running for the initial activation and still running when the
	// unvalidated write checks whether it left a stale process behind.
	calls := setupStaged(t, "state = running", "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	// From here every validation would fail — proving no validation runs.
	fakeDistro(t, "echo rejected >&2; exit 1")
	*calls = nil
	stale, err := a.WriteConfigYAMLNoValidate("debug", "exporters:\n  otlp:\n    endpoint: ${env:NOT_SET_YET}\n")
	if err != nil {
		t.Fatalf("WriteConfigYAMLNoValidate: %v", err)
	}
	if !stale {
		t.Error("runningStale = false, want true: the active running collector kept the previous version")
	}
	if called(*calls, "bootstrap") || called(*calls, "kickstart") {
		t.Errorf("unvalidated write touched the running collector: %v", *calls)
	}
	_, yaml, err := cfgstore.Get(a.Dir, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yaml, "NOT_SET_YET") {
		t.Errorf("yaml not written: %q", yaml)
	}

	// A config that is not the active one: written, not stale, still no
	// collector involvement.
	*calls = nil
	stale, err = a.WriteConfigYAMLNoValidate("otlp", "poked: true\n")
	if err != nil {
		t.Fatalf("WriteConfigYAMLNoValidate(otlp): %v", err)
	}
	if stale {
		t.Error("runningStale = true for a non-active config, want false")
	}
	if len(*calls) != 0 {
		t.Errorf("non-active unvalidated write called launchctl: %v", *calls)
	}

	if _, err := a.WriteConfigYAMLNoValidate("no-such-config", "a: 1\n"); err == nil {
		t.Error("WriteConfigYAMLNoValidate(unknown) = nil, want an error")
	}
}

// TestUseDistroStartupFailureRestoresTheBinary: switching the one collector
// to a binary that won't start puts the working one back, settings included.
func TestUseDistroStartupFailureRestoresTheBinary(t *testing.T) {
	// Running for the initial activation, down when the switched binary
	// fails to start, back up once the restore has run.
	setupStaged(t, "state = running", "", "state = running")
	fakeDistro(t, "exit 0")
	port := listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(t.TempDir(), "otelcol")
	if err := os.WriteFile(other, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.AddDistro("other", other); err != nil {
		t.Fatal(err)
	}

	closeListener(t, port)
	err = a.UseDistro("other")
	if err == nil {
		t.Fatal("UseDistro onto a collector that won't start = nil, want an error")
	}

	s, lerr := state.LoadSettings()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if s.Distro != "fake" {
		t.Errorf("settings.Distro = %q after a failed switch, want the working %q back", s.Distro, "fake")
	}
	// The message has to match what actually happened: the switch did NOT
	// stick, so it must not claim the default "is now other", and the
	// collector's own diagnostic stays wrapped.
	if strings.Contains(err.Error(), "default collector is now") {
		t.Errorf("error = %q, but the switch was reverted", err)
	}
	if !strings.Contains(err.Error(), "other") || !strings.Contains(err.Error(), "fake") {
		t.Errorf("error = %q, want it to name both the collector that failed and the one still in use", err)
	}
	if !strings.Contains(err.Error(), "did not come up") {
		t.Errorf("error = %q, want the collector diagnostic kept", err)
	}
}

// TestStillRunningOnlyWhenItActuallyIs: the reassurance is a claim about the
// world, so it is made only when launchd confirms the restored job is up.
func TestStillRunningOnlyWhenItActuallyIs(t *testing.T) {
	// launchd confirms the initial activation, reports "not running" for the
	// failing activation's check, then "running" once the previous
	// configuration has been put back.
	setupStaged(t, "state = running", "", "state = running")
	fakeDistro(t, "exit 0")
	port := listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetVar("debug", "prod", "K", "v"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("other", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	closeListener(t, port)

	err = a.Activate("other", "")
	if err == nil {
		t.Fatal("Activate(other) = nil, want a startup failure")
	}
	var sr interface{ StillRunning() string }
	if !errors.As(err, &sr) || sr.StillRunning() != "debug · prod" {
		t.Errorf("still_running = %v, want %q once launchd confirms the restore is up", err, "debug · prod")
	}
	if name, preset, _ := a.ActiveConfig(); name != "debug" || preset != "prod" {
		t.Errorf("active = %q/%q, want debug/prod", name, preset)
	}
}

// TestSquatterOnProbePortDoesNotFakeSuccess: a foreign process holding
// compy's gRPC port answers the probe's bare TCP dial, so a collector that
// crashed on "address already in use" used to look successfully started —
// exit 0, previous collector booted out, the broken setup snapshotted as
// last-good. launchd is the authority in both directions: probe success
// counts for nothing while launchd says the job is down.
func TestSquatterOnProbePortDoesNotFakeSuccess(t *testing.T) {
	// running during the initial activation, down for everything after —
	// the failing activation's check and the post-restore check both see a
	// dead job, so no still-running claim may be made.
	calls := setupStaged(t, "state = running", "")
	fakeDistro(t, "exit 0")
	listenPort(t) // stays open: the squatter answering compy's gRPC port

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatalf("Activate(debug): %v", err)
	}
	if err := a.CreateConfig("other", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	err = a.Activate("other", "")
	if err == nil {
		t.Fatal("Activate(other) = nil: a squatter answering the probe port faked a successful start")
	}
	var sr interface{ StillRunning() string }
	if errors.As(err, &sr) {
		t.Errorf("error claims %q still running while launchd reports the job down", sr.StillRunning())
	}
	// The existing restore machinery must have put debug back...
	if !called(*calls, "bootstrap") {
		t.Errorf("previous configuration not restored: %v", *calls)
	}
	if name, _, _ := a.ActiveConfig(); name != "debug" {
		t.Errorf("ActiveConfig = %q after the failure, want debug restored", name)
	}
	// ...and the broken setup must not have been snapshotted as last-good.
	data, rerr := os.ReadFile(filepath.Join(a.Dir, "last-good", "settings.json"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	var snap state.Settings
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.ActiveConfig != "debug" {
		t.Errorf("last-good active_config = %q, want debug (the broken setup was snapshotted)", snap.ActiveConfig)
	}
}

func TestStopUninstallsTheJob(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}
	path, err := launchd.PlistPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no plist after activate: %v", err)
	}

	*calls = nil
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !called(*calls, "bootout") {
		t.Errorf("Stop did not boot the job out: %v", *calls)
	}
	// The plist has RunAtLoad: leaving it behind would resurrect the
	// collector at the next login, which is not what "stopped" means.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("plist still present after Stop (stat err = %v)", err)
	}
}

// TestHealthOnlyReportsOurCollector: something else on :8888 — another
// collector on the same machine — must not be reported as ours.
func TestHealthOnlyReportsOurCollector(t *testing.T) {
	setup(t, "") // launchd: our job is not running
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	h, err := a.Health()
	if err != nil {
		t.Fatal(err)
	}
	if avail := h.(collector.Health).Available; avail {
		t.Errorf("Health().available = true while our collector is stopped")
	}

	// With the job running the scrape is attempted; nothing serves :8888 in
	// a test, so it comes back unavailable — but by the other route.
	setup(t, "state = running")
	a2, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a2.Health(); err != nil {
		t.Errorf("Health() with the job running: %v", err)
	}
}

// TestFactoryReset: a reset while the collector is running (per the shim)
// uninstalls the job and returns the state dir to as-installed — user
// configs gone, shipped configs back pristine, settings and distros back to
// defaults, downloaded binaries and logs deleted — without touching the
// directory itself.
func TestFactoryReset(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0") // a custom distro entry, selected
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("mine", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetVar("bronto", "prod", "BRONTO_API_KEY", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteConfigYAML("debug", "poked: true\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("mine", ""); err != nil {
		t.Fatal(err) // running: settings.json now names mine, last-good exists
	}
	// A "downloaded" collector binary and a log line, to be wiped.
	binDir := filepath.Join(a.Dir, "distros", "contrib-1.0")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "otelcol"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.LogPath(), []byte("a log line\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if err := a.FactoryReset(); err != nil {
		t.Fatalf("FactoryReset: %v", err)
	}

	// The running job was uninstalled: booted out, plist gone.
	if !called(*calls, "bootout") {
		t.Errorf("FactoryReset did not boot the job out: %v", *calls)
	}
	if path, err := launchd.PlistPath(); err == nil {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("plist still present after FactoryReset (stat err = %v)", err)
		}
	}

	// Exactly the shipped configs, all pristine.
	infos, err := a.Configs()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, info := range infos {
		names = append(names, info.Name)
		if info.Provenance != "shipped" || info.Modified {
			t.Errorf("config %s: provenance=%s modified=%v, want pristine shipped", info.Name, info.Provenance, info.Modified)
		}
		// Fresh configs start with exactly the empty default preset.
		if len(info.Meta.Presets) != 1 || len(info.Meta.Presets["default"]) != 0 || info.Meta.ActivePreset != "default" {
			t.Errorf("config %s presets = %+v active=%q, want just an empty default", info.Name, info.Meta.Presets, info.Meta.ActivePreset)
		}
	}
	if want := []string{"bronto", "debug", "otlp"}; !slices.Equal(names, want) {
		t.Errorf("configs after reset = %v, want %v", names, want)
	}

	// Settings back to defaults: default ports, no distro, nothing active.
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveConfig != "" || s.Distro != "" || s.GRPCPort != 14317 || s.HTTPPort != 14318 || len(s.Recent) != 0 {
		t.Errorf("settings after reset = %+v, want defaults", s)
	}
	// The custom distro entry is gone, and so are downloaded binaries.
	distros, err := state.LoadDistros()
	if err != nil {
		t.Fatal(err)
	}
	if len(distros) != 0 {
		t.Errorf("distros after reset = %v, want none", distros)
	}
	if _, err := os.Stat(filepath.Join(a.Dir, "distros")); !os.IsNotExist(err) {
		t.Errorf("downloaded binaries survived the reset (stat err = %v)", err)
	}
	// Logs and the last-good snapshot are gone; the dir layout is back.
	if _, err := os.Stat(a.LogPath()); !os.IsNotExist(err) {
		t.Errorf("collector log survived the reset (stat err = %v)", err)
	}
	if cfgstore.HasSnapshot(a.Dir) {
		t.Error("last-good snapshot survived the reset")
	}
	for _, sub := range []string{"configs", "logs", "last-good"} {
		if fi, err := os.Stat(filepath.Join(a.Dir, sub)); err != nil || !fi.IsDir() {
			t.Errorf("state dir missing %s/ after reset (err = %v)", sub, err)
		}
	}
}

// stubEnvExec captures the launchctl commands envvars would run.
func stubEnvExec(t *testing.T) *[]string {
	t.Helper()
	orig := envvars.Exec
	var calls []string
	envvars.Exec = func(name string, arg ...string) *exec.Cmd {
		calls = append(calls, name+" "+strings.Join(arg, " "))
		return exec.Command("true")
	}
	t.Cleanup(func() { envvars.Exec = orig })
	return &calls
}

// TestReapplyOSEnv: launchctl setenv does not survive a reboot, so the tray
// re-applies at login — but only when the setting is on.
func TestReapplyOSEnv(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}

	calls := stubEnvExec(t)
	if err := a.ReapplyOSEnv(); err != nil {
		t.Fatalf("ReapplyOSEnv (off): %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("os-env off: launchctl called: %v", *calls)
	}

	if err := a.SetOSEnv(true); err != nil {
		t.Fatalf("SetOSEnv: %v", err)
	}
	*calls = nil
	if err := a.ReapplyOSEnv(); err != nil {
		t.Fatalf("ReapplyOSEnv (on): %v", err)
	}
	if !slices.Contains(*calls, "launchctl setenv OTEL_EXPORTER_OTLP_ENDPOINT http://127.0.0.1:14318") {
		t.Errorf("os-env on: setenv not re-applied, calls = %v", *calls)
	}
}

// TestPutSettingsRefreshesOSEnv: with the OS env on, a port change must
// update the injected endpoint rather than leave the old one behind.
func TestPutSettingsRefreshesOSEnv(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	calls := stubEnvExec(t)
	if err := a.SetOSEnv(true); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	port := 25999
	if err := a.PutSettings(nil, &port, nil); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if !slices.Contains(*calls, "launchctl setenv OTEL_EXPORTER_OTLP_ENDPOINT http://127.0.0.1:25999") {
		t.Errorf("port change with os-env on did not refresh the OS env: %v", *calls)
	}

	if err := a.SetOSEnv(false); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	port = 26000
	if err := a.PutSettings(nil, &port, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range *calls {
		if strings.Contains(c, "setenv") {
			t.Errorf("os-env off: port change ran setenv: %v", *calls)
		}
	}
}

// setenvKeys extracts the var names from captured "launchctl setenv K V"
// calls.
func setenvKeys(calls []string) map[string]bool {
	keys := map[string]bool{}
	for _, c := range calls {
		if f := strings.Fields(c); len(f) >= 3 && f[1] == "setenv" {
			keys[f[2]] = true
		}
	}
	return keys
}

// TestPutSettingsProtocolSwitchOSEnv: with the OS env on, switching the
// advertised protocol re-points the injected endpoint — and, because Vars()
// keeps one key set for every protocol, a grpc→http/protobuf switch leaves
// no stale key (no OTEL_EXPORTER_OTLP_INSECURE, no key set under grpc that
// the http refresh doesn't overwrite).
func TestPutSettingsProtocolSwitchOSEnv(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	calls := stubEnvExec(t)
	if err := a.SetOSEnv(true); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	proto := "grpc"
	if err := a.PutSettings(nil, nil, &proto); err != nil {
		t.Fatalf("PutSettings(grpc): %v", err)
	}
	grpcCalls := append([]string(nil), *calls...)
	if !slices.Contains(grpcCalls, "launchctl setenv OTEL_EXPORTER_OTLP_ENDPOINT http://127.0.0.1:14317") {
		t.Errorf("grpc switch: endpoint not re-pointed at the grpc port, calls = %v", grpcCalls)
	}
	if !slices.Contains(grpcCalls, "launchctl setenv OTEL_EXPORTER_OTLP_PROTOCOL grpc") {
		t.Errorf("grpc switch: protocol not refreshed, calls = %v", grpcCalls)
	}

	*calls = nil
	proto = "http/protobuf"
	if err := a.PutSettings(nil, nil, &proto); err != nil {
		t.Fatalf("PutSettings(http/protobuf): %v", err)
	}
	httpCalls := append([]string(nil), *calls...)
	if !slices.Contains(httpCalls, "launchctl setenv OTEL_EXPORTER_OTLP_ENDPOINT http://127.0.0.1:14318") {
		t.Errorf("http switch: endpoint not re-pointed at the http port, calls = %v", httpCalls)
	}

	// Every key the grpc refresh set must be overwritten by the http one —
	// nothing from the grpc advertisement may linger in the OS env.
	httpKeys := setenvKeys(httpCalls)
	for k := range setenvKeys(grpcCalls) {
		if !httpKeys[k] {
			t.Errorf("stale key: grpc set %s but the http/protobuf refresh did not overwrite it", k)
		}
	}
	for _, c := range append(grpcCalls, httpCalls...) {
		if strings.Contains(c, "INSECURE") {
			t.Errorf("OTEL_EXPORTER_OTLP_INSECURE must not be set (http:// scheme already means plaintext): %v", c)
		}
	}
}

// Activate with an empty preset resolves the config's real active preset —
// the guaranteed default — and records it. An empty default preset produces
// exactly the environment a preset-less activation used to: compy's port
// variables, nothing else.
func TestActivateEmptyPresetUsesDefault(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")
	port := listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("debug", ""); err != nil {
		t.Fatalf("Activate(debug, \"\") = %v, want nil", err)
	}
	name, preset, err := a.ActiveConfig()
	if err != nil || name != "debug" || preset != "default" {
		t.Errorf("ActiveConfig() = %q,%q,%v, want debug,default", name, preset, err)
	}
	if st, err := a.Status(); err != nil || st.Preset != "default" {
		t.Errorf("Status().Preset = %q (%v), want default", st.Preset, err)
	}

	// activationEnv equivalence: the plist's environment dict holds exactly
	// compy's two port variables — what the old no-preset activation set.
	plist := readPlist(t)
	i := strings.Index(plist, "<key>EnvironmentVariables</key>")
	if i < 0 {
		t.Fatalf("plist has no EnvironmentVariables:\n%s", plist)
	}
	env := plist[i:]
	if j := strings.Index(env, "</dict>"); j >= 0 {
		env = env[:j]
	}
	if got := strings.Count(env, "<key>") - 1; got != 2 { // -1 for the dict's own key
		t.Errorf("env dict has %d keys, want exactly COMPY_GRPC_PORT and COMPY_HTTP_PORT:\n%s", got, env)
	}
	if !strings.Contains(env, "<key>COMPY_GRPC_PORT</key><string>"+strconv.Itoa(port)+"</string>") ||
		!strings.Contains(env, "<key>COMPY_HTTP_PORT</key>") {
		t.Errorf("env dict missing compy's ports:\n%s", env)
	}
}

// app.New backfills the every-config-has-a-preset invariant onto a
// pre-invariant config found on disk.
func TestNewBackfillsPresetlessConfig(t *testing.T) {
	setup(t, "")
	home := os.Getenv("COMPY_HOME")
	dir := filepath.Join(home, "configs", "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("receivers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"presets":{},"active_preset":""}`), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	info, _, err := a.Config("old")
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Meta.Presets) != 1 || len(info.Meta.Presets["default"]) != 0 || info.Meta.ActivePreset != "default" {
		t.Errorf("backfilled meta = %+v, want an empty active default preset", info.Meta)
	}
}

// An upstream answer that is equal or OLDER than the installed version is
// "already newest", never a silent downgrade — and nothing downloads (S2).
func TestUpdateDistroRefusesDowngrade(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	var d distro.Def
	for _, def := range distro.Defs() {
		if def.Name == "otlp" {
			d = def
		}
	}
	// Pre-place the pinned binary: only an installed distro is updatable.
	bin := filepath.Join(a.Dir, "distros", d.Name+"-"+d.Version, d.Binary)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		if strings.Contains(url, "releases?") {
			body := `[{"tag_name":"v0.0.1","prerelease":false}]`
			return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
		}
		t.Fatalf("a downgrade must download nothing, fetched %q", url)
		return nil, 0, nil
	}

	current, latest, updated, err := a.UpdateDistro("otlp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated || current != d.Version || latest != "0.0.1" {
		t.Fatalf("UpdateDistro = (%q, %q, %v), want already-newest no-op at %q", current, latest, updated, d.Version)
	}
	if _, _, started, err := a.StartUpdateDistro("otlp"); err != nil || started {
		t.Fatalf("StartUpdateDistro = (started=%v, err=%v), want a no-op", started, err)
	}
}

// A traversal-shaped upstream tag is refused before it can reach paths or
// URLs — the check errors, nothing downloads (S2).
func TestUpdateDistroRefusesTraversalTag(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	var d distro.Def
	for _, def := range distro.Defs() {
		if def.Name == "otlp" {
			d = def
		}
	}
	bin := filepath.Join(a.Dir, "distros", d.Name+"-"+d.Version, d.Binary)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		if strings.Contains(url, "releases?") {
			body := `[{"tag_name":"v../../../evil","prerelease":false}]`
			return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
		}
		t.Fatalf("a refused tag must download nothing, fetched %q", url)
		return nil, 0, nil
	}
	if _, _, _, err := a.UpdateDistro("otlp", nil); err == nil || !strings.Contains(err.Error(), "not a release version") {
		t.Fatalf("UpdateDistro with traversal tag = %v, want 'not a release version'", err)
	}
}
