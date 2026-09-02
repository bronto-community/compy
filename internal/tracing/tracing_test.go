package tracing

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/state"
)

// TestEndpointDefaultsToCompy: an unset endpoint means compy's OWN
// collector, and it FOLLOWS the configured OTLP/HTTP port — a user who
// moved compy off 14318 must not have their traces still aimed at it.
func TestEndpointDefaultsToCompy(t *testing.T) {
	if got := Endpoint(state.Settings{HTTPPort: 14318}); got != "http://127.0.0.1:14318" {
		t.Errorf("Endpoint(default) = %q", got)
	}
	if got := Endpoint(state.Settings{HTTPPort: 4318}); got != "http://127.0.0.1:4318" {
		t.Errorf("Endpoint does not follow the http port: %q", got)
	}
	if got := Endpoint(state.Settings{HTTPPort: 14318, TracingEndpoint: "https://otlp.example.com"}); got != "https://otlp.example.com" {
		t.Errorf("Endpoint(configured) = %q", got)
	}
	// Whitespace is not a value — an endpoint field the user blanked out
	// resolves back to compy, rather than to a URL of spaces.
	if got := Endpoint(state.Settings{HTTPPort: 14318, TracingEndpoint: "   "}); got != "http://127.0.0.1:14318" {
		t.Errorf("Endpoint(blank) = %q", got)
	}
}

// TestTracesURL: the setting is a BASE, so the signal path is appended.
// otlptracehttp.WithEndpointURL takes its URL literally, so getting this
// wrong posts to "/" and every span is silently 404'd away — which is
// exactly what happened before this existed.
func TestTracesURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://127.0.0.1:14318", "http://127.0.0.1:14318/v1/traces"},
		{"https://otlp.example.com", "https://otlp.example.com/v1/traces"},
		{"https://otlp.example.com/", "https://otlp.example.com/v1/traces"},
		// A base that already names a path is somebody's proxy: left alone.
		{"https://gateway.example.com/otlp/v1/traces", "https://gateway.example.com/otlp/v1/traces"},
		{"https://gateway.example.com/otlp", "https://gateway.example.com/otlp"},
	} {
		if got := TracesURL(tc.in); got != tc.want {
			t.Errorf("TracesURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseHeaders(t *testing.T) {
	got, err := ParseHeaders("Authorization: Bearer abc\n\n# a comment\nX-Tenant:  t1  \n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Authorization": "Bearer abc", "X-Tenant": "t1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseHeaders = %v, want %v", got, want)
	}
	if got, err := ParseHeaders(""); err != nil || len(got) != 0 {
		t.Errorf("ParseHeaders(empty) = %v, %v", got, err)
	}
	// A line the user got wrong is named, with its line number, and marked
	// BadRequest so the API answers 400 rather than 500.
	_, err = ParseHeaders("Authorization: Bearer abc\noops\n")
	if err == nil || !state.IsBadRequest(err) || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("ParseHeaders(bad) err = %v, want a BadRequest naming line 2", err)
	}
}

// TestSetupOffIsNoop: off is the default, and it must install nothing —
// every op() in app runs unguarded on the global provider.
func TestSetupOffIsNoop(t *testing.T) {
	shutdown, err := Setup(t.Context(), state.Settings{HTTPPort: 14318}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if shutdown == nil {
		t.Fatal("Setup returned a nil shutdown; callers always defer it")
	}
	if err := shutdown(t.Context()); err != nil {
		t.Errorf("no-op shutdown = %v", err)
	}
}

// TestSetupRejectsBadConfig: a malformed header line is the user's mistake
// and is reported, not swallowed — settings validation catches it first,
// but Setup runs on every process start against whatever is on disk.
func TestSetupRejectsBadConfig(t *testing.T) {
	_, err := Setup(t.Context(), state.Settings{Tracing: true, HTTPPort: 14318, TracingHeaders: "oops"}, "test")
	if err == nil || !state.IsBadRequest(err) {
		t.Errorf("Setup(bad headers) err = %v, want BadRequest", err)
	}
}
