//go:build darwin

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/cfgstore"
)

// statusLines renders the two status-block lines (README "5. Menu bar";
// ACCEPTANCE C5.1). Stopped is exactly "Stopped" / "no listeners" — no
// config or preset named, since nothing is running to name. Running always
// names the config and the preset — "default" when the config has none (a
// config with no `${VAR}` references activates with empty values; 2026-08-26
// feedback). warns is the collector log's warn-level line count only
// (controller ruling D2: warn-only here, unlike the window sidebar's
// warn+error sum), and the tail is omitted entirely at zero rather than
// printed as "0 warnings". The leading ●/○ stands in for the design's
// amber/grey running dot — a native menu item can't tint text, so a glyph
// carries what colour would.
//
// The ports segment is st.Listening — the ports the collector process is
// actually listening on, detected from the OS — never a claim derived from
// settings or YAML. Nothing detected omits the segment entirely.
func statusLines(st app.Status, warns int) (line1, line2 string) {
	if !st.Running {
		return "○ Stopped", "no listeners"
	}
	line1 = "● Running · " + st.Config
	if st.Preset != "" {
		line1 += " · " + st.Preset
	} else {
		line1 += " · default"
	}
	line2 = portsSegment(st.Listening)
	if warns > 0 {
		if line2 != "" {
			line2 += " · "
		}
		line2 += fmt.Sprintf("%d warnings", warns)
	}
	// The conformance verdict's warning, appended to the warnings segment:
	// apps following compy's advertised env would miss this collector.
	if st.Conformance != nil && !st.Conformance.Conforming {
		if line2 != "" {
			line2 += " · "
		}
		line2 += "ports mismatch"
	}
	return line1, line2
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

// splitInline divides the alphabetically ordered name list into up to n
// inline rows and the "More…" remainder, which simply continues the same
// alphabetical order (2026-08-26 amendment).
func splitInline(ordered []string, n int) (inline, overflow []string) {
	if len(ordered) <= n {
		return ordered, nil
	}
	return ordered[:n], ordered[n:]
}

// checkedConfig is the one configuration whose row (and running preset)
// carries the active indicator icon (it carried the native checkmark before
// the three-state icons replaced it): the active config while the collector
// is RUNNING, nobody when it is stopped. The indicator means "this is what
// is running" — the same honesty rule that keeps it parked during a pending
// activation — and with the collector stopped, nothing is running (the
// active config is still named in the status block).
func checkedConfig(st app.Status) string {
	if st.Running {
		return st.Config
	}
	return ""
}

// presetChoices reports a configuration's presets (sorted) and whether it
// gets a submenu at all: any config with a preset does (2026-08-26 feedback:
// switch "if a preset is available" — a single-preset submenu still shows
// what would run). Picking a preset there is the activation; a preset-less
// config activates directly on click.
func presetChoices(info cfgstore.Info) (names []string, submenu bool) {
	if len(info.Meta.Presets) == 0 {
		return nil, false
	}
	names = make([]string, 0, len(info.Meta.Presets))
	for n := range info.Meta.Presets {
		names = append(names, n)
	}
	slices.Sort(names)
	// A single preset needs no picker: clicking the config activates it
	// directly (clickPreset), whatever its name.
	return names, len(names) >= 2
}

// clickPreset is the preset a plain click on the config row activates: the
// config's only preset when it has exactly one — even if it was never the
// active one — and "" otherwise ("" keeps the config's own active preset;
// multi-preset configs activate through their submenu instead).
func clickPreset(info cfgstore.Info) string {
	if len(info.Meta.Presets) == 1 {
		for n := range info.Meta.Presets {
			return n
		}
	}
	return ""
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

// rowState is a config row's steady-state indicator: the active icon on the
// one configuration that is RUNNING (checkedConfig's rule, inherited from
// the checkmark it replaced), no icon otherwise — so a stopped collector
// shows no icons anywhere.
func rowState(name string, st app.Status) itemState {
	if name == checkedConfig(st) {
		return itemActive
	}
	return itemNone
}

// presetState is a preset submenu item's steady-state indicator: active only
// when its config is the running one AND it is that config's running preset.
func presetState(config, preset string, st app.Status) itemState {
	if config == checkedConfig(st) && preset == st.Preset {
		return itemActive
	}
	return itemNone
}

// swapMarks is what an in-flight action paints at click time: the config row
// and preset item going down (the running ones being deactivated) and the
// ones going up (the target). "" / the zero presetTarget mean "mark
// nothing". The end-of-action sync repaints launchd truth over these —
// success or failure alike.
type swapMarks struct {
	rowDown, rowUp       string
	presetDown, presetUp presetTarget
}

// activateMarks is the transition an activation click paints, given the last
// synced status: the still-running config row and its running preset go
// down, the clicked target row and preset go up. From stopped, nothing is
// going down — up only. A same-config preset swap marks only the presets
// (old down, new up); the row itself stays on the active icon, since that
// configuration keeps running. Re-clicking the running preset paints it
// going up (it is re-applied), never both directions at once.
func activateMarks(st app.Status, target presetTarget) swapMarks {
	m := swapMarks{}
	if target.preset != "" {
		m.presetUp = target
	}
	if !st.Running {
		m.rowUp = target.config
		return m
	}
	if st.Config != target.config {
		m.rowDown = st.Config
		m.rowUp = target.config
	}
	if st.Preset != "" {
		down := presetTarget{config: st.Config, preset: st.Preset}
		if down != m.presetUp {
			m.presetDown = down
		}
	}
	return m
}

// toggleMarks is the Stop/Start transition: stopping marks the running row
// (and its running preset) going down; starting marks the active config
// going up — the one Start will bring back.
func toggleMarks(st app.Status) swapMarks {
	m := swapMarks{}
	if st.Running {
		m.rowDown = st.Config
		if st.Preset != "" {
			m.presetDown = presetTarget{config: st.Config, preset: st.Preset}
		}
		return m
	}
	m.rowUp = st.Config
	if st.Preset != "" {
		m.presetUp = presetTarget{config: st.Config, preset: st.Preset}
	}
	return m
}

// restartMarks is the Restart transition: going up on the running config row
// and preset — the collector comes straight back, and a single paint can't
// show down-then-up, so up (the end state being worked toward) carries it.
// Restart is disabled while stopped, so a stopped status marks nothing.
func restartMarks(st app.Status) swapMarks {
	if !st.Running {
		return swapMarks{}
	}
	m := swapMarks{rowUp: st.Config}
	if st.Preset != "" {
		m.presetUp = presetTarget{config: st.Config, preset: st.Preset}
	}
	return m
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
// works too). No bundle, or exe already inside one: exe itself, unchanged.
func windowExe(exe string) string {
	cand := filepath.Join(filepath.Dir(exe), "compy.app", "Contents", "MacOS", "compy")
	if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() && fi.Mode()&0111 != 0 {
		return cand
	}
	return exe
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
