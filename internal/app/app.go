// Package app orchestrates compy's pieces: it turns the active
// configuration (plus its preset) into a validated, installed, running
// collector, and exposes the same operations to the CLI, the web UI, and the
// tray.
package app

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/collector"
	"github.com/bronto-community/compy/internal/distro"
	"github.com/bronto-community/compy/internal/envvars"
	"github.com/bronto-community/compy/internal/launchd"
	"github.com/bronto-community/compy/internal/state"
)

// probeTimeout is how long Activate waits for the collector to accept
// connections after kickstart.
const probeTimeout = 5 * time.Second

// DefaultDistro is the collector compy falls back to when settings name
// none and no bundled otelcol-compy sits next to the executable: contrib,
// downloaded (checksum-verified) automatically the first time anything
// needs a collector binary. An explicit settings.Distro — set via
// `compy distro use` or a first `compy distro add` — always wins.
const DefaultDistro = "contrib"

// effectiveDistro is the distro a blank settings.Distro resolves to: the
// bundled otelcol-compy when it is built next to the compy executable, else
// DefaultDistro. An explicit setting always wins.
func effectiveDistro(s state.Settings) string {
	if s.Distro != "" {
		return s.Distro
	}
	if p, _ := distro.Bundled(); p != "" {
		return distro.BundledName
	}
	return DefaultDistro
}

// App holds the resolved state directory. Settings are re-read per
// operation so concurrent editors (CLI and web UI) never fight over a
// cached copy. Download progress is the one thing App does keep: it belongs
// to the process doing the downloading and is meaningless to anyone else.
type App struct {
	Dir string

	// Fetch downloads distro archives; nil means plain HTTP(S). Tests
	// inject one so no test ever pulls a real collector release.
	Fetch distro.Fetch

	// Progress, when set, is EnsureDistro's default download reporter for
	// callers that pass none — the CLI sets it so an automatic download
	// (e.g. `compy use` on a fresh home fetching contrib) prints its
	// percent like `compy distro fetch` does.
	Progress func(name string, done, total int64)

	mu        sync.Mutex
	downloads map[string]download
}

// Status is the machine-readable service summary (`compy status --json`).
type Status struct {
	Running  bool     `json:"running"`
	Distro   string   `json:"distro"`
	GRPCPort int      `json:"grpc_port"`
	HTTPPort int      `json:"http_port"`
	Protocol string   `json:"protocol"` // effective advertised protocol, never ""
	Config   string   `json:"config"`
	Preset   string   `json:"preset"`
	OSEnv    bool     `json:"os_env"`
	Recent   []string `json:"recent"`
	// Listening is the TCP ports the collector process is actually listening
	// on, detected from the OS (launchd's pid + lsof) — never derived from
	// settings or YAML. Empty when stopped or undetectable: no detection
	// means no claim, not a guess.
	Listening []int `json:"listening,omitempty"`
	// Conformance is the ports verdict — whether an app following compy's
	// advertised endpoint would reach this collector. nil when stopped or
	// when detection is unavailable (no detection means no claim).
	Conformance *PortsVerdict `json:"conformance,omitempty"`
}

