//go:build darwin

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"fyne.io/systray"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/cfgstore"
)

func TestRecencyOrder(t *testing.T) {
	cases := []struct {
		name   string
		names  []string
		recent []string
		want   []string
	}{
		{"no recency: alphabetical", []string{"c", "a", "b"}, nil, []string{"a", "b", "c"}},
		{"recent first, rest alphabetical", []string{"a", "b", "c", "d"}, []string{"c", "a"}, []string{"c", "a", "b", "d"}},
		{"recent dedup keeps first occurrence", []string{"a", "b"}, []string{"a", "a", "b"}, []string{"a", "b"}},
		{
			// T1 review: DeleteConfig doesn't prune Settings.Recent, so a
			// recent entry can name a configuration that no longer exists —
			// it must not surface as a menu item (nor as a gap in the order).
			"stale recent entry (deleted config) dropped",
			[]string{"a", "b"}, []string{"ghost", "b", "a"}, []string{"b", "a"},
		},
		{"empty", nil, nil, nil},
	}
	for _, c := range cases {
		got := recencyOrder(c.names, c.recent)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: recencyOrder(%v, %v) = %v, want %v", c.name, c.names, c.recent, got, c.want)
		}
	}
}

func TestSplitInline(t *testing.T) {
	cases := []struct {
		name         string
		ordered      []string
		n            int
		wantInline   []string
		wantOverflow []string
	}{
		{"fits exactly", []string{"a", "b"}, 2, []string{"a", "b"}, nil},
		{"under capacity", []string{"a", "b"}, 4, []string{"a", "b"}, nil},
		{
			// C5.3: overflow is re-sorted alphabetically, independent of the
			// recency order that put it there.
			"overflow re-sorted alphabetically", []string{"z", "a", "c", "b"}, 2, []string{"z", "a"}, []string{"b", "c"},
		},
		{"empty", nil, 4, nil, nil},
	}
	for _, c := range cases {
		inline, overflow := splitInline(c.ordered, c.n)
		if !reflect.DeepEqual(inline, c.wantInline) || !reflect.DeepEqual(overflow, c.wantOverflow) {
			t.Errorf("%s: splitInline(%v, %d) = %v, %v, want %v, %v", c.name, c.ordered, c.n, inline, overflow, c.wantInline, c.wantOverflow)
		}
	}
}

func TestStatusLines(t *testing.T) {
	cases := []struct {
		name      string
		st        app.Status
		warns     int
		wantLine1 string
		wantLine2 string
	}{
		{
			name:      "running with preset and detected ports",
			st:        app.Status{Running: true, Config: "otlp-to-bronto", Preset: "staging", Listening: []int{4317, 4318}},
			wantLine1: "● Running · otlp-to-bronto · staging",
			wantLine2: ":4317 :4318",
		},
		{
			// 2026-08-26 feedback: the implicit preset is named "default",
			// consistently with the window, rather than omitted.
			name:      "running without a preset says default",
			st:        app.Status{Running: true, Config: "debug", Listening: []int{4317, 4318}},
			wantLine1: "● Running · debug · default",
			wantLine2: ":4317 :4318",
		},
		{
			name:      "stopped ignores config/preset/ports",
			st:        app.Status{Running: false, Config: "otlp-to-bronto", Preset: "staging", Listening: []int{4317}},
			wantLine1: "○ Stopped",
			wantLine2: "no listeners",
		},
		{
			name:      "warnings appended, warn-only (no error count)",
			st:        app.Status{Running: true, Config: "prod", Listening: []int{14317, 14318}},
			warns:     2,
			wantLine1: "● Running · prod · default",
			wantLine2: ":14317 :14318 · 2 warnings",
		},
		{
			// Detected-ports honesty: nothing detected means no claim at
			// all — never a guess from settings or YAML.
			name:      "nothing detected omits the ports segment",
			st:        app.Status{Running: true, Config: "custom", Preset: "p"},
			wantLine1: "● Running · custom · p",
			wantLine2: "",
		},
		{
			name:      "nothing detected but warnings keeps just the warning tail",
			st:        app.Status{Running: true, Config: "custom", Preset: "p"},
			warns:     1,
			wantLine1: "● Running · custom · p",
			wantLine2: "1 warnings",
		},
		{
			name:      "four ports listed in full",
			st:        app.Status{Running: true, Config: "prod", Preset: "p", Listening: []int{4317, 4318, 8888, 13133}},
			wantLine1: "● Running · prod · p",
			wantLine2: ":4317 :4318 :8888 :13133",
		},
		{
			name:      "more than four ports compact to a count",
			st:        app.Status{Running: true, Config: "prod", Preset: "p", Listening: []int{4317, 4318, 8888, 13133, 55679}},
			warns:     2,
			wantLine1: "● Running · prod · p",
			wantLine2: "5 ports open · 2 warnings",
		},
	}
	for _, c := range cases {
		line1, line2 := statusLines(c.st, c.warns)
		if line1 != c.wantLine1 || line2 != c.wantLine2 {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, line1, line2, c.wantLine1, c.wantLine2)
		}
	}
}

