package envvars

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/bronto-io/compy/internal/state"
)

func TestVars(t *testing.T) {
	got := Vars(state.Settings{GRPCPort: 14317, HTTPPort: 14318})
	want := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:14318",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Vars() = %#v, want %#v", got, want)
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
		{"sh", "export OTEL_EXPORTER_OTLP_ENDPOINT=\"http://127.0.0.1:14318\"\nexport OTEL_EXPORTER_OTLP_PROTOCOL=\"http/protobuf\"\n"},
		{"", "export OTEL_EXPORTER_OTLP_ENDPOINT=\"http://127.0.0.1:14318\"\nexport OTEL_EXPORTER_OTLP_PROTOCOL=\"http/protobuf\"\n"},
		{"fish", "set -gx OTEL_EXPORTER_OTLP_ENDPOINT \"http://127.0.0.1:14318\"\nset -gx OTEL_EXPORTER_OTLP_PROTOCOL \"http/protobuf\"\n"},
		{"pwsh", "$env:OTEL_EXPORTER_OTLP_ENDPOINT = \"http://127.0.0.1:14318\"\n$env:OTEL_EXPORTER_OTLP_PROTOCOL = \"http/protobuf\"\n"},
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
