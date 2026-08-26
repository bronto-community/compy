package collector

import (
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// lsofListen asks lsof for pid's listening TCP sockets in field format —
// stdout lines like "n127.0.0.1:6000" / "n*:4317". A package var so tests
// can stub the exec. Output (not CombinedOutput): lsof writes mount-table
// warnings to stderr that are not ours to parse or surface.
var lsofListen = func(pid int) ([]byte, error) {
	return exec.Command("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-p", strconv.Itoa(pid), "-Fn").Output()
}

// ListeningPorts reports the TCP ports pid is actually listening on, deduped
// and sorted ascending. Undetectable — lsof absent, failing, or finding
// nothing — is nil, never an error: no detection means no claim on screen,
// not a guess.
func ListeningPorts(pid int) []int {
	if pid <= 0 {
		return nil
	}
	out, err := lsofListen(pid)
	if err != nil {
		return nil
	}
	return parseListenPorts(out)
}

// parseListenPorts pulls the port after the last colon out of every `n…`
// name line of `lsof -Fn` output (the address may be v4, v6, or `*`).
func parseListenPorts(out []byte) []int {
	seen := make(map[int]bool)
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 || line[0] != 'n' {
			continue
		}
		i := strings.LastIndexByte(line, ':')
		if i < 0 {
			continue
		}
		p, err := strconv.Atoi(line[i+1:])
		if err != nil || p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
	}
	slices.Sort(ports)
	return ports
}
