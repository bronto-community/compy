package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/compy/internal/collector"
	"github.com/bronto-community/compy/internal/launchd"
)

// stubScrape points scrape at a fixed answer for one test.
func stubScrape(t *testing.T, h collector.Health) {
	t.Helper()
	orig := scrape
	scrape = func([]int) collector.Health { return h }
	t.Cleanup(func() { scrape = orig })
}

// troubleApp is an App over a temp state dir, with the collector log
// holding log.
func troubleApp(t *testing.T, log string) *App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("COMPY_HOME", dir)
	t.Setenv("HOME", t.TempDir()) // launchd reads ~/Library/LaunchAgents: never the real one
	a := &App{Dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.LogPath(), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	return a
}

// TestCrashFor covers each launchd shape compy tells apart (the shapes
// themselves are pinned in launchd's TestParseJob) and the reason it
// names: the busy port from the log, the upgraded-away binary, launchd's
// own "cannot start", or the exit as a last resort. Only DOWN is a crash:
// one launchd restarted that runs again is fine (owner ruling 2026-10-02).
func TestCrashFor(t *testing.T) {
	bindLog := "2026-10-02T10:00:00.000+0200\terror\tx.go:1\tlisten tcp 127.0.0.1:4318: bind: address already in use\n"
	// otelcol-contrib 0.161.0's actual output for an unknown receiver type.
	badConfigLog := "Error: failed to get config: cannot unmarshal the configuration: decoding failed due to the following error(s):\n\n'receivers' unknown type: \"nope\"\n"
	cases := []struct {
		name       string
		job        launchd.Job
		stale      bool
		log        string
		wantReason string // "" = no crash
	}{
		{name: "healthy", job: launchd.Job{State: "running", Runs: 1, PID: 7}},
		{name: "not loaded (stopped by the user)", job: launchd.Job{}},
		{name: "mid-spawn is not a crash", job: launchd.Job{State: "xpcproxy", Runs: 1}},
		{name: "restarted and running again is fine", job: launchd.Job{State: "running", Runs: 2, PID: 7, LastExit: "Killed: 9"}},
		{name: "busy port", job: launchd.Job{State: "spawn scheduled", Runs: 4, LastExit: "1"}, log: bindLog,
			wantReason: "port 4318 is already in use by another process"},
		{name: "stale binary wins over the exit code", job: launchd.Job{State: "spawn scheduled", Runs: 1, LastExit: "78: EX_CONFIG"}, stale: true,
			wantReason: "the collector binary was removed by a compy upgrade; restart the collector"},
		{name: "binary missing, not an upgrade", job: launchd.Job{State: "spawn scheduled", Runs: 1, LastExit: "78: EX_CONFIG"},
			wantReason: "launchd can't start the collector binary (exit code 78)"},
		{name: "exit with the collector's own error", job: launchd.Job{State: "spawn scheduled", Runs: 2, LastExit: "1"}, log: badConfigLog,
			wantReason: `the collector exited with code 1: failed to get config: cannot unmarshal the configuration: decoding failed due to the following error(s): 'receivers' unknown type: "nope"`},
		{name: "exit, an older error since followed by a good start", job: launchd.Job{State: "spawn scheduled", Runs: 2, LastExit: "1"},
			log:        badConfigLog + "2026-10-02T11:00:00.000+0200\tinfo\tservice.go:1\tEverything is ready. Begin running and processing data.\n",
			wantReason: "the collector exited with code 1"},
		{name: "plain exit, nothing logged", job: launchd.Job{State: "spawn scheduled", Runs: 2, LastExit: "1"},
			wantReason: "the collector exited with code 1"},
		{name: "killed: a signal writes nothing, so nothing is promised", job: launchd.Job{State: "spawn scheduled", Runs: 1, LastExit: "Killed: 9"},
			wantReason: "the collector was stopped by signal 9 (Killed)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := troubleApp(t, c.log)
			got := a.crashFor(c.job, c.stale)
			if c.wantReason == "" {
				if got != nil {
					t.Fatalf("crashFor = %+v, want nil", got)
				}
				return
			}
			if got == nil || got.Reason != c.wantReason || got.LastExit != c.job.LastExit {
				t.Fatalf("crashFor = %+v, want reason %q, last exit %q", got, c.wantReason, c.job.LastExit)
			}
		})
	}
}

