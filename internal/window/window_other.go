//go:build !darwin

package window

import (
	"errors"

	"github.com/bronto-community/compy/internal/app"
)

// Run is unavailable off darwin for now; use `compy ui` (browser) instead.
func Run(a *app.App) error {
	return errors.New("standalone window not supported on this platform; use `compy ui`")
}
