//go:build darwin

// Package tray implements compy's macOS menu-bar icon: a status line that
// refreshes periodically, "Open UI", and "Quit". No per-backend toggles in
// v1 — those live in the CLI and web UI.
package tray

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/webui"
)

// statusInterval is how often the status menu item is refreshed.
const statusInterval = 10 * time.Second

// Run starts the menu-bar tray and blocks until Quit is clicked.
// systray.Run must run on the main goroutine, so main() should call Run
// directly rather than in a goroutine.
func Run(a *app.App) error {
	systray.Run(func() { onReady(a) }, func() {})
	return nil
}

func onReady(a *app.App) {
	systray.SetTitle("compy")
	systray.SetTooltip("compy — local OpenTelemetry Collector manager")

	status := systray.AddMenuItem("...", "service status")
	status.Disable()
	systray.AddSeparator()
	openUI := systray.AddMenuItem("Open UI", "Open the web UI in your browser")
	// ponytail: backend toggles in tray when someone asks
	quit := systray.AddMenuItem("Quit", "Quit compy")

	go refreshStatus(a, status)
	go handleOpenUI(a, openUI)
	go func() {
		<-quit.ClickedCh
		systray.Quit()
	}()
}

// refreshStatus updates the disabled status line immediately, then every
// statusInterval.
func refreshStatus(a *app.App, item *systray.MenuItem) {
	set := func() {
		st, err := a.Status()
		if err != nil {
			item.SetTitle("status: " + err.Error())
			return
		}
		running := "stopped"
		if st.Running {
			running = "running"
		}
		item.SetTitle(fmt.Sprintf("%s — %s (grpc %d, http %d)", running, st.Distro, st.GRPCPort, st.HTTPPort))
	}
	set()
	for range time.Tick(statusInterval) {
		set()
	}
}

// handleOpenUI serves the web UI on an ephemeral port the first time "Open
// UI" is clicked, then reuses that server and just re-opens the URL on
// later clicks.
func handleOpenUI(a *app.App, item *systray.MenuItem) {
	var (
		once sync.Once
		url  string
	)
	for range item.ClickedCh {
		once.Do(func() {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				fmt.Fprintln(os.Stderr, "compy tray: open UI:", err)
				return
			}
			url = fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
			go http.Serve(ln, webui.Handler(a.WebUIAPI()))
		})
		if url == "" {
			continue // listener failed; nothing to open
		}
		if err := exec.Command("open", url).Run(); err != nil {
			fmt.Fprintln(os.Stderr, "compy tray: open UI:", err)
		}
	}
}
