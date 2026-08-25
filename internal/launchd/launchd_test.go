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
	out := RenderPlist("/usr/local/bin/otelcol", []string{"--config", "a & b.yaml"}, "/tmp/a & b.log", nil)
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
	if strings.Contains(s, "EnvironmentVariables") {
		t.Fatalf("nil env should not emit EnvironmentVariables: %s", s)
	}
}

func TestRenderPlistEnvDict(t *testing.T) {
	out := RenderPlist("/usr/local/bin/otelcol", nil, "/tmp/a.log", map[string]string{
		"ZEBRA": "z & q",
		"ALPHA": "a",
	})
	s := string(out)

	if !strings.Contains(s, "<key>EnvironmentVariables</key><dict>") {
		t.Fatalf("missing EnvironmentVariables dict: %s", s)
	}
	alphaIdx := strings.Index(s, "<key>ALPHA</key>")
	zebraIdx := strings.Index(s, "<key>ZEBRA</key>")
	if alphaIdx == -1 || zebraIdx == -1 || alphaIdx > zebraIdx {
		t.Fatalf("keys not sorted (ALPHA before ZEBRA): %s", s)
	}
	if !strings.Contains(s, "<key>ZEBRA</key><string>z &amp; q</string>") {
		t.Fatalf("value not escaped: %s", s)
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

	if err := Install("/usr/local/bin/otelcol", []string{"--config", "x.yaml"}, "/tmp/out.log", nil); err != nil {
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

	if err := Install("/usr/local/bin/otelcol", nil, "/tmp/out.log", nil); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("bootstrap attempts = %d, want 3", attempts)
	}
}

func TestRenderPlistKeepAliveVariants(t *testing.T) {
	on := string(renderPlist(Label, "/bin/otelcol", nil, "/tmp/x.log", true, nil))
	if !strings.Contains(on, "<key>KeepAlive</key><true/>") {
		t.Fatalf("keepAlive=true missing <true/>: %s", on)
	}
	off := string(renderPlist(TrayLabel, "/bin/compy", []string{"tray"}, "/tmp/t.log", false, nil))
	if !strings.Contains(off, "<key>KeepAlive</key><false/>") {
		t.Fatalf("keepAlive=false missing <false/>: %s", off)
	}
	if !strings.Contains(off, "<key>Label</key><string>"+TrayLabel+"</string>") {
		t.Fatalf("tray label missing: %s", off)
	}
}

func TestInstallAgentTrayLabel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var calls [][]string
	orig := Exec
	Exec = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, nil
	}
	defer func() { Exec = orig }()

	if err := InstallAgent(TrayLabel, "/bin/compy", []string{"tray"}, "/tmp/t.log", false, nil); err != nil {
		t.Fatalf("InstallAgent: %v", err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", TrayLabel+".plist")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("tray plist not written: %v", err)
	}
	if !strings.Contains(string(data), "<false/>") {
		t.Fatalf("tray plist should not keep alive: %s", data)
	}
	var bootstrapped bool
	for _, c := range calls {
		if len(c) > 2 && c[0] == "bootstrap" && c[2] == path {
			bootstrapped = true
		}
	}
	if !bootstrapped {
		t.Fatalf("bootstrap not called with tray plist: %v", calls)
	}

	if err := UninstallAgent(TrayLabel); err != nil {
		t.Fatalf("UninstallAgent: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("tray plist not removed: %v", err)
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
