//go:build darwin

package tray

import (
	"fmt"
	"slices"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/cfgstore"
)

// statusLines renders the two status-block lines: the running/config/set
// summary, and the ports line with an error/warning tail (from
// app.LogStats) shown only when there's something to report.
func statusLines(st app.Status, errs, warns int) (line1, line2 string) {
	if st.Config == "" {
		line1 = "no configuration"
	} else {
		state := "stopped"
		if st.Running {
			state = "running"
		}
		line1 = fmt.Sprintf("%s — %s", state, st.Config)
		if st.Set != "" {
			line1 += fmt.Sprintf(" (%s)", st.Set)
		}
	}
	line2 = fmt.Sprintf("grpc :%d · http :%d", st.GRPCPort, st.HTTPPort)
	if errs > 0 || warns > 0 {
		line2 += fmt.Sprintf(" · %d err · %d warn", errs, warns)
	}
	return line1, line2
}

// activeVariableSets reports the active configuration's variable sets
// (sorted) and its currently-active set, and whether the picker should be
// shown at all: only when there is an active config with 2+ sets.
func activeVariableSets(configs []cfgstore.Info, activeConfig string) (names []string, activeSet string, show bool) {
	if activeConfig == "" {
		return nil, "", false
	}
	for _, c := range configs {
		if c.Name != activeConfig {
			continue
		}
		if len(c.Meta.VariableSets) < 2 {
			return nil, "", false
		}
		names = make([]string, 0, len(c.Meta.VariableSets))
		for n := range c.Meta.VariableSets {
			names = append(names, n)
		}
		slices.Sort(names)
		return names, c.Meta.ActiveSet, true
	}
	return nil, "", false
}

// assignSlots splits the (sorted) config names into the inline menu slots
// and the overflow submenu. The active config is always inline: when it
// would land in overflow it takes the last slot, and the config it
// displaces moves to overflow (keeping sort order there).
func assignSlots(configs []string, active string, slots int) (inline, overflow []string) {
	if len(configs) <= slots {
		return append([]string(nil), configs...), nil
	}
	inline = append([]string(nil), configs[:slots]...)
	overflow = append([]string(nil), configs[slots:]...)
	for i, name := range overflow {
		if name == active {
			displaced := inline[slots-1]
			inline[slots-1] = name
			overflow = append(overflow[:i], overflow[i+1:]...)
			// keep overflow sorted: displaced comes from before any
			// remaining overflow entry, so it goes to the front
			overflow = append([]string{displaced}, overflow...)
			break
		}
	}
	return inline, overflow
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
