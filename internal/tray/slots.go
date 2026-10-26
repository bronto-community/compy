//go:build darwin

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/cfgstore"
)

// statusLines renders the two status-block lines (README "5. Menu bar";
// ACCEPTANCE C5.1). Stopped is exactly "Stopped" / "no listeners" — no
// config or preset named, since nothing is running to name. Running names
// the config and its preset — every config has a real one (cfgstore's
// default-preset invariant), so an empty Preset only means the status
// itself couldn't resolve it, and then nothing is claimed. warns is the
// collector log's warn-level line count only
// (controller ruling D2: warn-only here, unlike the window sidebar's
// warn+error sum), and the tail is omitted entirely at zero rather than
// printed as "0 warnings". The leading ●/○ stands in for the design's
// amber/grey running dot — a native menu item can't tint text, so a glyph
// carries what colour would.
//
// The ports segment is st.Listening — the ports the collector process is
// actually listening on, detected from the OS — never a claim derived from
// settings or YAML. Nothing detected omits the segment entirely.
//
// What is WRONG is not on these lines: app.Trouble's reasons get lines of
// their own right below (reasonLines), in full sentences — a chip squeezed
// onto the ports line said that something was off, never why. A collector
// launchd cannot keep up is neither running nor stopped: "✕ Crashed",
// named like a running one (it is the active setup failing); why is the
// first reason line.
func statusLines(st app.Status, warns int) (line1, line2 string) {
	if st.Crash != nil {
		line1 = "✕ Crashed · " + st.Config
		if st.Preset != "" {
			line1 += " · " + st.Preset
		}
		return line1, "launchd keeps retrying"
	}
	if !st.Running {
		line2 = "no listeners"
		// Stale plist while stopped: the user rebooted (or stopped) inside
		// the brew-upgrade window and launchd's login start failed on the
		// deleted binary. Start re-resolves and finishes the upgrade.
		if st.StaleBinary {
			line2 += " · restart needed"
		}
		return "○ Stopped", line2
	}
	line1 = "● Running · " + st.Config
	if st.Preset != "" {
		line1 += " · " + st.Preset
	}
	line2 = portsSegment(st.Listening)
	if warns > 0 {
		if line2 != "" {
			line2 += " · "
		}
		line2 += fmt.Sprintf("%d warnings", warns)
	}
	return line1, line2
}

// maxReasonLines is how many reason lines the menu has room for; past it,
// the last line says how many more there are (the window lists them all).
const maxReasonLines = 3

// reasonLines renders app.Trouble's reasons as the menu's "why" lines: one
// per reason, its own sentence (the same words the tooltip and the web UI
// show), marked ✕ when it is what makes the icon red and ⚠ for yellow — a
// native menu item cannot tint text, so the glyph carries the colour, the
// ●/○ convention of the status line. More than fit become "…and N more".
func reasonLines(tr app.Trouble) []string {
	var out []string
	for i, r := range tr.Reasons {
		if i == maxReasonLines-1 && len(tr.Reasons) > maxReasonLines {
			out = append(out, fmt.Sprintf("…and %d more — open compy", len(tr.Reasons)-i))
			break
		}
		out = append(out, reasonMark(r)+" "+truncate(sentence(r.Text), 80))
	}
	return out
}

// reasonMark is ✕ for the reasons that make the icon red, ⚠ otherwise.
func reasonMark(r app.Reason) string {
	if r.Code == "crashed" {
		return "✕"
	}
	return "⚠"
}

// tooltipFor is the menu-bar icon's hover text: the plain description while
// all is well, otherwise what is wrong — the one place a user sees why the
// icon is red or yellow without opening anything. (The status-item tooltip
// is the one macOS actually shows; see onReady.)
func tooltipFor(tr app.Trouble) string {
	if len(tr.Reasons) == 0 {
		return defaultTooltip
	}
	texts := make([]string, len(tr.Reasons))
	for i, r := range tr.Reasons {
		texts[i] = reasonMark(r) + " " + sentence(r.Text)
	}
	return "compy — " + strings.Join(texts, "\n")
}

