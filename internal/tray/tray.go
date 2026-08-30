//go:build darwin

// Package tray implements compy's macOS menu-bar item: status line, the
// CONFIGURATION list (flat rows of (config, preset) targets — a
// multi-preset config is N rows titled "name · preset", no submenus; owner
// ruling 2026-08-30 — alphabetical, "More…" overflow continuing it),
// "Restart collector", and "Open compy" for everything else, plus "Remove
// from Menu Bar" (the tray uninstall, run from the menu). The open menu
// carries key equivalents (keys_darwin.go); there is deliberately no global
// hotkey. Menu bar v5 — docs/design/handoff/README.md § "5. Menu bar" and
// its amendments, ACCEPTANCE.md C5.
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
	"github.com/bronto-community/compy/internal/launchd"
)

// refreshInterval is how often the status line and menu indicator icons are
// re-synced with the state directory (which the CLI or window may have
// changed behind our back).
const refreshInterval = 5 * time.Second

// maxInline is how many (config, preset) rows appear directly in the menu
// before the rest overflow into the "More…" submenu (ACCEPTANCE C5.3).
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

	slots       []*systray.MenuItem // pre-created inline rows (fixed menu position)
	slotTargets []presetTarget      // slotTargets[i] = the (config, preset) slots[i] activates, zero = hidden

	more      *systray.MenuItem // "More…" overflow submenu parent
	moreItems map[presetTarget]*systray.MenuItem

	restart *systray.MenuItem
	toggle  *systray.MenuItem // Stop/Start Collector, title tracks running state

	// last mirrors the last synced Status so click handlers can decide what
	// they are doing — Stop vs Start, which rows a swap transitions — at
	// click time, under m.mu (the same discipline as slotNames).
	last app.Status

	// icon is the state the menu-bar icon currently shows, so sync() only
	// calls into systray when it actually changes (not every 5s tick).
	icon iconState

	// itemIcons is the indicator each row currently shows, so setItemIcon
	// only calls into systray on a change. An item absent from the map has
	// never been painted (a fresh systray item has no image at all), so its
	// first paint always goes through — including the blank, which keeps
	// titles aligned with iconed rows and is also the only way to take an
	// icon back off (systray cannot clear one).
	itemIcons map[*systray.MenuItem]itemState
}

// presetTarget is the exact (config, preset) one flat row activates. A ""
// preset means "keep the config's own active preset" (a preset-less config —
// tests and broken state only, below cfgstore's default-preset invariant).
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
		a:         a,
		moreItems: map[presetTarget]*systray.MenuItem{},
		itemIcons: map[*systray.MenuItem]itemState{},
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
	// Fixed slots keep rows at this menu position even for configs that
	// appear while the tray runs (systray can only append new items).
	m.slotTargets = make([]presetTarget, maxInline)
	for i := 0; i < maxInline; i++ {
		// Plain items, not checkboxes: the three-state indicator icons
		// (active / going down / going up) carry the state the native
		// checkmark used to — one indicator system, not two fighting.
		slot := systray.AddMenuItem("", "")
		slot.Hide()
		m.slots = append(m.slots, slot)
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
	applyKeyEquivalents(append(keyEquivalents(m.toggle, m.restart, openApp, quit), digitEquivalents(m.slots)...))

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
	inline, overflow := splitInline(flatRows(configs), len(m.slots))

	for i, slot := range m.slots {
		if i >= len(inline) {
			m.slotTargets[i] = presetTarget{}
			slot.Hide()
			continue
		}
		r := inline[i]
		m.slotTargets[i] = r.target
		// SetTitle unconditionally: the same target's title can change when
		// its config gains a second preset ("name" → "name · preset").
		slot.SetTitle(r.title)
		slot.Show()
		m.setItemIcon(slot, rowState(r.target, st))
	}

	seen := map[presetTarget]bool{}
	for _, r := range overflow {
		seen[r.target] = true
		item, ok := m.moreItems[r.target]
		if !ok {
			// The closure over r.target is safe here, unlike the submenu
			// era's name-keyed preset caches: the key IS the full (config,
			// preset) identity, and the item is removed the moment that
			// identity leaves the overflow — no reuse across targets.
			item = m.more.AddSubMenuItem(r.title, "")
			m.moreItems[r.target] = item
			go m.handleMoreClicks(r.target, item)
		}
		item.SetTitle(r.title)
		m.setItemIcon(item, rowState(r.target, st))
	}
	for t, item := range m.moreItems {
		if !seen[t] {
			item.Remove()
			delete(m.moreItems, t)
			delete(m.itemIcons, item)
		}
	}
	if len(overflow) > 0 {
		m.more.Show()
	} else {
		m.more.Hide()
	}
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

// setItemIcon paints one row's indicator when — and
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
// on what is being deactivated, going-up on what is activating. A row the
// marks name but the menu doesn't currently show is skipped — the status
// line still narrates the action.
func (m *menu) paintMarks(marks swapMarks) {
	if it := m.rowItem(marks.down); it != nil {
		m.setItemIcon(it, itemDown)
	}
	if it := m.rowItem(marks.up); it != nil {
		m.setItemIcon(it, itemUp)
	}
}

// rowItem finds the menu item currently showing target t — an inline slot
// or a More… entry — or nil. Callers hold m.mu.
func (m *menu) rowItem(t presetTarget) *systray.MenuItem {
	if t == (presetTarget{}) {
		return nil
	}
	for i, st := range m.slotTargets {
		if st == t {
			return m.slots[i]
		}
	}
	return m.moreItems[t] // nil when absent
}

// activate is the one thing clicking any flat row does: bring up that
// row's exact (config, preset). Callers hold m.mu.
func (m *menu) activate(t presetTarget) {
	m.doAct(activatingLine(t.config, t.preset), activateMarks(m.last, t), func() error {
		return m.a.Activate(t.config, t.preset)
	})
}

// handleSlotClicks resolves the slot's current target at click time from the
// slot table — the assignment may have changed since the menu was drawn
// (creates/deletes/renames resort the fixed slots), and this click-time
// resolution is also what lets the digit key equivalents retarget for free.
// Clicks arrive from the mouse and from digits 1–9 alike.
func (m *menu) handleSlotClicks(i int, slot *systray.MenuItem) {
	for range slot.ClickedCh {
		m.mu.Lock()
		if t := m.slotTargets[i]; t != (presetTarget{}) {
			m.activate(t)
		}
		m.mu.Unlock()
	}
}

// handleMoreClicks activates one More… row's fixed target; the item is
// removed whenever that target leaves the overflow, so the closure can never
// misdirect (see sync's creation site).
func (m *menu) handleMoreClicks(t presetTarget, item *systray.MenuItem) {
	for range item.ClickedCh {
		m.mu.Lock()
		m.activate(t)
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
