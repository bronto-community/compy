// Package collector runs and probes a local OpenTelemetry Collector binary.
package collector

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"time"
)

// Validate runs `bin validate <configArgs...>` with env added to the
// process environment — the collector expands ${VAR} references itself, so
// validation must see the same variables the running service will get — and
// returns an error whose message contains the process's combined output if
// it exits non-zero.
func Validate(bin string, configArgs []string, env map[string]string) error {
	args := append([]string{"validate"}, configArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	for _, k := range slices.Sorted(maps.Keys(env)) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, out)
	}
	return nil
}

// Probe retries dialing 127.0.0.1:port until it succeeds or timeout elapses.
func Probe(port int, timeout time.Duration) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("probe %s: %w", addr, lastErr)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// bindErrRE matches the collector's "address already in use" line in both
// shapes it takes in the log: the zap JSON-tail component error
// (`... {"error": "listen tcp 127.0.0.1:16317: bind: address already in use", ...}`)
// and the final plain
// `Error: ... listen tcp 127.0.0.1:8888: bind: address already in use`.
var bindErrRE = regexp.MustCompile(`listen tcp ([^\s:"]+):(\d+): bind: address already in use`)

// BindError scans a log tail for an "address already in use" failure and
// returns a one-line human sentence naming the busy port — the actionable
// fact otherwise buried mid-tail — or "" when the tail has none. The two
// telemetry-port numbers get extra context: a config conflicts with them
// without ever mentioning them (:8888 is otelcol's own default, :18888 is
// where compy's overlay puts it). Compy pre-flights its own port and falls
// back rather than letting this happen, so in practice this fires for a
// config that hardcoded service::telemetry itself.
func BindError(tail string) string {
	m := bindErrRE.FindStringSubmatch(tail)
	if m == nil {
		return ""
	}
	msg := fmt.Sprintf("port %s is already in use by another process", m[2])
	if m[2] == "8888" || m[2] == defaultMetricsPortStr {
		msg += " — :" + m[2] + " is the collector's own telemetry port, which a config uses without naming it; settings' metrics_port moves it"
	}
	return msg
}

// tailReadCap bounds how much of the log file TailLog reads, measured back
// from the end. The collector log is append-only and never rotated, and
// pollers (tray, web UI) call TailLog every few seconds, so scanning the
// whole file on every poll is unbounded work against an ever-growing file;
// a generous fixed window keeps the cost constant regardless of log size.
// Overridable (var, not const) so tests can exercise the boundary without
// writing hundreds of KB of fixture data.
var tailReadCap int64 = 512 * 1024 // ~2000+ lines of zap output

// TailLog returns the last n lines of the file at path, or "" with no error
// if the file does not exist. Only the last tailReadCap bytes of the file
// are read; if that read starts mid-file, the first (partial) line is
// dropped.
func TailLog(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	skipFirst := false
	if info.Size() > tailReadCap {
		start = info.Size() - tailReadCap
		// The read window starts mid-file. The first token the scanner
		// reads is only a partial line — and must be dropped — unless
		// start happens to land exactly on a line boundary, i.e. the byte
		// right before it is a newline.
		prev := make([]byte, 1)
		if _, err := f.ReadAt(prev, start-1); err != nil {
			return "", err
		}
		skipFirst = prev[0] != '\n'
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if skipFirst {
			skipFirst = false
			continue // partial line: started before our read window
		}
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	result := ""
	for _, line := range lines {
		result += line + "\n"
	}
	return result, nil
}
