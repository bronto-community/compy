//go:build darwin

// Package tray implements compy's macOS menu-bar item: status line, the
// CONFIGURATION list (alphabetical, "More…" overflow continuing it, a
// preset submenu where picking a preset is the activation), "Restart
// collector", and "Open compy" for everything else, plus "Remove from Menu
// Bar" (the tray uninstall, run from the menu). The open menu carries key
// equivalents (keys_darwin.go); there is deliberately no global hotkey.
// Menu bar v4 — docs/design/handoff/README.md § "5. Menu bar" and its
// 2026-08-26 amendments (alphabetical ordering supersedes C5.2 recency),
// ACCEPTANCE.md C5.
package tray

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/launchd"
)

// refreshInterval is how often the status line and menu indicator icons are
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

	// updates is the disabled "Collector 0.x.y available" line under the
	// status block and compyUpdates its "compy 0.x available — brew upgrade
	// compy" sibling, each hidden while nothing newer is known; the *Line
	// fields cache what they currently show so the 5s resync only touches
	// systray on a change (the setIcon/itemIcons discipline).
	updates          *systray.MenuItem
	updatesLine      string
	compyUpdates     *systray.MenuItem
	compyUpdatesLine string

	slots       []*systray.MenuItem            // pre-created inline config rows (fixed menu position)
	slotNames   []string                       // slotNames[i] = config shown in slots[i], "" = hidden
	slotPresets []map[string]*systray.MenuItem // slotPresets[i]: that row's own preset submenu items

	more        *systray.MenuItem // "More…" overflow submenu parent
	moreItems   map[string]*systray.MenuItem
	morePresets map[string]map[string]*systray.MenuItem // per overflow config: its preset submenu items

	restart *systray.MenuItem
	toggle  *systray.MenuItem // Stop/Start Collector, title tracks running state

	// last mirrors the last synced Status so click handlers can decide what
	// they are doing — Stop vs Start, which rows a swap transitions — at
	// click time, under m.mu (the same discipline as slotNames).
	last app.Status

	// icon is the state the menu-bar icon currently shows, so sync() only
	// calls into systray when it actually changes (not every 5s tick).
	icon iconState

	// itemIcons is the indicator each config row / preset item currently
	// shows, so setItemIcon only calls into systray on a change. An item
	// absent from the map has never been painted (a fresh systray item has
	// no image at all), so its first paint always goes through — including
	// the blank, which keeps titles aligned with iconed rows and is also
	// the only way to take an icon back off (systray cannot clear one).
	itemIcons map[*systray.MenuItem]itemState

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
	// The status-item button tooltip is the one tooltip macOS actually
	// shows (on hovering the icon). Menu ITEM tooltips are invisible in a
	// status menu, so every item below passes "" — anything that mattered
	// in one has been moved into visible text (2026-08-29 HIG audit).
	systray.SetTooltip("compy — local OpenTelemetry Collector manager")

	m := &menu{
		a:           a,
		moreItems:   map[string]*systray.MenuItem{},
		morePresets: map[string]map[string]*systray.MenuItem{},
		presetOwner: map[*systray.MenuItem]presetTarget{},
		itemIcons:   map[*systray.MenuItem]itemState{},
	}

	m.status = systray.AddMenuItem("...", "")
	m.status.Disable()
	m.statusLine2 = systray.AddMenuItem("...", "")
	m.statusLine2.Disable()
	m.updates = systray.AddMenuItem("", "")
	m.updates.Disable()
	m.updates.Hide()
	m.compyUpdates = systray.AddMenuItem("", "")
	m.compyUpdates.Disable()
	m.compyUpdates.Hide()
	systray.AddSeparator()
	header := systray.AddMenuItem("CONFIGURATION", "")
	header.Disable()
	// Fixed slots keep configurations at this menu position even for configs
	// that appear while the tray runs (systray can only append new items).
	m.slotNames = make([]string, maxInline)
	m.slotPresets = make([]map[string]*systray.MenuItem, maxInline)
	for i := 0; i < maxInline; i++ {
		// Plain items, not checkboxes: the three-state indicator icons
		// (active / going down / going up) carry the state the native
		// checkmark used to — one indicator system, not two fighting.
		slot := systray.AddMenuItem("", "")
		slot.Hide()
		m.slots = append(m.slots, slot)
		m.slotPresets[i] = map[string]*systray.MenuItem{}
		go m.handleSlotClicks(i, slot)
	}
	m.more = systray.AddMenuItem("More…", "")
	m.more.Hide()
	systray.AddSeparator()
	m.toggle = systray.AddMenuItem(toggleTitle(false), "")
	m.restart = systray.AddMenuItem("Restart collector", "")
	systray.AddSeparator()
	// Tail order (HIG): the primary action first after the separator, the
	// two get-rid-of-it actions grouped at the end with the app-terminating
	// Quit last — Apple's own convention for menu extras. Remove from Menu
	// Bar is the honest removal (boots the login item, gone for good); Quit
	// by contrast only ends this run and the icon returns at login.
	openApp := systray.AddMenuItem("Open compy", "")
	remove := systray.AddMenuItem("Remove from Menu Bar", "")
	quit := systray.AddMenuItem("Quit", "")
	applyKeyEquivalents(keyEquivalents(m.slots, m.toggle, m.restart, openApp, quit))

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
	// Background release check: the tray is compy's one long-running process
	// (no daemon — v1 design rule), so the ~12h cadence lives here. The
	// hourly wakeups are just retry/due checks — app.MaybeCheckUpdates
	// declines without network until the persisted result is actually stale,
	// and stays silent on failure. Its own goroutine, off the 5s sync path.
	go func() {
		for {
			a.MaybeCheckUpdates()
			time.Sleep(time.Hour)
		}
	}()
	// The tray runs at every login — the one moment the OS-level env can be
	// re-applied, since `launchctl setenv` does not survive a reboot.
	go func() {
		if err := a.ReapplyOSEnv(); err != nil {
			fmt.Fprintln(os.Stderr, "compy tray: reapply os env:", err)
		}
	}()
	go m.handleToggle()
	go m.handleRestart()
	go handleOpenApp(openApp)
	go func() {
		<-remove.ClickedCh
		// Same machinery as `compy tray uninstall`. When the tray runs
		// under its LaunchAgent the bootout inside UninstallAgent SIGTERMs
		// this very process (which removes the icon — the desired end
		// state); the plist is already gone by then (UninstallAgent removes
		// it first), and the Quit below only matters for a foreground run.
		if err := launchd.UninstallAgent(launchd.TrayLabel); err != nil {
			fmt.Fprintln(os.Stderr, "compy tray: remove from menu bar:", err)
		}
		systray.Quit()
	}()
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
	line1, line2 := statusLines(st, warns, len(m.a.DropDiagnosis()) > 0)
	m.status.SetTitle(line1)
	m.statusLine2.SetTitle(line2)
	m.setIcon(iconFor(st.Running, errs))
	m.syncUpdatesLine()
	m.last = st
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
			item = m.more.AddSubMenuItem(name, "")
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
// the row's own indicator icon, and — for a configuration with presets
// (presetChoices) — its preset submenu, lazily created in presetItems
// (that row's own cache, since systray can only append items). Clicking a
// preset row is itself the activation; the running preset carries the
// active icon. The active icon means "this is what is RUNNING"
// (checkedConfig, rowState/presetState): a stopped collector marks
// nothing, however recently a config was active. Running as part of every
// sync, this is also what repaints truth over doAct's transition icons
// once the action has finished — success or failure alike.
//
// Every preset item's ownership is (re)recorded here on every sync, whether
// the item is newly created or reused from a previous sync — the click
// handler (handlePresetClicks/resolvePresetClick) resolves it from
// m.presetOwner at click time rather than a name closed over at creation,
// so a cache entry that outlives a config change never misdirects the
// activation (T3 review finding).
func (m *menu) syncRow(item *systray.MenuItem, presetItems map[string]*systray.MenuItem, name string, info cfgstore.Info, st app.Status) {
	m.setItemIcon(item, rowState(name, st))
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
			pi = item.AddSubMenuItem(preset, "")
			presetItems[preset] = pi
			go m.handlePresetClicks(pi)
		}
		m.setPresetOwner(pi, name, preset)
		m.setItemIcon(pi, presetState(name, preset, st))
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

