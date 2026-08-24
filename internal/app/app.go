// Package app orchestrates compy's pieces: it turns settings + backend
// fragments into a validated, installed, running collector, and exposes the
// same operations to the CLI, the web UI, and (later) the tray.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/bronto-io/compy/internal/collector"
	"github.com/bronto-io/compy/internal/config"
	"github.com/bronto-io/compy/internal/envvars"
	"github.com/bronto-io/compy/internal/launchd"
	"github.com/bronto-io/compy/internal/state"
	"github.com/bronto-io/compy/internal/webui"
)

// probeTimeout is how long Apply waits for the collector to accept
// connections after kickstart.
const probeTimeout = 5 * time.Second

// App holds the resolved state directory. Settings are re-read per
// operation so concurrent editors (CLI and web UI) never fight over a
// cached copy.
type App struct {
	Dir string
}

// Status is the machine-readable service summary (`compy status --json`).
type Status struct {
	Running  bool     `json:"running"`
	Distro   string   `json:"distro"`
	GRPCPort int      `json:"grpc_port"`
	HTTPPort int      `json:"http_port"`
	Enabled  []string `json:"enabled"`
	RawMode  bool     `json:"raw_mode"`
}

// New resolves the state dir and makes sure config/base.yaml exists.
func New() (*App, error) {
	dir, err := state.Dir()
	if err != nil {
		return nil, err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return nil, err
	}
	if _, err := config.EnsureBase(dir, s); err != nil {
		return nil, err
	}
	return &App{Dir: dir}, nil
}

// LogPath is where the LaunchAgent sends the collector's stdout and stderr.
func (a *App) LogPath() string { return filepath.Join(a.Dir, "logs", "collector.log") }

// RawPath is the raw-mode config the collector runs verbatim.
func (a *App) RawPath() string { return filepath.Join(a.Dir, "config", "custom.yaml") }

// SelectedDistro returns the currently selected distro.
func (a *App) SelectedDistro() (state.Distro, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return state.Distro{}, err
	}
	distros, err := state.LoadDistros()
	if err != nil {
		return state.Distro{}, err
	}
	for _, d := range distros {
		if d.Name == s.Distro {
			return d, nil
		}
	}
	return state.Distro{}, errors.New("no collector distro selected: run `compy distro add <name> <path>`")
}

// Apply validates the current config against the selected distro, snapshots
// the last known-good config if the collector is up, installs and restarts
// the LaunchAgent, and waits for the collector to answer on its gRPC port.
func (a *App) Apply() error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	d, err := a.SelectedDistro()
	if err != nil {
		return err
	}
	args, err := config.Args(a.Dir, s)
	if err != nil {
		return err
	}
	// Returned unchanged: it carries the collector's own diagnostics.
	if err := collector.Validate(d.Path, args); err != nil {
		return err
	}
	if err := launchd.Install(d.Path, args, a.LogPath()); err != nil {
		return err
	}
	if err := launchd.Kickstart(); err != nil {
		return err
	}
	if err := collector.Probe(s.GRPCPort, probeTimeout); err != nil {
		tail, _ := collector.TailLog(a.LogPath(), 20)
		return fmt.Errorf("collector did not come up: %w\n%s\nrun: compy rollback", err, tail)
	}
	// Snapshot only now, with the config proven to actually start. Taking it
	// before install would snapshot the *incoming* config: callers
	// (SetEnabled, SetRawMode, WriteFragment) persist their change before
	// calling Apply, so at that point disk no longer holds the running
	// config, and `compy rollback` would restore the very config that broke.
	return config.SnapshotLastGood(a.Dir)
}

// Rollback restores the last known-good config and applies it.
func (a *App) Rollback() error {
	if err := config.RestoreLastGood(a.Dir); err != nil {
		return err
	}
	return a.Apply()
}

// Validate checks the current config against the selected distro without
// touching the running service.
func (a *App) Validate() error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	d, err := a.SelectedDistro()
	if err != nil {
		return err
	}
	args, err := config.Args(a.Dir, s)
	if err != nil {
		return err
	}
	return collector.Validate(d.Path, args)
}

// Status reports the current service state.
func (a *App) Status() (Status, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return Status{}, err
	}
	// An error here means the job is not loaded, i.e. not running.
	running, _ := launchd.Running()
	if s.Enabled == nil {
		s.Enabled = []string{} // marshal as [], not null
	}
	return Status{
		Running:  running,
		Distro:   s.Distro,
		GRPCPort: s.GRPCPort,
		HTTPPort: s.HTTPPort,
		Enabled:  s.Enabled,
		RawMode:  s.RawMode,
	}, nil
}

// Backends lists every backend fragment on disk with its enabled flag.
func (a *App) Backends() ([]map[string]any, error) {
	names, err := config.ListBackends(a.Dir)
	if err != nil {
		return nil, err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "enabled": slices.Contains(s.Enabled, n)})
	}
	return out, nil
}

// AddBackend renders a preset fragment for a new backend. It does not
// enable it — `compy backend enable` does that.
func (a *App) AddBackend(name, kind, endpoint, apiKey string) error {
	yaml, err := config.Preset(kind, name, endpoint, apiKey)
	if err != nil {
		return err
	}
	return config.WriteBackend(a.Dir, name, yaml)
}

