//go:build darwin

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/cfgstore"
)

// TestAlphabetical pins the 2026-08-26 ordering ruling: the whole menu is
// alphabetical, case-insensitively — recency no longer orders it (the Recent
// list stays in status/API; only the tray stopped consuming it).
func TestAlphabetical(t *testing.T) {
	cases := []struct {
		name  string
		names []string
		want  []string
	}{
		{"sorted", []string{"c", "a", "b"}, []string{"a", "b", "c"}},
		{"case-insensitive", []string{"Zeta", "alpha", "Beta"}, []string{"alpha", "Beta", "Zeta"}},
		{"equal folds tie-break case-sensitively", []string{"a", "A"}, []string{"A", "a"}},
		{"empty", nil, nil},
	}
	for _, c := range cases {
		got := alphabetical(c.names)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: alphabetical(%v) = %v, want %v", c.name, c.names, got, c.want)
		}
	}
}

// TestFlatRows pins the flat activation list (owner ruling 2026-08-30: no
// preset submenus): one row per (config, preset) target — a single-preset
// config titled by its name alone, a multi-preset config as "name · preset"
// rows — configs alphabetical (case-insensitive), presets alphabetical
// within, stable across map iteration order.
func TestFlatRows(t *testing.T) {
	info := func(name string, presets ...string) cfgstore.Info {
		i := cfgstore.Info{Name: name}
		if len(presets) > 0 {
			i.Meta.Presets = map[string]map[string]string{}
			for _, p := range presets {
				i.Meta.Presets[p] = map[string]string{}
			}
		}
		return i
	}
	row := func(config, preset, title string) flatRow {
		return flatRow{presetTarget{config: config, preset: preset}, title}
	}
	cases := []struct {
		name    string
		configs []cfgstore.Info
		want    []flatRow
	}{
		{"empty", nil, nil},
		{
			"single-preset config is one row titled by name, activating that exact preset",
			[]cfgstore.Info{info("debug", "staging")},
			[]flatRow{row("debug", "staging", "debug")},
		},
		{
			"multi-preset config fans out to name · preset rows, presets sorted",
			[]cfgstore.Info{info("bronto", "staging", "default")},
			[]flatRow{row("bronto", "default", "bronto · default"), row("bronto", "staging", "bronto · staging")},
		},
		{
			"mix orders configs alphabetically (case-insensitive), presets within",
			[]cfgstore.Info{info("Zeta", "b", "a"), info("debug", "default"), info("bronto", "us", "eu")},
			[]flatRow{
				row("bronto", "eu", "bronto · eu"),
				row("bronto", "us", "bronto · us"),
				row("debug", "default", "debug"),
				row("Zeta", "a", "Zeta · a"),
				row("Zeta", "b", "Zeta · b"),
			},
		},
		{
			// Below cfgstore's default-preset invariant — broken state and
			// tests only — a preset-less config still gets a row; its ""
			// preset keeps the config's own active preset on activation.
			"preset-less config still rows, empty preset",
			[]cfgstore.Info{info("bare")},
			[]flatRow{row("bare", "", "bare")},
		},
	}
	for _, c := range cases {
		if got := flatRows(c.configs); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: flatRows = %+v, want %+v", c.name, got, c.want)
		}
	}
	// Stable across runs (map iteration must not leak into the order).
	many := []cfgstore.Info{info("a", "x", "y", "z"), info("b", "q", "p")}
	first := flatRows(many)
	for i := 0; i < 20; i++ {
		if got := flatRows(many); !reflect.DeepEqual(got, first) {
			t.Fatalf("flatRows unstable: run %d got %+v, first %+v", i, got, first)
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
			// 2026-08-26 amendment: More… simply continues the alphabetical
			// order — no re-sort of its own.
			"overflow continues the given order", []string{"a", "b", "c", "d"}, 2, []string{"a", "b"}, []string{"c", "d"},
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
		dropping  bool
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
			// Every config keeps a real preset now (cfgstore's default-preset
			// invariant), so an empty Preset means the status couldn't resolve
			// it — claim nothing rather than invent "default" (2026-08-27,
			// supersedes the 2026-08-26 implicit-default rule).
			name:      "unresolved preset claims nothing",
			st:        app.Status{Running: true, Config: "debug", Listening: []int{4317, 4318}},
			wantLine1: "● Running · debug",
			wantLine2: ":4317 :4318",
		},
		{
			name:      "stopped ignores config/preset/ports",
			st:        app.Status{Running: false, Config: "otlp-to-bronto", Preset: "staging", Listening: []int{4317}},
			wantLine1: "○ Stopped",
			wantLine2: "no listeners",
		},
		{
			// The brew-upgrade window: the plist names a deleted binary while
			// the process survives on the inode — only a restart runs the new
			// version, so the warnings segment says so.
			name:      "stale binary appends restart needed while running",
			st:        app.Status{Running: true, Config: "prod", Preset: "p", Listening: []int{14317}, StaleBinary: true},
			wantLine1: "● Running · prod · p",
			wantLine2: ":14317 · restart needed",
		},
		{
			// Rebooted (or stopped) inside the upgrade window: launchd's
			// login start failed on the deleted path — Start re-resolves.
			name:      "stale binary while stopped says restart needed",
			st:        app.Status{Running: false, Config: "prod", StaleBinary: true},
			wantLine1: "○ Stopped",
			wantLine2: "no listeners · restart needed",
		},
		{
			name:      "warnings appended, warn-only (no error count)",
			st:        app.Status{Running: true, Config: "prod", Preset: "default", Listening: []int{14317, 14318}},
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
		{
			// Nonconforming ports append to the warnings segment, in the
			// status line's own lowercase style.
			name: "nonconforming appends ports mismatch",
			st: app.Status{Running: true, Config: "odd", Preset: "p", Listening: []int{6000, 6001},
				Conformance: &app.PortsVerdict{Conforming: false, MissingHTTP: true, Actual: []int{6000, 6001}}},
			warns:     1,
			wantLine1: "● Running · odd · p",
			wantLine2: ":6000 :6001 · 1 warnings · ports mismatch",
		},
		{
			// A conforming verdict — even with the grpc port missing — adds
			// nothing here: the mismatch chip is for stranded apps only.
			name: "conforming grpc-only miss stays quiet",
			st: app.Status{Running: true, Config: "httponly", Preset: "p", Listening: []int{14318},
				Conformance: &app.PortsVerdict{Conforming: true, MissingGRPC: true, Actual: []int{14318}}},
			wantLine1: "● Running · httponly · p",
			wantLine2: ":14318",
		},
		{
			// The drop diagnosis joins the warnings segment: the collector
			// runs but silently drops because required values are missing.
			name:      "drop diagnosis appends dropping data",
			st:        app.Status{Running: true, Config: "bronto", Preset: "default", Listening: []int{14317, 14318}},
			warns:     2,
			dropping:  true,
			wantLine1: "● Running · bronto · default",
			wantLine2: ":14317 :14318 · 2 warnings · dropping data",
		},
		{
			name:      "drop diagnosis alone is the whole tail",
			st:        app.Status{Running: true, Config: "bronto", Preset: "default"},
			dropping:  true,
			wantLine1: "● Running · bronto · default",
			wantLine2: "dropping data",
		},
	}
	for _, c := range cases {
		line1, line2 := statusLines(c.st, c.warns, c.dropping)
		if line1 != c.wantLine1 || line2 != c.wantLine2 {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, line1, line2, c.wantLine1, c.wantLine2)
		}
	}
}

