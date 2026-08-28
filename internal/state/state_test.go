package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSettingsRoundTrip(t *testing.T) {
	t.Setenv("COMPY_HOME", t.TempDir())
	s, err := LoadSettings() // no file yet
	if err != nil || s.GRPCPort != 14317 || s.HTTPPort != 14318 {
		t.Fatalf("defaults wrong: %+v %v", s, err)
	}
	s.ActiveConfig = "debug"
	s.Distro = "core"
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	s.Recent = Remember(nil, "debug")
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s2, s) {
		t.Fatalf("round trip: got %+v, want %+v", s2, s)
	}
}

// A v1 settings.json carries "enabled"/"raw_mode" and no v2 fields: it must
// load without error, ignoring what it does not know and defaulting the rest.
func TestLoadSettingsAcceptsV1File(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPY_HOME", home)
	if _, err := Dir(); err != nil {
		t.Fatal(err)
	}
	old := `{"grpc_port":14317,"http_port":14318,"distro":"core","enabled":["bronto"],"raw_mode":true,"os_env":true}`
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings on a v1 file: %v", err)
	}
	if s.GRPCPort != 14317 || s.Distro != "core" || !s.OSEnv {
		t.Errorf("known fields lost: %+v", s)
	}
	if s.ActiveConfig != "" {
		t.Errorf("new fields not defaulted: %+v", s)
	}
}

// A settings.json missing the port fields must still get compy's defaults.
func TestLoadSettingsDefaultsMissingPorts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPY_HOME", home)
	if _, err := Dir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"distro":"core"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.GRPCPort != 14317 || s.HTTPPort != 14318 {
		t.Fatalf("ports = %d/%d, want defaults", s.GRPCPort, s.HTTPPort)
	}
}

func TestBaseDir(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		xdgDataHome string
		home        string
		want        string
	}{
		{"darwin", "darwin", "", "/Users/x", "/Users/x/Library/Application Support/compy"},
		{"darwin ignores XDG_DATA_HOME", "darwin", "/custom/data", "/Users/x", "/Users/x/Library/Application Support/compy"},
		{"linux with XDG_DATA_HOME", "linux", "/custom/data", "/home/x", "/custom/data/compy"},
		{"linux without XDG_DATA_HOME", "linux", "", "/home/x", "/home/x/.local/share/compy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := baseDir(c.goos, c.xdgDataHome, c.home); got != c.want {
				t.Errorf("baseDir(%q, %q, %q) = %q, want %q", c.goos, c.xdgDataHome, c.home, got, c.want)
			}
		})
	}
}

func TestValidBackendName(t *testing.T) {
	for name, want := range map[string]bool{
		"jaeger": true, "my-backend2": true, "": false, "-x": false,
		"Has/Slash": false, "UPPER": false, strings.Repeat("a", 65): false,
	} {
		if ValidBackendName(name) != want {
			t.Errorf("%q: want %v", name, want)
		}
	}
}

func TestDirCreatesSubdirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPY_HOME", home)
	if _, err := Dir(); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"configs", "logs", "last-good"} {
		if _, err := os.Stat(filepath.Join(home, sub)); err != nil {
			t.Fatal(sub, err)
		}
	}
}

func TestUpdateCheckRoundTrip(t *testing.T) {
	t.Setenv("COMPY_HOME", t.TempDir())
	c, err := LoadUpdateCheck() // no file yet: zero value, no claim
	if err != nil || c.Latest != "" || !c.CheckedAt.IsZero() {
		t.Fatalf("defaults wrong: %+v %v", c, err)
	}
	want := UpdateCheck{Latest: "0.161.0", CheckedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
	if err := SaveUpdateCheck(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadUpdateCheck()
	if err != nil || got.Latest != want.Latest || !got.CheckedAt.Equal(want.CheckedAt) {
		t.Fatalf("got %+v (%v), want %+v", got, err, want)
	}
}

func TestDistrosRoundTrip(t *testing.T) {
	t.Setenv("COMPY_HOME", t.TempDir())
	ds, err := LoadDistros() // no file yet
	if err != nil || len(ds) != 0 {
		t.Fatalf("defaults wrong: %+v %v", ds, err)
	}
	want := []Distro{{Name: "otelcol", Path: "/usr/local/bin/otelcol"}}
	if err := SaveDistros(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDistros()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// The upstream mark must survive fmt.Errorf("%w") wrapping — a marker a
// single %w drops is a marker nobody can rely on (same contract as
// BadRequest).
func TestUpstreamMarkSurvivesWrapping(t *testing.T) {
	err := Upstream(errors.New("release check: rate limited"))
	wrapped := fmt.Errorf("distro %q: %w", "otlp", err)
	if !IsUpstream(err) || !IsUpstream(wrapped) {
		t.Fatalf("IsUpstream = (%v, %v), want true for the marked error and its wrap", IsUpstream(err), IsUpstream(wrapped))
	}
	if IsUpstream(errors.New("plain")) || IsBadRequest(err) {
		t.Fatal("upstream mark must not leak into other classifications")
	}
	if wrapped.Error() != `distro "otlp": release check: rate limited` {
		t.Fatalf("message changed by marking: %q", wrapped.Error())
	}
}
