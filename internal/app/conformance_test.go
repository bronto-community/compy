package app

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/bronto-io/compy/internal/launchd"
	"github.com/bronto-io/compy/internal/state"
)

// TestPortsVerdict is the verdict matrix: conforming, http-miss, grpc-only
// miss, stopped, no detection, telemetry excluded — plus the grpc-primary
// mirror, where the verdict rides the gRPC port instead.
func TestPortsVerdict(t *testing.T) {
	cases := []struct {
		name        string
		running     bool
		listening   []int
		grpcPrimary bool
		want        *PortsVerdict
	}{
		{
			name: "conforming", running: true, listening: []int{14317, 14318, 8888},
			want: &PortsVerdict{Conforming: true, Actual: []int{14317, 14318}},
		},
		{
			name: "http missing warns", running: true, listening: []int{6000, 6001},
			want: &PortsVerdict{Conforming: false, MissingHTTP: true, MissingGRPC: true, Actual: []int{6000, 6001}},
		},
		{
			// The exported endpoint is the HTTP one: grpc missing alone
			// still conforms, but is reported for the softer addendum.
			name: "grpc-only miss still conforms", running: true, listening: []int{14318},
			want: &PortsVerdict{Conforming: true, MissingGRPC: true, Actual: []int{14318}},
		},
		{
			name: "stopped claims nothing", running: false, listening: []int{14317, 14318},
			want: nil,
		},
		{
			name: "no detection claims nothing", running: true, listening: nil,
			want: nil,
		},
		{
			// Telemetry-only listeners: nonconforming, with no candidates.
			name: "telemetry excluded from actual", running: true, listening: []int{8888},
			want: &PortsVerdict{Conforming: false, MissingHTTP: true, MissingGRPC: true},
		},
		{
			// grpc advertised: the verdict rides the gRPC port; the HTTP
			// port missing is only the softer addendum.
			name: "grpc primary: http-only miss still conforms", running: true, listening: []int{14317}, grpcPrimary: true,
			want: &PortsVerdict{Conforming: true, MissingHTTP: true, Actual: []int{14317}},
		},
		{
			// grpc advertised, only the http port bound: apps following the
			// grpc endpoint would miss it — warn.
			name: "grpc primary: grpc missing warns", running: true, listening: []int{14318}, grpcPrimary: true,
			want: &PortsVerdict{Conforming: false, MissingGRPC: true, Actual: []int{14318}},
		},
	}
	for _, c := range cases {
		got := portsVerdict(c.running, c.listening, 14317, 14318, 8888, c.grpcPrimary)
		if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", c.want) {
			t.Errorf("%s: portsVerdict = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// stubProbe makes ports in httpish classify as otlp/http and everything
// else as not-HTTP.
func stubProbe(t *testing.T, httpish ...int) {
	t.Helper()
	orig := probeHTTPPort
	probeHTTPPort = func(port int) bool {
		for _, p := range httpish {
			if p == port {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { probeHTTPPort = orig })
}

func TestClassifyCandidates(t *testing.T) {
	stubProbe(t, 6001, 7001)
	cases := []struct {
		name           string
		cands          []int
		grpcPrimary    bool
		wantGRPC       int // 0 = nil
		wantHTTP       int
		wantErrMention string
	}{
		{name: "one of each", cands: []int{6000, 6001}, wantGRPC: 6000, wantHTTP: 6001},
		{name: "http-only config leaves grpc alone", cands: []int{6001}, wantHTTP: 6001},
		{name: "two http-ish is ambiguous", cands: []int{6001, 7001}, wantErrMention: ":6001 :7001"},
		{name: "zero http-ish is ambiguous", cands: []int{6000, 7000}, wantErrMention: ":6000 :7000"},
		{name: "more candidates than slots", cands: []int{6000, 6001, 7001}, wantErrMention: ":6000 :6001 :7001"},
		{name: "nothing detected", cands: nil, wantErrMention: "nothing to adopt"},
		// Under a grpc advertisement a single non-HTTP listener adopts as
		// the grpc port — the mirror of the http-only case above…
		{name: "grpc primary: grpc-only config leaves http alone", cands: []int{6000}, grpcPrimary: true, wantGRPC: 6000},
		// …but two non-HTTP candidates stay ambiguous, and a lone non-HTTP
		// candidate without the grpc advertisement still refuses.
		{name: "grpc primary: two non-http still ambiguous", cands: []int{6000, 7000}, grpcPrimary: true, wantErrMention: ":6000 :7000"},
		{name: "http primary: lone non-http refuses", cands: []int{6000}, wantErrMention: ":6000"},
	}
	for _, c := range cases {
		g, h, err := classifyCandidates(c.cands, c.grpcPrimary)
		if c.wantErrMention != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErrMention) {
				t.Errorf("%s: err = %v, want mention of %q", c.name, err, c.wantErrMention)
			} else if !state.IsBadRequest(err) {
				t.Errorf("%s: ambiguity must be a 400, got unmarked %v", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		gotG, gotH := 0, 0
		if g != nil {
			gotG = *g
		}
		if h != nil {
			gotH = *h
		}
		if gotG != c.wantGRPC || gotH != c.wantHTTP {
			t.Errorf("%s: classify = grpc %d http %d, want grpc %d http %d", c.name, gotG, gotH, c.wantGRPC, c.wantHTTP)
		}
	}
}

// adoptSetup points the state dirs at temp dirs and stubs launchd to report
// a running job owned by the test process itself — so Status's real
// pid+lsof detection sees the listeners the test opens in-process.
func adoptSetup(t *testing.T, running bool) {
	t.Helper()
	t.Setenv("COMPY_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	print := "state = notrunning"
	if running {
		print = fmt.Sprintf("state = running\n\tpid = %d", os.Getpid())
	}
	orig := launchd.Exec
	launchd.Exec = func(args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "print" {
			return []byte(print), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { launchd.Exec = orig })
}

// listen opens a real listener in the test process, which lsof then detects
// as one of "the collector's" ports.
func listen(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestAdoptPorts(t *testing.T) {
	adoptSetup(t, true)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	grpcPort, httpPort := listen(t), listen(t)
	stubProbe(t, httpPort)

	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Conformance == nil || st.Conformance.Conforming {
		t.Fatalf("Status().Conformance = %+v, want a nonconforming verdict (detection of the test process's listeners)", st.Conformance)
	}

	if err := a.AdoptPorts(nil, nil); err != nil {
		t.Fatalf("AdoptPorts = %v", err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.GRPCPort != grpcPort || s.HTTPPort != httpPort {
		t.Errorf("settings after adopt = grpc %d http %d, want grpc %d http %d", s.GRPCPort, s.HTTPPort, grpcPort, httpPort)
	}
	// The verdict recomputes green.
	st, err = a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Conformance == nil || !st.Conformance.Conforming {
		t.Errorf("Status().Conformance after adopt = %+v, want conforming", st.Conformance)
	}
}

// TestAdoptPortsGRPCPrimary: with the advertised protocol grpc, the verdict
// rides the gRPC port, and a grpc-only config (one non-HTTP listener)
// adopts without needing an http candidate.
func TestAdoptPortsGRPCPrimary(t *testing.T) {
	adoptSetup(t, true)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	proto := "grpc"
	if err := a.PutSettings(nil, nil, &proto); err != nil {
		t.Fatal(err)
	}
	grpcPort := listen(t)
	stubProbe(t) // nothing http-ish

	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Conformance == nil || st.Conformance.Conforming {
		t.Fatalf("Status().Conformance = %+v, want nonconforming (grpc advertised, listener elsewhere)", st.Conformance)
	}

	if err := a.AdoptPorts(nil, nil); err != nil {
		t.Fatalf("AdoptPorts = %v", err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.GRPCPort != grpcPort {
		t.Errorf("settings after adopt = grpc %d, want %d", s.GRPCPort, grpcPort)
	}
	if s.HTTPPort != 14318 {
		t.Errorf("http port changed to %d by a grpc-only adopt, want untouched 14318", s.HTTPPort)
	}
	st, err = a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Conformance == nil || !st.Conformance.Conforming {
		t.Errorf("Status().Conformance after adopt = %+v, want conforming on the grpc port", st.Conformance)
	}
	if !st.Conformance.MissingHTTP {
		t.Errorf("verdict = %+v, want missing_http as the soft addendum", st.Conformance)
	}
}

func TestAdoptPortsAmbiguousRefusesThenExplicitResolves(t *testing.T) {
	adoptSetup(t, true)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := listen(t), listen(t)
	stubProbe(t, p1, p2) // both http-ish: ambiguous

	before, _ := state.LoadSettings()
	err = a.AdoptPorts(nil, nil)
	if err == nil || !state.IsBadRequest(err) {
		t.Fatalf("AdoptPorts(ambiguous) = %v, want a 400 refusal", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf(":%d", p1)) || !strings.Contains(err.Error(), fmt.Sprintf(":%d", p2)) {
		t.Errorf("refusal %q does not name the candidates", err)
	}
	after, _ := state.LoadSettings()
	if after.GRPCPort != before.GRPCPort || after.HTTPPort != before.HTTPPort {
		t.Errorf("a refused adopt changed settings: %+v -> %+v", before, after)
	}

	// Explicit assignment resolves the ambiguity.
	if err := a.AdoptPorts(&p1, &p2); err != nil {
		t.Fatalf("AdoptPorts(explicit) = %v", err)
	}
	s, _ := state.LoadSettings()
	if s.GRPCPort != p1 || s.HTTPPort != p2 {
		t.Errorf("settings = grpc %d http %d, want grpc %d http %d", s.GRPCPort, s.HTTPPort, p1, p2)
	}

	// An explicit port nobody listens on is refused.
	bogus := 1
	if err := a.AdoptPorts(&bogus, nil); err == nil || !state.IsBadRequest(err) {
		t.Errorf("AdoptPorts(bogus explicit) = %v, want a 400", err)
	}
}

func TestAdoptPortsStoppedRefuses(t *testing.T) {
	adoptSetup(t, false)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	err = a.AdoptPorts(nil, nil)
	if err == nil || !state.IsBadRequest(err) || !strings.Contains(err.Error(), "running") {
		t.Fatalf("AdoptPorts while stopped = %v, want a 400 naming the running requirement", err)
	}
}
