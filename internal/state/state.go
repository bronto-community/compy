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
	"time"
)

// Settings holds compy's global settings. Unknown fields in settings.json
// (v1's "enabled"/"raw_mode") are ignored on load, and missing fields keep
// their defaults, so a v1 file loads without error.
type Settings struct {
	GRPCPort int `json:"grpc_port"` // default 14317
	HTTPPort int `json:"http_port"` // default 14318

	// MetricsPort is where the collector serves its OWN telemetry (the
	// Prometheus endpoint compy's health strip scrapes; otelcol's own
	// default for it is :8888). Compy supplies it through a config overlay rather than by
	// editing anybody's yaml, so this works for hand-written configs too.
	// 0 means "let the OS pick a free one": nothing can collide, but the
	// port changes every restart, so only pid-based discovery finds it.
	// A missing field (every settings.json written before this existed)
	// loads as 0, which LoadSettings turns into the default instead.
	MetricsPort int `json:"metrics_port"`

	// MetricsLevel is how verbose that telemetry is: basic, normal
	// (default), or detailed. Detailed adds the collector's HTTP server and
	// client instrumentation — per-signal request counts in, and per-backend
	// HTTP STATUS out, which is where a rejected export names its status
	// code instead of burying it in the log. It costs roughly 4.5x the
	// series, bounded by the config rather than by traffic, which is why it
	// is opt-in. Empty means the default.
	MetricsLevel string `json:"metrics_level,omitempty"`
	Distro       string `json:"distro"`        // global default distro, "" = compy's default (contrib)
	ActiveConfig string `json:"active_config"` // active configuration, "" = none
	OSEnv        bool   `json:"os_env"`        // OS-level env injection active

	// Protocol is what the advertised OTLP endpoint speaks: "grpc",
	// "http/protobuf", or "http/json". "" means the default, http/protobuf.
	// It changes only the advertisement (env vars, status, conformance) —
	// the collector's receivers serve all of them regardless.
	Protocol string `json:"protocol,omitempty"`

	// Tracing turns on compy's OWN OpenTelemetry tracing — spans over
	// compy's operations, exported as OTLP. Off by default and free when
	// off: nothing installs a TracerProvider, so the global stays OTel's
	// no-op.
	Tracing bool `json:"tracing,omitempty"`

	// TracingEndpoint is where those spans go. Empty — the default — means
	// compy's own collector on 127.0.0.1:HTTPPort, so compy's telemetry
	// travels the path a user's applications do and lands wherever the
	// active configuration sends it. Set it to reach a backend directly.
	TracingEndpoint string `json:"tracing_endpoint,omitempty"`

	// TracingHeaders is "Name: value" lines sent with each OTLP export —
	// how a hosted backend's API key reaches it. Free text rather than a
	// map so the settings UI can be one field, parsed by tracing.
	TracingHeaders string `json:"tracing_headers,omitempty"`

	// Recent is the most recently activated configurations, newest first.
	// Nothing in compy consumes it today (the menu bar went alphabetical);
	// it stays maintained because /api/status exposes it — a committed part
	// of the REST contract external consumers may order by.
	Recent []string `json:"recent,omitempty"`

	// DistroVersions records pulled-update versions per shipped distro name
	// (`compy distro update`); a missing entry means the pinned version.
	// It lives in settings.json so the last-good snapshot/restore covers it:
	// an update whose collector fails to start rolls back with the rest of
	// the setup.
	DistroVersions map[string]string `json:"distro_versions,omitempty"`
}

// recentCap is how many configurations Recent keeps; plenty of history for
// an ordering nobody scrolls.
const recentCap = 20

// Remember returns recent with name moved to the front, keeping every other
// entry's order and dropping the oldest past the cap. Nothing in compy reads
// the result today — see Settings.Recent for why it is still maintained.
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

// DefaultProtocol is what the advertised endpoint speaks unless settings say
// otherwise.
const DefaultProtocol = "http/protobuf"

