// Package app orchestrates compy's pieces: it turns the active
// configuration (plus its variable set) into a validated, installed, running
// collector, and exposes the same operations to the CLI, the web UI, and the
// tray.
package app

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/bronto-io/compy/internal/cfgstore"
	"github.com/bronto-io/compy/internal/collector"
	"github.com/bronto-io/compy/internal/distro"
	"github.com/bronto-io/compy/internal/envvars"
	"github.com/bronto-io/compy/internal/launchd"
	"github.com/bronto-io/compy/internal/state"
	"github.com/bronto-io/compy/internal/webui"
)

// probeTimeout is how long Activate waits for the collector to accept
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
	Running  bool   `json:"running"`
	Distro   string `json:"distro"`
	GRPCPort int    `json:"grpc_port"`
	HTTPPort int    `json:"http_port"`
	Config   string `json:"config"`
	Set      string `json:"set"`
	OSEnv    bool   `json:"os_env"`
}

// New resolves the state dir, migrates a v1 layout if one is found, and
// materializes the shipped default configurations.
func New() (*App, error) {
	dir, err := state.Dir()
	if err != nil {
		return nil, err
	}
	a := &App{Dir: dir}
	// A failed migration must not brick the CLI: report it and carry on with
	// the legacy tree left in place for a retry on the next run.
	if err := a.migrateLegacy(); err != nil {
		fmt.Fprintln(os.Stderr, "compy: migration failed:", err)
	}
	if err := cfgstore.MaterializeDefaults(dir); err != nil {
		return nil, err
	}
	return a, nil
}

// LogPath is where the LaunchAgent sends the collector's stdout and stderr.
func (a *App) LogPath() string { return filepath.Join(a.Dir, "logs", "collector.log") }

// ConfigPath is a configuration's config.yaml (what the collector runs).
func (a *App) ConfigPath(name string) string {
	return filepath.Join(cfgstore.Dir(a.Dir), name, "config.yaml")
}

// Configs lists every configuration with its provenance, modified state, and
// parsed variables.
func (a *App) Configs() ([]cfgstore.Info, error) { return cfgstore.List(a.Dir) }

// Config returns one configuration's info and YAML.
func (a *App) Config(name string) (cfgstore.Info, string, error) {
	return cfgstore.Get(a.Dir, name)
}

// ActiveConfig returns the active configuration's name and its active
// variable set. Both are "" when nothing has been activated yet.
func (a *App) ActiveConfig() (string, string, error) {
	s, err := state.LoadSettings()
	if err != nil || s.ActiveConfig == "" {
		return "", "", err
	}
	info, _, err := cfgstore.Get(a.Dir, s.ActiveConfig)
	if err != nil {
		return s.ActiveConfig, "", err
	}
	return s.ActiveConfig, info.Meta.ActiveSet, nil
}

// activationEnv is the LaunchAgent's EnvironmentVariables dict: the active
// set's values plus compy's port variables, which the set may not override
// (shipped configs bind their receivers to them).
func activationEnv(values map[string]string, s state.Settings) map[string]string {
	env := maps.Clone(values)
	if env == nil {
		env = map[string]string{}
	}
	env["COMPY_GRPC_PORT"] = strconv.Itoa(s.GRPCPort)
	env["COMPY_HTTP_PORT"] = strconv.Itoa(s.HTTPPort)
	return env
}

// Activate makes name the running configuration: it resolves the distro
// (downloading a shipped definition on first use), validates, installs and
// restarts the LaunchAgent with the set's variables in its environment,
// waits for the collector to answer, and snapshots the result as last-good.
// An empty set keeps the configuration's current active_set.
func (a *App) Activate(name, set string) error {
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	if set == "" {
		set = info.Meta.ActiveSet
	}
	if set != "" {
		if _, ok := info.Meta.VariableSets[set]; !ok {
			return fmt.Errorf("config %q has no variable set %q", name, set)
		}
	}
	bin, err := a.EnsureDistro(info.Meta.Distro)
	if err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	env := activationEnv(info.Meta.VariableSets[set], s)
	args := []string{"--config", a.ConfigPath(name)}

	// Returned unchanged: it carries the collector's own diagnostics.
	if err := collector.Validate(bin, args, env); err != nil {
		return err
	}

	if set != "" && set != info.Meta.ActiveSet {
		if err := cfgstore.UseSet(a.Dir, name, set); err != nil {
			return err
		}
	}
	s.ActiveConfig = name
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	if err := launchd.Install(bin, args, a.LogPath(), env); err != nil {
		return err
	}
	if err := launchd.Kickstart(); err != nil {
		return err
	}
	if err := collector.Probe(s.GRPCPort, probeTimeout); err != nil {
		// v2 configurations own their receivers and may bind nowhere near
		// compy's ports, so a failed probe only means "not listening
		// there" — launchd is the authority on whether the job is up.
		if running, rerr := launchd.Running(); rerr != nil || !running {
			tail, _ := collector.TailLog(a.LogPath(), 20)
			return fmt.Errorf("collector did not come up: %w\n%s\nrun: compy rollback", err, tail)
		}
	}
	// Snapshot only now, with the configuration proven to actually start.
	return cfgstore.SnapshotActive(a.Dir, name)
}

