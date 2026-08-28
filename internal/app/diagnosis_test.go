package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/bronto-community/compy/internal/state"
)

// TestDropDiagnosisWiring runs the exported DropDiagnosis end to end: the
// launchd stub reports the test process itself as the running collector,
// real lsof detects the fake /metrics server the test opens in-process, and
// the scrape's dropped counter decides whether the active config's missing
// required vars are blamed. Both directions: drops name the vars, no drops
// stay quiet — the pure rule is TestDropDiagnosisRule's; this is the plumbing.
func TestDropDiagnosisWiring(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not installed")
	}
	adoptSetup(t, true) // launchd: running, pid = this test process

	var dropped atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "otelcol_receiver_accepted_spans 10\notelcol_exporter_send_failed_spans %d\n", dropped.Load())
	}))
	defer srv.Close()

	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// An active config whose yaml requires BRONTO_KEY (no fallback), with no
	// value in any preset.
	if err := a.CreateConfig("mine", "endpoint: ${env:BRONTO_KEY}\n"); err != nil {
		t.Fatal(err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.ActiveConfig = "mine"
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	// No drops yet: missing values alone are the pre-flight's business.
	if got := a.DropDiagnosis(); got != nil {
		t.Errorf("DropDiagnosis with zero drops = %v, want nil", got)
	}

	dropped.Store(5)
	if got := a.DropDiagnosis(); !reflect.DeepEqual(got, []string{"BRONTO_KEY"}) {
		t.Errorf("DropDiagnosis = %v, want [BRONTO_KEY] (running + dropping + missing)", got)
	}
}

// The drop-diagnosis honesty rule: vars are blamed only when running AND
// dropping AND missing all hold — any leg absent means no claim.
func TestDropDiagnosisRule(t *testing.T) {
	missing := []string{"BRONTO_API_KEY"}
	cases := []struct {
		name    string
		running bool
		dropped int64
		missing []string
		want    []string
	}{
		{"missing values and drops names the vars", true, 3, missing, missing},
		{"missing values but no drops stays quiet", true, 0, missing, nil},
		{"drops with all values present blames nothing", true, 3, nil, nil},
		{"stopped collector claims nothing", false, 3, missing, nil},
	}
	for _, c := range cases {
		if got := dropDiagnosis(c.running, c.dropped, c.missing); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: dropDiagnosis(%v, %d, %v) = %v, want %v",
				c.name, c.running, c.dropped, c.missing, got, c.want)
		}
	}
}
