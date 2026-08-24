//go:build darwin

// Package tray implements compy's macOS menu-bar item. The menu is the
// switchboard's front line: per-backend enable/disable toggles, distro
// switching, and rollback live directly in it; "Open compy" spawns the
// standalone window for everything else.
package tray

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/state"
)

// refreshInterval is how often the status line and menu check-states are
// re-synced with the state directory (which the CLI or window may have
// changed behind our back).
const refreshInterval = 5 * time.Second

// Run starts the menu-bar tray and blocks until Quit is clicked.
// systray.Run must run on the main goroutine, so main() should call Run
// directly rather than in a goroutine.
func Run(a *app.App) error {
	systray.Run(func() { onReady(a) }, func() {})
	return nil
}

type menu struct {
	a        *app.App
	mu       sync.Mutex // serializes apply-triggering actions
	status   *systray.MenuItem
	backends *systray.MenuItem
	distros  *systray.MenuItem

	backendItems map[string]*systray.MenuItem
	distroItems  map[string]*systray.MenuItem
}

func onReady(a *app.App) {
	systray.SetTitle("compy")
	systray.SetTooltip("compy — local OpenTelemetry Collector manager")

	m := &menu{
		a:            a,
		backendItems: map[string]*systray.MenuItem{},
		distroItems:  map[string]*systray.MenuItem{},
	}

	m.status = systray.AddMenuItem("...", "service status")
	m.status.Disable()
	systray.AddSeparator()
	m.backends = systray.AddMenuItem("Backends", "enable or disable telemetry backends")
	m.distros = systray.AddMenuItem("Distro", "switch the collector distribution")
	rollback := systray.AddMenuItem("Rollback", "restore the last known-good config")
	systray.AddSeparator()
	openApp := systray.AddMenuItem("Open compy", "open the compy window")
	quit := systray.AddMenuItem("Quit", "quit the compy menu bar item")

	m.sync()
	go func() {
		t := time.NewTicker(refreshInterval)
		defer t.Stop()
		for range t.C {
			m.sync()
		}
	}()
	go func() {
		for range rollback.ClickedCh {
			m.act("rolling back…", m.a.Rollback)
		}
	}()
	go handleOpenApp(openApp)
	go func() {
		<-quit.ClickedCh
		systray.Quit()
	}()
}

// sync reconciles the status line, the backend toggles, and the distro
// radio items with what is on disk right now.
func (m *menu) sync() {
	st, err := m.a.Status()
	if err != nil {
		m.status.SetTitle("status: " + err.Error())
		return
	}
	running := "stopped"
	if st.Running {
		running = "running"
	}
	distro := st.Distro
	if distro == "" {
		distro = "no distro"
	}
	m.status.SetTitle(fmt.Sprintf("%s — %s (grpc %d, http %d)", running, distro, st.GRPCPort, st.HTTPPort))

	backends, err := m.a.Backends()
	if err == nil {
		seen := map[string]bool{}
		for _, b := range backends {
			name, _ := b["name"].(string)
			enabled, _ := b["enabled"].(bool)
			seen[name] = true
			item, ok := m.backendItems[name]
			if !ok {
				item = m.backends.AddSubMenuItemCheckbox(name, "toggle "+name, enabled)
				m.backendItems[name] = item
				go m.handleBackendClicks(name, item)
			}
			setChecked(item, enabled)
		}
		removeStale(m.backendItems, seen)
	}

	distros, err := state.LoadDistros()
	if err == nil {
		seen := map[string]bool{}
		for _, d := range distros {
			seen[d.Name] = true
			item, ok := m.distroItems[d.Name]
			if !ok {
				item = m.distros.AddSubMenuItemCheckbox(d.Name, "switch to "+d.Name, false)
				m.distroItems[d.Name] = item
				go m.handleDistroClicks(d.Name, item)
			}
			setChecked(item, d.Name == st.Distro)
		}
		removeStale(m.distroItems, seen)
	}
}

func setChecked(item *systray.MenuItem, want bool) {
	if want && !item.Checked() {
		item.Check()
	} else if !want && item.Checked() {
		item.Uncheck()
	}
}

func removeStale(items map[string]*systray.MenuItem, seen map[string]bool) {
	for name, item := range items {
		if !seen[name] {
			item.Remove()
			delete(items, name)
		}
	}
}

func (m *menu) handleBackendClicks(name string, item *systray.MenuItem) {
	for range item.ClickedCh {
		m.act("applying…", func() error {
			// Toggle from current on-disk state, not the checkbox: the CLI
			// or window may have flipped it since the menu was drawn.
			s, err := state.LoadSettings()
			if err != nil {
				return err
			}
			enabled := false
			for _, n := range s.Enabled {
				if n == name {
					enabled = true
				}
			}
			return m.a.SetEnabled(name, !enabled)
		})
	}
}

func (m *menu) handleDistroClicks(name string, item *systray.MenuItem) {
	for range item.ClickedCh {
		m.act("switching distro…", func() error { return m.a.UseDistro(name) })
	}
}

// act runs an apply-triggering action with a progress note in the status
// line; errors land there too (truncated) and in the tray log.
func (m *menu) act(note string, fn func() error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.SetTitle(note)
	if err := fn(); err != nil {
		fmt.Fprintln(os.Stderr, "compy tray:", err)
		msg := err.Error()
		if len(msg) > 80 {
			msg = msg[:80] + "…"
		}
		m.status.SetTitle("error: " + msg)
		time.Sleep(3 * time.Second) // let the error be seen before resync
	}
	m.sync()
}

// handleOpenApp spawns the standalone window as its own process — systray
// owns this process's main thread, and the webview needs one of its own.
func handleOpenApp(item *systray.MenuItem) {
	for range item.ClickedCh {
		exe, err := os.Executable()
		if err == nil {
			err = exec.Command(exe, "window").Start()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "compy tray: open window:", err)
		}
	}
}