// Apply re-activates the current configuration and set.
func (a *App) Apply() error {
	name, _, err := a.activeName()
	if err != nil {
		return err
	}
	return a.Activate(name, "")
}

// activeName returns the active configuration, erroring if there is none.
func (a *App) activeName() (string, state.Settings, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return "", s, err
	}
	if s.ActiveConfig == "" {
		return "", s, errors.New("no active configuration: run `compy use <config>`")
	}
	return s.ActiveConfig, s, nil
}

// Rollback restores the last known-good configuration + settings and
// re-activates them.
func (a *App) Rollback() error {
	if err := cfgstore.RestoreActive(a.Dir); err != nil {
		return err
	}
	return a.Apply()
}

// Validate checks the active configuration against its distro without
// touching the running service.
func (a *App) Validate() error {
	name, s, err := a.activeName()
	if err != nil {
		return err
	}
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	bin, err := a.EnsureDistro(info.Meta.Distro)
	if err != nil {
		return err
	}
	env := activationEnv(info.Meta.VariableSets[info.Meta.ActiveSet], s)
	return collector.Validate(bin, []string{"--config", a.ConfigPath(name)}, env)
}

// Status reports the current service state.
func (a *App) Status() (Status, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return Status{}, err
	}
	// An error here means the job is not loaded, i.e. not running.
	running, _ := launchd.Running()
	set := ""
	if s.ActiveConfig != "" {
		if info, _, err := cfgstore.Get(a.Dir, s.ActiveConfig); err == nil {
			set = info.Meta.ActiveSet
		}
	}
	return Status{
		Running:  running,
		Distro:   s.Distro,
		GRPCPort: s.GRPCPort,
		HTTPPort: s.HTTPPort,
		Config:   s.ActiveConfig,
		Set:      set,
		OSEnv:    s.OSEnv,
	}, nil
}

// isActive reports whether name is the active configuration.
func (a *App) isActive(name string) bool {
	s, err := state.LoadSettings()
	return err == nil && s.ActiveConfig == name
}

// reactivateIf re-applies when name is the active configuration.
func (a *App) reactivateIf(name string) error {
	if a.isActive(name) {
		return a.Activate(name, "")
	}
	return nil
}

// CreateConfig makes a new local configuration.
func (a *App) CreateConfig(name, yaml string) error { return cfgstore.Create(a.Dir, name, yaml) }

// CreateFromURL creates a configuration from a remote YAML URL.
func (a *App) CreateFromURL(name, url string) error {
	return cfgstore.CreateFromURL(a.Dir, name, url, cfgstore.HTTPFetch)
}

// CopyConfig duplicates a configuration under a new name.
func (a *App) CopyConfig(src, dst string) error { return cfgstore.Copy(a.Dir, src, dst) }

// DeleteConfig removes a configuration. The active one may not be deleted.
func (a *App) DeleteConfig(name string) error {
	if a.isActive(name) {
		return fmt.Errorf("config %q is active; activate another one first", name)
	}
	return cfgstore.Delete(a.Dir, name)
}

// WriteConfigYAML replaces a configuration's YAML, re-activating it if it is
// the running one.
func (a *App) WriteConfigYAML(name, yaml string) error {
	if err := cfgstore.WriteYAML(a.Dir, name, yaml); err != nil {
		return err
	}
	return a.reactivateIf(name)
}

