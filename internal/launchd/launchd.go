// Package launchd manages the compy collector's macOS LaunchAgent: rendering
// its plist, and installing/uninstalling/inspecting it via
// launchctl.
package launchd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/bronto-community/compy/internal/state"
)

// Label is the collector job's launchd label, used for the plist filename
// and identity. TrayLabel is the menu-bar tray's job; it runs at load but is
// not kept alive, so quitting from the menu sticks.
const (
	Label     = "io.bronto.compy.collector"
	TrayLabel = "io.bronto.compy.tray"
)

// Exec is the launchctl runner, a package var so tests can stub it.
var Exec = func(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

var plistTemplate = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key><array>
    {{range .Argv}}<string>{{.}}</string>
    {{end}}</array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>{{if .KeepAlive}}<true/>{{else}}<false/>{{end}}
  <key>StandardErrorPath</key><string>{{.LogPath}}</string>
  <key>StandardOutPath</key><string>{{.LogPath}}</string>
  {{if .Env}}<key>EnvironmentVariables</key><dict>
    {{range .Env}}<key>{{.Key}}</key><string>{{.Value}}</string>
    {{end}}</dict>
  {{end}}</dict></plist>
`))

// envPair is a single sorted, XML-escaped EnvironmentVariables entry.
type envPair struct {
	Key   string
	Value string
}

// sortedEnvPairs returns env's entries sorted by key, XML-escaped, or nil if
// env is empty (so the plist omits EnvironmentVariables entirely).
func sortedEnvPairs(env map[string]string) []envPair {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]envPair, len(keys))
	for i, k := range keys {
		pairs[i] = envPair{Key: xmlEscape(k), Value: xmlEscape(env[k])}
	}
	return pairs
}

// agentPlistPath returns ~/Library/LaunchAgents/<label>.plist.
func agentPlistPath(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// renderPlist renders a LaunchAgent plist for bin run with args, logging to
// logPath, with env set as EnvironmentVariables (nil/empty omits the key
// entirely). Argv entries, logPath, and env are XML-escaped.
func renderPlist(label, bin string, args []string, logPath string, keepAlive bool, env map[string]string) []byte {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, xmlEscape(bin))
	for _, a := range args {
		argv = append(argv, xmlEscape(a))
	}

	var buf bytes.Buffer
	_ = plistTemplate.Execute(&buf, struct {
		Label     string
		Argv      []string
		LogPath   string
		KeepAlive bool
		Env       []envPair
	}{
		Label:     label,
		Argv:      argv,
		LogPath:   xmlEscape(logPath),
		KeepAlive: keepAlive,
		Env:       sortedEnvPairs(env),
	})
	return buf.Bytes()
}

func guiTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// Install writes the collector plist and (re)loads it via launchctl.
func Install(bin string, args []string, logPath string, env map[string]string) error {
	return InstallAgent(Label, bin, args, logPath, true, env)
}

// InstallAgent writes the plist for label and (re)loads it via launchctl:
// bootout any existing job (ignoring errors, since it may not be loaded),
// then bootstrap.
func InstallAgent(label, bin string, args []string, logPath string, keepAlive bool, env map[string]string) error {
	path, err := agentPlistPath(label)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 0600: EnvironmentVariables now carries the active variable set, i.e.
	// user API keys. launchd only requires the plist be owned by the user
	// and not group/world writable.
	if err := state.WriteFileAtomic(path, renderPlist(label, bin, args, logPath, keepAlive, env), 0o600); err != nil {
		return err
	}

	_, _ = Exec("bootout", guiTarget()+"/"+label) // ignore error: may not be loaded

	// bootout returns before launchd has finished tearing the job down, and
	// bootstrapping into that window fails with "5: Input/output error" —
	// leaving the collector stopped. Retry briefly (~2s) until it lands.
	var out []byte
	for i := 0; i < 20; i++ {
		if out, err = Exec("bootstrap", guiTarget(), path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
}

// Uninstall unloads the collector job (ignoring errors) and removes its
// plist file.
func Uninstall() error {
	return UninstallAgent(Label)
}

// UninstallAgent unloads the job for label (ignoring errors) and removes its
// plist file.
func UninstallAgent(label string) error {
	_, _ = Exec("bootout", guiTarget()+"/"+label) // ignore error: may not be loaded

	path, err := agentPlistPath(label)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// InstalledBinary returns the collector binary path baked into the
// installed collector plist (ProgramArguments[0]), or "" when no plist is
// installed.
func InstalledBinary() (string, error) {
	path, err := agentPlistPath(Label)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return programArg0(data), nil
}

// programArg0 pulls ProgramArguments[0] out of a rendered plist: the first
// <string> after the ProgramArguments key. Only our own template's output
// needs parsing (stdlib has no plist decoder), and the XML decoder
// un-escapes what renderPlist escaped.
func programArg0(plist []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(plist))
	inArgs := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		t, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case t.Name.Local == "key":
			var key string
			if dec.DecodeElement(&key, &t) == nil {
				inArgs = key == "ProgramArguments"
			}
		case inArgs && t.Name.Local == "string":
			var s string
			if dec.DecodeElement(&s, &t) == nil {
				return s
			}
			return ""
		}
	}
}

// StaleBinary reports whether the installed collector plist points at a
// binary that no longer exists — the state `brew upgrade` leaves behind,
// since activation bakes a resolved path inside the versioned Caskroom
// directory that the upgrade then deletes. No plist (stopped), or the file
// still present: false. The next Start/Apply/Activate re-resolves the
// binary and re-bakes the plist, which heals it.
func StaleBinary() bool {
	bin, err := InstalledBinary()
	if err != nil || bin == "" {
		return false
	}
	_, err = os.Stat(bin)
	return errors.Is(err, fs.ErrNotExist)
}

// Running reports whether the job is currently running, per
// `launchctl print gui/<uid>/<Label>`.
func Running() (bool, error) {
	running, _, err := Info()
	return running, err
}

// Info reports whether the job is currently running and, while it is, its
// pid, both parsed from `launchctl print gui/<uid>/<Label>`. pid is 0 when
// the job is not running or launchd printed no "pid = N" line.
func Info() (running bool, pid int, err error) {
	out, err := Exec("print", guiTarget()+"/"+Label)
	if err != nil {
		return false, 0, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		t := strings.TrimSpace(line)
		if t == "state = running" {
			running = true
		}
		if v, ok := strings.CutPrefix(t, "pid = "); ok {
			pid, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return running, pid, nil
}