// TestPendingActivationForms covers the status line's activating form (the
// in-flight state on the rows themselves is the transition icons — see
// TestActivateMarks — not a title suffix).
func TestPendingActivationForms(t *testing.T) {
	if got, want := activatingLine("otlp", ""), "Activating otlp…"; got != want {
		t.Errorf("activatingLine no preset = %q, want %q", got, want)
	}
	if got, want := activatingLine("otlp", "eu"), "Activating otlp · eu…"; got != want {
		t.Errorf("activatingLine with preset = %q, want %q", got, want)
	}
}

func TestToggleForms(t *testing.T) {
	if got, want := toggleTitle(true), "Stop collector"; got != want {
		t.Errorf("toggleTitle(running) = %q, want %q", got, want)
	}
	if got, want := toggleTitle(false), "Start collector"; got != want {
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

// TestSteadyStates pins the steady-state icon selection sync paints: the
// exact (config, preset) row that is RUNNING carries the active icon, every
// other row none — and a stopped collector shows no icons anywhere. This is
// also the failure repaint: a failed swap's end-of-doAct sync sees launchd
// still running the survivor, so the survivor gets the active icon back and
// the failed target drops its going-up mark to none.
func TestSteadyStates(t *testing.T) {
	running := app.Status{Running: true, Config: "acme", Preset: "eu"}
	if got := rowState(presetTarget{config: "acme", preset: "eu"}, running); got != itemActive {
		t.Errorf("running row = %v, want itemActive", got)
	}
	if got := rowState(presetTarget{config: "acme", preset: "us"}, running); got != itemNone {
		t.Errorf("other preset row of the running config = %v, want itemNone", got)
	}
	if got := rowState(presetTarget{config: "beta", preset: "eu"}, running); got != itemNone {
		t.Errorf("same-named preset row of another config = %v, want itemNone", got)
	}

	stopped := app.Status{Running: false, Config: "acme", Preset: "eu"}
	if rowState(presetTarget{config: "acme", preset: "eu"}, stopped) != itemNone {
		t.Error("stopped: no icons anywhere, however recently acme·eu was active")
	}
}

// TestActivateMarks pins the transition icons an activation click paints —
// both sides of a swap show their state: down on the row being deactivated,
// up on the row activating.
func TestActivateMarks(t *testing.T) {
	cases := []struct {
		name   string
		st     app.Status
		target presetTarget
		want   swapMarks
	}{
		{
			"swap A→B: A's row down, B's row up",
			app.Status{Running: true, Config: "acme", Preset: "eu"},
			presetTarget{config: "beta", preset: "us"},
			swapMarks{down: presetTarget{config: "acme", preset: "eu"}, up: presetTarget{config: "beta", preset: "us"}},
		},
		{
			// A same-config preset swap is just two rows now: old preset row
			// down, new preset row up.
			"same-config preset swap: old preset row down, new up",
			app.Status{Running: true, Config: "acme", Preset: "eu"},
			presetTarget{config: "acme", preset: "us"},
			swapMarks{down: presetTarget{config: "acme", preset: "eu"}, up: presetTarget{config: "acme", preset: "us"}},
		},
		{
			"activation from stopped: up only, nothing goes down",
			app.Status{Running: false, Config: "acme", Preset: "eu"},
			presetTarget{config: "beta", preset: "us"},
			swapMarks{up: presetTarget{config: "beta", preset: "us"}},
		},
		{
			"re-clicking the running row: up (re-apply), never down+up at once",
			app.Status{Running: true, Config: "acme", Preset: "eu"},
			presetTarget{config: "acme", preset: "eu"},
			swapMarks{up: presetTarget{config: "acme", preset: "eu"}},
		},
	}
	for _, c := range cases {
		if got := activateMarks(c.st, c.target); got != c.want {
			t.Errorf("%s: activateMarks = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestToggleAndRestartMarks: stopping shows going-down on the running row
// until sync confirms; starting shows going-up on the active row; restart
// shows going-up on the running row — the collector comes straight back,
// and one paint can't show down-then-up.
func TestToggleAndRestartMarks(t *testing.T) {
	running := app.Status{Running: true, Config: "acme", Preset: "eu"}
	stopped := app.Status{Running: false, Config: "acme", Preset: "eu"}
	acmeEU := presetTarget{config: "acme", preset: "eu"}

	if got, want := toggleMarks(running), (swapMarks{down: acmeEU}); got != want {
		t.Errorf("stop: toggleMarks = %+v, want %+v", got, want)
	}
	if got, want := toggleMarks(stopped), (swapMarks{up: acmeEU}); got != want {
		t.Errorf("start: toggleMarks = %+v, want %+v", got, want)
	}
	if got, want := restartMarks(running), (swapMarks{up: acmeEU}); got != want {
		t.Errorf("restart: restartMarks = %+v, want %+v", got, want)
	}
	if got := restartMarks(stopped); got != (swapMarks{}) {
		t.Errorf("restart while stopped marks nothing, got %+v", got)
	}

	// A preset-less config's row target has an empty preset.
	want := swapMarks{down: presetTarget{config: "debug"}}
	if got := toggleMarks(app.Status{Running: true, Config: "debug"}); got != want {
		t.Errorf("stop preset-less: toggleMarks = %+v, want %+v", got, want)
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

	rdir, err := filepath.EvalSymlinks(dir) // TempDir may itself hold symlinks (/var → /private/var)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(rdir, "compy.app", "Contents", "MacOS", "compy")

	// Running from inside the bundle already: nothing further to prefer.
	// (bundled is itself a symlink, so windowExe hands back its resolved
	// bundle path — the same file.)
	if got := windowExe(bundled); got != bundled && got != want {
		t.Errorf("exe inside bundle: windowExe = %q, want %q", got, bundled)
	}

	// Homebrew layout: the running exe is a symlink (/opt/homebrew/bin/compy)
	// into the Caskroom, where the bundle sits next to the real binary — the
	// symlink's own directory has no bundle, so windowExe must look next to
	// the symlink's target.
	bindir := filepath.Join(dir, "bin")
	if err := os.Mkdir(bindir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bindir, "compy")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	if got := windowExe(link); got != want {
		t.Errorf("symlinked exe (Homebrew): windowExe = %q, want %q", got, want)
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

// TestUpdateLines pins the availability notices under the status block:
// collector and compy updates are distinguished, each carries the way to
// get it VISIBLY (menu-item tooltips never show on macOS — 2026-08-29 HIG
// audit), both at once shows both, and nothing known shows nothing.
func TestUpdateLines(t *testing.T) {
	cases := []struct {
		name, collector, compy, want1, want2 string
	}{
		{"collector only", "0.161.0", "", "Collector 0.161.0 available — Open compy to update", ""},
		{"compy only", "", "0.2.0", "", "compy 0.2.0 available — brew upgrade compy"},
		{"both", "0.161.0", "0.2.0", "Collector 0.161.0 available — Open compy to update", "compy 0.2.0 available — brew upgrade compy"},
		{"none", "", "", "", ""},
	}
	for _, c := range cases {
		l1, l2 := updateLines(c.collector, c.compy)
		if l1 != c.want1 || l2 != c.want2 {
			t.Errorf("%s: updateLines(%q, %q) = %q, %q; want %q, %q", c.name, c.collector, c.compy, l1, l2, c.want1, c.want2)
		}
	}
}