// SetDistro sets a configuration's collector distribution ("" = the global
// default), re-activating if it is the running one.
func (a *App) SetDistro(name, distroName string) error {
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	info.Meta.Distro = distroName
	if err := cfgstore.WriteMeta(a.Dir, name, info.Meta); err != nil {
		return err
	}
	return a.reactivateIf(name)
}

// UpdateConfigMeta partially updates a configuration's distro and/or remote
// URL (nil = unchanged), re-activating if it is the running one. A non-empty
// distro must name an entry in the distro registry.
func (a *App) UpdateConfigMeta(name string, distroP, remoteURLP *string) error {
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	m := info.Meta
	if distroP != nil {
		if *distroP != "" {
			reg, err := distro.Registry(a.Dir)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(reg, func(d state.Distro) bool { return d.Name == *distroP }) {
				return fmt.Errorf("no such distro %q", *distroP)
			}
		}
		m.Distro = *distroP
	}
	if remoteURLP != nil {
		m.RemoteURL = *remoteURLP
	}
	if err := cfgstore.WriteMeta(a.Dir, name, m); err != nil {
		return err
	}
	return a.reactivateIf(name)
}

// Sync refetches a remote configuration (refusing if locally modified).
func (a *App) Sync(name string) error {
	if err := cfgstore.Sync(a.Dir, name, cfgstore.HTTPFetch); err != nil {
		return err
	}
	return a.reactivateIf(name)
}

// Resync refetches a remote configuration, discarding local edits.
func (a *App) Resync(name string) error {
	if err := cfgstore.Resync(a.Dir, name, cfgstore.HTTPFetch); err != nil {
		return err
	}
	return a.reactivateIf(name)
}

// SyncAll syncs every unmodified remote configuration, reporting the names
// it synced.
func (a *App) SyncAll() ([]string, error) {
	infos, err := a.Configs()
	if err != nil {
		return nil, err
	}
	var synced []string
	for _, info := range infos {
		if info.Meta.RemoteURL == "" || info.Modified {
			continue
		}
		if err := a.Sync(info.Name); err != nil {
			return synced, err
		}
		synced = append(synced, info.Name)
	}
	return synced, nil
}

// SetVar sets a variable in a set (creating the set), re-activating if that
// set is the running one.
func (a *App) SetVar(name, set, key, value string) error {
	if err := cfgstore.SetVar(a.Dir, name, set, key, value); err != nil {
		return err
	}
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	if info.Meta.ActiveSet == set {
		return a.reactivateIf(name)
	}
	return nil
}

// ReplaceSet creates or replaces a variable set's entire contents,
// re-activating if the configuration is active and set is its active_set.
func (a *App) ReplaceSet(name, set string, values map[string]string) error {
	if err := cfgstore.WriteSet(a.Dir, name, set, values); err != nil {
		return err
	}
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	if info.Meta.ActiveSet == set {
		return a.reactivateIf(name)
	}
	return nil
}

// UseSet makes set the configuration's active variable set, re-activating if
// the configuration is the running one.
func (a *App) UseSet(name, set string) error {
	if a.isActive(name) {
		return a.Activate(name, set)
	}
	return cfgstore.UseSet(a.Dir, name, set)
}

// DeleteSet removes a variable set (never the active one).
func (a *App) DeleteSet(name, set string) error { return cfgstore.DeleteSet(a.Dir, name, set) }

// httpFetch is distro.Fetch over plain HTTP(S); the caller closes the body.
func httpFetch(url string) (io.ReadCloser, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}

// EnsureDistro resolves a distro name to a collector binary path: "" means
// the global default from settings, a user-registered entry is used as-is,
// and a shipped definition is downloaded (checksum-verified) on first use.
func (a *App) EnsureDistro(name string) (string, error) {
	if name == "" {
		s, err := state.LoadSettings()
		if err != nil {
			return "", err
		}
		name = s.Distro
	}
	if name == "" {
		return "", errors.New("no collector distro selected: run `compy distro use <name>` (or `compy distro add <name> <path>`)")
	}
	user, err := state.LoadDistros()
	if err != nil {
		return "", err
	}
	if i := slices.IndexFunc(user, func(d state.Distro) bool { return d.Name == name }); i >= 0 {
		return user[i].Path, nil
	}
	for _, d := range distro.Defs() {
		if d.Name == name {
			return distro.Ensure(a.Dir, d, httpFetch)
		}
	}
	return "", fmt.Errorf("no such distro %q", name)
}

