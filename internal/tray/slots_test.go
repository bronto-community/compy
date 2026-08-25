//go:build darwin

package tray

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/cfgstore"
)

func TestAssignSlots(t *testing.T) {
	cases := []struct {
		name         string
		configs      []string
		active       string
		slots        int
		wantInline   []string
		wantOverflow []string
	}{
		{"fits", []string{"a", "b"}, "a", 4, []string{"a", "b"}, nil},
		{"exact", []string{"a", "b"}, "", 2, []string{"a", "b"}, nil},
		{"overflow", []string{"a", "b", "c", "d"}, "", 2, []string{"a", "b"}, []string{"c", "d"}},
		{"active promoted from overflow", []string{"a", "b", "c", "d"}, "d", 2, []string{"a", "d"}, []string{"b", "c"}},
		{"active already inline unchanged", []string{"a", "b", "c"}, "a", 2, []string{"a", "b"}, []string{"c"}},
		{"empty", nil, "", 4, nil, nil},
	}
	for _, c := range cases {
		inline, overflow := assignSlots(c.configs, c.active, c.slots)
		if !reflect.DeepEqual(inline, c.wantInline) || !reflect.DeepEqual(overflow, c.wantOverflow) {
			t.Errorf("%s: got inline=%v overflow=%v, want %v / %v", c.name, inline, overflow, c.wantInline, c.wantOverflow)
		}
	}
}

func TestStatusLines(t *testing.T) {
	cases := []struct {
		name        string
		st          app.Status
		errs, warns int
		wantLine1   string
		wantLine2   string
	}{
		{
			name:      "no configuration",
			st:        app.Status{Running: true, GRPCPort: 14317, HTTPPort: 14318},
			wantLine1: "no configuration",
			wantLine2: "grpc :14317 · http :14318",
		},
		{
			name:      "running with set",
			st:        app.Status{Running: true, Config: "prod", Preset: "eu", GRPCPort: 14317, HTTPPort: 14318},
			wantLine1: "running — prod (eu)",
			wantLine2: "grpc :14317 · http :14318",
		},
		{
			name:      "running without set omits parens",
			st:        app.Status{Running: true, Config: "prod", GRPCPort: 14317, HTTPPort: 14318},
			wantLine1: "running — prod",
		},
		{
			name:      "stopped",
			st:        app.Status{Running: false, Config: "prod", GRPCPort: 14317, HTTPPort: 14318},
			wantLine1: "stopped — prod",
		},
		{
			name:      "errors and warnings appended",
			st:        app.Status{Running: true, Config: "prod", GRPCPort: 14317, HTTPPort: 14318},
			errs:      2,
			warns:     1,
			wantLine1: "running — prod",
			wantLine2: "grpc :14317 · http :14318 · 2 err · 1 warn",
		},
		{
			name:      "zero errors/warnings omit the tail",
			st:        app.Status{Running: true, Config: "prod", GRPCPort: 14317, HTTPPort: 14318},
			errs:      0,
			warns:     0,
			wantLine1: "running — prod",
			wantLine2: "grpc :14317 · http :14318",
		},
	}
	for _, c := range cases {
		if c.wantLine2 == "" {
			c.wantLine2 = "grpc :14317 · http :14318"
		}
		line1, line2 := statusLines(c.st, c.errs, c.warns)
		if line1 != c.wantLine1 || line2 != c.wantLine2 {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, line1, line2, c.wantLine1, c.wantLine2)
		}
	}
}

func TestActiveVariableSets(t *testing.T) {
	configs := []cfgstore.Info{
		{Name: "solo", Meta: cfgstore.Meta{Presets: map[string]map[string]string{"default": {}}, ActivePreset: "default"}},
		{Name: "multi", Meta: cfgstore.Meta{
			Presets:      map[string]map[string]string{"eu": {}, "default": {}, "us": {}},
			ActivePreset: "us",
		}},
	}
	cases := []struct {
		name      string
		active    string
		wantNames []string
		wantSet   string
		wantShow  bool
	}{
		{"no active config", "", nil, "", false},
		{"unknown active config", "ghost", nil, "", false},
		{"single set hidden", "solo", nil, "", false},
		{"multi set shown sorted", "multi", []string{"default", "eu", "us"}, "us", true},
	}
	for _, c := range cases {
		names, set, show := activePresets(configs, c.active)
		if show != c.wantShow || set != c.wantSet || !reflect.DeepEqual(names, c.wantNames) {
			t.Errorf("%s: got names=%v set=%q show=%v, want names=%v set=%q show=%v",
				c.name, names, set, show, c.wantNames, c.wantSet, c.wantShow)
		}
	}
}

// TestOpenWindowReusesTheLiveOne covers the 2026-08-25 report that menu-bar
// "Open compy" stacks up another window every time: with a window already
// open the click must raise it, and only spawn when there is none (first
// click, or the previous one was closed).
func TestOpenWindowReusesTheLiveOne(t *testing.T) {
	spawns, raises := 0, 0
	live := &windowProc{pid: 4242, done: make(chan struct{})}
	spawn := func() (*windowProc, error) { spawns++; return live, nil }
	raise := func(pid int) error {
		if pid != live.pid {
			t.Errorf("raise(%d), want %d", pid, live.pid)
		}
		raises++
		return nil
	}

	cur, err := openWindow(nil, spawn, raise) // first click: nothing open yet
	if err != nil || cur != live {
		t.Fatalf("openWindow(nil) = %v, %v", cur, err)
	}
	if spawns != 1 || raises != 0 {
		t.Fatalf("first click: spawns=%d raises=%d, want 1/0", spawns, raises)
	}

	for i := 0; i < 3; i++ { // window still open: raise it, never spawn
		cur, err = openWindow(cur, spawn, raise)
		if err != nil || cur != live {
			t.Fatalf("openWindow(live) = %v, %v", cur, err)
		}
	}
	if spawns != 1 || raises != 3 {
		t.Fatalf("with a live window: spawns=%d raises=%d, want 1/3", spawns, raises)
	}

	close(live.done) // the user closed the window
	fresh := &windowProc{pid: 7, done: make(chan struct{})}
	spawn = func() (*windowProc, error) { spawns++; return fresh, nil }
	cur, err = openWindow(cur, spawn, raise)
	if err != nil || cur != fresh {
		t.Fatalf("openWindow after close = %v, %v", cur, err)
	}
	if spawns != 2 || raises != 3 {
		t.Fatalf("after close: spawns=%d raises=%d, want 2/3", spawns, raises)
	}
}

// TestOpenWindowKeepsTheProcessWhenRaisingFails: a failed raise (no
// Accessibility permission, say) must be reported, not papered over by
// spawning a second window -- that is the bug we are fixing.
func TestOpenWindowKeepsTheProcessWhenRaisingFails(t *testing.T) {
	live := &windowProc{pid: 1, done: make(chan struct{})}
	spawn := func() (*windowProc, error) { t.Error("must not spawn while a window is open"); return nil, nil }
	cur, err := openWindow(live, spawn, func(int) error { return errRaise })
	if err == nil {
		t.Error("openWindow err = nil, want the raise failure reported")
	}
	if cur != live {
		t.Errorf("openWindow returned %v, want the live process kept", cur)
	}
}

var errRaise = fmt.Errorf("no accessibility permission")
