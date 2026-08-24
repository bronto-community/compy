// Package collector runs and probes a local OpenTelemetry Collector binary.
package collector

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

// Validate runs `bin validate <configArgs...>` and returns an error whose
// message contains the process's combined output if it exits non-zero.
func Validate(bin string, configArgs []string) error {
	args := append([]string{"validate"}, configArgs...)
	out, err := exec.Command(bin, args...).CombinedOutput()
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
