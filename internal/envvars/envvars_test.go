package envvars

import (
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/state"
)

// TestVars: the endpoint follows the advertised protocol (grpc → gRPC port,
// both http flavors → HTTP port), the three signal exporters stay otlp
// regardless, and no protocol adds OTEL_EXPORTER_OTLP_INSECURE — the grpc
// endpoint's http:// scheme already means plaintext per the OTLP exporter
// spec.
func TestVars(t *testing.T) {
	base := map[string]string{
		"OTEL_TRACES_EXPORTER":  "otlp",
		"OTEL_METRICS_EXPORTER": "otlp",
		"OTEL_LOGS_EXPORTER":    "otlp",
	}
	cases := []struct {
		protocol     string // as stored in settings; "" = default
		wantProtocol string
		wantEndpoint string
	}{
		{"", "http/protobuf", "http://127.0.0.1:14318"},
		{"http/protobuf", "http/protobuf", "http://127.0.0.1:14318"},
		{"http/json", "http/json", "http://127.0.0.1:14318"},
		{"grpc", "grpc", "http://127.0.0.1:14317"},
	}
	for _, c := range cases {
		got := Vars(state.Settings{GRPCPort: 14317, HTTPPort: 14318, Protocol: c.protocol})
		want := map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": c.wantEndpoint,
			"OTEL_EXPORTER_OTLP_PROTOCOL": c.wantProtocol,
		}
		for k, v := range base {
			want[k] = v
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Vars(protocol=%q) = %#v, want %#v", c.protocol, got, want)
		}
	}
}

// TestVarsKeySetConstantAcrossProtocols locks the invariant app.PutSettings'
// OS-env refresh relies on: every protocol yields the same key set, so
// overwriting on a protocol switch can never strand a stale key in the OS
// environment. If a protocol ever needs an extra key (e.g. an INSECURE
// flag), that refresh must learn to unset removed keys first.
func TestVarsKeySetConstantAcrossProtocols(t *testing.T) {
	keysOf := func(m map[string]string) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return keys
	}
	ref := keysOf(Vars(state.Settings{GRPCPort: 14317, HTTPPort: 14318}))
	for _, p := range []string{"grpc", "http/protobuf", "http/json"} {
		got := keysOf(Vars(state.Settings{GRPCPort: 14317, HTTPPort: 14318, Protocol: p}))
		if !slices.Equal(got, ref) {
			t.Errorf("Vars(protocol=%q) keys = %v, want %v — see app.PutSettings before changing this", p, got, ref)
		}
	}
}

// TestEnvScriptSignalExporters: what `compy env` prints must pin the three
// per-signal exporters alongside endpoint/protocol.
func TestEnvScriptSignalExporters(t *testing.T) {
	script, err := Script(Vars(state.Settings{GRPCPort: 14317, HTTPPort: 14318}), "sh")
	if err != nil {
		t.Fatalf("Script() unexpected error: %v", err)
	}
	for _, line := range []string{
		"export OTEL_TRACES_EXPORTER='otlp'\n",
		"export OTEL_METRICS_EXPORTER='otlp'\n",
		"export OTEL_LOGS_EXPORTER='otlp'\n",
	} {
		if !strings.Contains(script, line) {
			t.Errorf("Script() = %q, missing %q", script, line)
		}
	}
}

// TestSetUnsetOSAllVars: SetOS/UnsetOS over Vars() must cover the full
// five-key set — toggling OS-env off has to unset everything set-os set.
func TestSetUnsetOSAllVars(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the launchctl OS env is darwin-only")
	}
	orig := Exec
	defer func() { Exec = orig }()

	var calls []string
	Exec = func(name string, arg ...string) *exec.Cmd {
		calls = append(calls, name+" "+strings.Join(arg, " "))
		return exec.Command("true")
	}

	vars := Vars(state.Settings{GRPCPort: 14317, HTTPPort: 14318})
	keys := []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_LOGS_EXPORTER",
		"OTEL_METRICS_EXPORTER",
		"OTEL_TRACES_EXPORTER",
	}

	if err := SetOS(vars); err != nil {
		t.Fatalf("SetOS() unexpected error: %v", err)
	}
	if len(calls) != len(keys) {
		t.Errorf("SetOS() ran %d commands, want %d: %v", len(calls), len(keys), calls)
	}
	for _, k := range keys {
		want := "launchctl setenv " + k + " " + vars[k]
		if !slices.Contains(calls, want) {
			t.Errorf("SetOS() calls = %v, missing %q", calls, want)
		}
	}

	calls = nil
	if err := UnsetOS(vars); err != nil {
		t.Fatalf("UnsetOS() unexpected error: %v", err)
	}
	if len(calls) != len(keys) {
		t.Errorf("UnsetOS() ran %d commands, want %d: %v", len(calls), len(keys), calls)
	}
	for _, k := range keys {
		want := "launchctl unsetenv " + k
		if !slices.Contains(calls, want) {
			t.Errorf("UnsetOS() calls = %v, missing %q", calls, want)
		}
	}
}

