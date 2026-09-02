package app

import (
	"testing"

	"github.com/bronto-community/compy/internal/state"
)

// Empty and whitespace-only preset values must not reach the environment:
// an exported-but-empty VAR is "set" to the collector and defeats the
// yaml's own ${env:VAR:-default} fallback.
func TestActivationEnvOmitsEmptyValues(t *testing.T) {
	s := state.Settings{GRPCPort: 14317, HTTPPort: 14318, MetricsPort: 8888}
	env := activationEnv(map[string]any{
		"EMPTY":      "",
		"WHITESPACE": "  \t ",
		"REAL":       "value",
		"TYPED":      true, // a demoted tier-3 bag's leftover: not env material
	}, s)

	want := map[string]string{
		"REAL":               "value",
		"COMPY_GRPC_PORT":    "14317",
		"COMPY_HTTP_PORT":    "14318",
		"COMPY_METRICS_PORT": "8888",
	}
	if len(env) != len(want) {
		t.Errorf("activationEnv = %v, want %v", env, want)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	for _, k := range []string{"EMPTY", "WHITESPACE", "TYPED"} {
		if _, ok := env[k]; ok {
			t.Errorf("env contains %s, want it omitted", k)
		}
	}
}

// A nil preset map still yields compy's port variables.
func TestActivationEnvNilValues(t *testing.T) {
	env := activationEnv(nil, state.Settings{GRPCPort: 1, HTTPPort: 2, MetricsPort: 3})
	if len(env) != 3 || env["COMPY_GRPC_PORT"] != "1" || env["COMPY_HTTP_PORT"] != "2" ||
		env["COMPY_METRICS_PORT"] != "3" {
		t.Errorf("activationEnv(nil) = %v, want just the ports", env)
	}
}
