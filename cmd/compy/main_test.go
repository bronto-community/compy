package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/envvars"
	"github.com/bronto-community/compy/internal/launchd"
	"github.com/bronto-community/compy/internal/state"
)

// cliSetup points COMPY_HOME and HOME at temp dirs and stubs launchd.Exec
// (answering print calls with launchdPrint) and envvars.Exec (a no-op
// command) — the same seams internal/app's tests use, so run() never
// touches the real machine. Returns the recorded launchctl invocations.
func cliSetup(t *testing.T, launchdPrint string) *[][]string {
	t.Helper()
	t.Setenv("COMPY_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	var calls [][]string
	origL := launchd.Exec
	launchd.Exec = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		if len(args) > 0 && args[0] == "print" {
			return []byte(launchdPrint), nil
		}
		return nil, nil
	}
	origE := envvars.Exec
	envvars.Exec = func(name string, arg ...string) *exec.Cmd { return exec.Command("true") }
	t.Cleanup(func() { launchd.Exec = origL; envvars.Exec = origE })
	return &calls
}

// cliFakeDistro registers a stub collector script as the selected distro.
func cliFakeDistro(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "otelcol")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
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

// captureStdout runs fn with os.Stdout redirected into a pipe and returns
// what it printed — run() writes straight to os.Stdout, so this is the seam.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

func TestRunStatusTextAndJSON(t *testing.T) {
	cliSetup(t, "")
	out, err := captureStdout(t, func() error { return run([]string{"status"}) })
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"service:  stopped", "config:   (none)", "endpoint: http://127.0.0.1:14318"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}

	out, err = captureStdout(t, func() error { return run([]string{"status", "--json"}) })
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, out)
	}
	if st["running"] != false || st["grpc_port"] != float64(14317) || st["http_port"] != float64(14318) {
		t.Errorf("status --json = %v, want stopped on the default ports", st)
	}
}

// TestRunUseActivates drives `compy use` through the stubs: the shipped
// debug config activates, launchctl sees the bootstrap, and the
// activation is recorded in settings.
func TestRunUseActivates(t *testing.T) {
	calls := cliSetup(t, "state = running")
	// app.New must run once first so the state dir exists for the distro
	// registration; `use` itself re-runs it idempotently.
	if err := run([]string{"status"}); err != nil {
		t.Fatal(err)
	}
	cliFakeDistro(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.GRPCPort = ln.Addr().(*net.TCPAddr).Port
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"use", "debug"}); err != nil {
		t.Fatalf("use debug: %v", err)
	}
	var sawBootstrap bool
	for _, c := range *calls {
		if len(c) > 0 && c[0] == "bootstrap" {
			sawBootstrap = true
		}
	}
	if !sawBootstrap {
		t.Errorf("use debug never bootstrapped the job: %v", *calls)
	}
	if s, _ := state.LoadSettings(); s.ActiveConfig != "debug" {
		t.Errorf("ActiveConfig = %q after use, want debug", s.ActiveConfig)
	}
}

func TestRunPresetsSetAndVars(t *testing.T) {
	cliSetup(t, "")
	if err := run([]string{"status"}); err != nil { // materialize the state dir
		t.Fatal(err)
	}
	if err := run([]string{"presets", "set", "debug", "prod", "DEBUG_VERBOSITY=detailed"}); err != nil {
		t.Fatalf("presets set: %v", err)
	}
	out, err := captureStdout(t, func() error { return run([]string{"vars", "debug"}) })
	if err != nil {
		t.Fatalf("vars: %v", err)
	}
	if !strings.Contains(out, "DEBUG_VERBOSITY") || !strings.Contains(out, "detailed") || !strings.Contains(out, "prod") {
		t.Errorf("vars output missing the set value:\n%s", out)
	}
	out, err = captureStdout(t, func() error { return run([]string{"config", "list"}) })
	if err != nil {
		t.Fatalf("config list: %v", err)
	}
	if !strings.Contains(out, "debug") || !strings.Contains(out, "shipped") {
		t.Errorf("config list output missing the shipped debug row:\n%s", out)
	}
}

func TestRunSettingsShowAndSet(t *testing.T) {
	cliSetup(t, "")
	out, err := captureStdout(t, func() error { return run([]string{"settings"}) })
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if !strings.Contains(out, "grpc-port: 14317") || !strings.Contains(out, "protocol: http/protobuf") {
		t.Errorf("settings output = %q, want the defaults", out)
	}

	if err := run([]string{"settings", "set", "--grpc-port", "5000"}); err != nil {
		t.Fatalf("settings set: %v", err)
	}
	out, _ = captureStdout(t, func() error { return run([]string{"settings"}) })
	if !strings.Contains(out, "grpc-port: 5000") || !strings.Contains(out, "http-port: 14318") {
		t.Errorf("settings after set = %q, want grpc 5000 with http untouched", out)
	}

	// A bad port is refused by the app layer, not silently stored.
	if err := run([]string{"settings", "set", "--http-port", "70000"}); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("settings set bad port = %v, want the out-of-range refusal", err)
	}
}

// TestRunArgErrors: one malformed invocation per subcommand family answers
// with usage guidance instead of doing anything.
func TestRunArgErrors(t *testing.T) {
	cliSetup(t, "")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"bogus"}, "unknown command"},
		{[]string{"use"}, "need <config>"},
		{[]string{"config"}, "need a subcommand"},
		{[]string{"config", "copy", "only-one"}, "need <src> <dst>"},
		{[]string{"presets", "set", "debug", "prod", "NOEQUALS"}, "need KEY=VALUE"},
		{[]string{"settings", "nope"}, "unknown subcommand"},
		{[]string{"distro"}, "need a subcommand"},
		{[]string{"distro", "use", "nope"}, "no such distro"},
		{[]string{"service", "frobnicate"}, "unknown subcommand"},
		{[]string{"vars"}, "need <config>"},
	}
	for _, c := range cases {
		_, err := captureStdout(t, func() error { return run(c.args) })
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("run(%v) = %v, want an error mentioning %q", c.args, err, c.want)
		}
	}
}

