// Package state manages compy's on-disk state: settings, distros, and the
// state directory layout.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

// Settings holds compy's global settings. Unknown fields in settings.json
// (v1's "enabled"/"raw_mode") are ignored on load, and missing fields keep
// their defaults, so a v1 file loads without error.
type Settings struct {
	GRPCPort     int    `json:"grpc_port"`     // default 14317
	HTTPPort     int    `json:"http_port"`     // default 14318
	Distro       string `json:"distro"`        // global default distro, "" = compy's default (contrib)
	ActiveConfig string `json:"active_config"` // active configuration, "" = none
	OSEnv        bool   `json:"os_env"`        // OS-level env injection active

	// Recent is the configurations that have run, most recent first. The
	// menu bar orders by it (the window sorts alphabetically everywhere).
	Recent []string `json:"recent,omitempty"`
}

// recentCap is how many configurations Recent keeps. The menu shows ten and
// overflows the rest into More…; twice that is plenty of history for an
// ordering nobody scrolls.
const recentCap = 20

// Remember returns recent with name moved to the front, keeping every other
// entry's order and dropping the oldest past the cap.
func Remember(recent []string, name string) []string {
	out := make([]string, 0, len(recent)+1)
	out = append(out, name)
	for _, n := range recent {
		if n != name {
			out = append(out, n)
		}
	}
	if len(out) > recentCap {
		out = out[:recentCap]
	}
	return out
}

// Distro describes a selectable collector distribution.
type Distro struct {
	Name string `json:"name"`
	Path string `json:"path"` // absolute path to collector binary
}

const (
	defaultGRPCPort = 14317
	defaultHTTPPort = 14318
)

// badRequestErr marks an error as the caller's mistake — a bad name, an
// unknown configuration, a config the collector rejects — rather than a
// failure of ours. The REST layer answers 400 for a marked error and 500
// for everything else, and the web UI staples a collector log tail onto a
// 500 only, so mis-classifying a user mistake buries its own message.
//
// It lives here, in the leaf package everything already imports, so that
// cfgstore and app can mark their errors without importing the HTTP layer
// back. internal/webui matches it structurally (a BadRequest() bool
// method), which keeps that package free of internal dependencies.
type badRequestErr struct{ error }

// BadRequest reports true, satisfying webui's badRequester interface.
func (badRequestErr) BadRequest() bool { return true }

// Unwrap keeps the marked error reachable through errors.Is/As, so marking
// never hides what actually went wrong.
func (e badRequestErr) Unwrap() error { return e.error }

// BadRequest marks err as the caller's mistake, keeping its message
// untouched. Mark only errors that are deterministic given the input: an
// I/O or launchctl failure is ours, and must stay a 500.
func BadRequest(err error) error { return badRequestErr{err} }

// IsBadRequest reports whether err, or anything it wraps, was marked. It
// unwraps rather than type-asserting: callers routinely add context with
// fmt.Errorf("...: %w", err), and a marker that a single %w silently drops
// is a marker you cannot rely on.
func IsBadRequest(err error) bool {
	var b badRequestErr
	return errors.As(err, &b)
}

// stillRunningErr carries what is running instead, when an activation fails
// and the previous configuration is put back. The REST layer copies it into
// the error body as "still_running" so the failure panel can reassure with
// the same words the design asks for ("otlp-to-bronto still running").
//
// Like BadRequest it lives here, in the leaf package, and internal/webui
// matches it structurally (a StillRunning() string method).
type stillRunningErr struct {
	error
	desc string
}

// StillRunning reports what kept running, e.g. "otlp-to-bronto · staging".
func (e stillRunningErr) StillRunning() string { return e.desc }

// Unwrap keeps the marked error reachable through errors.Is/As.
func (e stillRunningErr) Unwrap() error { return e.error }

// StillRunning marks err with the configuration (and preset) that is running
// instead, keeping err's message untouched.
func StillRunning(err error, desc string) error { return stillRunningErr{err, desc} }

var backendNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidBackendName reports whether name is a valid backend name:
// ^[a-z0-9][a-z0-9-]*$ and length <= 64.
func ValidBackendName(name string) bool {
	return len(name) <= 64 && backendNameRE.MatchString(name)
}

// baseDir computes the default (COMPY_HOME-less) state directory for a given
// GOOS/XDG_DATA_HOME/home, factored out so the non-darwin branch is testable
// without build tags.
func baseDir(goos, xdgDataHome, home string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "compy")
	}
	if xdgDataHome == "" {
		xdgDataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(xdgDataHome, "compy")
}

// Dir resolves the compy state directory, creating it (and its configs,
// logs, and last-good subdirectories) if needed. It deliberately does NOT
// create the v1 config/ tree: its presence is what triggers migration.
func Dir() (string, error) {
	base := os.Getenv("COMPY_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = baseDir(runtime.GOOS, os.Getenv("XDG_DATA_HOME"), home)
	}
	for _, sub := range []string{"configs", "logs", "last-good"} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o755); err != nil {
			return "", err
		}
	}
	return base, nil
}

// WriteFileAtomic writes data to path atomically via a temp file + rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func loadJSON[T any](name string, zero T) (T, error) {
	dir, err := Dir()
	if err != nil {
		return zero, err
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	v := zero // fields absent from the file keep their default
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, err
	}
	return v, nil
}

func saveJSON(name string, v any) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(dir, name), data, 0o600)
}

// LoadSettings loads settings.json from the state dir. A missing file
// yields defaults (ports set, rest zero).
func LoadSettings() (Settings, error) {
	defaults := Settings{GRPCPort: defaultGRPCPort, HTTPPort: defaultHTTPPort}
	return loadJSON("settings.json", defaults)
}

// SaveSettings writes settings.json atomically.
func SaveSettings(s Settings) error {
	return saveJSON("settings.json", s)
}

// LoadDistros loads distros.json from the state dir. A missing file yields
// an empty slice.
func LoadDistros() ([]Distro, error) {
	return loadJSON("distros.json", []Distro{})
}

// SaveDistros writes distros.json atomically.
func SaveDistros(d []Distro) error {
	return saveJSON("distros.json", d)
}