// EndpointPort is the port the advertised OTLP endpoint uses: the gRPC port
// when the advertised protocol is grpc, the HTTP port for both http flavors.
// The one protocol→port rule — every displayed endpoint derives from it.
func (st Status) EndpointPort() int {
	if st.Protocol == "grpc" {
		return st.GRPCPort
	}
	return st.HTTPPort
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
	// One-time repair for state written before the every-config-has-a-preset
	// invariant: a config with no presets gains the default preset.
	if err := cfgstore.EnsurePresets(dir); err != nil {
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
// preset. Both are "" when nothing has been activated yet.
func (a *App) ActiveConfig() (string, string, error) {
	s, err := state.LoadSettings()
	if err != nil || s.ActiveConfig == "" {
		return "", "", err
	}
	info, _, err := cfgstore.Get(a.Dir, s.ActiveConfig)
	if err != nil {
		return s.ActiveConfig, "", err
	}
	return s.ActiveConfig, info.Meta.ActivePreset, nil
}

// activationEnv is the LaunchAgent's EnvironmentVariables dict: the active
// preset's values plus compy's port variables, which the preset may not override
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

// launch (re)installs the collector LaunchAgent for configuration name with
// preset's variables in its environment, and kickstarts it. It is the part
// of Activate that actually changes what runs, and the part restorePrevious
// replays.
func (a *App) launch(name, preset string) error {
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	bin, err := a.EnsureDistro("", nil)
	if err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	env := activationEnv(info.Meta.Presets[preset], s)
	if err := launchd.Install(bin, []string{"--config", a.ConfigPath(name)}, a.LogPath(), env); err != nil {
		return err
	}
	return launchd.Kickstart()
}

// restorePrevious puts back the last setup that actually started — the
// snapshot's YAML, its preset, and the settings that named it, collector
// binary included — and starts it again, returning what it put back
// ("otlp-to-bronto · staging"). It deliberately does not re-probe: that
// configuration was up moments ago, and a second probe timeout on the
// failure path only delays the diagnostic the user is waiting for.
func (a *App) restorePrevious() (string, error) {
	if err := cfgstore.RestoreActive(a.Dir); err != nil {
		return "", err
	}
	name, preset, err := a.ActiveConfig()
	if err != nil {
		return "", err
	}
	if err := a.launch(name, preset); err != nil {
		return "", err
	}
	if preset == "" {
		return name, nil
	}
	return name + " · " + preset, nil
}

// Activate makes name the running configuration: it resolves the collector
// binary (downloading a shipped definition on first use), validates,
// installs and restarts the LaunchAgent with the preset's variables in its
// environment, and waits for the collector to answer. An empty preset keeps
// the configuration's current active_preset.
//
// A configuration the collector rejects changes nothing. A configuration it
// accepts but cannot start restores the last-good snapshot — the last setup
// that actually started — per the design's guarantee that "on failure the
// previously active configuration keeps running".
//
// The snapshot is taken on success, never before the install. Every
// reactivating caller (WriteConfigYAML, Sync, UseDistro, the preset writes)
// persists the user's intent BEFORE re-activating, so a snapshot taken here
// would capture the very edit that is about to fail and "restore" it.
func (a *App) Activate(name, preset string) error {
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	if preset == "" {
		preset = info.Meta.ActivePreset // always a real preset (EnsurePresets)
	}
	if _, ok := info.Meta.Presets[preset]; !ok {
		return state.BadRequest(fmt.Errorf("config %q has no preset %q", name, preset))
	}
	bin, err := a.EnsureDistro("", nil)
	if err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	env := activationEnv(info.Meta.Presets[preset], s)
	args := []string{"--config", a.ConfigPath(name)}

	// A config the collector rejects is the user's YAML being wrong, not a
	// fault of ours: 400, and the collector's own diagnostics are the whole
	// answer (a log tail from the previous run would only bury them).
	if err := collector.Validate(bin, args, env); err != nil {
		return state.BadRequest(err)
	}

	if preset != info.Meta.ActivePreset {
		if err := cfgstore.UsePreset(a.Dir, name, preset); err != nil {
			return err
		}
	}
	s.ActiveConfig = name
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	if err := a.launch(name, preset); err != nil {
		return err
	}
	// The probe is only the settle/wait: it retries until something answers
	// on compy's gRPC port or the timeout passes. launchd is the authority
	// on "up" in BOTH directions — a foreign process squatting the port
	// answers the dial for a collector that crashed on it, and a
	// configuration owns its receivers and may bind nowhere near compy's
	// ports — so the job counts as started only when launchd confirms it
	// (a launchctl error counts as not-up either way).
	probeErr := collector.Probe(s.GRPCPort, probeTimeout)
	if running, rerr := launchd.Running(); rerr != nil || !running {
		if probeErr == nil {
			probeErr = errors.New("something else answers the probe port, but launchd reports the job is not running")
		}
		tail, _ := collector.TailLog(a.LogPath(), 20)
		failure := fmt.Errorf("collector did not come up: %w\n%s", probeErr, tail)
		// The busy port — not compy's probe port — is the actionable line,
		// and it is otherwise buried in the tail: lead with it.
		if bind := collector.BindError(tail); bind != "" {
			failure = fmt.Errorf("%s\ncollector did not come up: %v\n%s", bind, probeErr, tail)
		}
		if !cfgstore.HasSnapshot(a.Dir) {
			return failure // nothing ever started; nothing to come back to
		}
		still, rerr := a.restorePrevious()
		if rerr != nil {
			return fmt.Errorf("%w\nand restoring the last working setup failed too: %v", failure, rerr)
		}
		// The reassurance is a claim about the world, so make it only
		// when launchd agrees: a restore that itself failed to start
		// must not be reported as "still running" — say it died instead.
		if back, _ := launchd.Running(); !back {
			return fmt.Errorf("%w\nthe previous setup (%s) was restored but did not start either; nothing is running now", failure, still)
		}
		return state.StillRunning(failure, still)
	}
	// Proven to have started: this is the setup a later failure comes back
	// to. Snapshot before remember() so the two writes to settings.json
	// cannot interleave into a snapshot of a half-updated file.
	if err := cfgstore.SnapshotActive(a.Dir, name); err != nil {
		return err
	}
	return a.remember(name)
}

// remember moves name to the front of the recency list. It runs only after
// a successful activation — the menu bar orders by what has actually run.
func (a *App) remember(name string) error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	s.Recent = state.Remember(s.Recent, name)
	return state.SaveSettings(s)
}

// Stop stops the collector by removing its LaunchAgent entirely. Booting the
// job out while leaving the plist behind would only stop it until the next
// login — the plist carries RunAtLoad — and "stopped" has to mean stopped.
// Start reinstalls it. Nothing is recorded: a stopped collector is simply one
// whose job is absent, and the active configuration stays named so the window
// can show it dimmed rather than forget it.
func (a *App) Stop() error { return launchd.Uninstall() }

// Start runs the active configuration again — the same operation as Apply,
// under the word the UI and CLI use for it.
func (a *App) Start() error { return a.Apply() }

// FactoryReset returns compy to its as-installed state, as if it had never
// run: the collector's LaunchAgent is uninstalled (tolerating "was not
// running", like Stop), every entry inside the state directory is deleted —
// configs/, logs/, last-good/, legacy-v1* archives, downloaded collector
// binaries (distros/), settings.json, distros.json — and the first-run path
// runs again (recreate the layout, materialize the shipped defaults). The
// directory itself survives: it may be user-placed or a symlink, so its
// contents are wiped, never the path followed elsewhere. The tray's own
// LaunchAgent is a separate job and stays installed.
func (a *App) FactoryReset() error {
	if err := launchd.Uninstall(); err != nil {
		return err
	}
	entries, err := os.ReadDir(a.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(a.Dir, e.Name())); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.downloads = nil // any recorded download now points at a deleted binary
	a.mu.Unlock()
	// The same first-run path app.New takes: state.Dir recreates the layout,
	// MaterializeDefaults puts the shipped configs back. No settings file
	// means defaults (default ports, no distro, nothing active).
	if _, err := state.Dir(); err != nil {
		return err
	}
	return cfgstore.MaterializeDefaults(a.Dir)
}

// Apply re-activates the current configuration and preset.
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
		return "", s, state.BadRequest(errors.New("no active configuration: run `compy use <config>`"))
	}
	return s.ActiveConfig, s, nil
}