// sentence capitalizes s's first letter: the reasons are written in the web
// UI's lowercase voice, and a menu line reads as a sentence.
func sentence(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// live reports whether there is a collector to stop or restart: running,
// or crashed with launchd still retrying it — Stop is how a user ends a
// crash loop, and Restart (a fresh apply) is how a stale binary heals.
func live(st app.Status) bool {
	return st.Running || st.Crash != nil
}

// truncate cuts s to n runes with an ellipsis — the crash reason can be a
// long sentence, and a menu item grows to fit whatever it is given.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// portsSegment compacts detected listening ports for one status line: up to
// four shown as ":6000 :6001 :8888", more as "N ports open", none as "" —
// no detection, no claim.
func portsSegment(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	if len(ports) > 4 {
		return fmt.Sprintf("%d ports open", len(ports))
	}
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprintf(":%d", p)
	}
	return strings.Join(parts, " ")
}

// updateLines renders the disabled availability lines under the status
// block: one for a newer collector release, one for a newer compy release,
// each with the way to get it — visibly, since menu-item tooltips (where
// the pointer used to live) never show on macOS, and the tray itself hosts
// no update actions. "" hides a line; both known shows both.
func updateLines(collectorLatest, compyLatest string) (collectorLine, compyLine string) {
	if collectorLatest != "" {
		collectorLine = "Collector " + collectorLatest + " available — Open compy to update"
	}
	if compyLatest != "" {
		compyLine = "compy " + compyLatest + " available — brew upgrade compy"
	}
	return collectorLine, compyLine
}

