package collector

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIsHTTPPort classifies stubbed listeners: a real HTTP server (whatever
// its status code) is http, a listener that answers non-HTTP bytes (the
// shape a gRPC listener's HTTP/2 SETTINGS frame takes) is not, and a closed
// port is not.
func TestIsHTTPPort(t *testing.T) {
	// An HTTP server answering 404 — what otelcol's OTLP/HTTP server does
	// for GET / (verified against otelcol-compy 0.159.0).
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if !IsHTTPPort(portOf(t, srv.Listener)) {
		t.Error("IsHTTPPort(http 404 server) = false, want true")
	}

	// A listener speaking raw HTTP/2 bytes, as otelcol's gRPC listener does.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("\x00\x00\x06\x04\x00\x00\x00\x00\x00"))
			c.Close()
		}
	}()
	if IsHTTPPort(portOf(t, ln)) {
		t.Error("IsHTTPPort(raw http/2 bytes) = true, want false")
	}

	// A closed port: grab one, close it, probe it.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := portOf(t, ln2)
	ln2.Close()
	if IsHTTPPort(closed) {
		t.Error("IsHTTPPort(closed port) = true, want false")
	}
}

func portOf(t *testing.T, ln net.Listener) int {
	t.Helper()
	return ln.Addr().(*net.TCPAddr).Port
}
