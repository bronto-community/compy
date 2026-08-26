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
