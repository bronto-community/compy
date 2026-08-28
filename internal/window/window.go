//go:build darwin

// Package window opens compy's UI in a standalone native window (WKWebView
// via webview_go) instead of a browser tab. It runs as its own process: the
// tray owns its process's main thread for systray, and the webview needs a
// main thread of its own. State is file-based, so this process serves the
// same UI over its own ephemeral localhost listener. The window opens at
// the design size (1240×838, docs/design/handoff/README.md) and stays
// resizable (HintNone); the layout is expected to hold down to ~900px wide.
package window

import (
	"fmt"
	"net"

	webview "github.com/webview/webview_go"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/webui"
)

// Run serves the web UI on an ephemeral localhost port and blocks in a
// native window showing it. Returning (window closed) ends the process,
// which also tears the listener down.
func Run(a *app.App) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	go func() { _ = webui.ServeListener(ln, a.WebUIAPI()) }()

	w := webview.New(false)
	defer w.Destroy()
	installMainMenu() // after New (app is launched), before Run; enables Cmd+C/V/X/A/Z
	w.SetTitle("compy")
	w.SetSize(1240, 838, webview.HintNone)
	w.Navigate(fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port))
	w.Run()
	return nil
}
