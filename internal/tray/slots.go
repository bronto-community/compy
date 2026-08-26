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
// carries the radio checkmark: the active config while the collector is
// RUNNING, nobody when it is stopped. The checkmark means "this is what is
// running" — the same honesty rule that keeps it parked during a pending
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
	return names, true
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

// pendingTitle marks a clicked config/preset row while its activation is in
// flight. Only the title carries the pending state — the checkmark itself
// keeps meaning "this is what launchd is running" and never moves until a
// post-activation sync says so.
func pendingTitle(base string) string {
	return base + " — Activating…"
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
