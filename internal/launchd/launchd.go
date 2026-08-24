// Package launchd manages the compy collector's macOS LaunchAgent: rendering
// its plist, and installing/uninstalling/kickstarting/inspecting it via
// launchctl.
package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/bronto-io/compy/internal/state"
)

// Label is the launchd job label used for the plist filename and identity.
const Label = "io.bronto.compy.collector"

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
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>{{.LogPath}}</string>
  <key>StandardOutPath</key><string>{{.LogPath}}</string>
</dict></plist>
`))

// PlistPath returns ~/Library/LaunchAgents/<Label>.plist.
func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// RenderPlist renders the LaunchAgent plist for bin run with args, logging
// to logPath. Argv entries and logPath are XML-escaped.
func RenderPlist(bin string, args []string, logPath string) []byte {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, xmlEscape(bin))
	for _, a := range args {
		argv = append(argv, xmlEscape(a))
	}

	var buf bytes.Buffer
	_ = plistTemplate.Execute(&buf, struct {
		Label   string
		Argv    []string
		LogPath string
	}{
		Label:   Label,
		Argv:    argv,
		LogPath: xmlEscape(logPath),
	})
	return buf.Bytes()
}

func guiTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// Install writes the plist and (re)loads it via launchctl: bootout any
// existing job (ignoring errors, since it may not be loaded), then
// bootstrap.
func Install(bin string, args []string, logPath string) error {
	path, err := PlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := state.WriteFileAtomic(path, RenderPlist(bin, args, logPath), 0o644); err != nil {
		return err
	}

	_, _ = Exec("bootout", guiTarget()+"/"+Label) // ignore error: may not be loaded

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

// Uninstall unloads the job (ignoring errors) and removes the plist file.
func Uninstall() error {
	_, _ = Exec("bootout", guiTarget()+"/"+Label) // ignore error: may not be loaded

	path, err := PlistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Kickstart restarts the running (or not) job in place.
func Kickstart() error {
	_, err := Exec("kickstart", "-k", guiTarget()+"/"+Label)
	return err
}

// Running reports whether the job is currently running, per
// `launchctl print gui/<uid>/<Label>`.
func Running() (bool, error) {
	out, err := Exec("print", guiTarget()+"/"+Label)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "state = running" {
			return true, nil
		}
	}
	return false, nil
}
