package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bronto-io/compy/internal/state"
)

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
