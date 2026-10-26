package tray

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"
)

// The menu-bar icon: the designed "track + signals" glyph in one of three
// shape-only states (icons/README.md — macOS strips colour from template
// images, so state must be shape). Each .icns packs a 16×16 (1×) and a
// 32×32 (2×) black-on-transparent PNG; systray hands the bytes to
// NSImage, which picks the rep for the display.
//
// On top of those, the opt-in coloured states (settings' tray_colors): the
// same glyphs tinted red or yellow and shipped as NON-template images,
// since a template's colour is discarded. They are tinted here at runtime
// from the committed rasters rather than committed as rasters of their
// own, so the geometry can never drift from the designed glyph.

//go:embed icons/running.icns
var runningICNS []byte

//go:embed icons/stopped.icns
var stoppedICNS []byte

//go:embed icons/attention.icns
var attentionICNS []byte

//go:embed icons/running-16.png
var running16PNG []byte

//go:embed icons/running-32.png
var running32PNG []byte

//go:embed icons/attention-16.png
var attention16PNG []byte

//go:embed icons/attention-32.png
var attention32PNG []byte

// The tints. Red is macOS's systemRed; yellow is deeper than systemYellow
// (#FFCC00), which all but vanishes on a light menu bar — a non-template
// image gets no help from AppKit there.
var (
	errorColor   = color.NRGBA{0xFF, 0x3B, 0x30, 0xFF}
	warningColor = color.NRGBA{0xF0, 0xB0, 0x00, 0xFF}
)

// iconState is which menu-bar icon is showing.
type iconState int

const (
	iconStopped iconState = iota
	iconRunning
	iconAttention
	iconError   // the attention glyph, red
	iconWarning // the running glyph, yellow
)

// iconFor maps collector status to the icon state. level is app.Trouble's
// ("ok", "warning", "error"); colors is settings' tray_colors.
//
// An error — crashed, crash-looping, cannot start — shows the attention
// glyph, red unless colours are off, and it wins even while the process is
// down: a crash must not look like a deliberate stop. Otherwise a stopped
// collector shows stopped. A warning shows yellow only when colours reach
// that far ("warnings"), and the plain running glyph otherwise: warnings
// never had a shape of their own.
func iconFor(running bool, level, colors string) iconState {
	switch {
	case level == "error" && colors != "off":
		return iconError
	case level == "error":
		return iconAttention
	case !running:
		return iconStopped
	case level == "warning" && colors == "warnings":
		return iconWarning
	default:
		return iconRunning
	}
}

// template reports whether the state ships as a template image (AppKit
// tints it for the menu bar) — every state but the coloured ones.
func (s iconState) template() bool {
	return s != iconError && s != iconWarning
}

// data is the state's .icns bytes: embedded, or tinted on first use.
func (s iconState) data() []byte {
	switch s {
	case iconRunning:
		return runningICNS
	case iconAttention:
		return attentionICNS
	case iconError:
		return errorICNS()
	case iconWarning:
		return warningICNS()
	default:
		return stoppedICNS
	}
}

var (
	errorICNS   = sync.OnceValue(func() []byte { return tintedICNS(attention16PNG, attention32PNG, errorColor) })
	warningICNS = sync.OnceValue(func() []byte { return tintedICNS(running16PNG, running32PNG, warningColor) })
)

// tintedICNS recolours a black-on-transparent 1×/2× pair to c, keeping
// each pixel's alpha (the glyph's anti-aliasing), and packs the pair as an
// .icns the way iconutil does for an icon_16x16 iconset: icp4 (16 px) and
// ic11 (16@2x), both PNG payloads. The inputs are compiled in, so a decode
// failure is a broken build, not a runtime condition.
func tintedICNS(png16, png32 []byte, c color.NRGBA) []byte {
	var out bytes.Buffer
	out.WriteString("icns")
	_ = binary.Write(&out, binary.BigEndian, uint32(0)) // total length, patched below
	for _, e := range []struct {
		typ string
		src []byte
	}{{"icp4", png16}, {"ic11", png32}} {
		p := tintPNG(e.src, c)
		out.WriteString(e.typ)
		_ = binary.Write(&out, binary.BigEndian, uint32(8+len(p)))
		out.Write(p)
	}
	b := out.Bytes()
	binary.BigEndian.PutUint32(b[4:8], uint32(len(b)))
	return b
}

// tintPNG is tintedICNS's per-raster half.
func tintPNG(src []byte, c color.NRGBA) []byte {
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		panic("tray: embedded icon raster: " + err.Error())
	}
	n := image.NewNRGBA(img.Bounds())
	draw.Draw(n, n.Bounds(), img, img.Bounds().Min, draw.Src)
	for i := 0; i < len(n.Pix); i += 4 {
		a := n.Pix[i+3]
		n.Pix[i], n.Pix[i+1], n.Pix[i+2] = c.R, c.G, c.B
		n.Pix[i+3] = uint8(uint16(a) * uint16(c.A) / 255)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, n); err != nil {
		panic("tray: encode tinted icon: " + err.Error())
	}
	return out.Bytes()
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
