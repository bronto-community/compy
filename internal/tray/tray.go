//go:build darwin

// Package tray implements compy's macOS menu-bar item: the service status,
// the configurations inline as checkable items (overflowing into a "More
// configurations" submenu only when there are many), an optional distro
// switcher, and rollback; "Open compy" spawns the standalone window for
// everything else. (The full menu-bar rework is P4.)
package tray

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// maxInline is how many configurations appear directly in the menu before
// the rest overflow into the "More configurations" submenu.
const maxInline = 10

// Run starts the menu-bar tray and blocks until Quit is clicked.
// systray.Run must run on the main goroutine, so main() should call Run
// directly rather than in a goroutine.
func Run(a *app.App) error {
	systray.Run(func() { onReady(a) }, func() {})
	return nil
}

type menu struct {
	a           *app.App
	mu          sync.Mutex // serializes apply-triggering actions and guards the fields below
	status      *systray.MenuItem
	statusLine2 *systray.MenuItem
	distros     *systray.MenuItem

	slots            []*systray.MenuItem // pre-created inline config items (fixed menu position)
	slotNames        []string            // slotNames[i] = config shown in slots[i], "" = hidden
	more             *systray.MenuItem   // overflow submenu parent
	moreItems        map[string]*systray.MenuItem
	variableSet      *systray.MenuItem // "Variable set" submenu parent, shown only for a multi-set active config
	variableSetItems map[string]*systray.MenuItem
	distroItems      map[string]*systray.MenuItem
}

func onReady(a *app.App) {
	systray.SetTitle("compy")
	systray.SetTooltip("compy — local OpenTelemetry Collector manager")

	m := &menu{
		a:                a,
		moreItems:        map[string]*systray.MenuItem{},
		variableSetItems: map[string]*systray.MenuItem{},
		distroItems:      map[string]*systray.MenuItem{},
	}

	m.status = systray.AddMenuItem("...", "service status")
	m.status.Disable()
	m.statusLine2 = systray.AddMenuItem("...", "service status")
	m.statusLine2.Disable()
	systray.AddSeparator()
	// Fixed slots keep configurations at this menu position even for configs
	// that appear while the tray runs (systray can only append new items).
	m.slotNames = make([]string, maxInline)
	for i := 0; i < maxInline; i++ {
		slot := systray.AddMenuItemCheckbox("", "activate this configuration", false)
		slot.Hide()
		m.slots = append(m.slots, slot)
		go m.handleSlotClicks(i, slot)
	}
	m.more = systray.AddMenuItem("More configurations", "the rest of your configurations")
	m.more.Hide()
	m.variableSet = systray.AddMenuItem("Variable set", "switch the active configuration's variable set")
	m.variableSet.Hide()
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
	errs, warns, _ := m.a.LogStats(500) // best-effort: a log-read error just omits the tail
	line1, line2 := statusLines(st, errs, warns)
	m.status.SetTitle(line1)
	m.statusLine2.SetTitle(line2)

	configs, err := m.a.Configs()
	if err == nil {
		names := make([]string, 0, len(configs))
		for _, c := range configs {
			names = append(names, c.Name)
		}
		inline, overflow := assignSlots(names, st.Config, len(m.slots))
		for i, slot := range m.slots {
			if i < len(inline) {
				m.slotNames[i] = inline[i]
				slot.SetTitle(inline[i])
				slot.Show()
				setChecked(slot, inline[i] == st.Config)
			} else {
				m.slotNames[i] = ""
				slot.Hide()
			}
		}
		seen := map[string]bool{}
		for _, name := range overflow {
			seen[name] = true
			item, ok := m.moreItems[name]
			if !ok {
				item = m.more.AddSubMenuItemCheckbox(name, "activate "+name, false)
				m.moreItems[name] = item
				go m.handleConfigClicks(name, item)
			}
			setChecked(item, name == st.Config)
		}
		removeStale(m.moreItems, seen)
		if len(overflow) > 0 {
			m.more.Show()
		} else {
			m.more.Hide()
		}

		setNames, activeSet, show := activeVariableSets(configs, st.Config)
		if show {
			seen := map[string]bool{}
			for _, name := range setNames {
				seen[name] = true
				item, ok := m.variableSetItems[name]
				if !ok {
					item = m.variableSet.AddSubMenuItemCheckbox(name, "switch to variable set "+name, false)
					m.variableSetItems[name] = item
					go m.handleVariableSetClicks(name, item)
				}
				setChecked(item, name == activeSet)
			}
			removeStale(m.variableSetItems, seen)
			m.variableSet.Show()
		} else {
			removeStale(m.variableSetItems, map[string]bool{})
			m.variableSet.Hide()
		}
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

// handleSlotClicks resolves the slot's current config at click time — the
// assignment may have changed since the menu was drawn.
func (m *menu) handleSlotClicks(i int, slot *systray.MenuItem) {
	for range slot.ClickedCh {
		m.mu.Lock()
		name := m.slotNames[i]
		if name == "" {
			m.mu.Unlock()
			continue
		}
		m.doAct("activating "+name+"…", func() error { return m.a.Activate(name, "") })
		m.mu.Unlock()
	}
}

func (m *menu) handleDistroClicks(name string, item *systray.MenuItem) {
	for range item.ClickedCh {
		m.act("switching distro…", func() error { return m.a.UseDistro(name) })
	}
}

// handleVariableSetClicks resolves the active configuration at click time —
// the submenu is only shown for it, but that may have changed since the
// menu was drawn.
func (m *menu) handleVariableSetClicks(set string, item *systray.MenuItem) {
	for range item.ClickedCh {
		m.act("switching set…", func() error {
			st, err := m.a.Status()
			if err != nil {
				return err
			}
			return m.a.Activate(st.Config, set)
		})
	}
}

// act runs an apply-triggering action with a progress note in the status
// line; errors land there too (truncated) and in the tray log.
func (m *menu) act(note string, fn func() error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.doAct(note, fn)
}

// doAct is act's body; callers hold m.mu.
func (m *menu) doAct(note string, fn func() error) {
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

// handleOpenApp opens the standalone window: spawned as its own process —
// systray owns this process's main thread, and the webview needs one of its
// own — but at most one at a time. Clicking again raises the window that is
// already open (see openWindow).
func handleOpenApp(item *systray.MenuItem) {
	var cur *windowProc
	for range item.ClickedCh {
		next, err := openWindow(cur, spawnWindow, raiseWindow)
		if err != nil {
			fmt.Fprintln(os.Stderr, "compy tray: open window:", err)
			continue // keep cur: a failed raise must not stack a second window
		}
		cur = next
	}
}

// spawnWindow starts `compy window` and reaps it in the background, so
// windowProc.alive() flips as soon as the user closes it.
func spawnWindow() (*windowProc, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "window")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w := &windowProc{pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(w.done)
	}()
	return w, nil
}

// raiseWindow brings another process's windows to the front. compy has no
// app bundle of its own to activate, so System Events by unix id is the way
// in; it needs Accessibility permission, and says so when it doesn't have it.
func raiseWindow(pid int) error {
	script := fmt.Sprintf("tell application \"System Events\" to set frontmost of every process whose unix id is %d to true", pid)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("raise the open window (pid %d): %w: %s", pid, err, strings.TrimSpace(string(out)))
	}
	return nil
}