// Validate checks the active configuration against its distro without
// touching the running service.
func (a *App) Validate() error {
	name, _, err := a.activeName()
	if err != nil {
		return err
	}
	return a.ValidateConfig(name)
}

// ValidateConfig checks name's configuration against its own distro, using
// its own active preset's environment — unlike Validate, name need not be the
// active configuration.
func (a *App) ValidateConfig(name string) error {
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	bin, err := a.EnsureDistro("", nil)
	if err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	env := activationEnv(info.Meta.Presets[info.Meta.ActivePreset], s)
	if err := collector.Validate(bin, []string{"--config", a.ConfigPath(name)}, env); err != nil {
		return state.BadRequest(err)
	}
	return nil
}

// Status reports the current service state.
func (a *App) Status() (Status, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return Status{}, err
	}
	// An error here means the job is not loaded, i.e. not running.
	running, pid, _ := launchd.Info()
	preset := ""
	if s.ActiveConfig != "" {
		if info, _, err := cfgstore.Get(a.Dir, s.ActiveConfig); err == nil {
			preset = info.Meta.ActivePreset
		}
	}
	var listening []int
	if running && pid > 0 {
		listening = collector.ListeningPorts(pid)
	}
	return Status{
		Running:   running,
		Distro:    effectiveDistro(s),
		GRPCPort:  s.GRPCPort,
		HTTPPort:  s.HTTPPort,
		Protocol:  s.EffectiveProtocol(),
		Config:    s.ActiveConfig,
		Preset:    preset,
		OSEnv:     s.OSEnv,
		Recent:    s.Recent,
		Listening: listening,
		// The telemetry port (otelcol's :8888 default, health's knowledge)
		// is excluded from the verdict's OTLP candidates. The primary port is
		// whichever the advertised protocol's endpoint uses.
		Conformance: portsVerdict(running, listening, s.GRPCPort, s.HTTPPort, collector.TelemetryPort(), s.EffectiveProtocol() == "grpc"),
	}, nil
}

