package app_test

import (
	"testing"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/state"
)

// TestPutSettingsTrayColors is the settings contract for the menu-bar icon
// colouring: unset resolves to "warnings", the three known values round-trip,
// and anything else is the caller's mistake that saves nothing.
func TestPutSettingsTrayColors(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveTrayColors(); got != "warnings" {
		t.Errorf("default EffectiveTrayColors() = %q, want warnings", got)
	}
	for _, c := range []string{"off", "warnings", "errors"} {
		c := c
		if err := a.PutSettings(nil, nil, nil, nil, nil, &c); err != nil {
			t.Fatalf("PutSettings(tray_colors=%q): %v", c, err)
		}
		if s, _ = a.GetSettings(); s.TrayColors != c {
			t.Errorf("TrayColors after set = %q, want %q", s.TrayColors, c)
		}
	}
	for _, c := range []string{"", "red", "Errors", "all"} {
		c := c
		if err := a.PutSettings(nil, nil, nil, nil, nil, &c); err == nil || !state.IsBadRequest(err) {
			t.Errorf("PutSettings(tray_colors=%q) = %v, want a BadRequest", c, err)
		}
	}
	if s, _ = a.GetSettings(); s.TrayColors != "errors" {
		t.Errorf("TrayColors after rejected updates = %q, want unchanged errors", s.TrayColors)
	}
}
