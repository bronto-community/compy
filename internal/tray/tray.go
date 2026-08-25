//go:build darwin

// Package tray implements compy's macOS menu-bar item: the service status,
// a picker for the active configuration, an optional distro switcher, and
// rollback; "Open compy" spawns the standalone window for everything else.
// (The full menu-bar rework is P4.)
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
	a       *app.App
	mu      sync.Mutex // serializes apply-triggering actions
	status  *systray.MenuItem
	configs *systray.MenuItem
	distros *systray.MenuItem

	configItems map[string]*systray.MenuItem
	distroItems map[string]*systray.MenuItem
}

func onReady(a *app.App) {
	systray.SetTitle("compy")
	systray.SetTooltip("compy — local OpenTelemetry Collector manager")

	m := &menu{
		a:           a,
		configItems: map[string]*systray.MenuItem{},
		distroItems: map[string]*systray.MenuItem{},
	}

	m.status = systray.AddMenuItem("...", "service status")
	m.status.Disable()
	systray.AddSeparator()
	m.configs = systray.AddMenuItem("Configurations", "pick the active configuration")
	m.distros = systray.AddMenuItem("Distro", "switch the collector distribution")
	m.distros.Hide() // shown by sync() only when settings.MenuDistroSwap is on
	rollback := systray.AddMenuItem("Rollback", "restore the last known-good config")
	systray.AddSeparator()
	openApp := systray.AddMenuItem("Open compy", "open the compy window")
	quit := systray.AddMenuItem("Quit", "quit the compy menu bar item")

	m.sync()
	go func() {
		t := time.NewTicker(refreshInterval)
		defer t.Stop()
		for range t.C {
			// m.mu also guards the menu-item maps sync() mutates: act()
			// calls sync() under the same lock from click goroutines.
			m.mu.Lock()
			m.sync()
			m.mu.Unlock()
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

// sync reconciles the status line, the configuration picker, and the distro
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

	configs, err := m.a.Configs()
	if err == nil {
		seen := map[string]bool{}
		for _, c := range configs {
			seen[c.Name] = true
			item, ok := m.configItems[c.Name]
			if !ok {
				item = m.configs.AddSubMenuItemCheckbox(c.Name, "activate "+c.Name, false)
				m.configItems[c.Name] = item
				go m.handleConfigClicks(c.Name, item)
			}
			setChecked(item, c.Name == st.Config)
		}
		removeStale(m.configItems, seen)
	}

	s, err := state.LoadSettings()
	if err != nil || !s.MenuDistroSwap {
		m.distros.Hide()
		return
	}
	m.distros.Show()
	distros, err := m.a.Distros()
	if err == nil {
		seen := map[string]bool{}
		for _, d := range distros {
			name, _ := d["name"].(string)
			seen[name] = true
			item, ok := m.distroItems[name]
			if !ok {
				item = m.distros.AddSubMenuItemCheckbox(name, "switch to "+name, false)
				m.distroItems[name] = item
				go m.handleDistroClicks(name, item)
			}
			setChecked(item, name == st.Distro)
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

func (m *menu) handleConfigClicks(name string, item *systray.MenuItem) {
	for range item.ClickedCh {
		// Activate with the configuration's own active set ("" keeps it).
		m.act("activating "+name+"…", func() error { return m.a.Activate(name, "") })
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
