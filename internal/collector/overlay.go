package collector

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// MetricsPortEnv is the variable the telemetry overlay expands. It joins
// COMPY_GRPC_PORT / COMPY_HTTP_PORT as the third port compy injects, and is
// the only one the user's own config never mentions.
const MetricsPortEnv = "COMPY_METRICS_PORT"

// OverlayYAML is compy's telemetry overlay: the `service::telemetry` block
// that puts the collector's own Prometheus endpoint on a port compy chose.
//
// It is passed as a SEPARATE `--config` source, BEFORE the user's config,
// and that ordering is the whole design:
//
//   - Separate, because compy must never edit a configuration to make its
//     own machinery work. Editing would only ever reach the configs compy
//     renders, leaving hand-written ones — exactly the ones a user cannot
//     have compy fix for them — stuck on a port they cannot move.
//   - Before, because the collector's confmap deep-merges its sources and
//     the LAST one wins a conflict. Overlay first makes this block a
//     DEFAULT: a config carrying its own `service::telemetry` keeps it
//     (verified: user's 17777 wins over the overlay's port), and a config
//     that says nothing gets compy's. Same posture as
//     ${env:COMPY_HTTP_PORT:-14318} inside the shipped configs.
//
// The shape is otelcol 0.159's. The older `metrics: {address: host:port}`
// form is GONE — it fails config decoding — and an empty `readers: []` is
// rejected outright ("collector telemetry metrics reader should exist when
// metric level is not none"), so this reader form is the only way to move
// the port at all.
//
// The value stays an ${env:} reference rather than a baked number so the
// file is written once and the port travels in the LaunchAgent environment,
// like every other compy-injected port.
const OverlayYAML = `# Written by compy. Not a configuration — an overlay passed as an extra
# --config source BEFORE yours, so your configuration keeps whatever it
# says. It exists only to put the collector's own metrics endpoint on a
# port compy picked; set your own service::telemetry to override it.
service:
  telemetry:
    metrics:
      readers:
        - pull:
            exporter:
              prometheus:
                host: 127.0.0.1
                port: ${env:` + MetricsPortEnv + `:-` + defaultMetricsPortStr + `}
`

// defaultMetricsPortStr keeps the overlay's :-fallback and the scrape's
// blind default the same number without a fmt call in a const.
const defaultMetricsPortStr = "8888"

// PortFree reports whether 127.0.0.1:port can be bound right now. It is the
// pre-flight for the telemetry port: the collector's Prometheus reader does
// a plain net.Listen and returns the error, which aborts startup entirely
// (contrib/otelconf metric.go — no retry, no fallback), so a busy port must
// be caught before the collector ever sees it.
//
// Inherently a point-in-time answer: something can take the port between
// this call and the collector's bind. That race leaves the same startup
// failure BindError already recognises, so it degrades to today's behaviour
// rather than to something new.
func PortFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// lsofOnPort asks lsof which processes listen on port, in field format —
// stdout lines like "p1234". A package var so tests can stub the exec.
var lsofOnPort = func(port int) ([]byte, error) {
	return exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fp").Output()
}

// psExe asks for a pid's executable path. Stubbed in tests alongside
// lsofOnPort.
//
// macOS's `ps -o comm=` prints the full executable path, which is what the
// comparison needs. Linux prints the short command name instead, so the
// match simply fails there and the caller takes the cautious branch — the
// collector job is launchd-only anyway.
var psExe = func(pid int) ([]byte, error) {
	return exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
}

// PortHeldBy reports whether the process listening on port is running the
// given binary. It answers one question, on the rare path where the
// telemetry port is busy: "is that just MY collector, the one I am about to
// replace?"
//
// Without it, every re-activation would find its own predecessor holding
// 8888, conclude the port was taken, and drift onto an OS-assigned one — so
// a configured metrics_port would survive exactly one activation.
//
// Deliberately NOT answered by asking launchd for the job's pid: the port
// question has nothing to do with job state, and every launchd consult is a
// real round trip on the activation path. Undetectable is false — the
// cautious answer, which costs an unnecessary fallback rather than a
// collector that refuses to start.
func PortHeldBy(port int, binary string) bool {
	out, err := lsofOnPort(port)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 || line[0] != 'p' {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil || pid <= 0 {
			continue
		}
		exe, err := psExe(pid)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(exe)) == binary {
			return true
		}
	}
	return false
}
