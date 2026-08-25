// Package collector runs and probes a local OpenTelemetry Collector binary.
package collector

import (
	"bufio"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
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

// TailLog returns the last n lines of the file at path, or "" with no error
// if the file does not exist.
func TailLog(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
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
