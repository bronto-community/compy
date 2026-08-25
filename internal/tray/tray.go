//go:build darwin

// Package tray implements compy's macOS menu-bar item: status line, the
// CONFIGURATION list (recency-first, "More…" overflow alphabetical, a
// preset submenu where picking a preset is the activation), "Restart
// collector", and "Open compy" for everything else. Menu bar v4 —
// docs/design/handoff/README.md § "5. Menu bar", ACCEPTANCE.md C5.
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
	"github.com/bronto-io/compy/internal/cfgstore"
)

// refreshInterval is how often the status line and menu check-states are
// re-synced with the state directory (which the CLI or window may have
// changed behind our back).
const refreshInterval = 5 * time.Second

// maxInline is how many configurations appear directly in the menu before
// the rest overflow into the "More…" submenu (ACCEPTANCE C5.3).
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

	slots       []*systray.MenuItem            // pre-created inline config rows (fixed menu position)
	slotNames   []string                       // slotNames[i] = config shown in slots[i], "" = hidden
	slotPresets []map[string]*systray.MenuItem // slotPresets[i]: that row's own preset submenu items

	more        *systray.MenuItem // "More…" overflow submenu parent
	moreItems   map[string]*systray.MenuItem
	morePresets map[string]map[string]*systray.MenuItem // per overflow config: its preset submenu items

	restart *systray.MenuItem
}

func onReady(a *app.App) {
	systray.SetTitle("compy")
	systray.SetTooltip("compy — local OpenTelemetry Collector manager")

	m := &menu{
		a:           a,
		moreItems:   map[string]*systray.MenuItem{},
		morePresets: map[string]map[string]*systray.MenuItem{},
	}

	m.status = systray.AddMenuItem("...", "service status")
	m.status.Disable()
	m.statusLine2 = systray.AddMenuItem("...", "service status")
	m.statusLine2.Disable()
	systray.AddSeparator()
	header := systray.AddMenuItem("CONFIGURATION", "your configurations")
	header.Disable()
	// Fixed slots keep configurations at this menu position even for configs
	// that appear while the tray runs (systray can only append new items).
	m.slotNames = make([]string, maxInline)
	m.slotPresets = make([]map[string]*systray.MenuItem, maxInline)
	for i := 0; i < maxInline; i++ {
		slot := systray.AddMenuItemCheckbox("", "activate this configuration", false)
		slot.Hide()
		m.slots = append(m.slots, slot)
		m.slotPresets[i] = map[string]*systray.MenuItem{}
		go m.handleSlotClicks(i, slot)
	}
	m.more = systray.AddMenuItem("More…", "the rest of your configurations")
	m.more.Hide()
	systray.AddSeparator()
	m.restart = systray.AddMenuItem("Restart collector", "restart the collector")
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
	go m.handleRestart()
	go handleOpenApp(openApp)
	go func() {
		<-quit.ClickedCh
		systray.Quit()
	}()
}

// sync reconciles the status line and the configuration/preset rows with
// what is on disk right now.
func (m *menu) sync() {
	st, err := m.a.Status()
	if err != nil {
		m.status.SetTitle("status: " + err.Error())
		return
	}
	// Menu bar counts warn-level lines only (controller ruling D2); the
	// error count that used to sit alongside it is dropped from this line.
	_, warns, _ := m.a.LogStats(500) // best-effort: a log-read error just omits the tail
	line1, line2 := statusLines(st, warns)
	m.status.SetTitle(line1)
	m.statusLine2.SetTitle(line2)

	configs, err := m.a.Configs()
	if err != nil {
		return
	}
	byName := make(map[string]cfgstore.Info, len(configs))
	names := make([]string, 0, len(configs))
	for _, c := range configs {
		byName[c.Name] = c
		names = append(names, c.Name)
	}
	inline, overflow := splitInline(recencyOrder(names, st.Recent), len(m.slots))

	for i, slot := range m.slots {
		if i >= len(inline) {
			m.slotNames[i] = ""
			slot.Hide()
			removeStale(m.slotPresets[i], map[string]bool{})
			continue
		}
		name := inline[i]
		m.slotNames[i] = name
		slot.SetTitle(name)
		slot.Show()
		m.syncRow(slot, m.slotPresets[i], name, byName[name], st)
	}

	seen := map[string]bool{}
	for _, name := range overflow {
		seen[name] = true
		item, ok := m.moreItems[name]
		if !ok {
			item = m.more.AddSubMenuItemCheckbox(name, "activate "+name, false)
			m.moreItems[name] = item
			m.morePresets[name] = map[string]*systray.MenuItem{}
			go m.handleConfigClicks(name, item)
		}
		m.syncRow(item, m.morePresets[name], name, byName[name], st)
	}
	for name, item := range m.moreItems {
		if !seen[name] {
			item.Remove()
			delete(m.moreItems, name)
			delete(m.morePresets, name)
		}
	}
	if len(overflow) > 0 {
		m.more.Show()
	} else {
		m.more.Hide()
	}
}

// syncRow reconciles one configuration's menu row against current status:
// the row's own checkmark, and — only for a 2+-preset configuration
// (ACCEPTANCE C5.4) — its preset submenu, lazily created in presetItems
// (that row's own cache, since systray can only append items). Clicking a
// preset row is itself the activation; the running preset is checked.
func (m *menu) syncRow(item *systray.MenuItem, presetItems map[string]*systray.MenuItem, name string, info cfgstore.Info, st app.Status) {
	active := name == st.Config
	setChecked(item, active)
	presets, multi := presetChoices(info)
	if !multi {
		removeStale(presetItems, map[string]bool{})
		return
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		seen[preset] = true
		pi, ok := presetItems[preset]
		if !ok {
			pi = item.AddSubMenuItemCheckbox(preset, "activate "+name+" · "+preset, false)
			presetItems[preset] = pi
			go m.handlePresetClicks(name, preset, pi)
		}
		setChecked(pi, active && preset == st.Preset)
	}
	removeStale(presetItems, seen)
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
		// Activate with the configuration's own active preset ("" keeps it).
		// Native menus don't deliver this click at all once the row has grown
		// a submenu (multi-preset) — picking a preset there is the activation.
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

// handlePresetClicks activates the (name, preset) this specific submenu row
// stands for — "picking a preset is the activation" (README § 5).
func (m *menu) handlePresetClicks(name, preset string, item *systray.MenuItem) {
	for range item.ClickedCh {
		m.act("activating "+name+" · "+preset+"…", func() error { return m.a.Activate(name, preset) })
	}
}

// handleRestart re-applies the active configuration (README "Restart
// collector"). The design's in-flight pulse has no native-menu equivalent;
// a static "Restarting…" title stands in for it (busy state still visible,
// no animation needed).
func (m *menu) handleRestart() {
	for range m.restart.ClickedCh {
		m.act("Restarting…", func() error { return m.a.Apply() })
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