// ValidProtocol reports whether p is a protocol the advertisement can speak.
func ValidProtocol(p string) bool {
	return p == "grpc" || p == "http/protobuf" || p == "http/json"
}

// EffectiveProtocol resolves the empty default to http/protobuf.
func (s Settings) EffectiveProtocol() string {
	if s.Protocol == "" {
		return DefaultProtocol
	}
	return s.Protocol
}

// Distro describes a selectable collector distribution.
type Distro struct {
	Name string `json:"name"`
	Path string `json:"path"` // absolute path to collector binary
}

const (
	defaultGRPCPort = 14317
	defaultHTTPPort = 14318
	// defaultMetricsPort follows the same convention as the OTLP ports:
	// the standard port plus 10000. otelcol's own telemetry default is
	// :8888, and 8888 is a popular port — Prometheus examples, other
	// collectors, whatever else is on a developer's machine — so sitting on
	// it by default invites exactly the collision this port became
	// configurable for. :18888 is compy's, the way :14317/:14318 are.
	defaultMetricsPort = 18888
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

// upstreamErr marks an error as an upstream service's failure — the GitHub
// release check timing out, rate-limiting, or answering garbage — rather
// than the caller's mistake (400) or ours (500). The REST layer answers 502
// for a marked error, and the web UI shows its message WITHOUT the
// collector log tail it appends to a 500: the collector has nothing to do
// with an upstream that would not answer.
//
// Like BadRequest it lives here, in the leaf package, and internal/webui
// matches it structurally (an Upstream() bool method).
type upstreamErr struct{ error }

// Upstream reports true, satisfying webui's upstreamer interface.
func (upstreamErr) Upstream() bool { return true }

// Unwrap keeps the marked error reachable through errors.Is/As.
func (e upstreamErr) Unwrap() error { return e.error }

// Upstream marks err as an upstream service's failure, keeping its message
// untouched.
func Upstream(err error) error { return upstreamErr{err} }

// IsUpstream reports whether err, or anything it wraps, was marked.
func IsUpstream(err error) bool {
	var u upstreamErr
	return errors.As(err, &u)
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
	defaults := Settings{GRPCPort: defaultGRPCPort, HTTPPort: defaultHTTPPort, MetricsPort: defaultMetricsPort}
	// loadJSON unmarshals INTO the defaults, so a settings.json written
	// before metrics_port existed picks up the default, while an explicit
	// "metrics_port": 0 means what it says.
	return loadJSON("settings.json", defaults)
}

// SaveSettings writes settings.json atomically.
func SaveSettings(s Settings) error {
	return saveJSON("settings.json", s)
}

// UpdateCheck is the persisted result of the last successful upstream
// release check (distro-updates.json). One release of
// opentelemetry-collector-releases carries every pinned distro's assets, so
// a single latest version covers core, contrib, and otlp alike; per-row
// availability is that version compared against the row's version in
// effect. Reading it is a file read — it never triggers network — and a
// failed check writes nothing, so the previous result (with its honest
// CheckedAt) stands.
type UpdateCheck struct {
	Latest    string    `json:"latest"`
	CheckedAt time.Time `json:"checked_at"`
	// CompyLatest is compy's own newest known release, recorded by the same
	// background check but written independently of the collector fields —
	// either half failing (compy's lookup 404s while the repo is private)
	// leaves the other's record intact. Absent in files written before it
	// existed; those still load (omitempty keeps the shape stable).
	CompyLatest string `json:"compy_latest,omitempty"`
}

// LoadUpdateCheck loads distro-updates.json; a missing file yields the zero
// value (no result yet).
func LoadUpdateCheck() (UpdateCheck, error) {
	return loadJSON("distro-updates.json", UpdateCheck{})
}

// SaveUpdateCheck writes distro-updates.json atomically.
func SaveUpdateCheck(c UpdateCheck) error {
	return saveJSON("distro-updates.json", c)
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
