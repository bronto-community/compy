package state

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	t.Setenv("COMPY_HOME", t.TempDir())
	s, err := LoadSettings() // no file yet
	if err != nil || s.GRPCPort != 14317 || s.HTTPPort != 14318 {
		t.Fatalf("defaults wrong: %+v %v", s, err)
	}
	s.Enabled = []string{"jaeger", "bronto"}
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	s2, _ := LoadSettings()
	if !slices.Equal(s2.Enabled, []string{"bronto", "jaeger"}) { // sorted on save
		t.Fatalf("got %v", s2.Enabled)
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
	for _, sub := range []string{"config/backends", "logs", "last-good"} {
		if _, err := os.Stat(filepath.Join(home, sub)); err != nil {
			t.Fatal(sub, err)
		}
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
