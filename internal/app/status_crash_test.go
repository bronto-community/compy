package app_test

import (
	"testing"

	"github.com/bronto-community/compy/internal/app"
)

// TestStatusReportsCrash: launchd printing the job as loaded but failing
// (the crash-loop shape, captured from a real KeepAlive agent) is a crash
// on Status, not a stop — and a job launchd does not know at all (what
// Stop leaves behind) is a plain stop, with no crash.
func TestStatusReportsCrash(t *testing.T) {
	setup(t, "gui/501/io.bronto.compy.collector = {\n\tstate = spawn scheduled\n\truns = 3\n\tlast exit code = 1\n\tendpoints = {\n\t\tstate = active\n\t}\n}\n")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Running || st.Crash == nil || st.Restarts != 2 || st.LastExit != "1" {
		t.Fatalf("Status = running %v, crash %+v, restarts %d, last exit %q; want a crash, 2, \"1\"", st.Running, st.Crash, st.Restarts, st.LastExit)
	}
	if tr := a.Trouble(st); tr.Level != "error" {
		t.Errorf("Trouble(crashed) = %s, want error", tr.Level)
	}

	setup(t, "")
	a, err = app.New()
	if err != nil {
		t.Fatal(err)
	}
	if st, _ = a.Status(); st.Running || st.Crash != nil {
		t.Errorf("unloaded job: Status = running %v, crash %+v; want a plain stop", st.Running, st.Crash)
	}
}

// TestStatusRecoveredCrashIsHistory: launchd restarted the collector and it
// runs again — no crash, no trouble, the restart kept as plain history.
func TestStatusRecoveredCrashIsHistory(t *testing.T) {
	setup(t, "gui/501/io.bronto.compy.collector = {\n\tstate = running\n\truns = 2\n\tpid = 25019\n\tlast terminating signal = Killed: 9\n}\n")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Crash != nil || st.Restarts != 1 || st.LastExit != "Killed: 9" {
		t.Fatalf("Status = running %v, crash %+v, restarts %d, last exit %q; want running, no crash, 1, Killed: 9", st.Running, st.Crash, st.Restarts, st.LastExit)
	}
}
