package app_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/cfgstore"
	"github.com/bronto-io/compy/internal/distro"
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

	// The snapshot is of the setup that was running BEFORE this activation,
	// so a first activation takes none — there is nothing to come back to.
	if _, err := os.Stat(filepath.Join(a.Dir, "last-good", "settings.json")); err == nil {
		t.Error("a first activation took a snapshot; there was no previous setup")
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

func TestReplacePresetReactivatesWhenActivePreset(t *testing.T) {
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
	if err := a.PutSettings(&grpc, nil); err != nil {
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
	if err := a.PutSettings(&bad, nil); err == nil {
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
	if err := a.PutSettings(nil, &zero); err == nil {
		t.Fatal("PutSettings with port 0: want error, got nil")
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

func TestRenameSetApp(t *testing.T) {
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
		{"PutSettings port out of range", func() error { p := 99999; return a.PutSettings(&p, nil) }},
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
	calls := setup(t, "")
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
	var sr interface{ StillRunning() string }
	if !errors.As(err, &sr) || sr.StillRunning() != "debug · prod" {
		t.Errorf("error carries still_running = %v, want %q", err, "debug · prod")
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