// RemoveBackend deletes a backend's fragment, disabling it first if needed.
func (a *App) RemoveBackend(name string) error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	wasEnabled := slices.Contains(s.Enabled, name)
	if wasEnabled {
		s.Enabled = slices.DeleteFunc(slices.Clone(s.Enabled), func(n string) bool { return n == name })
		if err := state.SaveSettings(s); err != nil {
			return err
		}
	}
	if err := config.DeleteBackend(a.Dir, name); err != nil {
		return err
	}
	if wasEnabled {
		return a.Apply()
	}
	return nil
}

// SetEnabled enables or disables a backend and applies the change.
func (a *App) SetEnabled(name string, enabled bool) error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if enabled {
		if !state.ValidBackendName(name) {
			return fmt.Errorf("invalid backend name %q", name)
		}
		if _, err := os.Stat(config.BackendPath(a.Dir, name)); err != nil {
			return fmt.Errorf("no such backend %q", name)
		}
		if !slices.Contains(s.Enabled, name) {
			s.Enabled = append(slices.Clone(s.Enabled), name)
		}
	} else {
		s.Enabled = slices.DeleteFunc(slices.Clone(s.Enabled), func(n string) bool { return n == name })
	}
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	return a.Apply()
}

// ReadFragment returns a backend's config fragment.
func (a *App) ReadFragment(name string) (string, error) {
	if !state.ValidBackendName(name) {
		return "", fmt.Errorf("invalid backend name %q", name)
	}
	b, err := os.ReadFile(config.BackendPath(a.Dir, name))
	return string(b), err
}

// WriteFragment replaces a backend's config fragment, applying if the
// backend is enabled.
func (a *App) WriteFragment(name, content string) error {
	if err := config.WriteBackend(a.Dir, name, []byte(content)); err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if slices.Contains(s.Enabled, name) {
		return a.Apply()
	}
	return nil
}

// SetRawMode toggles raw mode and applies. Turning it on seeds
// config/custom.yaml from base.yaml if it does not exist yet, so the first
// apply has something valid to run.
func (a *App) SetRawMode(on bool) error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if on {
		if _, err := os.Stat(a.RawPath()); errors.Is(err, os.ErrNotExist) {
			base, err := os.ReadFile(filepath.Join(a.Dir, "config", "base.yaml"))
			if err != nil {
				return err
			}
			if err := state.WriteFileAtomic(a.RawPath(), base, 0o600); err != nil {
				return err
			}
		}
	}
	s.RawMode = on
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	return a.Apply()
}

// ReadRaw returns config/custom.yaml, or "" if it does not exist.
func (a *App) ReadRaw() (string, error) {
	b, err := os.ReadFile(a.RawPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// WriteRaw replaces config/custom.yaml, applying if raw mode is on.
func (a *App) WriteRaw(content string) error {
	if err := state.WriteFileAtomic(a.RawPath(), []byte(content), 0o600); err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if s.RawMode {
		return a.Apply()
	}
	return nil
}

// AddDistro registers a collector binary, selecting it if it is the first.
func (a *App) AddDistro(name, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", abs)
	}
	distros, err := state.LoadDistros()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(distros, func(d state.Distro) bool { return d.Name == name }) {
		return fmt.Errorf("distro %q already exists", name)
	}
	first := len(distros) == 0
	if err := state.SaveDistros(append(distros, state.Distro{Name: name, Path: abs})); err != nil {
		return err
	}
	if first {
		s, err := state.LoadSettings()
		if err != nil {
			return err
		}
		s.Distro = name
		return state.SaveSettings(s)
	}
	return nil
}

// UseDistro selects a registered distro and re-validates against it.
func (a *App) UseDistro(name string) error {
	distros, err := state.LoadDistros()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(distros, func(d state.Distro) bool { return d.Name == name }) {
		return fmt.Errorf("no such distro %q", name)
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	s.Distro = name
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	return a.Apply()
}

// Vars returns the OTEL_* environment variables for the current settings.
func (a *App) Vars() (map[string]string, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return nil, err
	}
	return envvars.Vars(s), nil
}

// SetOSEnv sets or clears the launchd user environment and records it.
func (a *App) SetOSEnv(on bool) error {
	vars, err := a.Vars()
	if err != nil {
		return err
	}
	if on {
		err = envvars.SetOS(vars)
	} else {
		err = envvars.UnsetOS(vars)
	}
	if err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	s.OSEnv = on
	return state.SaveSettings(s)
}

// statusMap is the web UI's view of Status (plus the OTLP endpoint it
// displays).
func (a *App) statusMap() (map[string]any, error) {
	st, err := a.Status()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"running":   st.Running,
		"distro":    st.Distro,
		"grpc_port": st.GRPCPort,
		"http_port": st.HTTPPort,
		"endpoint":  fmt.Sprintf("http://127.0.0.1:%d", st.HTTPPort),
		"enabled":   st.Enabled,
		"raw_mode":  st.RawMode,
	}, nil
}

// WebUIAPI wires the web UI's closures onto App methods.
func (a *App) WebUIAPI() webui.API {
	return webui.API{
		Status:        a.statusMap,
		Backends:      a.Backends,
		AddBackend:    a.AddBackend,
		RemoveBackend: a.RemoveBackend,
		SetEnabled:    a.SetEnabled,
		Apply:         a.Apply,
		Rollback:      a.Rollback,
		ReadFragment:  a.ReadFragment,
		WriteFragment: a.WriteFragment,
		SetRawMode:    a.SetRawMode,
		ReadRaw:       a.ReadRaw,
		WriteRaw:      a.WriteRaw,
		LastError:     func() (string, error) { return collector.TailLog(a.LogPath(), 50) },
	}
}