// syncUpdatesLine shows the disabled availability lines when the persisted
// release check knows a newer collector than some pinned distro runs, or a
// newer compy than this build — read-only file checks via
// app.UpdateAvailable / app.CompyUpdateAvailable, never network. Called
// from sync(), so under m.mu everywhere but onReady's initial call.
// Read-only on purpose: updating still lives in the settings screen and the
// CLI (deliberately no actions here, like the no-per-backend-toggles rule).
func (m *menu) syncUpdatesLine() {
	collector := ""
	if latest, err := m.a.UpdateAvailable(); err == nil {
		collector = latest
	}
	l1, l2 := updateLines(collector, m.a.CompyUpdateAvailable())
	syncNoticeItem(m.updates, &m.updatesLine, l1)
	syncNoticeItem(m.compyUpdates, &m.compyUpdatesLine, l2)
}

// syncNoticeItem shows item with line ("" hides it), touching systray only
// when the cached line actually changed.
func syncNoticeItem(item *systray.MenuItem, cache *string, line string) {
	if line == *cache {
		return
	}
	*cache = line
	if line == "" {
		item.Hide()
		return
	}
	item.SetTitle(line)
	item.Show()
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

// setItemIcon paints one config row's / preset item's indicator when — and
// only when — it changed (the itemIcons cache), so the 5s resync doesn't
// churn systray. itemNone paints the transparent blank: systray has no way
// to clear an item's icon, and the blank keeps every title aligned with the
// iconed rows. Called from sync() and doAct(), so under m.mu everywhere but
// onReady's initial single-threaded call — the slotNames discipline.
func (m *menu) setItemIcon(item *systray.MenuItem, s itemState) {
	if cur, ok := m.itemIcons[item]; ok && cur == s {
		return
	}
	m.itemIcons[item] = s
	item.SetTemplateIcon(s.data(), s.data())
}

// paintMarks puts an action's transition icons up at click time: going-down
// on what is being deactivated, going-up on what is activating. Rows or
// presets the marks name but the menu doesn't currently show (an overflow
// config whose submenu was never built, say) are skipped — the status line
// still narrates the action.
func (m *menu) paintMarks(marks swapMarks) {
	if it := m.rowItem(marks.rowDown); it != nil {
		m.setItemIcon(it, itemDown)
	}
	if it := m.rowItem(marks.rowUp); it != nil {
		m.setItemIcon(it, itemUp)
	}
	if it := m.presetItem(marks.presetDown); it != nil {
		m.setItemIcon(it, itemDown)
	}
	if it := m.presetItem(marks.presetUp); it != nil {
		m.setItemIcon(it, itemUp)
	}
}

// rowItem finds the menu item currently showing config name — an inline
// slot or a More… entry — or nil. Callers hold m.mu.
func (m *menu) rowItem(name string) *systray.MenuItem {
	if name == "" {
		return nil
	}
	for i, n := range m.slotNames {
		if n == name {
			return m.slots[i]
		}
	}
	return m.moreItems[name] // nil when absent
}

// presetItem finds the preset submenu item t currently names, or nil.
// Callers hold m.mu.
func (m *menu) presetItem(t presetTarget) *systray.MenuItem {
	if t == (presetTarget{}) {
		return nil
	}
	for i, n := range m.slotNames {
		if n == t.config {
			return m.slotPresets[i][t.preset]
		}
	}
	return m.morePresets[t.config][t.preset] // nil map lookup is fine
}

// removeStale removes every item in items not named in seen, dropping its
// preset ownership and icon records too (ownership is a no-op for items
// that were never a preset row's key, e.g. the top-level "More…" entries).
func (m *menu) removeStale(items map[string]*systray.MenuItem, seen map[string]bool) {
	for name, item := range items {
		if !seen[name] {
			item.Remove()
			delete(items, name)
			delete(m.presetOwner, item)
			delete(m.itemIcons, item)
		}
	}
}

func (m *menu) handleConfigClicks(name string, item *systray.MenuItem) {
	for range item.ClickedCh {
		// A single-preset config activates that preset directly (resolved
		// fresh at click time); otherwise "" keeps the config's own active
		// preset. Native menus don't deliver this click at all once the row
		// has grown a submenu (multi-preset) — picking a preset there is
		// the activation.
		m.mu.Lock()
		p := m.presetFor(name)
		m.doAct(activatingLine(name, p), activateMarks(m.last, presetTarget{config: name, preset: p}), func() error { return m.a.Activate(name, p) })
		m.mu.Unlock()
	}
}

// presetFor resolves what preset a plain click on name should activate,
// reading the config fresh — presets may have changed since the menu was
// drawn. Unreadable config → "" (Activate will report the real error).
func (m *menu) presetFor(name string) string {
	info, _, err := m.a.Config(name)
	if err != nil {
		return ""
	}
	return clickPreset(info)
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
		p := m.presetFor(name)
		m.doAct(activatingLine(name, p), activateMarks(m.last, presetTarget{config: name, preset: p}), func() error { return m.a.Activate(name, p) })
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
		m.mu.Lock()
		m.doAct(activatingLine(target.config, target.preset), activateMarks(m.last, target), func() error {
			return m.a.Activate(target.config, target.preset)
		})
		m.mu.Unlock()
	}
}

