//go:build darwin

// Package window opens compy's UI in a standalone native window (Wails v2
// as a library — no wails CLI, no frontend toolchain). It runs as its own
// process: the tray owns its process's main thread for systray, and the
// window needs a main thread of its own.
//
// There is no localhost listener at all: Wails on darwin serves the UI
// in-process via a WKURLSchemeHandler on wails://wails/, and every request
// (static assets AND /api/*) is routed through the same webui.Handler the
// browser UI uses.
//
// Build note: Wails requires its own `desktop,production` build tags for
// wails.Run to work (untagged builds compile but error at runtime by
// design), plus CGO_LDFLAGS="-framework UniformTypeIdentifiers" on darwin.
// See CLAUDE.md's build section.
package window

import (
	"context"
	"net/http"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	rt "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/webui"
)

// Run opens compy's UI in a native window at the design size (1240×838,
// docs/design/handoff/README.md; layout holds down to ~900px wide) and
// blocks until the window closes.
func Run(a *app.App) error {
	var ctx context.Context

	appMenu := menu.NewMenu()
	appMenu.Append(menu.AppMenu()) // About/Hide/Quit (Cmd+Q)
	// Cmd+W: single-window app, so Close == Quit (same as the old window,
	// where closing the webview ended the process). Not automatic in Wails
	// v2 (no File/Window menu role wired on darwin).
	fileMenu := appMenu.AddSubmenu("File")
	fileMenu.AddText("Close Window", keys.CmdOrCtrl("w"), func(*menu.CallbackData) { rt.Quit(ctx) })
	appMenu.Append(menu.EditMenu()) // Undo/Redo/Cut/Copy/Paste/Select All

	return wails.Run(&options.App{
		Title:     "compy",
		Width:     1240,
		Height:    838,
		Menu:      appMenu,
		OnStartup: func(c context.Context) { ctx = c },
		AssetServer: &assetserver.Options{
			// Assets nil: everything goes to Handler — the exact same
			// hostCheck-wrapped mux the browser UI serves.
			Handler: inProcess(webui.Handler(a.WebUIAPI())),
		},
	})
}

// inProcess adapts requests from Wails' in-process asset server so webui's
// hostCheck (a network-boundary guard: DNS rebinding + cross-site) passes.
// There is no network boundary here: the wails:// scheme handler is
// registered on this window's WKWebView configuration only — no TCP
// listener exists, and no other process or page can address it. Rewriting
// Host therefore removes no protection.
func inProcess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "localhost"
		if o := r.Header.Get("Origin"); o != "" {
			r.Header.Set("Origin", "http://localhost")
		}
		if s := r.Header.Get("Sec-Fetch-Site"); s != "" {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		next.ServeHTTP(w, r)
	})
}
