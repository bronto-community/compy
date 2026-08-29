//go:build darwin

package tray

import (
	"strconv"
	"testing"

	"fyne.io/systray"
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

// TestDigitEquivalents pins the submenu rule (2026-08-29 owner report: the
// digit overlapped the submenu chevron — and a submenu parent takes no
// click anyway): a slot with a preset submenu gets key "" (clearing any
// previous digit), positional numbering is kept for the rest, and only
// slots 1–9 ever carry digits.
func TestDigitEquivalents(t *testing.T) {
	slots := make([]*systray.MenuItem, maxInline)
	for i := range slots {
		slots[i] = &systray.MenuItem{}
	}
	hasSub := make([]bool, maxInline)
	hasSub[1] = true // 2nd row has a preset submenu
	eqs := digitEquivalents(slots, hasSub)
	if len(eqs) != 9 {
		t.Fatalf("got %d digit entries, want 9 (the 10th slot carries none)", len(eqs))
	}
	for i, e := range eqs {
		wantKey := strconv.Itoa(i + 1)
		if i == 1 {
			wantKey = "" // submenu row: digit cleared, numbering stays positional
		}
		if e.item != slots[i] || e.key != wantKey || e.cmd {
			t.Errorf("eqs[%d] = {%q cmd=%v}, want key %q on slot %d", i, e.key, e.cmd, wantKey, i)
		}
	}
}

// TestDigitsRetargetOnResort documents why the digits need no re-binding
// when the config list reorders: digit d is bound to slot d-1, a FIXED menu
// position, and sync() assigns the d-th alphabetical config to exactly that
// slot — so after any create/delete/rename, digit d fires whatever config
// is the d-th visible row now (the click handler resolves slotNames at
// click time, same as a mouse click on the row).
func TestDigitsRetargetOnResort(t *testing.T) {
	slots := make([]*systray.MenuItem, maxInline)
	for i := range slots {
		slots[i] = &systray.MenuItem{}
	}
	eqs := digitEquivalents(slots, make([]bool, maxInline))

	// digitTarget mirrors the sync()+click path: what config does digit d
	// activate for this set of names?
	digitTarget := func(names []string, d int) string {
		inline, _ := splitInline(alphabetical(names), maxInline)
		for _, e := range eqs {
			if e.key != strconv.Itoa(d) {
				continue
			}
			for i, slot := range slots {
				if slot == e.item && i < len(inline) {
					return inline[i] // slotNames[i], resolved at click time
				}
			}
		}
		return ""
	}

	before := []string{"debug", "otlp", "bronto"}
	if got := digitTarget(before, 2); got != "debug" {
		t.Fatalf("digit 2 before resort = %q, want debug (bronto, debug, otlp)", got)
	}
	// A new config sorting first shifts every row down one: digit 2 must now
	// fire the new 2nd row, not follow debug to position 3.
	after := append(before, "aaa-new")
	if got := digitTarget(after, 2); got != "bronto" {
		t.Errorf("digit 2 after resort = %q, want bronto (aaa-new, bronto, debug, otlp)", got)
	}
	if got := digitTarget(after, 4); got != "otlp" {
		t.Errorf("digit 4 after resort = %q, want otlp", got)
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
