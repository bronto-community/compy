package launchd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderPlist(t *testing.T) {
	out := RenderPlist("/usr/local/bin/otelcol", []string{"--config", "a & b.yaml"}, "/tmp/a & b.log")
	s := string(out)

	if !strings.Contains(s, "<key>Label</key><string>"+Label+"</string>") {
		t.Fatalf("missing Label: %s", s)
	}
	if !strings.Contains(s, "<string>/usr/local/bin/otelcol</string>") {
		t.Fatalf("missing bin argv: %s", s)
	}
	if !strings.Contains(s, "<string>--config</string>") {
		t.Fatalf("missing --config argv: %s", s)
	}
	if !strings.Contains(s, "<string>a &amp; b.yaml</string>") {
		t.Fatalf("arg not escaped: %s", s)
	}
	if !strings.Contains(s, "<string>/tmp/a &amp; b.log</string>") {
		t.Fatalf("logpath not escaped: %s", s)
	}
	if strings.Contains(s, "a & b.yaml") {
		t.Fatalf("raw unescaped & leaked through: %s", s)
	}
}

func TestInstallCallsLaunchctl(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var calls [][]string
	orig := Exec
	Exec = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, nil
	}
	defer func() { Exec = orig }()

	if err := Install("/usr/local/bin/otelcol", []string{"--config", "x.yaml"}, "/tmp/out.log"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	path, err := PlistPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	if !strings.Contains(string(data), Label) {
		t.Fatalf("plist missing label: %s", data)
	}

	var foundBootout, foundBootstrap bool
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	for _, c := range calls {
		if len(c) > 0 && c[0] == "bootout" {
			foundBootout = true
		}
		if len(c) > 0 && c[0] == "bootstrap" {
			foundBootstrap = true
			if len(c) < 3 || c[1] != uid || c[2] != path {
				t.Fatalf("bootstrap args wrong: %v", c)
			}
		}
	}
	if !foundBootout {
		t.Fatalf("bootout not called: %v", calls)
	}
	if !foundBootstrap {
		t.Fatalf("bootstrap not called: %v", calls)
	}
}

// launchctl bootout returns before the job is fully gone; the bootstrap that
// follows then fails with "5: Input/output error". Install must retry.
func TestInstallRetriesBootstrapAfterBootoutRace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	attempts := 0
	orig := Exec
	Exec = func(args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "bootstrap" {
			attempts++
			if attempts < 3 {
				return []byte("Bootstrap failed: 5: Input/output error"), errors.New("exit status 5")
			}
		}
		return nil, nil
	}
	defer func() { Exec = orig }()

	if err := Install("/usr/local/bin/otelcol", nil, "/tmp/out.log"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("bootstrap attempts = %d, want 3", attempts)
	}
}

func TestRunningParsesState(t *testing.T) {
	orig := Exec
	defer func() { Exec = orig }()

	Exec = func(args ...string) ([]byte, error) {
		return []byte("some header\n\tstate = running\n"), nil
	}
	ok, err := Running()
	if err != nil || !ok {
		t.Fatalf("want running true, got %v %v", ok, err)
	}

	Exec = func(args ...string) ([]byte, error) {
		return []byte("some header\n\tstate = not running\n"), nil
	}
	ok, err = Running()
	if err != nil || ok {
		t.Fatalf("want running false, got %v %v", ok, err)
	}
}

func TestPlistPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := PlistPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if path != want {
		t.Fatalf("got %s want %s", path, want)
	}
}
