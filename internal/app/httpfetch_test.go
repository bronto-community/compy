package app

// Internal test: httpFetch and fetchClient are unexported, and the point
// here is the transport, not the App surface.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A server that accepts the connection and never answers must be abandoned
// by the response-header deadline, not held forever (G1: a stalled upstream
// used to wedge an activation's auto-download and the tray's update loop).
// The deadline is shrunk by swapping fetchClient — same code path, fast test.
func TestHTTPFetchAbandonsStalledServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // accept, never respond
	}))
	defer srv.Close()

	orig := fetchClient
	fetchClient = &http.Client{Transport: &http.Transport{
		ResponseHeaderTimeout: 100 * time.Millisecond,
	}}
	defer func() { fetchClient = orig }()

	start := time.Now()
	if _, _, err := httpFetch(srv.URL); err == nil {
		t.Fatal("httpFetch on a stalled server = nil, want a deadline error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("stalled fetch took %v, want abandonment near the 100ms deadline", d)
	}
}

// The real client keeps a deadline on every phase and no overall Timeout
// (archives are hundreds of MB).
func TestFetchClientHasPhaseDeadlinesNotOverallTimeout(t *testing.T) {
	tr := fetchClient.Transport.(*http.Transport)
	if tr.DialContext == nil || tr.TLSHandshakeTimeout == 0 || tr.ResponseHeaderTimeout == 0 {
		t.Fatalf("fetchClient transport missing a phase deadline: %+v", tr)
	}
	if fetchClient.Timeout != 0 {
		t.Fatalf("fetchClient.Timeout = %v, want 0 (an overall timeout would kill large archive downloads)", fetchClient.Timeout)
	}
}
