package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bronto-community/compy/internal/collector"
	"github.com/bronto-community/compy/internal/state"
)

// PortsVerdict answers one question: would an app following compy's
// advertised env (OTEL_EXPORTER_OTLP_ENDPOINT) reach the running collector?
// Conforming rides on the primary port alone — the port the advertised
// protocol's endpoint actually uses (the HTTP port for http/protobuf and
// http/json, the gRPC port for grpc); the other port missing is reported
// (MissingHTTP/MissingGRPC) but never fails the verdict by itself. Actual
// is the detected non-telemetry listeners, what an "adopt" would adopt.
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
// advertised ports. grpcPrimary says the advertised protocol is grpc, making
// the gRPC port the one the verdict rides on. telemetryPort (0 = unknown) is
// excluded from Actual — the collector's own /metrics endpoint is not an
// OTLP candidate.
func portsVerdict(running bool, listening []int, grpcPort, httpPort, telemetryPort int, grpcPrimary bool) *PortsVerdict {
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
	if grpcPrimary {
		v.Conforming = !v.MissingGRPC
	} else {
		v.Conforming = !v.MissingHTTP
	}
	return v
}

// probeHTTPPort classifies a detected port for AdoptPorts; a package var so
// tests can stub the network probe.
var probeHTTPPort = collector.IsHTTPPort

// PortList renders ports as ":6000 :6001" for messages — shared with the
// CLI's status/adopt-ports output.
func PortList(ports []int) string {
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
		grpcP, httpP, err = classifyCandidates(v.Actual, st.Protocol == "grpc")
		if err != nil {
			return err
		}
	} else {
		for _, p := range []*int{grpcP, httpP} {
			if p != nil && !slices.Contains(v.Actual, *p) {
				return state.BadRequest(fmt.Errorf("port %d is not one of this config's detected listeners (%s)", *p, PortList(v.Actual)))
			}
		}
	}
	return a.PutSettings(grpcP, httpP, nil)
}

// classifyCandidates sorts a config's detected non-telemetry listeners into
// the grpc and http slots by probing which of them speak HTTP/1.1. nil for
// a slot means "leave that setting unchanged" (a config with only an http
// receiver has no grpc port to adopt — and, mirrored under a grpc
// advertisement, grpcPrimary lets a single non-HTTP listener adopt as the
// grpc port with no http candidate at all).
func classifyCandidates(cands []int, grpcPrimary bool) (grpcP, httpP *int, err error) {
	if len(cands) == 0 {
		return nil, nil, state.BadRequest(errors.New("nothing to adopt: no non-telemetry listeners detected"))
	}
	if len(cands) > 2 {
		return nil, nil, state.BadRequest(fmt.Errorf("this config listens on more ports than compy advertises (%s) — say which is which, e.g. `compy adopt-ports --grpc %d --http %d`", PortList(cands), cands[0], cands[1]))
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
		// A grpc-only config under a grpc advertisement: exactly one
		// candidate, and it doesn't speak HTTP — adopt it as the grpc port.
		if grpcPrimary && len(httpish) == 0 && len(rest) == 1 {
			return &rest[0], nil, nil
		}
		return nil, nil, state.BadRequest(fmt.Errorf("can't tell which of %s is the otlp/http port — say which is which, e.g. `compy adopt-ports --grpc N --http N`", PortList(cands)))
	}
	httpP = &httpish[0]
	if len(rest) == 1 {
		grpcP = &rest[0]
	}
	return grpcP, httpP, nil
}
