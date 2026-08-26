//go:build darwin

package tray

import (
	"fmt"
	"slices"

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
// grpcRef/httpRef say whether the active config's YAML references
// COMPY_GRPC_PORT / COMPY_HTTP_PORT: compy only injects those variables, so
// a config that doesn't reference them listens wherever its YAML says, and
// printing the settings ports would be a lie (2026-08-26 feedback).
func statusLines(st app.Status, warns int, grpcRef, httpRef bool) (line1, line2 string) {
	if !st.Running {
		return "○ Stopped", "no listeners"
	}
	line1 = "● Running · " + st.Config
	if st.Preset != "" {
		line1 += " · " + st.Preset
	} else {
		line1 += " · default"
	}
	switch {
	case grpcRef && httpRef:
		line2 = fmt.Sprintf(":%d :%d", st.GRPCPort, st.HTTPPort)
	case grpcRef:
		line2 = fmt.Sprintf(":%d grpc", st.GRPCPort)
	case httpRef:
		line2 = fmt.Sprintf(":%d http", st.HTTPPort)
	default:
		line2 = "ports per config.yaml"
	}
	if warns > 0 {
		line2 += fmt.Sprintf(" · %d warnings", warns)
	}
	return line1, line2
}

// activePortRefs reports whether the active config's YAML references
// COMPY_GRPC_PORT / COMPY_HTTP_PORT — the only ports compy actually injects.
// Unknown config (deleted, or the list failed to load) reports neither, so
// the status line falls back to the honest "ports per config.yaml".
func activePortRefs(configs []cfgstore.Info, active string) (grpcRef, httpRef bool) {
	for _, c := range configs {
		if c.Name != active {
			continue
		}
		for _, v := range c.Vars {
			if v.Name == "COMPY_GRPC_PORT" {
				grpcRef = true
			}
			if v.Name == "COMPY_HTTP_PORT" {
				httpRef = true
			}
		}
	}
	return grpcRef, httpRef
}

// recencyOrder orders configuration names per ACCEPTANCE C5.2: `recent`'s
// activation order first, then every other existing config alphabetically.
// A `recent` entry for a configuration that no longer exists (e.g. deleted
// since) is dropped rather than surfaced as a menu item.
func recencyOrder(names []string, recent []string) []string {
	if len(names) == 0 {
		return nil
	}
	exists := make(map[string]bool, len(names))
	for _, n := range names {
		exists[n] = true
	}
	ordered := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, r := range recent {
		if exists[r] && !seen[r] {
			ordered = append(ordered, r)
			seen[r] = true
		}
	}
	rest := make([]string, 0, len(names)-len(ordered))
	for _, n := range names {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	slices.Sort(rest)
	return append(ordered, rest...)
}

// splitInline divides a recency-ordered name list into up to n inline rows
// and the "More…" remainder, itself re-sorted alphabetically regardless of
// where in the recency order it fell (ACCEPTANCE C5.3).
func splitInline(ordered []string, n int) (inline, overflow []string) {
	if len(ordered) <= n {
		return ordered, nil
	}
	inline = ordered[:n]
	overflow = append([]string(nil), ordered[n:]...)
	slices.Sort(overflow)
	return inline, overflow
}

// presetChoices reports a configuration's presets (sorted) and whether it
// gets a submenu at all: only 2+ presets do (ACCEPTANCE C5.4) — a config
// with zero or one preset activates directly on click.
func presetChoices(info cfgstore.Info) (names []string, multi bool) {
	if len(info.Meta.Presets) < 2 {
		return nil, false
	}
	names = make([]string, 0, len(info.Meta.Presets))
	for n := range info.Meta.Presets {
		names = append(names, n)
	}
	slices.Sort(names)
	return names, true
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
