package app_test

import (
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/state"
)

// TestPutSettingsTracing is the settings contract for compy's own tracing:
// partial updates, and the validation that keeps an unusable configuration
// out of settings.json rather than discovering it at the next process start
// — where the failure is silent by design.
func TestPutSettingsTracing(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	ptr := func(s string) *string { return &s }
	on, off := true, false
	ep, hd := "https://otlp.example.com", "Authorization: Bearer k"

	if err := a.PutSettings(nil, nil, nil, nil, &app.Tracing{On: &on, Endpoint: &ep, Headers: &hd}); err != nil {
		t.Fatal(err)
	}
	s, _ := state.LoadSettings()
	if !s.Tracing || s.TracingEndpoint != ep || s.TracingHeaders != hd {
		t.Fatalf("settings = %+v", s)
	}

	// Partial: flipping the switch leaves the destination alone, which is
	// what lets the UI's toggle send one field.
	if err := a.PutSettings(nil, nil, nil, nil, &app.Tracing{On: &off}); err != nil {
		t.Fatal(err)
	}
	if s, _ = state.LoadSettings(); s.Tracing || s.TracingEndpoint != ep {
		t.Errorf("partial update lost the endpoint: %+v", s)
	}

	// Clearing the endpoint is a real value, not a no-op: it means "back to
	// compy's own collector".
	if err := a.PutSettings(nil, nil, nil, nil, &app.Tracing{Endpoint: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if s, _ = state.LoadSettings(); s.TracingEndpoint != "" {
		t.Errorf("endpoint not cleared: %+v", s)
	}

	for _, tc := range []struct {
		name, want string
		tr         app.Tracing
	}{
		{"not a url", "http(s) URL", app.Tracing{Endpoint: ptr("nope")}},
		{"wrong scheme", "http(s) URL", app.Tracing{Endpoint: ptr("ftp://x.example")}},
		{"header without a colon", "Name: value", app.Tracing{Headers: ptr("Authorization Bearer k")}},
	} {
		err := a.PutSettings(nil, nil, nil, nil, &tc.tr)
		if err == nil || !state.IsBadRequest(err) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want a BadRequest naming %q", tc.name, err, tc.want)
		}
	}
}
