package app

import (
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bronto-community/compy/internal/collector"
	"github.com/bronto-community/compy/internal/launchd"
)

// dropHold is how long a rise in the collector's dropped-items counter
// keeps the warning up: drops come in bursts, and a warning that blinks
// for one 5s refresh is a warning nobody sees.
const dropHold = 3 * time.Minute

// Crash describes a collector launchd cannot keep up (Status.Crash): down
// between its restarts, or unable to start it at all. Only while it is
// DOWN — a collector launchd restarted and that runs again is fine, and
// says so (owner ruling 2026-10-02: no hold after recovery; red that
// outlives the problem only asks "what should I do?" with no answer). Its
// restart history stays visible, uncoloured, as Status.Restarts.
type Crash struct {
	// LastExit is how the previous run ended: "1", "78: EX_CONFIG", or a
	// signal such as "Killed: 9".
	LastExit string `json:"last_exit,omitempty"`
	// Reason is one human sentence, the actionable cause when compy can
	// name it (a busy port, an upgraded-away binary).
	Reason string `json:"reason"`
}

// scrape is collector.ScrapePorts, a var so tests can stage a dropped
// counter without a real collector — and without ever scraping this
// machine's real :18888.
var scrape = collector.ScrapePorts

// crashFor turns launchd's view of the job into a Crash, or nil. stale is
// launchd.StaleBinary, already computed by Status.
//
// The reason states what is known and nothing more: the cause when compy
// can name it, else the collector's own last words, else just how it
// ended. A process killed by a signal writes nothing — so never point at
// the log as if it explained.
func (a *App) crashFor(j launchd.Job, stale bool) *Crash {
	if !j.Down() {
		return nil
	}
	c := &Crash{LastExit: j.LastExit}
	tail, _ := collector.TailLog(a.LogPath(), 50)
	code, signal := splitExit(j.LastExit)
	switch bind := collector.BindError(tail); {
	case stale:
		c.Reason = "the collector binary was removed by a compy upgrade; restart the collector"
	case bind != "":
		c.Reason = bind
	case signal != "":
		c.Reason = "the collector was stopped by signal " + signal
	case code == "78":
		// launchd's EX_CONFIG: it could not exec the binary at all.
		c.Reason = "launchd can't start the collector binary (exit code 78)"
		if bin, err := launchd.InstalledBinary(); err == nil && bin != "" {
			c.Reason = "launchd can't start " + bin + " (exit code 78)"
		}
	default:
		c.Reason = "the collector exited with code " + code
		if msg := lastFatal(tail); msg != "" {
			c.Reason += ": " + msg
		}
	}
	return c
}

// splitExit reads launchd's LastExit: an exit code ("1", "78: EX_CONFIG" →
// code "1", "78") or a signal ("Killed: 9" → signal "9 (Killed)").
func splitExit(last string) (code, signal string) {
	name, num, _ := strings.Cut(last, ": ")
	if last != "" && unicode.IsDigit(rune(last[0])) {
		return name, ""
	}
	if num == "" {
		return "", last
	}
	return "", num + " (" + name + ")"
}

// lastFatal is the collector's own reason for exiting: otelcol ends a
// failed start with one "Error: ..." line (a bad config, a port it cannot
// bind — verified against otelcol-contrib 0.161.0). Only one that no
// successful start followed counts; a message ending in ":" continues on
// the next non-empty line (config decoding errors do), which is joined on.
func lastFatal(tail string) string {
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "Everything is ready") {
			return ""
		}
		msg, ok := strings.CutPrefix(lines[i], "Error: ")
		if !ok {
			continue
		}
		msg = strings.TrimSpace(msg)
		if strings.HasSuffix(msg, ":") {
			for _, next := range lines[i+1:] {
				if next = strings.TrimSpace(next); next != "" {
					msg += " " + next
					break
				}
			}
		}
		return msg
	}
	return ""
}

// Trouble is how wrong the collector is right now, for every surface that
// shows it: the menu-bar icon's colour, `compy status`, the web UI.
//
// Level "error" means the collector is not running when it should be —
// crashed and not back yet, crash-looping, or unable to start. "warning" means it runs but something
// a user would want to fix is happening: it is losing data, apps following
// compy's advertisement miss it, or it needs a restart. Log lines are
// deliberately NOT an input: a long-running collector keeps every
// transient error in its log, and real damage shows up here anyway (a
// crash, or the dropped counter rising).
type Trouble struct {
	Level   string   `json:"level"` // "ok", "warning", or "error"
	Reasons []Reason `json:"reasons,omitempty"`
}

// Reason is one cause of a Trouble: a stable Code for surfaces that render
// their own words (the menu bar's short status line), and Text for the
// ones that show it as is.
type Reason struct {
	Code string `json:"code"` // crashed, dropping, ports_mismatch, restart_needed
	Text string `json:"text"`
}

// dropWatch remembers the collector's dropped-items counter between looks,
// so "dropping" means dropping NOW — the counter is cumulative since the
// collector started, and > 0 alone would mean "ever". Per App instance: a
// long-lived tray or UI process builds the history; a one-shot CLI call
// has none, and only reports drops it can attribute (DropDiagnosis).
type dropWatch struct {
	mu   sync.Mutex
	seen bool
	last int64
	rose time.Time
}

// observe records one reading and reports whether the counter rose within
// dropHold. A reading below the last is a collector restart (the counter
// starts over): rebaseline, not a recovery claim.
func (w *dropWatch) observe(v int64, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen && v > w.last {
		w.rose = now
	}
	w.last, w.seen = v, true
	return !w.rose.IsZero() && now.Sub(w.rose) < dropHold
}

// Trouble assesses st (from Status). A running collector costs one scrape
// of its own metrics, for the drop counter.
//
// Every reason's Text is a sentence a user can act on — the menu bar, the
// icon's tooltip, and the web UI's sidebar all show it as is. What a user
// cannot act on stays out, however real: the telemetry port sitting
// somewhere other than the configured one (a busy port compy already fell
// back from, or a collector started before the setting) is a note on the
// collector screen, not a colour (owner ruling 2026-10-02).
func (a *App) Trouble(st Status) Trouble {
	var t Trouble
	if c := st.Crash; c != nil {
		t.Reasons = append(t.Reasons, Reason{"crashed", c.Reason})
	}
	if st.Running {
		h := scrape(st.Listening)
		if h.Available {
			rising := a.drops.observe(h.Dropped, time.Now())
			if vars := dropDiagnosis(true, h.Dropped, a.activeMissing()); len(vars) > 0 {
				t.Reasons = append(t.Reasons, Reason{"dropping", "dropping data: " + strings.Join(vars, ", ") + " not set in the active preset"})
			} else if rising {
				t.Reasons = append(t.Reasons, Reason{"dropping", "dropping data: the collector dropped telemetry in the last 3 minutes"})
			}
		}
		if v := st.Conformance; v != nil && !v.Conforming {
			t.Reasons = append(t.Reasons, Reason{"ports_mismatch", "ports mismatch: apps following compy's settings can't reach this collector"})
		}
		if st.StaleBinary {
			t.Reasons = append(t.Reasons, Reason{"restart_needed", "restart needed: compy was upgraded; restart the collector to run the new version"})
		}
	}
	switch {
	case st.Crash != nil:
		t.Level = "error"
	case len(t.Reasons) > 0:
		t.Level = "warning"
	default:
		t.Level = "ok"
	}
	return t
}
