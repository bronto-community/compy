package app

import (
	"strings"

	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/collector"
	"github.com/bronto-community/compy/internal/launchd"
	"github.com/bronto-community/compy/internal/state"
)

// Health reports what the running collector's own telemetry says about the
// data moving through it. It never fails: a stopped or unreachable collector
// answers {"available": false}, which is what the Collector screen shows as
// dashes.
func (a *App) Health() (any, error) {
	// Only our own collector's numbers are ours to show. :8888 is otelcol's
	// default, so a second collector on the machine answers there when ours
	// is stopped — and its throughput is not this one's.
	running, pid, err := launchd.Info()
	if err != nil || !running {
		return collector.Health{}, nil
	}
	// Pid-bound: scrape only the ports the process actually listens on
	// (:8888 first when it is among them); the blind default probe exists
	// only as a fallback when port detection is unavailable.
	h := collector.ScrapePorts(collector.ListeningPorts(pid))
	view := healthView{Health: h}
	if vars := dropDiagnosis(true, h.Dropped, a.activeMissing()); len(vars) > 0 {
		view.Dropping = &dropping{Vars: vars}
	}
	return view, nil
}

// healthView is the health payload: the collector's own numbers plus the
// drop diagnosis when it holds. No new polling anywhere — every surface
// derives the diagnosis from data it already fetches.
type healthView struct {
	collector.Health
	Dropping *dropping `json:"dropping,omitempty"`
}

type dropping struct {
	Vars []string `json:"vars"`
}

// dropDiagnosis is the honesty rule for "runs but silently drops": a config
// activated with missing required values (via "activate anyway") starts
// fine — validation never binds or sends — and only the runtime evidence
// shows the loss. Blame the variables ONLY when all three legs hold: the
// collector is running, telemetry is actually being dropped, and the active
// preset is missing required values. Drops with all values present have
// some other cause and get no vars named; missing values without drops are
// the pre-flight's business, not a runtime warning.
func dropDiagnosis(running bool, dropped int64, missing []string) []string {
	if !running || dropped <= 0 || len(missing) == 0 {
		return nil
	}
	return missing
}

// activeMissing names the ACTIVE configuration's missing required values —
// cfgstore.MissingRequired (the pre-flight's own rule) against its active
// preset. Nil when nothing is active or the config is unreadable: no
// config, no claim.
func (a *App) activeMissing() []string {
	s, err := state.LoadSettings()
	if err != nil || s.ActiveConfig == "" {
		return nil
	}
	info, _, err := cfgstore.Get(a.Dir, s.ActiveConfig)
	if err != nil {
		return nil
	}
	return cfgstore.MissingRequired(a.Dir, info, "")
}

// DropDiagnosis is the tray's and the CLI's entry to the same rule Health
// embeds for the window. The cheap check runs first: a fully valued active
// preset — the common case — costs one settings and one config read, never
// a launchctl call or a metrics scrape.
func (a *App) DropDiagnosis() []string {
	missing := a.activeMissing()
	if len(missing) == 0 {
		return nil
	}
	running, pid, err := launchd.Info()
	if err != nil || !running {
		return nil
	}
	h := collector.ScrapePorts(collector.ListeningPorts(pid))
	return dropDiagnosis(true, h.Dropped, missing)
}

// Log returns the last n lines of the collector log.
func (a *App) Log(n int) (string, error) { return collector.TailLog(a.LogPath(), n) }

// LogStats counts collector log lines, among the last `lines` lines, whose
// level field is "error" or "warn" — counting only lines since the
// collector's last startup. otelcol logs a "Starting otelcol..." message at
// each boot; counting the whole persisted tail instead would let yesterday's
// errors pin the tray's attention icon (and the status line's counts) on a
// perfectly healthy run today. No startup marker in the window means a
// long-running collector whose marker scrolled out of the tail — those lines
// are all current-session anyway, so the whole window counts. Collector zap
// lines are tab-separated (timestamp, level, caller, message);
// strings.Fields tolerates a space-delimited log the same way. A missing log
// file counts as zero, not an error.
func (a *App) LogStats(lines int) (errors, warnings int, err error) {
	tail, err := collector.TailLog(a.LogPath(), lines)
	if err != nil {
		return 0, 0, err
	}
	all := strings.Split(tail, "\n")
	for i := len(all) - 1; i >= 0; i-- {
		if strings.Contains(all[i], "Starting otelcol") {
			all = all[i+1:]
			break
		}
	}
	for _, line := range all {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[1] {
		case "error":
			errors++
		case "warn":
			warnings++
		}
	}
	return errors, warnings, nil
}
