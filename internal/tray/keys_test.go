//go:build darwin

package tray

import (
	"strconv"
	"testing"

	"fyne.io/systray"
)

// TestKeyEquivalents pins the shortcut map the open menu carries: digits
// 1–9 on the first nine slots only, plain s/r/o on toggle/restart/open, and
// ⌘Q (cmd set) on Quit — nothing else.
func TestKeyEquivalents(t *testing.T) {
	slots := make([]*systray.MenuItem, maxInline)
	for i := range slots {
		slots[i] = &systray.MenuItem{}
	}
	toggle, restart, open, quit := &systray.MenuItem{}, &systray.MenuItem{}, &systray.MenuItem{}, &systray.MenuItem{}

	eqs := keyEquivalents(slots, toggle, restart, open, quit)
	if len(eqs) != 9+4 {
		t.Fatalf("got %d equivalents, want 13 (nine digits + s/r/o/⌘Q)", len(eqs))
	}
	for i := 0; i < 9; i++ {
		if eqs[i].item != slots[i] || eqs[i].key != strconv.Itoa(i+1) || eqs[i].cmd {
			t.Errorf("eqs[%d] = {%p %q cmd=%v}, want slot %d with plain key %q", i, eqs[i].item, eqs[i].key, eqs[i].cmd, i, strconv.Itoa(i+1))
		}
	}
	for _, e := range eqs {
		if e.item == slots[9] {
			t.Error("the 10th slot must carry no digit — only 1–9 exist")
		}
	}
	want := []keyEquiv{{toggle, "s", false}, {restart, "r", false}, {open, "o", false}, {quit, "q", true}}
	for i, w := range want {
		if got := eqs[9+i]; got != w {
			t.Errorf("eqs[%d] = {%q cmd=%v} on wrong item, want %q cmd=%v", 9+i, got.key, got.cmd, w.key, w.cmd)
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
	eqs := keyEquivalents(slots, &systray.MenuItem{}, &systray.MenuItem{}, &systray.MenuItem{}, &systray.MenuItem{})

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
