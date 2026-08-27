//go:build !darwin

package tray

import (
	"errors"

	"github.com/bronto-community/compy/internal/app"
)

// Run is a stub on non-macOS platforms: compy's tray is macOS-only.
func Run(a *app.App) error {
	return errors.New("tray not supported on this platform")
}