func TestScriptShells(t *testing.T) {
	vars := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:14318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
	}

	cases := []struct {
		shell string
		want  string
	}{
		{"sh", "export OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:14318'\nexport OTEL_EXPORTER_OTLP_PROTOCOL='http/protobuf'\n"},
		{"", "export OTEL_EXPORTER_OTLP_ENDPOINT='http://127.0.0.1:14318'\nexport OTEL_EXPORTER_OTLP_PROTOCOL='http/protobuf'\n"},
		{"fish", "set -gx OTEL_EXPORTER_OTLP_ENDPOINT 'http://127.0.0.1:14318'\nset -gx OTEL_EXPORTER_OTLP_PROTOCOL 'http/protobuf'\n"},
		{"pwsh", "$env:OTEL_EXPORTER_OTLP_ENDPOINT = 'http://127.0.0.1:14318'\n$env:OTEL_EXPORTER_OTLP_PROTOCOL = 'http/protobuf'\n"},
	}
	for _, c := range cases {
		got, err := Script(vars, c.shell)
		if err != nil {
			t.Errorf("Script(shell=%q) unexpected error: %v", c.shell, err)
			continue
		}
		if got != c.want {
			t.Errorf("Script(shell=%q) = %q, want %q", c.shell, got, c.want)
		}
	}

	if _, err := Script(vars, "bogus"); err == nil {
		t.Error("Script(shell=\"bogus\") expected error, got nil")
	}
}

func TestScriptHostileValues(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		wantSh   string
		wantFish string
		wantPwsh string
	}{
		{
			name:     "command substitution",
			value:    "$(whoami)",
			wantSh:   "export K='$(whoami)'\n",
			wantFish: "set -gx K '$(whoami)'\n",
			wantPwsh: "$env:K = '$(whoami)'\n",
		},
		{
			name:     "single quote",
			value:    "it's a test",
			wantSh:   `export K='it'\''s a test'` + "\n",
			wantFish: `set -gx K 'it\'s a test'` + "\n",
			wantPwsh: "$env:K = 'it''s a test'\n",
		},
		{
			name:     "double quote",
			value:    `say "hi"`,
			wantSh:   `export K='say "hi"'` + "\n",
			wantFish: `set -gx K 'say "hi"'` + "\n",
			wantPwsh: "$env:K = 'say \"hi\"'\n",
		},
	}

	for _, c := range cases {
		vars := map[string]string{"K": c.value}
		for _, s := range []struct {
			shell string
			want  string
		}{
			{"sh", c.wantSh},
			{"fish", c.wantFish},
			{"pwsh", c.wantPwsh},
		} {
			got, err := Script(vars, s.shell)
			if err != nil {
				t.Errorf("%s: Script(shell=%q) unexpected error: %v", c.name, s.shell, err)
				continue
			}
			if got != s.want {
				t.Errorf("%s: Script(shell=%q) = %q, want %q", c.name, s.shell, got, s.want)
			}
		}
	}
}

func TestScriptShRoundTrip(t *testing.T) {
	hostileValues := []string{
		"$(whoami)",
		"it's a test",
		`say "hi"`,
		"`echo pwned`",
		"$HOME and $(rm -rf /)",
	}

	for _, v := range hostileValues {
		script, err := Script(map[string]string{"K": v}, "sh")
		if err != nil {
			t.Fatalf("Script(%q) unexpected error: %v", v, err)
		}

		// script already ends in a newline, which terminates the export
		// statement, so no separating ";" is needed before printf.
		cmd := exec.Command("/bin/sh", "-c", script+`printf %s "$K"`)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("running script for value %q: %v", v, err)
		}
		if string(out) != v {
			t.Errorf("round-trip for %q: sh printed %q", v, string(out))
		}
	}
}

func TestRunExitCode(t *testing.T) {
	vars := map[string]string{"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf"}

	code, err := Run(vars, []string{"/bin/sh", "-c", `test "$OTEL_EXPORTER_OTLP_PROTOCOL" = http/protobuf`})
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if code != 0 {
		t.Errorf("Run() code = %d, want 0", code)
	}

	code, err = Run(vars, []string{"/bin/sh", "-c", "exit 3"})
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if code != 3 {
		t.Errorf("Run() code = %d, want 3", code)
	}
}

func TestSetOSCallsLaunchctl(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the launchctl OS env is darwin-only")
	}
	orig := Exec
	defer func() { Exec = orig }()

	var calls []string
	Exec = func(name string, arg ...string) *exec.Cmd {
		calls = append(calls, name+" "+strings.Join(arg, " "))
		return exec.Command("true")
	}

	if err := SetOS(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:14318"}); err != nil {
		t.Fatalf("SetOS() unexpected error: %v", err)
	}

	want := "launchctl setenv OTEL_EXPORTER_OTLP_ENDPOINT http://127.0.0.1:14318"
	found := false
	for _, c := range calls {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Exec calls = %v, want to contain %q", calls, want)
	}
}
