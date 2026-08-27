// Package app orchestrates compy's pieces: it turns the active
// configuration (plus its preset) into a validated, installed, running
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
	"strings"
	"sync"
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

// download is one collector binary's in-flight (or finished) fetch, as the
// Settings screen renders it: a progress bar while it runs, "download
// failed · <reason>" when it does not.
type download struct {
	status string // "downloading" | "done" | "failed"
	pct    int
	err    string
}

// beginDownload marks name as downloading, reporting false if a fetch for it
// is already in flight (two extracts into one directory is nobody's idea of
// a good time).
func (a *App) beginDownload(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.downloads == nil {
		a.downloads = map[string]download{}
	}
	if a.downloads[name].status == "downloading" {
		return false
	}
	a.downloads[name] = download{status: "downloading"}
	return true
}

// setDownloadProgress records bytes-so-far as a percentage. A server that
// declares no length leaves the bar where it is — an indeterminate strip is
// the UI's problem, not a made-up number's.
func (a *App) setDownloadProgress(name string, done, total int64) {
	if total <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.downloads[name] = download{status: "downloading", pct: int(done * 100 / total)}
}

// endDownload records how the fetch finished.
func (a *App) endDownload(name string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.downloads[name] = download{status: "failed", err: err.Error()}
		return
	}
	a.downloads[name] = download{status: "done", pct: 100}
}

// DownloadProgress reports how name's fetch is going. A name nobody has
// fetched in this process is "idle" — including one that is already on disk,
// which the distro list says separately.
func (a *App) DownloadProgress(name string) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d, ok := a.downloads[name]
	if !ok {
		d = download{status: "idle"}
	}
	out := map[string]any{"status": d.status, "pct": d.pct}
	if d.err != "" {
		out["error"] = d.err
	}
	return out, nil
}