// TestTroubleLevels: a crash (down) is an error; a crash launchd recovered
// from is history, not trouble; running trouble is a warning; a stopped collector is ok — and the log is
// not an input at all, however many errors it holds.
func TestTroubleLevels(t *testing.T) {
	noisy := strings.Repeat("2026-09-30T17:07:25.850+0200\terror\tq.go:62\tExporting failed. Dropping data.\n", 12)
	a := troubleApp(t, noisy)
	stubScrape(t, collector.Health{Available: true, Port: 18888})

	cases := []struct {
		name      string
		st        Status
		wantLevel string
		wantCodes []string
	}{
		{"healthy, noisy log", Status{Running: true, MetricsPort: 18888}, "ok", nil},
		{"stopped", Status{}, "ok", nil},
		{"crashed", Status{Crash: &Crash{Reason: "r"}}, "error", []string{"crashed"}},
		{"restarted, running again", Status{Running: true, MetricsPort: 18888, Restarts: 1, LastExit: "Killed: 9"}, "ok", nil},
		{"ports mismatch", Status{Running: true, MetricsPort: 18888, Conformance: &PortsVerdict{Conforming: false}}, "warning", []string{"ports_mismatch"}},
		{"restart needed", Status{Running: true, MetricsPort: 18888, StaleBinary: true}, "warning", []string{"restart_needed"}},
		// Real, but nothing for the user to do: a note on the collector
		// screen, not a colour (owner ruling 2026-10-02).
		{"telemetry port elsewhere is not trouble", Status{Running: true, MetricsPort: 19999}, "ok", nil},
		{"stale binary while stopped is not a running warning", Status{StaleBinary: true}, "ok", nil},
	}
	for _, c := range cases {
		tr := a.Trouble(c.st)
		var codes []string
		for _, r := range tr.Reasons {
			codes = append(codes, r.Code)
		}
		if tr.Level != c.wantLevel || strings.Join(codes, ",") != strings.Join(c.wantCodes, ",") {
			t.Errorf("%s: Trouble = %s %v, want %s %v", c.name, tr.Level, codes, c.wantLevel, c.wantCodes)
		}
	}
}

// TestTroubleDropping: "dropping" is the counter RISING, held for dropHold
// — not the cumulative counter being > 0, which is what kept the first
// version of this red for days over one recovered outage.
func TestTroubleDropping(t *testing.T) {
	a := troubleApp(t, "")
	st := Status{Running: true, MetricsPort: 18888}
	dropped := int64(500) // drops from long ago, already in the counter
	origScrape := scrape
	scrape = func([]int) collector.Health {
		return collector.Health{Available: true, Port: 18888, Dropped: dropped}
	}
	t.Cleanup(func() { scrape = origScrape })

	if tr := a.Trouble(st); tr.Level != "ok" {
		t.Fatalf("first look at an old count = %s, want ok (no history, no claim)", tr.Level)
	}
	if tr := a.Trouble(st); tr.Level != "ok" {
		t.Fatalf("unchanged count = %s, want ok", tr.Level)
	}
	dropped = 520
	if tr := a.Trouble(st); tr.Level != "warning" || tr.Reasons[0].Code != "dropping" {
		t.Fatalf("rising count = %+v, want a dropping warning", tr)
	}
	if tr := a.Trouble(st); tr.Level != "warning" {
		t.Fatalf("flat right after a rise = %s, want the warning held", tr.Level)
	}
}

// TestDropWatch pins the hold and the restart rebaseline directly.
func TestDropWatch(t *testing.T) {
	var w dropWatch
	t0 := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	if w.observe(100, t0) {
		t.Fatal("first reading claims a rise")
	}
	if !w.observe(101, t0.Add(5*time.Second)) {
		t.Fatal("rise not seen")
	}
	if !w.observe(101, t0.Add(5*time.Second+dropHold-time.Second)) {
		t.Fatal("rise not held for dropHold")
	}
	if w.observe(101, t0.Add(5*time.Second+dropHold)) {
		t.Fatal("rise held past dropHold")
	}
	// A collector restart starts the counter over: not a rise...
	if w.observe(3, t0.Add(time.Hour)) {
		t.Fatal("counter reset read as a rise")
	}
	// ...and the new baseline counts from there.
	if !w.observe(4, t0.Add(time.Hour+5*time.Second)) {
		t.Fatal("rise after a reset not seen")
	}
}
