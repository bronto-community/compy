//go:build darwin

package tray

import (
	"strconv"
	"testing"

	"fyne.io/systray"

	"github.com/bronto-community/compy/internal/cfgstore"
)

// TestKeyEquivalents pins the static half: plain s/r/o on
// toggle/restart/open and ⌘Q (cmd set) on Quit — nothing else. Digits are
// the dynamic half (TestDigitEquivalents).
func TestKeyEquivalents(t *testing.T) {
	toggle, restart, open, quit := &systray.MenuItem{}, &systray.MenuItem{}, &systray.MenuItem{}, &systray.MenuItem{}
	eqs := keyEquivalents(toggle, restart, open, quit)
	want := []keyEquiv{{toggle, "s", false}, {restart, "r", false}, {open, "o", false}, {quit, "q", true}}
	if len(eqs) != len(want) {
		t.Fatalf("got %d equivalents, want %d", len(eqs), len(want))
	}
	for i, w := range want {
		if eqs[i] != w {
			t.Errorf("eqs[%d] = {%q cmd=%v} on wrong item, want %q cmd=%v", i, eqs[i].key, eqs[i].cmd, w.key, w.cmd)
		}
	}
}

// TestDigitEquivalents pins the flat-menu digits: every one of the first
// nine slots carries its positional digit — all rows are plain items now
// (owner ruling 2026-08-30), so every digit renders visibly and fires
// "activate this row's (config, preset)". Only slots 1–9 carry digits.
func TestDigitEquivalents(t *testing.T) {
	slots := make([]*systray.MenuItem, maxInline)
	for i := range slots {
		slots[i] = &systray.MenuItem{}
	}
	eqs := digitEquivalents(slots)
	if len(eqs) != 9 {
		t.Fatalf("got %d digit entries, want 9 (the 10th slot carries none)", len(eqs))
	}
	for i, e := range eqs {
		if e.item != slots[i] || e.key != strconv.Itoa(i+1) || e.cmd {
			t.Errorf("eqs[%d] = {%q cmd=%v}, want key %q on slot %d", i, e.key, e.cmd, strconv.Itoa(i+1), i)
		}
	}
}

// TestDigitsRetargetOnResort documents why the digits need no re-binding
// when the flat list reorders: digit d is bound to slot d-1, a FIXED menu
// position, and sync() assigns the d-th flat row to exactly that slot — so
// after any create/delete/rename or preset change, digit d fires whatever
// (config, preset) is the d-th visible row now (the click handler resolves
// slotTargets at click time, same as a mouse click on the row).
func TestDigitsRetargetOnResort(t *testing.T) {
	slots := make([]*systray.MenuItem, maxInline)
	for i := range slots {
		slots[i] = &systray.MenuItem{}
	}
	eqs := digitEquivalents(slots)

	info := func(name string, presets ...string) cfgstore.Info {
		i := cfgstore.Info{Name: name, Meta: cfgstore.Meta{Presets: map[string]map[string]any{}}}
		for _, p := range presets {
			i.Meta.Presets[p] = map[string]any{}
		}
		return i
	}

	// digitTarget mirrors the sync()+click path: what (config, preset) does
	// digit d activate for this configs snapshot?
	digitTarget := func(configs []cfgstore.Info, d int) presetTarget {
		inline, _ := splitInline(flatRows(configs), maxInline)
		for _, e := range eqs {
			if e.key != strconv.Itoa(d) {
				continue
			}
			for i, slot := range slots {
				if slot == e.item && i < len(inline) {
					return inline[i].target // slotTargets[i], resolved at click time
				}
			}
		}
		return presetTarget{}
	}

	// bronto{default,staging} fans out to two rows, so the flat list is
	// bronto·default, bronto·staging, debug, otlp — digits land on preset
	// rows exactly like on single-preset ones.
	before := []cfgstore.Info{info("debug", "default"), info("otlp", "default"), info("bronto", "staging", "default")}
	if got, want := digitTarget(before, 2), (presetTarget{config: "bronto", preset: "staging"}); got != want {
		t.Fatalf("digit 2 before resort = %+v, want %+v", got, want)
	}
	if got, want := digitTarget(before, 3), (presetTarget{config: "debug", preset: "default"}); got != want {
		t.Fatalf("digit 3 before resort = %+v, want %+v", got, want)
	}
	// A new config sorting first shifts every row down one: digit 2 must now
	// fire the new 2nd row, not follow bronto·staging to position 3.
	after := append(before, info("aaa-new", "default"))
	if got, want := digitTarget(after, 2), (presetTarget{config: "bronto", preset: "default"}); got != want {
		t.Errorf("digit 2 after resort = %+v, want %+v", got, want)
	}
	if got, want := digitTarget(after, 5), (presetTarget{config: "otlp", preset: "default"}); got != want {
		t.Errorf("digit 5 after resort = %+v, want %+v", got, want)
	}
}

// TestMenuIDField pins the seam keys_darwin.go leans on: systray.MenuItem's
// unexported uint32 id — the NSMenuItem tag on darwin. A systray upgrade
// that renames it fails here loudly instead of shortcuts vanishing quietly.
func TestMenuIDField(t *testing.T) {
	id, ok := menuID(&systray.MenuItem{})
	if !ok {
		t.Fatal("menuID: systray.MenuItem no longer has a uint32 id field — key equivalents are silently off; find the new seam")
	}
	if id != 0 {
		t.Errorf("zero-value item id = %d, want 0", id)
	}
}
