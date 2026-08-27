// Package envvars computes the OTEL_* environment variables compy exposes,
// and emits them as shell scripts, subprocess environments, or OS-level
// (launchctl) settings.
package envvars

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/bronto-io/compy/internal/state"
)

// Exec creates commands run by SetOS/UnsetOS. Overridable in tests so they
// never invoke the real launchctl.
var Exec = exec.Command

// Vars computes the OTEL_* environment variables for the given settings.
//
// PROTOCOL stays explicit because SDK protocol defaults vary (the spec
// prefers http/protobuf, but several stable SDKs kept grpc). The per-signal
// exporters mostly default to otlp already, BUT some zero-code agents
// default logs to "none" — pinning all three makes "point your app at
// compy" hold for traces, metrics, AND logs. A process's own environment
// always shadows these.
func Vars(s state.Settings) map[string]string {
	return map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": fmt.Sprintf("http://127.0.0.1:%d", s.HTTPPort),
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_TRACES_EXPORTER":        "otlp",
		"OTEL_METRICS_EXPORTER":       "otlp",
		"OTEL_LOGS_EXPORTER":          "otlp",
	}
}

// Script renders vars as a shell script for the given shell: "sh" (the
// default, also used for ""), "fish", or "pwsh". Values are single-quoted
// per the target shell's own escaping rules so values are inert to the
// shell (no expansion of "$(...)", backticks, "$VAR", etc.) even when they
// come from user input (e.g. OTEL_RESOURCE_ATTRIBUTES). Keys are sorted for
// deterministic output. An unknown shell returns an error.
func Script(vars map[string]string, shell string) (string, error) {
	var b strings.Builder
	for _, k := range sortedKeys(vars) {
		v := vars[k]
		switch shell {
		case "", "sh":
			fmt.Fprintf(&b, "export %s=%s\n", k, quoteSh(v))
		case "fish":
			fmt.Fprintf(&b, "set -gx %s %s\n", k, quoteFish(v))
		case "pwsh":
			fmt.Fprintf(&b, "$env:%s = %s\n", k, quotePwsh(v))
		default:
			return "", fmt.Errorf("envvars: unsupported shell %q", shell)
		}
	}
	return b.String(), nil
}

// quoteSh single-quotes v for POSIX sh: each embedded quote character is
// replaced with: close quote, backslash-escaped literal quote, reopen quote.
func quoteSh(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// quoteFish single-quotes v for fish, whose single-quoted strings only
// recognize the backslash escapes \\ and \': backslash must be escaped
// first, then the quote.
func quoteFish(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "'", `\'`)
	return "'" + v + "'"
}

// quotePwsh single-quotes v for PowerShell: each embedded quote character
// is doubled.
func quotePwsh(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// Run execs argv[0] with os.Environ() plus vars, stdio inherited, and
// returns the child's exit code.
func Run(vars map[string]string, argv []string) (int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), envList(vars)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// SetOS sets vars in the launchd user environment via `launchctl setenv`.
func SetOS(vars map[string]string) error {
	return launchctlEach("setenv", vars)
}

// UnsetOS removes vars from the launchd user environment via
// `launchctl unsetenv`.
func UnsetOS(vars map[string]string) error {
	return launchctlEach("unsetenv", vars)
}

func launchctlEach(sub string, vars map[string]string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("not supported")
	}
	for _, k := range sortedKeys(vars) {
		args := []string{sub, k}
		if sub == "setenv" {
			args = append(args, vars[k])
		}
		if err := Exec("launchctl", args...).Run(); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func envList(vars map[string]string) []string {
	out := make([]string, 0, len(vars))
	for _, k := range sortedKeys(vars) {
		out = append(out, k+"="+vars[k])
	}
	return out
}