// isActive reports whether name is the active configuration.
func (a *App) isActive(name string) bool {
	s, err := state.LoadSettings()
	return err == nil && s.ActiveConfig == name
}

// reactivateIf re-applies when name is the active configuration AND the
// collector is running: a stopped collector stays stopped — editing,
// resetting, or resyncing the active config must not start it.
func (a *App) reactivateIf(name string) error {
	if a.isActive(name) {
		if running, _ := launchd.Running(); running {
			return a.Activate(name, "")
		}
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
		return state.BadRequest(fmt.Errorf("config %q is active; activate another one first", name))
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

// WriteConfigYAMLNoValidate replaces a configuration's YAML without asking
// the collector and without touching the running process — the escape hatch
// for "write the yaml first, fill the variable values second", where
// validation cannot pass yet. An unvalidated config must never be
// auto-applied, so the reactivate step is skipped entirely; when name is the
// active configuration and the collector is running, the write still lands
// but the process keeps its previous config until the user restarts or
// activates. runningStale reports exactly that case, so callers can say so.
func (a *App) WriteConfigYAMLNoValidate(name, yaml string) (runningStale bool, err error) {
	if err := cfgstore.WriteYAML(a.Dir, name, yaml); err != nil {
		return false, err
	}
	if a.isActive(name) {
		if running, _ := launchd.Running(); running {
			return true, nil
		}
	}
	return false, nil
}

// UpdateConfigMeta updates a configuration's remote URL (nil = unchanged),
// re-activating if it is the running one. There is no per-config collector
// binary to update: one collector runs every configuration.
func (a *App) UpdateConfigMeta(name string, remoteURLP *string) error {
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	m := info.Meta
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

// Reset restores a modified built-in configuration to its shipped version,
// re-activating it if it is the running one (the builtin twin of Resync).
func (a *App) Reset(name string) error {
	if err := cfgstore.Reset(a.Dir, name); err != nil {
		return err
	}
	return a.reactivateIf(name)
}

// RenameConfig moves a configuration to a new name. The active configuration
// follows the rename in settings (Recent included); if it is also running,
// the LaunchAgent is re-applied so its plist tracks the new config path. A
// stopped collector is left stopped — its plist is already gone, and
// re-applying would kickstart it.
func (a *App) RenameConfig(from, to string) error {
	if err := cfgstore.Rename(a.Dir, from, to); err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	active := s.ActiveConfig == from
	if active {
		s.ActiveConfig = to
	}
	for i, n := range s.Recent {
		if n == from {
			s.Recent[i] = to
		}
	}
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	if active {
		if running, _ := launchd.Running(); running {
			return a.Activate(to, "")
		}
	}
	return nil
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

// SetVar sets a variable in a preset (creating the preset), re-activating if that
// preset is the running one.
func (a *App) SetVar(name, preset, key, value string) error {
	if err := cfgstore.SetVar(a.Dir, name, preset, key, value); err != nil {
		return err
	}
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	if info.Meta.ActivePreset == preset {
		return a.reactivateIf(name)
	}
	return nil
}

// ReplacePreset creates or replaces a preset's entire contents,
// re-activating if the configuration is active and preset is its active_preset.
func (a *App) ReplacePreset(name, preset string, values map[string]string) error {
	if err := cfgstore.WritePreset(a.Dir, name, preset, values); err != nil {
		return err
	}
	info, _, err := cfgstore.Get(a.Dir, name)
	if err != nil {
		return err
	}
	if info.Meta.ActivePreset == preset {
		return a.reactivateIf(name)
	}
	return nil
}

// UsePreset makes preset the configuration's active preset, re-activating if
// the configuration is the running one.
func (a *App) UsePreset(name, preset string) error {
	if a.isActive(name) {
		return a.Activate(name, preset)
	}
	return cfgstore.UsePreset(a.Dir, name, preset)
}

// DeletePreset removes a preset (never the active one).
func (a *App) DeletePreset(name, preset string) error {
	return cfgstore.DeletePreset(a.Dir, name, preset)
}

// RenamePreset renames a preset (the active preset follows the rename
// automatically, in cfgstore).
func (a *App) RenamePreset(name, from, to string) error {
	return cfgstore.RenamePreset(a.Dir, name, from, to)
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
// grpcP/httpP must be in 1-65535, protocol one of grpc, http/protobuf,
// http/json. Port changes take effect on the next Apply/Activate, not
// immediately; a protocol change is advertisement-only and needs no restart.
func (a *App) PutSettings(grpcP, httpP *int, protocol *string) error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	validPort := func(p int) bool { return p >= 1 && p <= 65535 }
	if grpcP != nil {
		if !validPort(*grpcP) {
			return state.BadRequest(fmt.Errorf("grpc port %d out of range 1-65535", *grpcP))
		}
		s.GRPCPort = *grpcP
	}
	if httpP != nil {
		if !validPort(*httpP) {
			return state.BadRequest(fmt.Errorf("http port %d out of range 1-65535", *httpP))
		}
		s.HTTPPort = *httpP
	}
	if protocol != nil {
		if !state.ValidProtocol(*protocol) {
			return state.BadRequest(fmt.Errorf("protocol %q is not one of grpc, http/protobuf, http/json", *protocol))
		}
		s.Protocol = *protocol
	}
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	// With OS-level env on, the OTEL_* values derive from these settings —
	// refresh them so the OS environment doesn't keep pointing at the old
	// endpoint until the toggle is flipped. Overwriting is enough: Vars()
	// keeps one key set for every protocol (locked by
	// TestVarsKeySetConstantAcrossProtocols), so no key can go stale on a
	// protocol switch. If that invariant ever breaks, this refresh must
	// unset the keys the old settings had that the new ones don't.
	if s.OSEnv {
		return envvars.SetOS(envvars.Vars(s))
	}
	return nil
}

// ReapplyOSEnv re-runs the OS-level env injection when the setting is on.
// `launchctl setenv` does not survive a reboot, so something that runs at
// every login (the tray) must call this — otherwise the toggle claims "on"
// over an empty OS environment after a restart.
func (a *App) ReapplyOSEnv() error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if !s.OSEnv {
		return nil
	}
	return envvars.SetOS(envvars.Vars(s))
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
