package collector

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func TestParseListenPorts(t *testing.T) {
	// Real `lsof -Fn` shape: p<pid> once, f<fd>/n<name> per socket, plus the
	// forms that must be handled — v4, v6, wildcard, a dupe, and junk.
	out := []byte("p48213\nf5\nn127.0.0.1:6000\nf6\nn*:4317\nf7\nn[::1]:8888\nf8\nn127.0.0.1:6000\nf9\nnnoport\n")
	got := parseListenPorts(out)
	want := []int{4317, 6000, 8888}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseListenPorts = %v, want %v", got, want)
	}
	if got := parseListenPorts(nil); got != nil {
		t.Fatalf("empty output: got %v, want nil", got)
	}
}

func TestListeningPortsUndetectable(t *testing.T) {
	orig := lsofListen
	defer func() { lsofListen = orig }()
	lsofListen = func(pid int) ([]byte, error) { return nil, errors.New("lsof: not found") }
	if got := ListeningPorts(123); got != nil {
		t.Fatalf("lsof failure: got %v, want nil (no claim)", got)
	}
	if got := ListeningPorts(0); got != nil {
		t.Fatalf("pid 0: got %v, want nil", got)
	}
}

// TestListeningPortsReal is a real lsof round trip: the test process opens a
// listener and must find its own port. Skipped where lsof is absent (linux CI).
func TestListeningPortsReal(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not installed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	got := ListeningPorts(os.Getpid())
	for _, p := range got {
		if p == port {
			return
		}
	}
	t.Fatalf("ListeningPorts(self) = %v, want it to include %d", got, port)
}
