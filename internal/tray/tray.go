//go:build darwin

// Package tray implements compy's macOS menu-bar item: status line, the
// CONFIGURATION list (alphabetical, "More…" overflow continuing it, a
// preset submenu where picking a preset is the activation), "Restart
// collector", and "Open compy" for everything else. Menu bar v4 —
// docs/design/handoff/README.md § "5. Menu bar" and its 2026-08-26
// amendments (alphabetical ordering supersedes C5.2 recency), ACCEPTANCE.md
// C5.
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
	toggle  *systray.MenuItem // Stop/Start Collector, title tracks running state

	// running mirrors the last synced Status.Running so the toggle's click
	// handler can decide Stop vs Start at click time, under m.mu — the same
	// discipline as slotNames.
	running bool

	// icon is the state the menu-bar icon currently shows, so sync() only
	// calls into systray when it actually changes (not every 5s tick).
	icon iconState

	// presetOwner records which (config, preset) each live preset-submenu
	// item currently represents, so its click handler can resolve that at
	// click time (like handleSlotClicks does via slotNames) instead of
	// trusting a name captured in a closure at item-creation time. That
	// distinction matters because slot positions are fixed and reused
	// across configs (creates/deletes/renames reorder who sits at slot i):
	// a preset-name
	// cache keyed only by "default" would otherwise let one config's leftover
	// item — and its stale closure — silently activate a different config
	// that happens to share a preset name (T3 review finding).
	presetOwner map[*systray.MenuItem]presetTarget
}

// presetTarget is the (config, preset) one preset-submenu item currently
// stands for; see menu.presetOwner.
type presetTarget struct {
	config, preset string
}