// handleToggle stops the collector when it is running and starts it when it
// is not, resolving which at click time from the last synced state (under
// m.mu, like handleSlotClicks). Stopping marks the running row going down,
// starting marks the active row going up (toggleMarks); doAct's closing
// sync() flips the title, the Restart enable-state, and the menu-bar icon.
func (m *menu) handleToggle() {
	for range m.toggle.ClickedCh {
		m.mu.Lock()
		marks := toggleMarks(m.last)
		if m.last.Running {
			m.doAct(toggleBusyLine(true), marks, func() error { return m.a.Stop() })
		} else {
			m.doAct(toggleBusyLine(false), marks, func() error { return m.a.Start() })
		}
		m.mu.Unlock()
	}
}

// handleRestart re-applies the active configuration (README "Restart
// collector"). The design's in-flight pulse has no native-menu equivalent;
// a static "Restarting…" title stands in for it (busy state still visible,
// no animation needed), and the running row shows going-up (restartMarks).
func (m *menu) handleRestart() {
	for range m.restart.ClickedCh {
		m.mu.Lock()
		m.doAct("Restarting…", restartMarks(m.last), func() error { return m.a.Apply() })
		m.mu.Unlock()
	}
}

// doAct runs an apply-triggering action with a progress note in the status
// line; errors land there too (truncated) and in the tray log. marks are
// the transition icons painted up front — going-down on what the action
// deactivates, going-up on what it brings up — immediate feedback that the
// click registered, while the ACTIVE icon stays honest and only moves on
// the post-completion sync, which repaints launchd truth over the
// transitions: success puts active on the new config, failure puts it back
// on the survivor. Callers hold m.mu — which is also the sync-vs-transition
// guard: the 5s ticker syncs under the same lock, so it cannot repaint (and
// erase the transition icons) while the action is still running; a second
// click while one is in flight queues on m.mu and runs after.
func (m *menu) doAct(note string, marks swapMarks, fn func() error) {
	m.paintMarks(marks)
	m.status.SetTitle(note)
	err := fn()
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
