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
	"slices"
)

// Settings holds the persisted collector configuration.
type Settings struct {
	GRPCPort int      `json:"grpc_port"` // default 14317
	HTTPPort int      `json:"http_port"` // default 14318
	Distro   string   `json:"distro"`    // name of selected distro, "" = none
	Enabled  []string `json:"enabled"`   // enabled backend names, kept sorted
	RawMode  bool     `json:"raw_mode"`
	OSEnv    bool     `json:"os_env"` // OS-level env injection active
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

// Dir resolves the compy state directory, creating it (and its
// config/backends, logs, and last-good subdirectories) if needed.
func Dir() (string, error) {
	base := os.Getenv("COMPY_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = baseDir(runtime.GOOS, os.Getenv("XDG_DATA_HOME"), home)
	}
	for _, sub := range []string{filepath.Join("config", "backends"), "logs", "last-good"} {
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
	var v T
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

// SaveSettings writes settings.json atomically, keeping Enabled sorted.
// The caller's Enabled slice is not mutated.
func SaveSettings(s Settings) error {
	s.Enabled = slices.Clone(s.Enabled)
	slices.Sort(s.Enabled)
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