// alphabetical orders configuration names case-insensitively (2026-08-26
// amendment: the whole menu is alphabetical — supersedes the v4 recency
// ordering, which read as "what is the order here?"). Names equal under
// folding tie-break case-sensitively for a deterministic menu.
func alphabetical(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := slices.Clone(names)
	slices.SortFunc(out, func(a, b string) int {
		if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return out
}

// splitInline divides the ordered flat rows into up to n inline slots and
// the "More…" remainder, which simply continues the same order (2026-08-26
// amendment).
func splitInline[T any](ordered []T, n int) (inline, overflow []T) {
	if len(ordered) <= n {
		return ordered, nil
	}
	return ordered[:n], ordered[n:]
}

// flatRow is one activation row of the flat menu: the exact (config, preset)
// a click activates, and the title it shows.
type flatRow struct {
	target presetTarget
	title  string
}

// flatRows builds the flat activation list from a configs snapshot (owner
// ruling 2026-08-30: no preset submenus). A config with one preset is one
// row titled by the config name alone (its click still activates that exact
// preset, whatever its name); N>1 presets are N rows titled "name · preset".
// Configs order alphabetically (case-insensitive, the 2026-08-26 ruling),
// presets alphabetically within their config. A preset-less config — below
// cfgstore's default-preset invariant, so tests and broken state only —
// still gets a row; its "" preset means "keep the config's own active one".
func flatRows(configs []cfgstore.Info) []flatRow {
	byName := make(map[string]cfgstore.Info, len(configs))
	names := make([]string, 0, len(configs))
	for _, c := range configs {
		byName[c.Name] = c
		names = append(names, c.Name)
	}
	var out []flatRow
	for _, name := range alphabetical(names) {
		presets := make([]string, 0, len(byName[name].Meta.Presets))
		for p := range byName[name].Meta.Presets {
			presets = append(presets, p)
		}
		slices.Sort(presets)
		switch len(presets) {
		case 0:
			out = append(out, flatRow{presetTarget{config: name}, name})
		case 1:
			out = append(out, flatRow{presetTarget{name, presets[0]}, name})
		default:
			for _, p := range presets {
				out = append(out, flatRow{presetTarget{name, p}, name + " · " + p})
			}
		}
	}
	return out
}

// toggleTitle is the Stop/Start menu item's label for the collector's
// current run state — one item, never a "Stop" that can't stop anything.
func toggleTitle(running bool) string {
	if running {
		return "Stop collector"
	}
	return "Start collector"
}

// toggleBusyLine is the status block's first line while the toggle's action
// is in flight: stopping when it was running, starting when it wasn't.
func toggleBusyLine(running bool) string {
	if running {
		return "Stopping…"
	}
	return "Starting…"
}

// rowState is a flat row's steady-state indicator: the active icon on the
// exact (config, preset) that is RUNNING — the honesty rule the checkmark
// carried, at (config, preset) precision now that every row is one target —
// no icon otherwise, so a stopped collector shows no icons anywhere. A
// preset-less row's "" preset matches an equally unresolved status preset.
func rowState(t presetTarget, st app.Status) itemState {
	if st.Running && t.config == st.Config && t.preset == st.Preset {
		return itemActive
	}
	return itemNone
}

// swapMarks is what an in-flight action paints at click time: the row going
// down (the running target being deactivated) and the row going up (the
// target being brought up). The zero presetTarget means "mark nothing". The
// end-of-action sync repaints launchd truth over these — success or failure
// alike.
type swapMarks struct {
	down, up presetTarget
}

// activateMarks is the transition an activation click paints, given the last
// synced status: the running row goes down, the clicked row goes up. From
// stopped, nothing is going down — up only. Re-clicking the running row
// paints it going up (it is re-applied), never both directions at once.
func activateMarks(st app.Status, target presetTarget) swapMarks {
	m := swapMarks{up: target}
	if !st.Running {
		return m
	}
	if down := (presetTarget{config: st.Config, preset: st.Preset}); down != target {
		m.down = down
	}
	return m
}

// toggleMarks is the Stop/Start transition: stopping marks the running row
// going down; starting marks the active one going up — the row Start will
// bring back.
func toggleMarks(st app.Status) swapMarks {
	t := presetTarget{config: st.Config, preset: st.Preset}
	if live(st) {
		return swapMarks{down: t}
	}
	return swapMarks{up: t}
}

// restartMarks is the Restart transition: going up on the running row — the
// collector comes straight back, and a single paint can't show down-then-up,
// so up (the end state being worked toward) carries it. Restart is disabled
// while stopped, so a stopped status marks nothing.
func restartMarks(st app.Status) swapMarks {
	if !live(st) {
		return swapMarks{}
	}
	return swapMarks{up: presetTarget{config: st.Config, preset: st.Preset}}
}

// activatingLine is the status block's first line while an activation is in
// flight, so feedback exists even if the open menu doesn't repaint live.
// preset "" means "keep the config's own active preset" and names nothing.
func activatingLine(config, preset string) string {
	if preset == "" {
		return "Activating " + config + "…"
	}
	return "Activating " + config + " · " + preset + "…"
}

// errorLine renders a failed action for the status line (truncated — the
// full error goes to the tray's stderr log).
func errorLine(err error) string {
	msg := err.Error()
	if len(msg) > 80 {
		msg = msg[:80] + "…"
	}
	return "error: " + msg
}

// windowProc is the standalone window process the tray spawned. alive() is
// answered from done rather than by signalling the pid: the tray never
// Waits on the child in the foreground, so a signal-0 probe would keep
// answering "alive" for a zombie.
type windowProc struct {
	pid  int
	done chan struct{} // closed once the process has exited and been reaped
}

func (w *windowProc) alive() bool {
	if w == nil {
		return false
	}
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}

// windowExe picks the executable to spawn the standalone window with. When
// a compy.app bundle sits next to the running binary (packaging/macos/
// make-app.sh assembles one), the window is spawned via the bundle's copy of
// the binary: AppKit derives app identity — menu name, Dock icon — from the
// bundle containing the executable's path, so only that spawn gets the
// compy name and icon (verified empirically; a symlinked bundle binary
// works too). exe itself may be a symlink — Homebrew's /opt/homebrew/bin/
// compy points into the Caskroom, where the bundle sits next to the real
// binary — so when nothing sits next to the symlink, look next to its
// target too. No bundle anywhere, or exe already inside one: exe itself,
// unchanged.
func windowExe(exe string) string {
	if p, ok := bundleBinary(filepath.Dir(exe)); ok {
		return p
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil && r != exe {
		if p, ok := bundleBinary(filepath.Dir(r)); ok {
			return p
		}
	}
	return exe
}

// bundleBinary reports the compy.app bundle binary under dir, if a runnable
// one is there.
func bundleBinary(dir string) (string, bool) {
	cand := filepath.Join(dir, "compy.app", "Contents", "MacOS", "compy")
	if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() && fi.Mode()&0111 != 0 {
		return cand, true
	}
	return "", false
}

// openWindow raises the window from a previous click if it is still open,
// and otherwise spawns one. "Open compy" used to spawn unconditionally, so
// every click stacked another window on the screen. A raise that fails is
// reported and the process kept: spawning a second window instead would be
// the very bug this replaces.
func openWindow(cur *windowProc, spawn func() (*windowProc, error), raise func(pid int) error) (*windowProc, error) {
	if cur.alive() {
		return cur, raise(cur.pid)
	}
	return spawn()
}
