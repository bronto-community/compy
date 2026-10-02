package tray

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"image/png"
	"testing"
)

func TestIconFor(t *testing.T) {
	cases := []struct {
		running bool
		level   string
		colors  string
		want    iconState
	}{
		// A crash wins even while the process is down: it must not look
		// like a deliberate stop. Off keeps it a shape (attention).
		{false, "error", "warnings", iconError},
		{false, "error", "errors", iconError},
		{false, "error", "off", iconAttention},
		{true, "error", "warnings", iconError},
		{true, "error", "off", iconAttention},

		// Stopped (no crash) is stopped, whatever else.
		{false, "ok", "warnings", iconStopped},
		{false, "warning", "warnings", iconStopped},

		{true, "ok", "warnings", iconRunning},
		{true, "ok", "off", iconRunning},

		// Warnings: yellow only at "warnings"; no shape of their own.
		{true, "warning", "warnings", iconWarning},
		{true, "warning", "errors", iconRunning},
		{true, "warning", "off", iconRunning},
	}
	for _, c := range cases {
		if got := iconFor(c.running, c.level, c.colors); got != c.want {
			t.Errorf("iconFor(%v, %q, %q) = %v, want %v", c.running, c.level, c.colors, got, c.want)
		}
	}
}

// TestIconTemplate: only the coloured states drop the template flag — a
// template's colour is discarded by AppKit, and a non-template plain state
// would stop following the menu bar's light/dark appearance.
func TestIconTemplate(t *testing.T) {
	for s, want := range map[iconState]bool{
		iconStopped: true, iconRunning: true, iconAttention: true,
		iconError: false, iconWarning: false,
	} {
		if got := s.template(); got != want {
			t.Errorf("state %v template() = %v, want %v", s, got, want)
		}
	}
}

func TestIconDataEmbedded(t *testing.T) {
	icnsMagic := []byte("icns")
	seen := map[*byte]bool{}
	for _, s := range []iconState{iconStopped, iconRunning, iconAttention, iconError, iconWarning} {
		d := s.data()
		if len(d) == 0 || !bytes.HasPrefix(d, icnsMagic) {
			t.Errorf("state %v: not .icns data (len %d)", s, len(d))
			continue
		}
		if seen[&d[0]] {
			t.Errorf("state %v: shares icon bytes with another state", s)
		}
		seen[&d[0]] = true
	}
}

// TestTintedICNS unpacks a coloured icon: a well-formed .icns (declared
// length = actual) holding a 16 px icp4 and a 32 px ic11 PNG, every
// visible pixel exactly the tint, and the glyph's alpha untouched — the
// same coverage as the template raster it was tinted from.
func TestTintedICNS(t *testing.T) {
	d := iconError.data()
	if got := binary.BigEndian.Uint32(d[4:8]); int(got) != len(d) {
		t.Fatalf("icns length field = %d, data is %d bytes", got, len(d))
	}
	srcs := map[string][]byte{"icp4": attention16PNG, "ic11": attention32PNG}
	sizes := map[string]int{"icp4": 16, "ic11": 32}
	found := 0
	for off := 8; off < len(d); {
		typ := string(d[off : off+4])
		n := int(binary.BigEndian.Uint32(d[off+4 : off+8]))
		img, err := png.Decode(bytes.NewReader(d[off+8 : off+n]))
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		src, _ := png.Decode(bytes.NewReader(srcs[typ]))
		if w := img.Bounds().Dx(); w != sizes[typ] {
			t.Errorf("%s: %d px wide, want %d", typ, w, sizes[typ])
		}
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				got := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
				want := color.NRGBAModel.Convert(src.At(x, y)).(color.NRGBA)
				if got.A != want.A {
					t.Fatalf("%s (%d,%d): alpha %d, source has %d", typ, x, y, got.A, want.A)
				}
				if got.A > 0 && (got.R != errorColor.R || got.G != errorColor.G || got.B != errorColor.B) {
					t.Fatalf("%s (%d,%d): colour %v, want the tint %v", typ, x, y, got, errorColor)
				}
			}
		}
		found++
		off += n
	}
	if found != 2 {
		t.Errorf("found %d icon entries, want 2 (icp4 + ic11)", found)
	}
}

func TestItemIconDataEmbedded(t *testing.T) {
	icnsMagic := []byte("icns")
	seen := map[*byte]bool{}
	for _, s := range []itemState{itemNone, itemActive, itemDown, itemUp} {
		d := s.data()
		if len(d) == 0 || !bytes.HasPrefix(d, icnsMagic) {
			t.Errorf("item state %v: not .icns data (len %d)", s, len(d))
			continue
		}
		if seen[&d[0]] {
			t.Errorf("item state %v: shares icon bytes with another state", s)
		}
		seen[&d[0]] = true
	}
}