func TestRunFactoryResetGatedOnYes(t *testing.T) {
	cliSetup(t, "")
	if err := run([]string{"status"}); err != nil { // materialize the state dir
		t.Fatal(err)
	}
	dir := os.Getenv("COMPY_HOME")

	// Without --yes: refuse, name the flag, delete nothing.
	err := run([]string{"factory-reset"})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("factory-reset without --yes = %v, want the refusal naming the flag", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "configs", "debug", "config.yaml")); err != nil {
		t.Errorf("refused reset still deleted state: %v", err)
	}

	// With --yes: wipe and recreate the shipped defaults.
	out, err := captureStdout(t, func() error { return run([]string{"factory-reset", "--yes"}) })
	if err != nil {
		t.Fatalf("factory-reset --yes: %v", err)
	}
	if !strings.Contains(out, "reset to factory settings") {
		t.Errorf("factory-reset output = %q, want the confirmation line", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "configs", "debug", "config.yaml")); err != nil {
		t.Errorf("shipped defaults not re-materialized after reset: %v", err)
	}
}

// TestErrorText: the CLI must show the still-running reassurance REST users
// get (the marker never changes the message itself, so main.go has to
// render it), and leave every other error untouched.
func TestErrorText(t *testing.T) {
	if got := errorText(errors.New("boom")); got != "boom" {
		t.Errorf("errorText(plain) = %q, want boom", got)
	}

	err := state.StillRunning(errors.New("collector did not come up"), "debug · prod")
	want := "collector did not come up\nthe previous setup is still running: debug · prod"
	if got := errorText(err); got != want {
		t.Errorf("errorText(still-running) = %q, want %q", got, want)
	}

	// The marker survives a caller's %w wrap (UseDistro wraps this way).
	wrapped := fmt.Errorf("activating: %w", err)
	want = "activating: collector did not come up\nthe previous setup is still running: debug · prod"
	if got := errorText(wrapped); got != want {
		t.Errorf("errorText(wrapped) = %q, want %q", got, want)
	}
}

// TestRunTemplates: `compy templates` lists the catalog, and the
// create/re-render commands drive the full CLI shape — knobs file parsed,
// validation errors naming the field, --discard-edits mirroring resync.
func TestRunTemplates(t *testing.T) {
	cliSetup(t, "")

	out, err := captureStdout(t, func() error { return run([]string{"templates"}) })
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	if !strings.Contains(out, "custom-endpoints") {
		t.Errorf("templates output missing custom-endpoints:\n%s", out)
	}

	knobs := filepath.Join(t.TempDir(), "knobs.json")
	if err := os.WriteFile(knobs, []byte(`{"backends":[{"name":"hc","endpoint":"https://api.hc.example","auth_header":"x-key"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error {
		return run([]string{"config", "create", "mine", "--template", "custom-endpoints", "--knobs", knobs})
	}); err != nil {
		t.Fatalf("config create --template: %v", err)
	}
	out, err = captureStdout(t, func() error { return run([]string{"config", "show", "mine"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "${env:HC_API_KEY}  # hc api key") {
		t.Errorf("rendered config missing secret card:\n%s", out)
	}

	// A bad knobs file names the offending field.
	badKnobs := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badKnobs, []byte(`{"backends":[{"name":"hc","endpoint":"nope"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = captureStdout(t, func() error {
		return run([]string{"config", "create", "other", "--template", "custom-endpoints", "--knobs", badKnobs})
	})
	if err == nil || !strings.Contains(err.Error(), "backends[0].endpoint") {
		t.Errorf("bad knobs err = %v, want the field named", err)
	}

	// Re-render without --knobs reuses the stored ones; a hand-edited
	// config needs --discard-edits, mirroring resync.
	if _, err := captureStdout(t, func() error { return run([]string{"config", "re-render", "mine"}) }); err != nil {
		t.Fatalf("re-render: %v", err)
	}
	dir := os.Getenv("COMPY_HOME")
	yamlPath := filepath.Join(dir, "configs", "mine", "config.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, append(data, []byte("# edited\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = captureStdout(t, func() error { return run([]string{"config", "re-render", "mine"}) })
	if err == nil || !strings.Contains(err.Error(), "locally modified") {
		t.Errorf("re-render of edited = %v, want the modified refusal", err)
	}
	if _, err := captureStdout(t, func() error {
		return run([]string{"config", "re-render", "mine", "--discard-edits"})
	}); err != nil {
		t.Fatalf("re-render --discard-edits: %v", err)
	}
	if data, _ := os.ReadFile(yamlPath); strings.Contains(string(data), "# edited") {
		t.Error("--discard-edits left the edits in place")
	}

	// Flag conflicts are refused up front.
	_, err = captureStdout(t, func() error {
		return run([]string{"config", "create", "x", "--template", "custom-endpoints", "--from-url", "https://x"})
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("--template + --from-url = %v", err)
	}
	_, err = captureStdout(t, func() error {
		return run([]string{"config", "create", "x", "--knobs", knobs})
	})
	if err == nil || !strings.Contains(err.Error(), "--knobs needs --template") {
		t.Errorf("--knobs without --template = %v", err)
	}
}