// Distros lists the distro registry: shipped definitions (flagged available
// for this platform and whether they are downloaded) plus user entries.
func (a *App) Distros() ([]map[string]any, error) {
	reg, err := distro.Registry(a.Dir)
	if err != nil {
		return nil, err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return nil, err
	}
	defs := map[string]distro.Def{}
	for _, d := range distro.Defs() {
		defs[d.Name] = d
	}
	out := make([]map[string]any, 0, len(reg))
	for _, d := range reg {
		def, isDef := defs[d.Name]
		out = append(out, map[string]any{
			"name":       d.Name,
			"path":       d.Path,
			"selected":   d.Name == s.Distro,
			"definition": isDef,
			"available":  !isDef || distro.Available(def),
			"downloaded": d.Path != "",
		})
	}
	return out, nil
}

// distroOverrideWarning returns the warning text AddDistro prints when name
// collides with a shipped distro definition, or "" if it doesn't.
func distroOverrideWarning(name string) string {
	if slices.ContainsFunc(distro.Defs(), func(d distro.Def) bool { return d.Name == name }) {
		return fmt.Sprintf("%q is a shipped distro definition; this path overrides it", name)
	}
	return ""
}

// AddDistroWarning reports the warning AddDistro would print for name (the
// shipped-definition-override text), or "" if none applies. It lets the
// REST surface return the same warning as a response field instead of only
// a stderr line.
func (a *App) AddDistroWarning(name string) string { return distroOverrideWarning(name) }

// AddDistro registers a collector binary, selecting it if it is the first.
func (a *App) AddDistro(name, path string) error {
	if !state.ValidBackendName(name) {
		return fmt.Errorf("invalid distro name %q", name)
	}
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
	if w := distroOverrideWarning(name); w != "" {
		fmt.Fprintf(os.Stderr, "compy: %s\n", w)
	}
	if err := state.SaveDistros(append(distros, state.Distro{Name: name, Path: abs})); err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if s.Distro == "" {
		s.Distro = name
		return state.SaveSettings(s)
	}
	return nil
}

// UseDistro selects the global default distro, re-applying if a
// configuration is active.
func (a *App) UseDistro(name string) error {
	if _, err := a.EnsureDistro(name); err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	s.Distro = name
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	if s.ActiveConfig == "" {
		return nil
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

// EnvInfo returns the OTEL_* environment variables plus their rendering as a
// POSIX sh script (what `compy env` prints).
func (a *App) EnvInfo() (map[string]string, string, error) {
	vars, err := a.Vars()
	if err != nil {
		return nil, "", err
	}
	script, err := envvars.Script(vars, "sh")
	if err != nil {
		return nil, "", err
	}
	return vars, script, nil
}

// GetSettings returns compy's global settings.
func (a *App) GetSettings() (state.Settings, error) { return state.LoadSettings() }

// PutSettings partially updates compy's global settings (nil = unchanged);
// grpcP/httpP must be in 1-65535. Port changes take effect on the next
// Apply/Activate, not immediately.
func (a *App) PutSettings(grpcP, httpP *int, menuSwapP *bool) error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	validPort := func(p int) bool { return p >= 1 && p <= 65535 }
	if grpcP != nil {
		if !validPort(*grpcP) {
			return fmt.Errorf("grpc port %d out of range 1-65535", *grpcP)
		}
		s.GRPCPort = *grpcP
	}
	if httpP != nil {
		if !validPort(*httpP) {
			return fmt.Errorf("http port %d out of range 1-65535", *httpP)
		}
		s.HTTPPort = *httpP
	}
	if menuSwapP != nil {
		s.MenuDistroSwap = *menuSwapP
	}
	return state.SaveSettings(s)
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
		"config":    st.Config,
		"set":       st.Set,
		"os_env":    st.OSEnv,
	}, nil
}

// WebUIAPI wires the web UI's closures onto App methods. The v2 UI is a
// stopgap (P3 rebuilds it): status, the configuration list, and activation.
func (a *App) WebUIAPI() webui.API {
	return webui.API{
		Status:   a.statusMap,
		Configs:  func() (any, error) { return a.Configs() },
		Activate: func(name string) error { return a.Activate(name, "") },
		Log:      func() (string, error) { return collector.TailLog(a.LogPath(), 50) },
	}
}