// Status is the machine-readable service summary (`compy status --json`).
type Status struct {
	Running  bool     `json:"running"`
	Distro   string   `json:"distro"`
	GRPCPort int      `json:"grpc_port"`
	HTTPPort int      `json:"http_port"`
	Config   string   `json:"config"`
	Preset   string   `json:"preset"`
	OSEnv    bool     `json:"os_env"`
	Recent   []string `json:"recent"`
	// Listening is the TCP ports the collector process is actually listening
	// on, detected from the OS (launchd's pid + lsof) — never derived from
	// settings or YAML. Empty when stopped or undetectable: no detection
	// means no claim, not a guess.
	Listening []int `json:"listening,omitempty"`
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

// configDetail is the web UI's view of one configuration: its Info plus
// YAML.
func (a *App) configDetail(name string) (any, error) {
	info, yaml, err := a.Config(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"info": info, "yaml": yaml}, nil
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
		preset = info.Meta.ActivePreset
	}
	if preset != "" {
		if _, ok := info.Meta.Presets[preset]; !ok {
			return state.BadRequest(fmt.Errorf("config %q has no preset %q", name, preset))
		}
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

	if preset != "" && preset != info.Meta.ActivePreset {
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
			return fmt.Errorf("%w\nthe previous setup (%s) was restored but did not start either — nothing is running now", failure, still)
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
		Config:    s.ActiveConfig,
		Preset:    preset,
		OSEnv:     s.OSEnv,
		Recent:    s.Recent,
		Listening: listening,
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

// Health reports what the running collector's own telemetry says about the
// data moving through it. It never fails: a stopped or unreachable collector
// answers {"available": false}, which is what the Collector screen shows as
// dashes.
func (a *App) Health() (any, error) {
	// Only our own collector's numbers are ours to show. :8888 is otelcol's
	// default, so a second collector on the machine answers there when ours
	// is stopped — and its throughput is not this one's.
	running, pid, err := launchd.Info()
	if err != nil || !running {
		return collector.Health{}, nil
	}
	// Pid-bound: scrape only the ports the process actually listens on
	// (:8888 first when it is among them); the blind default probe exists
	// only as a fallback when port detection is unavailable.
	return collector.ScrapePorts(collector.ListeningPorts(pid)), nil
}

// Log returns the last n lines of the collector log.
func (a *App) Log(n int) (string, error) { return collector.TailLog(a.LogPath(), n) }

// LogStats counts collector log lines, among the last `lines` lines, whose
// level field is "error" or "warn" — counting only lines since the
// collector's last startup. otelcol logs a "Starting otelcol..." message at
// each boot; counting the whole persisted tail instead would let yesterday's
// errors pin the tray's attention icon (and the status line's counts) on a
// perfectly healthy run today. No startup marker in the window means a
// long-running collector whose marker scrolled out of the tail — those lines
// are all current-session anyway, so the whole window counts. Collector zap
// lines are tab-separated (timestamp, level, caller, message);
// strings.Fields tolerates a space-delimited log the same way. A missing log
// file counts as zero, not an error.
func (a *App) LogStats(lines int) (errors, warnings int, err error) {
	tail, err := collector.TailLog(a.LogPath(), lines)
	if err != nil {
		return 0, 0, err
	}
	all := strings.Split(tail, "\n")
	for i := len(all) - 1; i >= 0; i-- {
		if strings.Contains(all[i], "Starting otelcol") {
			all = all[i+1:]
			break
		}
	}
	for _, line := range all {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[1] {
		case "error":
			errors++
		case "warn":
			warnings++
		}
	}
	return errors, warnings, nil
}

// httpFetch is distro.Fetch over plain HTTP(S), reporting Content-Length as
// the total (-1 when the server declares none); the caller closes the body.
func httpFetch(url string) (io.ReadCloser, int64, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

// fetchFn returns the injected Fetch, or plain HTTP(S).
func (a *App) fetchFn() distro.Fetch {
	if a.Fetch != nil {
		return a.Fetch
	}
	return httpFetch
}

// EnsureDistro resolves a distro name to a collector binary path: "" means
// the global default from settings (or DefaultDistro when none is set), a
// user-registered entry is used as-is, and a shipped definition is
// downloaded (checksum-verified) on first use, reporting bytes to progress
// (nil = a.Progress, if set) as it arrives. A download is also reported to
// the same in-process tracker the Settings screen polls (DownloadProgress),
// so an automatic fetch during activation shows the same progress bar as an
// explicit one.
func (a *App) EnsureDistro(name string, progress distro.Progress) (string, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return "", err
	}
	if name == "" {
		name = effectiveDistro(s)
	}
	user, err := state.LoadDistros()
	if err != nil {
		return "", err
	}
	if i := slices.IndexFunc(user, func(d state.Distro) bool { return d.Name == name }); i >= 0 {
		return user[i].Path, nil
	}
	if name == distro.BundledName {
		p, _ := distro.Bundled()
		if p == "" {
			return "", state.BadRequest(errors.New("the bundled collector is not built — run packaging/collector/build.sh"))
		}
		return p, nil
	}
	for _, d := range distro.Defs() {
		if d.Name == name {
			if !distro.Available(d) {
				return "", state.BadRequest(fmt.Errorf("distro %q has no build for this platform", name))
			}
			if progress == nil && a.Progress != nil {
				progress = func(done, total int64) { a.Progress(name, done, total) }
			}
			// Tracker entries begin lazily, on the first byte: an
			// already-installed binary reports nothing. began stays false
			// when another fetch owns the tracker slot (StartFetchDistro),
			// which then also owns endDownload.
			began := false
			track := func(done, total int64) {
				if !began {
					began = a.beginDownload(name)
				}
				a.setDownloadProgress(name, done, total)
				if progress != nil {
					progress(done, total)
				}
			}
			path, err := distro.EnsureVersion(a.Dir, d, distro.EffectiveVersion(d, s), a.fetchFn(), track)
			if began {
				a.endDownload(name, err)
			}
			return path, err
		}
	}
	return "", state.BadRequest(fmt.Errorf("no such distro %q", name))
}

// StartFetchDistro begins downloading name's collector binary and returns
// at once: a download takes seconds and the Settings screen follows it with
// DownloadProgress rather than holding a request open. A fetch already in
// flight for the same name is left alone. Everything that can go wrong —
// including an unknown name — surfaces through the progress, since the
// request that started it is gone by then.
func (a *App) StartFetchDistro(name string) error {
	if !a.beginDownload(name) {
		return nil
	}
	go func() {
		// EnsureDistro reports the bytes into the tracker itself; this
		// goroutine owns the begin/end so pre-download errors (an unknown
		// name) still land in the progress.
		_, err := a.EnsureDistro(name, nil)
		a.endDownload(name, err)
	}()
	return nil
}

// Distros lists the distro registry: shipped definitions (flagged available
// for this platform and whether they are downloaded) plus user entries.
// "user_entry" distinguishes an actual registry override/custom distro (in
// state.LoadDistros/distros.json — DELETE-able) from a shipped definition
// that's merely been downloaded to its default path but never overridden
// (not DELETE-able: there's no registry entry to remove).
func (a *App) Distros() ([]map[string]any, error) {
	reg, err := distro.Registry(a.Dir)
	if err != nil {
		return nil, err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return nil, err
	}
	userDistros, err := state.LoadDistros()
	if err != nil {
		return nil, err
	}
	isUserEntry := make(map[string]bool, len(userDistros))
	for _, u := range userDistros {
		isUserEntry[u.Name] = true
	}
	defs := map[string]distro.Def{}
	for _, d := range distro.Defs() {
		defs[d.Name] = d
	}
	// Best-effort: an unreadable check-result cache means no availability
	// claim, never a broken listing (the zero value claims nothing).
	chk, _ := state.LoadUpdateCheck()
	selected := effectiveDistro(s)
	out := make([]map[string]any, 0, len(reg))
	for _, d := range reg {
		def, isDef := defs[d.Name]
		bundled := d.Name == distro.BundledName && !isUserEntry[d.Name]
		version := ""
		if isDef {
			version = distro.EffectiveVersion(def, s)
		} else if bundled && d.Path != "" {
			_, version = distro.Bundled()
		}
		row := map[string]any{
			"name":       d.Name,
			"path":       d.Path,
			"version":    version,
			"selected":   d.Name == selected,
			"definition": isDef,
			"bundled":    bundled,
			"available":  !isDef || distro.Available(def),
			"downloaded": d.Path != "",
			"user_entry": isUserEntry[d.Name],
		}
		// Availability rides only on the updatable rows — pinned definitions
		// not overridden by a user entry. The bundled collector updates with
		// compy and user paths are the user's to update; NewerVersion never
		// claims on a malformed/unknown current version.
		if isDef && !isUserEntry[d.Name] {
			if !chk.CheckedAt.IsZero() {
				row["checked_at"] = chk.CheckedAt.Format(time.RFC3339)
			}
			if distro.NewerVersion(chk.Latest, version) {
				row["latest_available"] = chk.Latest
			}
		}
		// A download this process started without the settings screen's
		// help (activation auto-fetching the default) rides along in the
		// row, so the screen's periodic refresh still shows the bar.
		a.mu.Lock()
		if dl, ok := a.downloads[d.Name]; ok && dl.status == "downloading" {
			row["download"] = map[string]any{"status": dl.status, "pct": dl.pct}
		}
		a.mu.Unlock()
		out = append(out, row)
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

// validateDistroBinary resolves path to an absolute path and checks it
// exists and is executable.
func validateDistroBinary(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", abs)
	}
	return abs, nil
}

// selectDistroIfNone makes name the global default distro if none is preset
// yet (first registration).
func selectDistroIfNone(name string) error {
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if s.Distro != "" {
		return nil
	}
	s.Distro = name
	return state.SaveSettings(s)
}

// AddDistro registers a collector binary, selecting it if it is the first.
func (a *App) AddDistro(name, path string) error {
	if !state.ValidBackendName(name) {
		return state.BadRequest(fmt.Errorf("invalid distro name %q: use lowercase letters, digits, dashes", name))
	}
	abs, err := validateDistroBinary(path)
	if err != nil {
		return state.BadRequest(err)
	}
	distros, err := state.LoadDistros()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(distros, func(d state.Distro) bool { return d.Name == name }) {
		return state.BadRequest(fmt.Errorf("distro %q already exists", name))
	}
	if w := distroOverrideWarning(name); w != "" {
		fmt.Fprintf(os.Stderr, "compy: %s\n", w)
	}
	if err := state.SaveDistros(append(distros, state.Distro{Name: name, Path: abs})); err != nil {
		return err
	}
	return selectDistroIfNone(name)
}

// SetDistroPath registers or updates a user distro registry entry's binary
// path (must exist and be executable), selecting it as the default if none
// is set yet. Overriding a shipped definition's name returns the same
// warning AddDistro's stderr line carries, as a response field instead.
func (a *App) SetDistroPath(name, path string) (string, error) {
	if !state.ValidBackendName(name) {
		return "", state.BadRequest(fmt.Errorf("invalid distro name %q: use lowercase letters, digits, dashes", name))
	}
	abs, err := validateDistroBinary(path)
	if err != nil {
		return "", state.BadRequest(err)
	}
	distros, err := state.LoadDistros()
	if err != nil {
		return "", err
	}
	warning := distroOverrideWarning(name)
	if i := slices.IndexFunc(distros, func(d state.Distro) bool { return d.Name == name }); i >= 0 {
		distros[i].Path = abs
		return warning, state.SaveDistros(distros)
	}
	if err := state.SaveDistros(append(distros, state.Distro{Name: name, Path: abs})); err != nil {
		return "", err
	}
	return warning, selectDistroIfNone(name)
}

// RemoveDistro removes a user registry entry. Removing a definition-name
// override "reverts" to the shipped definition (still selectable, and
// downloads on next use); removing an entry with no shipped definition
// drops it from the registry entirely — the response's "reverted" field
// says which happened. It returns a webui.BadRequest-marked error (400) for
// a pure definition name with no user entry (nothing to remove) or for the
// selected distro (pick another default first).
func (a *App) RemoveDistro(name string) (bool, error) {
	s, err := state.LoadSettings()
	if err != nil {
		return false, err
	}
	if s.Distro == name {
		return false, state.BadRequest(fmt.Errorf("distro %q is the selected default; select another distro first", name))
	}
	distros, err := state.LoadDistros()
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(distros, func(d state.Distro) bool { return d.Name == name })
	if i < 0 {
		return false, state.BadRequest(fmt.Errorf("no user distro entry named %q", name))
	}
	distros = slices.Delete(distros, i, i+1)
	if err := state.SaveDistros(distros); err != nil {
		return false, err
	}
	reverted := slices.ContainsFunc(distro.Defs(), func(d distro.Def) bool { return d.Name == name })
	return reverted, nil
}

// UseDistro selects the global default distro, re-applying if a
// configuration is active. The switch sticks when the collector starts, and
// when the active configuration is merely rejected by it (nothing moved); it
// does not stick when the new collector fails to start, because the restore
// that puts the previous configuration back restores settings.json with it.
func (a *App) UseDistro(name string) error {
	if _, err := a.EnsureDistro(name, nil); err != nil {
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
	err = a.Apply()
	if err == nil {
		return nil
	}
	// Whether the switch survived depends on how the apply failed, so say
	// which happened rather than assume.
	//
	// A configuration the collector rejects never reached launchd: nothing
	// moved, the switch stands, and it is a caller mistake (400).
	if state.IsBadRequest(err) {
		// Already marked, so the wrap stays a 400 (IsBadRequest unwraps).
		return fmt.Errorf("default collector is now %q, but the active configuration does not run with it: %w", name, err)
	}
	// A collector that will not start restores the last-good snapshot,
	// settings.json included, which puts the previous collector back — so
	// read the setting rather than claim it stuck.
	if after, lerr := state.LoadSettings(); lerr == nil && after.Distro != name {
		return fmt.Errorf("%q did not start; the collector is still %q: %w", name, effectiveDistro(after), err)
	}
	// Anything else — a plist write, a launchctl refusal — is our fault and
	// keeps both its own message and its 500 (the collector log tail the UI
	// shows there is the diagnostic).
	return err
}

// updatableDef returns the shipped definition behind name when `compy
// distro update` applies to it. The bundled collector and user-managed
// entries are refused with a message the UI shows verbatim.
func (a *App) updatableDef(name string) (distro.Def, error) {
	if name == distro.BundledName {
		return distro.Def{}, state.BadRequest(errors.New("the bundled collector updates with compy releases"))
	}
	user, err := state.LoadDistros()
	if err != nil {
		return distro.Def{}, err
	}
	if slices.ContainsFunc(user, func(d state.Distro) bool { return d.Name == name }) {
		return distro.Def{}, state.BadRequest(fmt.Errorf("distro %q is user-managed — update the binary at its path yourself", name))
	}
	for _, d := range distro.Defs() {
		if d.Name == name {
			if !distro.Available(d) {
				return distro.Def{}, state.BadRequest(fmt.Errorf("distro %q has no build for this platform", name))
			}
			return d, nil
		}
	}
	return distro.Def{}, state.BadRequest(fmt.Errorf("no such distro %q", name))
}

// latestVersion is the one release-check path — the on-demand check and the
// tray's background MaybeCheckUpdates both land here, so every successful
// check records the same distro-updates.json and all surfaces agree. One
// listing covers every pinned distro. A record-write failure doesn't fail
// the check (the caller still gets its answer); it just isn't cached.
func (a *App) latestVersion() (string, error) {
	latest, err := distro.LatestVersion(a.fetchFn())
	if err != nil {
		return "", err
	}
	if err := state.SaveUpdateCheck(state.UpdateCheck{Latest: latest, CheckedAt: time.Now().UTC()}); err != nil {
		fmt.Fprintln(os.Stderr, "compy: record release check:", err)
	}
	return latest, nil
}

// updateCheckInterval is the background release-check cadence. The tray's
// goroutine calls MaybeCheckUpdates more often; it declines until due.
const updateCheckInterval = 12 * time.Hour

// MaybeCheckUpdates runs one upstream release check when the persisted
// result is older than updateCheckInterval — the tray's background job.
// Fully silent on failure (offline, rate-limited): stderr at most, the
// previous result stands with its honest checked_at, the next call retries.
func (a *App) MaybeCheckUpdates() {
	chk, err := state.LoadUpdateCheck()
	if err == nil && time.Since(chk.CheckedAt) < updateCheckInterval {
		return
	}
	if _, err := a.latestVersion(); err != nil {
		fmt.Fprintln(os.Stderr, "compy: release check:", err)
	}
}

// UpdateAvailable reports the newest known upstream release when it is
// newer than some updatable pinned distro's version in effect, "" when none
// is. Read-only — it answers from the persisted check result, never the
// network — so the tray can afford it on its 5s resync.
func (a *App) UpdateAvailable() (string, error) {
	chk, err := state.LoadUpdateCheck()
	if err != nil || chk.Latest == "" {
		return "", err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return "", err
	}
	user, err := state.LoadDistros()
	if err != nil {
		return "", err
	}
	for _, d := range distro.Defs() {
		if slices.ContainsFunc(user, func(u state.Distro) bool { return u.Name == d.Name }) {
			continue // user-managed override: theirs to update
		}
		if distro.Available(d) && distro.NewerVersion(chk.Latest, distro.EffectiveVersion(d, s)) {
			return chk.Latest, nil
		}
	}
	return "", nil
}

// checkDistroUpdate resolves name's definition, its version in effect, and
// the latest upstream release (persisting the check result). A network
// failure or rate limit surfaces as its own error, never as a claim about
// versions.
func (a *App) checkDistroUpdate(name string) (d distro.Def, current, latest string, err error) {
	d, err = a.updatableDef(name)
	if err != nil {
		return d, "", "", err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return d, "", "", err
	}
	current = distro.EffectiveVersion(d, s)
	latest, err = a.latestVersion()
	return d, current, latest, err
}

// CheckDistroUpdate reports name's version in effect and the latest
// upstream release, downloading nothing.
func (a *App) CheckDistroUpdate(name string) (current, latest string, err error) {
	_, current, latest, err = a.checkDistroUpdate(name)
	return current, latest, err
}

// applyDistroUpdate downloads and verifies version alongside the current
// one (versioned dirs — nothing is overwritten), records it as d's version
// in effect, and — when d is the collector in use and launchd reports the
// job running — re-applies so the running collector switches to it. A new
// collector that fails to start rolls the whole update back: distro
// versions live in settings.json, which the last-good restore puts back
// with the rest of the setup. A stopped collector stays stopped.
func (a *App) applyDistroUpdate(d distro.Def, version string, progress distro.Progress) error {
	name := d.Name
	if progress == nil && a.Progress != nil {
		progress = func(done, total int64) { a.Progress(name, done, total) }
	}
	began := false
	track := func(done, total int64) {
		if !began {
			began = a.beginDownload(name)
		}
		a.setDownloadProgress(name, done, total)
		if progress != nil {
			progress(done, total)
		}
	}
	_, err := distro.EnsureVersion(a.Dir, d, version, a.fetchFn(), track)
	if began {
		a.endDownload(name, err)
	}
	if err != nil {
		return err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return err
	}
	if s.DistroVersions == nil {
		s.DistroVersions = map[string]string{}
	}
	s.DistroVersions[name] = version
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	if effectiveDistro(s) != name || s.ActiveConfig == "" {
		return nil
	}
	if running, rerr := launchd.Running(); rerr != nil || !running {
		return nil // stopped stays stopped; the new version runs on next start
	}
	err = a.Apply()
	if err == nil {
		return nil
	}
	// Same honesty as UseDistro: whether the update survived depends on how
	// the apply failed. A rejected configuration never reached launchd (the
	// old binary keeps running until the next restart, which uses version);
	// a collector that would not start was rolled back by the last-good
	// restore, settings.json — version record included — with it.
	if state.IsBadRequest(err) {
		return fmt.Errorf("%s is now %s, but the active configuration does not run with it: %w", name, version, err)
	}
	if after, lerr := state.LoadSettings(); lerr == nil && after.DistroVersions[name] != version {
		return fmt.Errorf("%s %s did not start; the collector is still %s: %w", name, version, distro.EffectiveVersion(d, after), err)
	}
	return err
}

// UpdateDistro is the blocking update the CLI uses: check upstream, and
// when a newer release exists, download, verify, and switch to it.
// updated=false with err=nil means name is already the newest release.
func (a *App) UpdateDistro(name string, progress distro.Progress) (current, latest string, updated bool, err error) {
	d, current, latest, err := a.checkDistroUpdate(name)
	if err != nil || latest == current {
		return current, latest, false, err
	}
	return current, latest, true, a.applyDistroUpdate(d, latest, progress)
}

// StartUpdateDistro is UpdateDistro for the REST surface: the release check
// answers synchronously (started=false with latest==current means nothing
// to do), the download runs in the background reporting to the same tracker
// the fetch progress route polls. An update already in flight is left alone.
func (a *App) StartUpdateDistro(name string) (current, latest string, started bool, err error) {
	d, current, latest, err := a.checkDistroUpdate(name)
	if err != nil {
		return "", "", false, err
	}
	if latest == current {
		return current, latest, false, nil
	}
	if !a.beginDownload(name) {
		return current, latest, true, nil
	}
	go func() {
		err := a.applyDistroUpdate(d, latest, nil)
		a.endDownload(name, err)
	}()
	return current, latest, true, nil
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

// settingsMap is the web UI's view of Settings.
func (a *App) settingsMap() (map[string]any, error) {
	s, err := a.GetSettings()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"grpc_port": s.GRPCPort,
		"http_port": s.HTTPPort,
	}, nil
}

// PutSettings partially updates compy's global settings (nil = unchanged);
// grpcP/httpP must be in 1-65535. Port changes take effect on the next
// Apply/Activate, not immediately.
func (a *App) PutSettings(grpcP, httpP *int) error {
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
	if err := state.SaveSettings(s); err != nil {
		return err
	}
	// With OS-level env on, the OTEL_* values derive from these ports —
	// refresh them so the OS environment doesn't keep pointing at the old
	// endpoint until the toggle is flipped.
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
		"preset":    st.Preset,
		"os_env":    st.OSEnv,
		"recent":    st.Recent,
		"listening": st.Listening,
	}, nil
}

// WebUIAPI wires the web UI's closures onto App methods: the full v2 REST
// surface (docs/superpowers/plans/2026-08-25-compy-v2-p2-rest.md).
func (a *App) WebUIAPI() webui.API {
	return webui.API{
		Status:   a.statusMap,
		Log:      a.Log,
		Env:      a.EnvInfo,
		SetOSEnv: a.SetOSEnv,

		GetSettings: a.settingsMap,
		PutSettings: a.PutSettings,

		Health:       a.Health,
		Apply:        a.Apply,
		Stop:         a.Stop,
		Start:        a.Start,
		Validate:     a.Validate,
		FactoryReset: a.FactoryReset,

		Configs:        func() (any, error) { return a.Configs() },
		CreateConfig:   a.CreateConfig,
		CreateFromURL:  a.CreateFromURL,
		GetConfig:      a.configDetail,
		PutConfigYAML:  a.WriteConfigYAML,
		PutConfigMeta:  a.UpdateConfigMeta,
		DeleteConfig:   a.DeleteConfig,
		CopyConfig:     a.CopyConfig,
		Activate:       a.Activate,
		ValidateConfig: a.ValidateConfig,
		Sync:           a.Sync,
		Resync:         a.Resync,
		Reset:          a.Reset,
		RenameConfig:   a.RenameConfig,
		SyncAll:        a.SyncAll,

		PutPreset:    a.ReplacePreset,
		DeletePreset: a.DeletePreset,
		UsePreset:    a.UsePreset,
		RenamePreset: a.RenamePreset,

		Distros: func() (any, error) { return a.Distros() },
		AddDistro: func(name, path string) (string, error) {
			warning := a.AddDistroWarning(name)
			if err := a.AddDistro(name, path); err != nil {
				return "", err
			}
			return warning, nil
		},
		SetDistroPath:     a.SetDistroPath,
		RemoveDistro:      a.RemoveDistro,
		UseDistro:         a.UseDistro,
		FetchDistro:       a.StartFetchDistro,
		DownloadProgress:  a.DownloadProgress,
		CheckDistroUpdate: a.CheckDistroUpdate,
		UpdateDistro:      a.StartUpdateDistro,
	}
}
