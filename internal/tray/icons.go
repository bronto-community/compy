package tray

import _ "embed"

// The menu-bar icon: the designed "track + signals" glyph in one of three
// shape-only states (icons/README.md — macOS strips colour from template
// images, so state must be shape). Each .icns packs a 16×16 (1×) and a
// 32×32 (2×) black-on-transparent PNG; systray hands the bytes to
// NSImage, which picks the rep for the display.

//go:embed icons/running.icns
var runningICNS []byte

//go:embed icons/stopped.icns
var stoppedICNS []byte

//go:embed icons/attention.icns
var attentionICNS []byte

// iconState is which of the three designed menu-bar icons is showing.
type iconState int

const (
	iconStopped iconState = iota
	iconRunning
	iconAttention
)

// iconFor maps collector status to the icon state: stopped when the
// collector isn't running, attention when it is running but the log tail
// counts errors (the same errs LogStats already reports for the status
// line), running otherwise.
func iconFor(running bool, errs int) iconState {
	switch {
	case !running:
		return iconStopped
	case errs > 0:
		return iconAttention
	default:
		return iconRunning
	}
}

// data is the state's embedded .icns bytes.
func (s iconState) data() []byte {
	switch s {
	case iconRunning:
		return runningICNS
	case iconAttention:
		return attentionICNS
	default:
		return stoppedICNS
	}
}

// Per-menu-item indicator icons (icons/item-*.svg, same family as the
// menu-bar glyph: black-on-transparent template images, 16 + 16@2x). These
// replaced the native checkmark as the state carrier on the (config,
// preset) rows: three states — active (filled dot), going down (down
// chevron), going up (up chevron) — plus a fully transparent blank, because
// systray has no way to clear an item's icon once set (and a uniform blank
// keeps every row's title aligned with the iconed ones).

//go:embed icons/item-active.icns
var itemActiveICNS []byte

//go:embed icons/item-down.icns
var itemDownICNS []byte

//go:embed icons/item-up.icns
var itemUpICNS []byte

//go:embed icons/item-blank.icns
var itemBlankICNS []byte

// itemState is one row's indicator: what is running
// (itemActive), what a click is taking down or bringing up while the apply
// is in flight (itemDown/itemUp), or nothing (itemNone — the blank icon).
type itemState int

const (
	itemNone itemState = iota
	itemActive
	itemDown
	itemUp
)

// data is the state's embedded .icns bytes.
func (s itemState) data() []byte {
	switch s {
	case itemActive:
		return itemActiveICNS
	case itemDown:
		return itemDownICNS
	case itemUp:
		return itemUpICNS
	default:
		return itemBlankICNS
	}
}