func onReady(a *app.App) {
	// Icon-only, no title (macOS convention); the designed template icon —
	// black-on-transparent, tinted by AppKit — carries the state by shape
	// (icons/README.md). Start at stopped until the first sync says better.
	systray.SetTemplateIcon(iconStopped.data(), iconStopped.data())
	systray.SetTooltip("compy — local OpenTelemetry Collector manager")

	m := &menu{
		a:           a,
		moreItems:   map[string]*systray.MenuItem{},
		morePresets: map[string]map[string]*systray.MenuItem{},
		presetOwner: map[*systray.MenuItem]presetTarget{},
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
	m.toggle = systray.AddMenuItem(toggleTitle(false), "stop or start the collector")
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
	go m.handleToggle()
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
	// error count feeds the icon's attention state instead of this line.
	errs, warns, _ := m.a.LogStats(500) // best-effort: a log-read error just omits the tail
	configs, cfgErr := m.a.Configs()
	line1, line2 := statusLines(st, warns)
	m.status.SetTitle(line1)
	m.statusLine2.SetTitle(line2)
	m.setIcon(iconFor(st.Running, errs))
	m.running = st.Running
	m.toggle.SetTitle(toggleTitle(st.Running))
	// Restarting a stopped collector makes no sense — the toggle's Start is
	// the way up.
	if st.Running {
		m.restart.Enable()
	} else {
		m.restart.Disable()
	}

	if cfgErr != nil {
		return
	}
	byName := make(map[string]cfgstore.Info, len(configs))
	names := make([]string, 0, len(configs))
	for _, c := range configs {
		byName[c.Name] = c
		names = append(names, c.Name)
	}
	inline, overflow := splitInline(alphabetical(names), len(m.slots))

	for i, slot := range m.slots {
		if i >= len(inline) {
			m.slotNames[i] = ""
			slot.Hide()
			m.removeStale(m.slotPresets[i], map[string]bool{})
			continue
		}
		name := inline[i]
		if m.slotNames[i] != name {
			// This slot changed which config it shows (a create, delete or
			// rename shifted the alphabetical order). Drop its whole preset
			// cache rather than let a same-named preset item survive into a
			// different config's ownership — belt-and-braces alongside the
			// click-time resolution in syncRow/resolvePresetClick (T3
			// review finding).
			m.removeStale(m.slotPresets[i], map[string]bool{})
		}
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
			m.removeStale(m.morePresets[name], map[string]bool{}) // drop that config's own preset-item ownership too
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
// the row's own checkmark, and — for a configuration with presets
// (presetChoices) — its preset submenu, lazily created in presetItems
// (that row's own cache, since systray can only append items). Clicking a
// preset row is itself the activation; the running preset is checked.
// Checkmarks mean "this is what is RUNNING" (checkedConfig): a stopped
// collector checks nothing, however recently a config was active.
//
// Every preset item's ownership is (re)recorded here on every sync, whether
// the item is newly created or reused from a previous sync — the click
// handler (handlePresetClicks/resolvePresetClick) resolves it from
// m.presetOwner at click time rather than a name closed over at creation,
// so a cache entry that outlives a config change never misdirects the
// activation (T3 review finding).
func (m *menu) syncRow(item *systray.MenuItem, presetItems map[string]*systray.MenuItem, name string, info cfgstore.Info, st app.Status) {
	active := name == checkedConfig(st)
	setChecked(item, active)
	presets, submenu := presetChoices(info)
	if !submenu {
		m.removeStale(presetItems, map[string]bool{})
		return
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		seen[preset] = true
		pi, ok := presetItems[preset]
		if !ok {
			pi = item.AddSubMenuItemCheckbox(preset, "activate "+name+" · "+preset, false)
			presetItems[preset] = pi
			go m.handlePresetClicks(pi)
		}
		pi.SetTooltip("activate " + name + " · " + preset)
		m.setPresetOwner(pi, name, preset)
		setChecked(pi, active && preset == st.Preset)
	}
	m.removeStale(presetItems, seen)
}

// setPresetOwner records which (config, preset) item currently represents.
// Called only from sync() (directly, or via syncRow), which every caller
// except the very first (onReady's initial, single-threaded m.sync()) makes
// under m.mu — the same discipline slotNames already relies on.
func (m *menu) setPresetOwner(item *systray.MenuItem, config, preset string) {
	m.presetOwner[item] = presetTarget{config: config, preset: preset}
}

// resolvePresetClick looks up what a preset-submenu item currently stands
// for, under mu, at click time — see menu.presetOwner.
func (m *menu) resolvePresetClick(item *systray.MenuItem) (presetTarget, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	target, ok := m.presetOwner[item]
	return target, ok
}

// setIcon swaps the menu-bar icon when — and only when — the state changed,
// so the 5s resync doesn't churn systray. Called from sync(), so it runs
// under m.mu everywhere but onReady's initial single-threaded call (the
// same discipline as slotNames/setPresetOwner). The zero value of m.icon is
// iconStopped, matching the icon onReady sets before the first sync.
func (m *menu) setIcon(s iconState) {
	if s == m.icon {
		return
	}
	m.icon = s
	systray.SetTemplateIcon(s.data(), s.data())
}

func setChecked(item *systray.MenuItem, want bool) {
	if want && !item.Checked() {
		item.Check()
	} else if !want && item.Checked() {
		item.Uncheck()
	}
}

// removeStale removes every item in items not named in seen, dropping its
// preset ownership record too (a no-op for items that were never a preset
// row's key, e.g. the top-level "More…" entries).
func (m *menu) removeStale(items map[string]*systray.MenuItem, seen map[string]bool) {
	for name, item := range items {
		if !seen[name] {
			item.Remove()
			delete(items, name)
			delete(m.presetOwner, item)
		}
	}
}

func (m *menu) handleConfigClicks(name string, item *systray.MenuItem) {
	for range item.ClickedCh {
		// Activate with the configuration's own active preset ("" keeps it).
		// Native menus don't deliver this click at all once the row has grown
		// a submenu (multi-preset) — picking a preset there is the activation.
		m.act(activatingLine(name, ""), item, name, func() error { return m.a.Activate(name, "") })
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
		m.doAct(activatingLine(name, ""), slot, name, func() error { return m.a.Activate(name, "") })
		m.mu.Unlock()
	}
}

// handlePresetClicks activates whatever (config, preset) this submenu item
// currently owns, per m.presetOwner — resolved fresh at click time rather
// than a name captured when the item was created, since a fixed-position
// row's preset items can end up reused for a different config when the
// list reorders. "Picking a preset is the activation" (README § 5).
func (m *menu) handlePresetClicks(item *systray.MenuItem) {
	for range item.ClickedCh {
		target, ok := m.resolvePresetClick(item)
		if !ok {
			continue
		}
		m.act(activatingLine(target.config, target.preset), item, target.preset, func() error {
			return m.a.Activate(target.config, target.preset)
		})
	}
}

// handleToggle stops the collector when it is running and starts it when it
// is not, resolving which at click time from the last synced state (under
// m.mu, like handleSlotClicks). doAct's closing sync() flips the title, the
// Restart enable-state, and the menu-bar icon.
func (m *menu) handleToggle() {
	for range m.toggle.ClickedCh {
		m.mu.Lock()
		if m.running {
			m.doAct(toggleBusyLine(true), nil, "", func() error { return m.a.Stop() })
		} else {
			m.doAct(toggleBusyLine(false), nil, "", func() error { return m.a.Start() })
		}
		m.mu.Unlock()
	}
}

// handleRestart re-applies the active configuration (README "Restart
// collector"). The design's in-flight pulse has no native-menu equivalent;
// a static "Restarting…" title stands in for it (busy state still visible,
// no animation needed).
func (m *menu) handleRestart() {
	for range m.restart.ClickedCh {
		m.act("Restarting…", nil, "", func() error { return m.a.Apply() })
	}
}

// act runs an apply-triggering action with a progress note in the status
// line; errors land there too (truncated) and in the tray log. pending, if
// non-nil, is the clicked menu item: its title gains pendingTitle's
// "— Activating…" suffix for the duration (immediate feedback that the click
// registered — the checkmark stays honest and only moves on post-completion
// sync), with base its normal title to restore. A second click while one
// activation is in flight queues on m.mu and runs after.
func (m *menu) act(note string, pending *systray.MenuItem, base string, fn func() error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.doAct(note, pending, base, fn)
}

// doAct is act's body; callers hold m.mu — which is also the sync-vs-pending
// guard: the 5s ticker syncs under the same lock, so it cannot repaint (and
// erase the pending suffix) while the activation is still running.
func (m *menu) doAct(note string, pending *systray.MenuItem, base string, fn func() error) {
	if pending != nil {
		pending.SetTitle(pendingTitle(base))
	}
	m.status.SetTitle(note)
	err := fn()
	if pending != nil {
		pending.SetTitle(base) // cleared on success and failure alike, before the error pause
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "compy tray:", err)
		m.status.SetTitle(errorLine(err))
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
// windowProc.alive() flips as soon as the user closes it. It spawns the
// bundled binary when a compy.app sits next to this one (see windowExe), so
// the window shows the compy name and Dock icon.
func spawnWindow() (*windowProc, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(windowExe(exe), "window")
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