// TestPendingActivationForms covers the click-feedback strings: the clicked
// row's pending title suffix and the status line's activating form. The
// checkmark itself is untouched by design — it only ever reflects launchd
// truth, so pending state lives in titles alone.
func TestPendingActivationForms(t *testing.T) {
	if got, want := pendingTitle("otlp"), "otlp — Activating…"; got != want {
		t.Errorf("pendingTitle = %q, want %q", got, want)
	}
	if got, want := activatingLine("otlp", ""), "Activating otlp…"; got != want {
		t.Errorf("activatingLine no preset = %q, want %q", got, want)
	}
	if got, want := activatingLine("otlp", "eu"), "Activating otlp · eu…"; got != want {
		t.Errorf("activatingLine with preset = %q, want %q", got, want)
	}
}

func TestToggleForms(t *testing.T) {
	if got, want := toggleTitle(true), "Stop Collector"; got != want {
		t.Errorf("toggleTitle(running) = %q, want %q", got, want)
	}
	if got, want := toggleTitle(false), "Start Collector"; got != want {
		t.Errorf("toggleTitle(stopped) = %q, want %q", got, want)
	}
	if got, want := toggleBusyLine(true), "Stopping…"; got != want {
		t.Errorf("toggleBusyLine(running) = %q, want %q", got, want)
	}
	if got, want := toggleBusyLine(false), "Starting…"; got != want {
		t.Errorf("toggleBusyLine(stopped) = %q, want %q", got, want)
	}
}

func TestErrorLine(t *testing.T) {
	if got, want := errorLine(fmt.Errorf("boom")), "error: boom"; got != want {
		t.Errorf("errorLine = %q, want %q", got, want)
	}
	long := errorLine(fmt.Errorf("%s", strings.Repeat("x", 120)))
	if want := "error: " + strings.Repeat("x", 80) + "…"; long != want {
		t.Errorf("errorLine long = %q, want %q", long, want)
	}
}

func TestPresetChoices(t *testing.T) {
	cases := []struct {
		name      string
		info      cfgstore.Info
		wantNames []string
		wantMulti bool
	}{
		{"no presets: single-click", cfgstore.Info{}, nil, false},
		{
			"one preset: single-click",
			cfgstore.Info{Meta: cfgstore.Meta{Presets: map[string]map[string]string{"default": {}}}},
			nil, false,
		},
		{
			"two+ presets: submenu, sorted",
			cfgstore.Info{Meta: cfgstore.Meta{Presets: map[string]map[string]string{"us": {}, "default": {}, "eu": {}}}},
			[]string{"default", "eu", "us"}, true,
		},
	}
	for _, c := range cases {
		names, multi := presetChoices(c.info)
		if multi != c.wantMulti || !reflect.DeepEqual(names, c.wantNames) {
			t.Errorf("%s: presetChoices() = %v, %v, want %v, %v", c.name, names, multi, c.wantNames, c.wantMulti)
		}
	}
}

// TestPresetOwnershipFollowsSlotReassignment is the T3 review's regression:
// slot i is a fixed menu position, and a recency reorder can put a
// different configuration there between syncs. Config acme{default,prod}
// occupies slot i, then a re-sync reassigns it to beta{default,us} — both
// configs have a "default" preset, so the slot's preset-item cache (keyed
// by preset name only) reuses the very same *systray.MenuItem for
// "default" under both configs. Without click-time resolution, that item's
// click would still fire against acme (whoever it was created for);
// clicking it must activate beta, the config it currently represents.
//
// This drives menu.setPresetOwner/resolvePresetClick directly — the two
// syncRow calls a real reorder would make — rather than through syncRow's
// actual systray.MenuItem creation: AddSubMenuItemCheckbox blocks on the
// Cocoa main-thread run loop that only exists once systray.Run is driving
// it, so calling it here (outside Run) would hang the test rather than
// fail it.
func TestPresetOwnershipFollowsSlotReassignment(t *testing.T) {
	m := &menu{presetOwner: map[*systray.MenuItem]presetTarget{}}
	item := &systray.MenuItem{} // slot i's "default" preset row, reused across configs

	m.setPresetOwner(item, "acme", "default") // acme{default,prod} occupies slot i
	m.setPresetOwner(item, "beta", "default") // re-sync: beta{default,us} took the slot

	target, ok := m.resolvePresetClick(item)
	if !ok {
		t.Fatal("resolvePresetClick: no owner recorded")
	}
	if target.config != "beta" || target.preset != "default" {
		t.Errorf("resolvePresetClick = %+v, want {config:beta preset:default} — a click on the reused item must activate whoever owns it now, not acme", target)
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

// TestWindowExe: the tray spawns the window via the compy.app bundle next
// to the running binary when there is one (that path carries the app
// identity — menu name, Dock icon), and plain `exe` otherwise: no bundle,
// or already running from inside one.
func TestWindowExe(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "compy")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := windowExe(exe); got != exe {
		t.Errorf("no bundle: windowExe = %q, want %q", got, exe)
	}

	macos := filepath.Join(dir, "compy.app", "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		t.Fatal(err)
	}
	bundled := filepath.Join(macos, "compy")
	if err := os.Symlink(exe, bundled); err != nil { // make-app.sh symlinks
		t.Fatal(err)
	}
	if got := windowExe(exe); got != bundled {
		t.Errorf("bundle beside exe: windowExe = %q, want %q", got, bundled)
	}

	// Running from inside the bundle already: nothing further to prefer.
	if got := windowExe(bundled); got != bundled {
		t.Errorf("exe inside bundle: windowExe = %q, want %q", got, bundled)
	}

	// A dangling symlink (binary rebuilt elsewhere, bundle left behind)
	// must not be spawned.
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if got := windowExe(exe); got != exe {
		t.Errorf("dangling bundle symlink: windowExe = %q, want %q", got, exe)
	}
}
