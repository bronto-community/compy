//go:build integration

// Package integration runs compy end-to-end against a real collector binary.
// The v1 test drove the deleted base+fragments model; T6 rewrites it for
// configurations (one config.yaml, variables injected through the
// environment).
package integration

import "testing"

func TestE2E(t *testing.T) {
	t.Skip("rewritten in T6 for the v2 configuration model")
}
