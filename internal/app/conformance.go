package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bronto-io/compy/internal/collector"
	"github.com/bronto-io/compy/internal/state"
)

// PortsVerdict answers one question: would an app following compy's
// advertised env (OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<http_port>)
// reach the running collector? Conforming rides on the HTTP port alone —
// that is what the exported endpoint uses; a missing gRPC port is reported
// (MissingGRPC) but never fails the verdict by itself. Actual is the
// detected non-telemetry listeners, what an "adopt" would adopt.
//
// A verdict exists only while the collector runs with port detection
// available: stopped or undetectable is nil — no detection, no claim.
type PortsVerdict struct {
	Conforming  bool  `json:"conforming"`
	MissingHTTP bool  `json:"missing_http"`
	MissingGRPC bool  `json:"missing_grpc"`
	Actual      []int `json:"actual,omitempty"`
}

// portsVerdict computes the verdict from detected listeners and the
// advertised ports. telemetryPort (0 = unknown) is excluded from Actual —
// the collector's own /metrics endpoint is not an OTLP candidate.
func portsVerdict(running bool, listening []int, grpcPort, httpPort, telemetryPort int) *PortsVerdict {
	if !running || len(listening) == 0 {
		return nil
	}
	v := &PortsVerdict{MissingHTTP: true, MissingGRPC: true}
	for _, p := range listening {
		if p == httpPort {
			v.MissingHTTP = false
		}
		if p == grpcPort {
			v.MissingGRPC = false
		}
		if p != telemetryPort {
			v.Actual = append(v.Actual, p)
		}
	}
	v.Conforming = !v.MissingHTTP
	return v
}

// probeHTTPPort classifies a detected port for AdoptPorts; a package var so
// tests can stub the network probe.
var probeHTTPPort = collector.IsHTTPPort

// portList renders ports as ":6000 :6001" for messages.
func portList(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprintf(":%d", p)
	}
	return strings.Join(parts, " ")
}

// AdoptPorts points compy's advertised ports at the running config's actual
// OTLP listeners, so the advertisement follows a deliberate config instead
// of stranding every app that trusts it. With both arguments nil the
// detected candidates are classified (otlp/http speaks HTTP/1.1, grpc does
// not); any ambiguity — no or several http-ish candidates, more candidates
// than slots — is refused with the candidates named, never guessed
// silently. Explicit arguments resolve that: each must be one of the
// detected candidates. Applying goes through PutSettings (which refreshes
// OS-level env); shipped configs pick the new ports up on their next
// activation.
func (a *App) AdoptPorts(grpcP, httpP *int) error {
	st, err := a.Status()
	if err != nil {
		return err
	}
	v := st.Conformance
	if v == nil {
		return state.BadRequest(errors.New("no listeners detected: the collector must be running to adopt its ports"))
	}
	if grpcP == nil && httpP == nil {
		grpcP, httpP, err = classifyCandidates(v.Actual)
		if err != nil {
			return err
		}
	} else {
		for _, p := range []*int{grpcP, httpP} {
			if p != nil && !slices.Contains(v.Actual, *p) {
				return state.BadRequest(fmt.Errorf("port %d is not one of this config's detected listeners (%s)", *p, portList(v.Actual)))
			}
		}
	}
	return a.PutSettings(grpcP, httpP)
}

// classifyCandidates sorts a config's detected non-telemetry listeners into
// the grpc and http slots by probing which of them speak HTTP/1.1. nil for
// a slot means "leave that setting unchanged" (a config with only an http
// receiver has no grpc port to adopt).
func classifyCandidates(cands []int) (grpcP, httpP *int, err error) {
	if len(cands) == 0 {
		return nil, nil, state.BadRequest(errors.New("nothing to adopt: no non-telemetry listeners detected"))
	}
	if len(cands) > 2 {
		return nil, nil, state.BadRequest(fmt.Errorf("this config listens on more ports than compy advertises (%s) — say which is which, e.g. `compy adopt-ports --grpc %d --http %d`", portList(cands), cands[0], cands[1]))
	}
	var httpish, rest []int
	for _, p := range cands {
		if probeHTTPPort(p) {
			httpish = append(httpish, p)
		} else {
			rest = append(rest, p)
		}
	}
	if len(httpish) != 1 {
		return nil, nil, state.BadRequest(fmt.Errorf("can't tell which of %s is the otlp/http port — say which is which, e.g. `compy adopt-ports --grpc N --http N`", portList(cands)))
	}
	httpP = &httpish[0]
	if len(rest) == 1 {
		grpcP = &rest[0]
	}
	return grpcP, httpP, nil
}
