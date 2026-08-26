package tray

import (
	"bytes"
	"testing"
)

func TestIconFor(t *testing.T) {
	cases := []struct {
		running bool
		errs    int
		want    iconState
	}{
		{false, 0, iconStopped},
		{false, 3, iconStopped}, // stale errors from a stopped collector: stopped wins
		{true, 0, iconRunning},
		{true, 1, iconAttention},
		{true, 42, iconAttention},
	}
	for _, c := range cases {
		if got := iconFor(c.running, c.errs); got != c.want {
			t.Errorf("iconFor(%v, %d) = %v, want %v", c.running, c.errs, got, c.want)
		}
	}
}

func TestIconDataEmbedded(t *testing.T) {
	icnsMagic := []byte("icns")
	seen := map[*byte]bool{}
	for _, s := range []iconState{iconStopped, iconRunning, iconAttention} {
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
