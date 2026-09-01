// Package cfgstore manages compy's Configurations: on-disk directories
// under configs/<name>/ holding a collector config.yaml + meta.json
// (provenance, presets). See
// docs/superpowers/specs/2026-08-25-compy-v2-configs-design.md.
package cfgstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bronto-community/compy/internal/catalog"
	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/vars"
)

// Meta is the persisted metadata for a configuration (meta.json). There is
// deliberately no per-config collector binary: one collector, chosen once in
// settings, runs every configuration (docs/design/handoff/README.md,
// "Departures"). A "distro" key left in an older meta.json is ignored.
type Meta struct {
	RemoteURL      string `json:"remote_url,omitempty"`
	PristineSHA256 string `json:"pristine_sha256,omitempty"`
	// Presets are the configuration's value bags: one preset holds ALL of a
	// config's values (Amendment 4) — plain env strings for a tier-2 config,
	// typed schema values (options, repeat groups, toggles, secrets) for a
	// tier-3 one. Activation renders the source with the selected preset's
	// bag; only secret-typed values travel via the environment.
	Presets      map[string]map[string]any `json:"presets"`
	ActivePreset string                    `json:"active_preset"`
	// Knobs is the options-era value store (pre-Amendment 4). EnsurePresets
	// merges it into every preset on first load and deletes it; it is never
	// written non-nil again.
	Knobs map[string]any `json:"knobs,omitempty"`
	// Template is a legacy wizard-era marker ("created from this catalog
	// template"). The corrected model has no such object — a config OWNS its
	// source — so EnsurePresets strips it (with the wizard's pristine hash
	// and knobs) on first load; it is never written non-empty again.
	Template string `json:"template,omitempty"`
}

// Info describes a configuration for listing/inspection.
type Info struct {
	Name       string `json:"name"`
	Provenance string `json:"provenance"` // "shipped" | "remote" | "local"
	Modified   bool   `json:"modified"`   // hash != pristine (always false for "local")
	// HasTemplate marks a tier-3 configuration: configs/<name>/config.tmpl
	// exists — the SOURCE the rendered config.yaml derives from.
	HasTemplate bool       `json:"has_template,omitempty"`
	Meta        Meta       `json:"meta"`
	Vars        []vars.Var `json:"vars"`
}

// MissingRequired names the values an activation of this config with this
// preset would leave unfilled. Tier 2: variables with no `${VAR:-default}`
// fallback in the yaml, not compy-injected (COMPY_*), and empty or absent
// in the preset. Tier 3 (a template source exists): schema-required fields
// — secrets included, non-secrets too — absent or blank in the preset's
// bag, named as field paths ("backends[0].api_key"), plus this preset's
// FREE vars (hand-written ${env:} refs in its render) under the tier-2
// rule, named by var name. An empty preset name
// resolves to the config's active preset, exactly as Activate does — and
// every config has one (EnsurePresets). Callers decide what to do with the
// answer: the window asks before activating, the CLI warns and proceeds.
// KEEP IN LOCKSTEP with missingRequired / missingRequiredT3 in
// webui/static/helpers.js — the web client's pre-flight mirror (its
// free-var list is the config detail's free_vars[preset]).
func MissingRequired(root string, info Info, preset string) []string {
	if preset == "" {
		preset = info.Meta.ActivePreset
	}
	values := info.Meta.Presets[preset]
	if src, ok := readSource(root, info.Name); ok {
		t, err := catalog.ParseSource(src)
		if err != nil {
			return nil // an unparseable source can't say; activation will
		}
		missing := t.MissingRequired(values)
		// Free vars (tier 2 inside tier 3): a hand-written ${env:VAR} in
		// this preset's render with no :-default and no bag value is missing
		// exactly as a tier-2 var is. Lenient on render failure — the
		// schema's own missing fields above are already the answer.
		if rendered, rerr := t.Render(t.PruneUnknown(values), StorageDir(root)); rerr == nil {
			for _, v := range t.FreeVars(rendered, values) {
				if v.HasDefault {
					continue
				}
				if s, isStr := values[v.Name].(string); values[v.Name] == nil || (isStr && strings.TrimSpace(s) == "") {
					missing = append(missing, v.Name)
				}
			}
		}
		return missing
	}
	var missing []string
	for _, v := range info.Vars {
		if v.HasDefault || strings.HasPrefix(v.Name, "COMPY_") {
			continue
		}
		// Non-string values (a demoted tier-3 bag's leftovers) count as set:
		// no claim is better than a wrong one.
		if s, isStr := values[v.Name].(string); values[v.Name] == nil || (isStr && strings.TrimSpace(s) == "") {
			missing = append(missing, v.Name)
		}
	}
	return missing
}

// Fetch retrieves the bytes at url. Production code should use HTTPFetch;
// tests inject a fake.
type Fetch func(url string) ([]byte, error)

// HTTPFetch is the production Fetch: a plain HTTP(S) GET with a 30s timeout
// and a 5MB response cap.
func HTTPFetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	const limit = 5 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("fetch %s: response exceeds %d byte limit", url, limit)
	}
	return data, nil
}

// Dir returns the configs directory under root.
func Dir(root string) string {
	return filepath.Join(root, "configs")
}

func configDir(root, name string) string {
	return filepath.Join(Dir(root), name)
}

func yamlPath(root, name string) string {
	return filepath.Join(configDir(root, name), "config.yaml")
}

// SourcePath is a tier-3 configuration's template source. config.yaml stays
// the rendered output — the collector path, the plist, and the vars cards
// never learn templates exist.
func SourcePath(root, name string) string {
	return filepath.Join(configDir(root, name), "config.tmpl")
}

// StorageDir is where a rendered offline queue keeps its file_storage
// state: inside the state directory, next to configs/ and logs/. It bakes
// into rendered YAML as a literal — the config stays plain.
func StorageDir(root string) string {
	return filepath.Join(root, "storage")
}

// readSource returns a configuration's template source, reporting whether
// one exists (tier 3).
func readSource(root, name string) (string, bool) {
	data, err := os.ReadFile(SourcePath(root, name))
	if err != nil {
		return "", false
	}
	return string(data), true
}

func metaPath(root, name string) string {
	return filepath.Join(configDir(root, name), "meta.json")
}

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func provenanceFor(m Meta) string {
	if m.PristineSHA256 == "" {
		return "local"
	}
	if m.RemoteURL != "" {
		return "remote"
	}
	return "shipped"
}

func modifiedFor(m Meta, currentYAML string) bool {
	if m.PristineSHA256 == "" {
		return false
	}
	return hashOf(currentYAML) != m.PristineSHA256
}

func exists(root, name string) bool {
	_, err := os.Stat(yamlPath(root, name))
	return err == nil
}

func readYAML(root, name string) (string, error) {
	data, err := os.ReadFile(yamlPath(root, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", userErrf("config %q not found", name)
		}
		return "", err
	}
	return string(data), nil
}

// readMeta reads meta.json, accepting v2's key names for presets
// ("variable_sets"/"active_set") so an existing state directory keeps its
// presets across the rename. The next write emits the new names.
func readMeta(root, name string) (Meta, error) {
	data, err := os.ReadFile(metaPath(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return Meta{Presets: map[string]map[string]any{}}, nil
	}
	if err != nil {
		return Meta{}, err
	}
	var raw struct {
		Meta
		LegacyPresets map[string]map[string]any `json:"variable_sets"`
		LegacyActive  string                    `json:"active_set"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Meta{}, err
	}
	m := raw.Meta
	if m.Presets == nil {
		m.Presets = raw.LegacyPresets
	}
	if m.ActivePreset == "" {
		m.ActivePreset = raw.LegacyActive
	}
	if m.Presets == nil {
		m.Presets = map[string]map[string]any{}
	}
	return m, nil
}

// DefaultPreset is the preset every configuration starts with. Its empty
// values make activation behave exactly as a preset-less config once did:
// no variables in the environment beyond compy's own COMPY_* ports.
const DefaultPreset = "default"

// ensureDefaultPreset enforces the invariant that a configuration always
// has at least one preset and an active_preset naming one of them: an empty
// preset map gains {"default": {}}, and an active_preset naming no existing
// preset is repointed ("default" when present, else the first name in
// sorted order — deterministic). Reports whether m changed.
func ensureDefaultPreset(m *Meta) bool {
	changed := false
	if len(m.Presets) == 0 {
		m.Presets = map[string]map[string]any{DefaultPreset: {}}
		changed = true
	}
	if _, ok := m.Presets[m.ActivePreset]; !ok {
		if _, ok := m.Presets[DefaultPreset]; ok {
			m.ActivePreset = DefaultPreset
		} else {
			m.ActivePreset = slices.Min(slices.Collect(maps.Keys(m.Presets)))
		}
		changed = true
	}
	return changed
}

// withDefaultPreset is ensureDefaultPreset for the meta a creation path is
// about to write.
func withDefaultPreset(m Meta) Meta {
	ensureDefaultPreset(&m)
	return m
}

// EnsurePresets backfills the every-config-has-a-preset invariant onto
// configs written before it existed (app.New runs it once per start): any
// config on disk with no presets gets presets {"default": {}} and
// active_preset "default". It is also the Amendment 4 migration: a tier-3
// config's options-era `knobs` merge into every preset's bag (existing
// preset values win — they are newer) and the knobs key is deleted.
// Deterministic and idempotent; a repair, not an event, so nothing is
// logged.
func EnsurePresets(root string) error {
	entries, err := os.ReadDir(Dir(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !exists(root, e.Name()) {
			continue
		}
		m, err := readMeta(root, e.Name())
		if err != nil {
			return err
		}
		changed := ensureDefaultPreset(&m)
		// Wizard-era template-born configs hold rendered plain YAML — they
		// simply ARE tier 1/2 configs now. Strip the dead marker (and the
		// wizard's pristine hash + knobs; nothing re-renders them) so no UI
		// shows dead affordances. Knobs without a source file are strays
		// either way: only config.tmpl makes them meaningful.
		if _, hasSrc := readSource(root, e.Name()); !hasSrc {
			if m.Template != "" {
				m.Template, m.PristineSHA256 = "", ""
				changed = true
			}
			if m.Knobs != nil {
				m.Knobs = nil
				changed = true
			}
		} else if m.Knobs != nil {
			// Amendment 4: presets own everything. The options-era knobs merge
			// into every preset's bag — existing preset values win (they are
			// newer) — and the store forgets knobs ever existed.
			for name, bag := range m.Presets {
				if bag == nil {
					bag = map[string]any{}
					m.Presets[name] = bag
				}
				for k, v := range m.Knobs {
					if _, ok := bag[k]; !ok {
						bag[k] = v
					}
				}
			}
			m.Knobs = nil
			changed = true
		}
		if changed {
			if err := writeMeta(root, e.Name(), m); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeMeta(root, name string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteFileAtomic(metaPath(root, name), data, 0o600)
}

func writeYAMLFile(root, name, yaml string) error {
	return state.WriteFileAtomic(yamlPath(root, name), []byte(yaml), 0o600)
}

// userErrf builds a state.BadRequest-marked error: a caller mistake (a bad
// name, a missing or duplicate configuration, a preset that isn't there) the
// REST layer answers 400 for, rather than a failure of the store itself
// (500). The web UI dumps a collector log tail onto a 5xx and nothing else,
// so mis-classifying a user mistake buries its own message.
func userErrf(format string, a ...any) error {
	return state.BadRequest(fmt.Errorf(format, a...))
}

func validateName(name string) error {
	if !state.ValidBackendName(name) {
		return userErrf("invalid config name %q: use lowercase letters, digits, dashes", name)
	}
	return nil
}

// validatePresetName applies the configuration-name rule to preset names
// too: they show up in URLs and in the menu bar exactly like config names,
// and the same rule deserves the same wording wherever it can surface.
func validatePresetName(preset string) error {
	if !state.ValidBackendName(preset) {
		return userErrf("invalid preset name %q: use lowercase letters, digits, dashes", preset)
	}
	return nil
}

func buildInfo(root, name string) (Info, string, error) {
	yaml, err := readYAML(root, name)
	if err != nil {
		return Info{}, "", err
	}
	m, err := readMeta(root, name)
	if err != nil {
		return Info{}, "", err
	}
	// For a tier-3 config the SOURCE is the config: the pristine hash covers
	// it, so modified means the source diverged from what was shipped/synced.
	// Vars still parse from the rendered yaml — that is what runs.
	hashed := yaml
	src, hasSrc := readSource(root, name)
	if hasSrc {
		hashed = src
	}
	info := Info{
		Name:        name,
		Provenance:  provenanceFor(m),
		Modified:    modifiedFor(m, hashed),
		HasTemplate: hasSrc,
		Meta:        m,
		Vars:        vars.Parse(yaml),
	}
	return info, yaml, nil
}

// Source returns a configuration's template source; ok is false for a plain
// (tier 1/2) config. The error covers only a bad name or a missing config.
func Source(root, name string) (src string, ok bool, err error) {
	if err := validateName(name); err != nil {
		return "", false, err
	}
	if !exists(root, name) {
		return "", false, userErrf("config %q not found", name)
	}
	src, ok = readSource(root, name)
	return src, ok, nil
}

// List returns all configurations, sorted by name.
func List(root string) ([]Info, error) {
	entries, err := os.ReadDir(Dir(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !exists(root, e.Name()) {
			continue // no config.yaml: e.g. left behind by a failed CreateFromURL
		}
		info, _, err := buildInfo(root, e.Name())
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	slices.SortFunc(infos, func(a, b Info) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return infos, nil
}

// Get returns the configuration's info and its config.yaml content.
func Get(root, name string) (Info, string, error) {
	if err := validateName(name); err != nil {
		return Info{}, "", err
	}
	return buildInfo(root, name)
}

func createDir(root, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if exists(root, name) {
		return userErrf("config %q already exists", name)
	}
	return os.MkdirAll(configDir(root, name), 0o755)
}

// Create makes a new local configuration from the given content. Content
// that carries template front matter is tier-3 source: it is stored as
// config.tmpl and rendered (with the schema's defaults) into config.yaml —
// pasting a source into the yaml box creates a templated config, exactly as
// pasting ${env:} yaml lights up cards.
func Create(root, name, content string) error {
	if catalog.IsSource(content) {
		return createSource(root, name, content, nil, Meta{})
	}
	if err := createDir(root, name); err != nil {
		return err
	}
	if err := writeYAMLFile(root, name, content); err != nil {
		return err
	}
	return writeMeta(root, name, withDefaultPreset(Meta{}))
}

// CreateWithSource makes a new local tier-3 configuration from a template
// source plus initial values (nil = the schema's defaults). This is what
// creating from a catalog entry does: the source is COPIED in, immediately
// the user's own — no provenance, nothing special about it afterward.
func CreateWithSource(root, name, src string, values map[string]any) error {
	return createSource(root, name, src, values, Meta{})
}

// createSource parses, normalizes the initial values, and renders BEFORE
// creating any directory, so a bad source or unsatisfiable values leave
// nothing behind. Values are strict here (an unknown field is a 400 naming
// it — they are fresh, a typo deserves an answer); only SAVES over stored
// bags prune. The normalized bag seeds the fresh default preset — a new
// tier-3 config's one preset IS its schema defaults plus whatever the
// caller filled in. m carries provenance (remote URL / pristine hash) from
// the caller.
func createSource(root, name, src string, values map[string]any, m Meta) error {
	t, err := catalog.ParseSource(src)
	if err != nil {
		return err
	}
	if values == nil {
		// No caller values (a source pasted into the yaml box): the schema's
		// own default bag, groups seeded to their Min rows — the same seed
		// MaterializeDefaults uses, so a min-1 group does not turn "paste a
		// source" into "need 1 to 8 entries, got 0".
		values = t.Reconcile(nil, StorageDir(root))
	}
	norm, err := t.NormalizeBag(values)
	if err != nil {
		return err
	}
	rendered, err := t.Render(norm, StorageDir(root))
	if err != nil {
		return err
	}
	if err := createDir(root, name); err != nil {
		return err
	}
	if err := state.WriteFileAtomic(SourcePath(root, name), []byte(src), 0o600); err != nil {
		return err
	}
	if err := writeYAMLFile(root, name, rendered); err != nil {
		return err
	}
	m = withDefaultPreset(m)
	m.Presets[m.ActivePreset] = norm
	return writeMeta(root, name, m)
}

// CreateFromURL fetches yaml from url and creates a new remote configuration,
// recording the pristine hash for edit-protection. The fetch runs before any
// directory is created, so a failed fetch (typo'd URL) leaves nothing behind
// for a retry to trip over.
func CreateFromURL(root, name, url string, fetch Fetch) error {
	if err := validateName(name); err != nil {
		return err
	}
	if exists(root, name) {
		return userErrf("config %q already exists", name)
	}
	if IsOTelBinURL(url) {
		// otelbin links are snapshots, not syncable sources: the decoded
		// YAML becomes a plain LOCAL config — no remote_url, no pristine
		// hash. See otelbin.go.
		yaml, err := FetchOTelBinYAML(url)
		if err != nil {
			return state.BadRequest(err)
		}
		if err := createDir(root, name); err != nil {
			return err
		}
		if err := writeYAMLFile(root, name, yaml); err != nil {
			return err
		}
		return writeMeta(root, name, withDefaultPreset(Meta{}))
	}
	content, err := fetch(url)
	if err != nil {
		return state.BadRequest(err) // the URL is the user's; a log tail says nothing about it
	}
	body := string(content)
	// A remote config may BE tier-3 source: the pristine hash then covers the
	// SOURCE (it is the config), and sync keeps comparing against it.
	if catalog.IsSource(body) {
		return createSource(root, name, body, nil, Meta{
			RemoteURL:      url,
			PristineSHA256: hashOf(body),
		})
	}
	if err := createDir(root, name); err != nil {
		return err
	}
	if err := writeYAMLFile(root, name, body); err != nil {
		return err
	}
	return writeMeta(root, name, withDefaultPreset(Meta{
		RemoteURL:      url,
		PristineSHA256: hashOf(body),
	}))
}

// Copy duplicates src's YAML, template source, and presets (bags and all)
// into a new local configuration dst; provenance (remote URL / pristine
// hash) is dropped.
func Copy(root, src, dst string) error {
	if err := validateName(src); err != nil {
		return err
	}
	if err := validateName(dst); err != nil {
		return err
	}
	yaml, err := readYAML(root, src)
	if err != nil {
		return err
	}
	srcMeta, err := readMeta(root, src)
	if err != nil {
		return err
	}
	tmpl, hasTmpl := readSource(root, src)
	if err := createDir(root, dst); err != nil {
		return err
	}
	if err := writeYAMLFile(root, dst, yaml); err != nil {
		return err
	}
	m := Meta{
		Presets:      make(map[string]map[string]any, len(srcMeta.Presets)),
		ActivePreset: srcMeta.ActivePreset,
	}
	for name, kv := range srcMeta.Presets {
		m.Presets[name] = maps.Clone(kv)
	}
	if hasTmpl {
		if err := state.WriteFileAtomic(SourcePath(root, dst), []byte(tmpl), 0o600); err != nil {
			return err
		}
	}
	return writeMeta(root, dst, withDefaultPreset(m))
}

// Delete removes a configuration entirely.
func Delete(root, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	return os.RemoveAll(configDir(root, name))
}

// WriteYAML overwrites a configuration's config.yaml with plain YAML.
// Modified status is derived from the pristine hash, not tracked
// separately. On a tier-3 config this is a demotion — writing plain yaml
// over a templated config means it IS a plain config now, so the source and
// its knobs go (editing is editing; no lock-in, per the tier ladder).
func WriteYAML(root, name, yaml string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	return clearSource(root, name)
}

// clearSource removes a configuration's template source and any stray
// options-era knobs — the tier-3 → plain demotion shared by WriteYAML,
// Reset, and a sync whose remote turned plain. Preset bags stay: they are
// the user's values, and deleting a source must never delete a secret.
func clearSource(root, name string) error {
	if err := os.Remove(SourcePath(root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if m.Knobs == nil {
		return nil
	}
	m.Knobs = nil
	return writeMeta(root, name, m)
}

// WriteSource stores a tier-3 configuration's pair: the source and the
// yaml rendered from it. The caller (app) has already parsed, rendered
// with a preset's bag, and decided about collector validation; this is
// only the write, with its failure atomicity: each file lands via
// temp+rename, and a failure after the source landed puts the prior source
// back — a config never holds a source its rendered yaml doesn't derive
// from. Meta (presets, pristine hash) is untouched.
func WriteSource(root, name, src, rendered string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	prevSrc, hadSrc := readSource(root, name)
	if err := state.WriteFileAtomic(SourcePath(root, name), []byte(src), 0o600); err != nil {
		return err
	}
	if err := writeYAMLFile(root, name, rendered); err != nil {
		if hadSrc {
			_ = state.WriteFileAtomic(SourcePath(root, name), []byte(prevSrc), 0o600)
		} else {
			_ = os.Remove(SourcePath(root, name))
		}
		return err
	}
	return nil
}

// WriteRendered overwrites a tier-3 configuration's DERIVED config.yaml —
// what activation regenerates from the selected preset's bag — leaving the
// source and meta untouched. Unlike WriteYAML this is no demotion: the
// yaml is output, not the config.
func WriteRendered(root, name, rendered string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	return writeYAMLFile(root, name, rendered)
}

// WriteMeta overwrites a configuration's meta.json.
func WriteMeta(root, name string, m Meta) error {
	if err := validateName(name); err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	return writeMeta(root, name, m)
}

func refetch(root, name string, fetch Fetch) error {
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if m.RemoteURL == "" {
		return userErrf("config %q has no remote URL configured", name)
	}
	content, err := fetch(m.RemoteURL)
	if err != nil {
		return state.BadRequest(err) // the URL is the user's; a log tail says nothing about it
	}
	body := string(content)
	// A tier-3 remote syncs its SOURCE: every preset's bag is reconciled
	// with the fetched schema (removed fields pruned, new defaulted ones
	// filled — per preset), and the yaml re-rendered from the ACTIVE
	// preset's bag. Render trouble (a schema change the active bag cannot
	// satisfy) aborts before anything is written.
	if catalog.IsSource(body) {
		return applySource(root, name, body, m)
	}
	if err := writeYAMLFile(root, name, body); err != nil {
		return err
	}
	m.Knobs = nil // the remote turned plain; so does the config
	m.PristineSHA256 = hashOf(body)
	if err := writeMeta(root, name, m); err != nil {
		return err
	}
	if err := os.Remove(SourcePath(root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// applySource installs a new template source over an existing configuration
// — the shared tier-3 arm of sync, the shipped upgrade, and Reset: every
// preset's bag reconciled with the new schema (removed fields pruned, newly
// defaulted filled), config.yaml re-rendered from the ACTIVE preset's bag,
// the pristine hash moved to the source. Render trouble (a schema this
// state cannot satisfy) aborts before anything is written.
func applySource(root, name, src string, m Meta) error {
	t, err := catalog.ParseSource(src)
	if err != nil {
		return err
	}
	m = withDefaultPreset(m)
	for pname, bag := range m.Presets {
		m.Presets[pname] = t.Reconcile(bag, StorageDir(root))
	}
	rendered, err := t.Render(t.PruneUnknown(m.Presets[m.ActivePreset]), StorageDir(root))
	if err != nil {
		return err
	}
	if err := WriteSource(root, name, src, rendered); err != nil {
		return err
	}
	m.Knobs = nil
	m.PristineSHA256 = hashOf(src)
	return writeMeta(root, name, m)
}

// Sync refetches a remote configuration's YAML and updates the pristine
// hash. It errors if the configuration has been locally modified.
func Sync(root, name string, fetch Fetch) error {
	if err := validateName(name); err != nil {
		return err
	}
	info, _, err := buildInfo(root, name)
	if err != nil {
		return err
	}
	if info.Modified {
		return userErrf("config %q is locally modified; use Resync to discard local edits", name)
	}
	return refetch(root, name, fetch)
}

// Resync forcibly refetches a remote configuration, discarding any local
// edits.
func Resync(root, name string, fetch Fetch) error {
	if err := validateName(name); err != nil {
		return err
	}
	return refetch(root, name, fetch)
}

// Reset restores a modified shipped configuration's config.yaml to the
// embedded default and recomputes the pristine hash; presets and the rest
// of meta are kept. It is the builtin twin of Resync: it refuses
// non-builtins and unmodified builtins (nothing to reset).
func Reset(root, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	info, _, err := buildInfo(root, name)
	if err != nil {
		return err
	}
	if info.Provenance != "shipped" {
		return userErrf("config %q is not a built-in config; only built-ins can be reset", name)
	}
	if !info.Modified {
		return userErrf("config %q already matches the shipped version; nothing to reset", name)
	}
	// A shipped TEMPLATED default resets from the embedded catalog template:
	// source + render + pristine hash back to shipped, presets kept (each
	// bag reconciled with the schema, as sync does).
	if t, err := catalog.Get(name); err == nil {
		return applySource(root, name, t.Source(), info.Meta)
	}
	content, err := embeddedDefaults.ReadFile("defaults/" + name + ".yaml")
	if err != nil {
		return userErrf("config %q has no shipped default to reset to", name)
	}
	yaml := string(content)
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	// A source pasted over a built-in goes with the reset (shipped defaults
	// are plain): back to exactly what shipped.
	if err := clearSource(root, name); err != nil {
		return err
	}
	m, err := readMeta(root, name) // clearSource may have rewritten meta
	if err != nil {
		return err
	}
	m.PristineSHA256 = hashOf(yaml)
	return writeMeta(root, name, m)
}

// Rename moves a configuration from -> to, keeping its YAML and presets.
// It refuses a missing source and an existing target. A shipped config
// becomes local under its new name (like Copy): shipped identity is bound
// to the name — Reset and the upgrade path read defaults/<name>.yaml — so
// carrying the pristine hash to another name would reset or upgrade it
// against the wrong shipped YAML.
func Rename(root, from, to string) error {
	if err := validateName(from); err != nil {
		return err
	}
	if err := validateName(to); err != nil {
		return err
	}
	if !exists(root, from) {
		return userErrf("config %q not found", from)
	}
	if exists(root, to) {
		return userErrf("config %q already exists", to)
	}
	m, err := readMeta(root, from)
	if err != nil {
		return err
	}
	if err := os.Rename(configDir(root, from), configDir(root, to)); err != nil {
		return err
	}
	if provenanceFor(m) == "shipped" {
		m.PristineSHA256 = ""
		return writeMeta(root, to, m)
	}
	return nil
}

// validateVarKey refuses an empty (or all-whitespace) variable name — the
// CLI shape `compy presets set c p =value` — before it can flow into the
// LaunchAgent environment as a nameless entry.
func validateVarKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return userErrf("variable name must not be empty")
	}
	return nil
}

// SetVar sets a key/value pair in a preset's bag, creating the preset on
// first write. The value is typed: a plain string for tier-2 env values,
// whatever the schema says for a tier-3 field (app parses; this is only
// the store write).
func SetVar(root, name, preset, key string, value any) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validatePresetName(preset); err != nil {
		return err
	}
	if err := validateVarKey(key); err != nil {
		return err
	}
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	if m.Presets == nil {
		m.Presets = map[string]map[string]any{}
	}
	if m.Presets[preset] == nil {
		m.Presets[preset] = map[string]any{}
	}
	m.Presets[preset][key] = value
	return writeMeta(root, name, m)
}

// WritePreset creates or replaces a preset's entire bag (the preset need
// not already exist).
func WritePreset(root, name, preset string, values map[string]any) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validatePresetName(preset); err != nil {
		return err
	}
	for key := range values {
		if err := validateVarKey(key); err != nil {
			return err
		}
	}
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	if m.Presets == nil {
		m.Presets = map[string]map[string]any{}
	}
	m.Presets[preset] = maps.Clone(values)
	return writeMeta(root, name, m)
}

// DeletePreset removes a preset. It errors if the preset does not exist, is
// the last one (every configuration keeps at least one preset), or is the
// active preset.
func DeletePreset(root, name, preset string) error {
	if err := validateName(name); err != nil {
		return err
	}
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	if _, ok := m.Presets[preset]; !ok {
		return userErrf("config %q has no preset %q", name, preset)
	}
	if len(m.Presets) == 1 {
		return userErrf("a configuration keeps at least one preset; edit it or add another first")
	}
	if preset == m.ActivePreset {
		return userErrf("cannot delete active preset %q", preset)
	}
	delete(m.Presets, preset)
	return writeMeta(root, name, m)
}

// UsePreset makes preset the active preset. It errors if the preset does not
// exist.
func UsePreset(root, name, preset string) error {
	if err := validateName(name); err != nil {
		return err
	}
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	if _, ok := m.Presets[preset]; !ok {
		return userErrf("config %q has no preset %q", name, preset)
	}
	m.ActivePreset = preset
	return writeMeta(root, name, m)
}

// RenamePreset renames a preset from -> to. It errors if to is empty,
// from does not exist, or to already exists. Renaming the active preset
// updates active_preset to follow it.
func RenamePreset(root, name, from, to string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validatePresetName(to); err != nil {
		return err
	}
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	if _, ok := m.Presets[from]; !ok {
		return userErrf("config %q has no preset %q", name, from)
	}
	if _, ok := m.Presets[to]; ok {
		return userErrf("config %q already has a preset %q", name, to)
	}
	m.Presets[to] = m.Presets[from]
	delete(m.Presets, from)
	if m.ActivePreset == from {
		m.ActivePreset = to
	}
	return writeMeta(root, name, m)
}

// SnapshotActive copies configs/<name>/ and settings.json into last-good/,
// replacing any prior snapshot. Callers take it only once a configuration is
// proven to have started, so the snapshot is always a setup that ran: it is
// what RestoreActive puts back when the next one fails to start.
func SnapshotActive(root, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	dst := filepath.Join(root, "last-good")
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := copyTree(configDir(root, name), filepath.Join(dst, "config")); err != nil {
		return err
	}
	return copyFile(filepath.Join(root, "settings.json"), filepath.Join(dst, "settings.json"), 0o600)
}

// HasSnapshot reports whether a last-good snapshot exists to restore.
// state.Dir() pre-creates last-good/ empty, so its mere existence proves
// nothing; settings.json only lands there via SnapshotActive.
func HasSnapshot(root string) bool {
	_, err := os.Stat(filepath.Join(root, "last-good", "settings.json"))
	return err == nil
}

// RestoreActive copies the last-good snapshot back over settings.json and
// the configuration it was taken from (the active_config recorded in the
// snapshot's settings.json). It errors if no snapshot exists.
func RestoreActive(root string) error {
	src := filepath.Join(root, "last-good")
	data, err := os.ReadFile(filepath.Join(src, "settings.json"))
	if err != nil {
		return errors.New("no last-good snapshot to restore")
	}
	var s state.Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if err := validateName(s.ActiveConfig); err != nil {
		return err
	}
	dir := configDir(root, s.ActiveConfig)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := copyTree(filepath.Join(src, "config"), dir); err != nil {
		return err
	}
	return copyFile(filepath.Join(src, "settings.json"), filepath.Join(root, "settings.json"), 0o600)
}

// copyTree recursively copies src to dst. A missing src is a no-op.
func copyTree(src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, 0o600)
	})
}

// copyFile copies src to dst atomically. A missing src is a no-op.
func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return state.WriteFileAtomic(dst, data, perm)
}

// MaterializeDefaults creates or upgrades shipped default configurations:
// plain ones from the embedded defaults/ directory, templated ones from the
// embedded catalog (source + a default preset seeded with the schema's
// normalized defaults + a render, pristine hash on the SOURCE). It is
// idempotent: missing configs are created (provenance "shipped"); existing,
// unmodified configs are overwritten when the embedded content has changed
// (the upgrade path — a still-plain config whose default turned templated
// upgrades in place, every preset's bag reconciled exactly as Sync does);
// existing, modified configs and non-"shipped" provenance are left
// untouched. A shipped-provenance config that is unmodified, not active,
// and no longer has a shipped default of its name is retired (deleted).
func MaterializeDefaults(root string) error {
	entries, err := embeddedDefaults.ReadDir("defaults")
	if err != nil {
		return err
	}
	shipped := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()[:len(e.Name())-len(filepath.Ext(e.Name()))]
		shipped[name] = true
		content, err := embeddedDefaults.ReadFile("defaults/" + e.Name())
		if err != nil {
			return err
		}
		embedYAML := string(content)

		if !exists(root, name) {
			if err := createDir(root, name); err != nil {
				return err
			}
			if err := writeYAMLFile(root, name, embedYAML); err != nil {
				return err
			}
			if err := writeMeta(root, name, withDefaultPreset(Meta{
				PristineSHA256: hashOf(embedYAML),
			})); err != nil {
				return err
			}
			continue
		}

		info, currentYAML, err := buildInfo(root, name)
		if err != nil {
			return err
		}
		if info.Provenance != "shipped" {
			continue // a local or remote config on this name is the user's, never upgraded
		}
		if info.Modified {
			continue // leave untouched
		}
		if currentYAML == embedYAML {
			continue // nothing to upgrade
		}
		if err := writeYAMLFile(root, name, embedYAML); err != nil {
			return err
		}
		m := info.Meta
		m.PristineSHA256 = hashOf(embedYAML)
		if err := writeMeta(root, name, m); err != nil {
			return err
		}
	}

	// Templated defaults: every embedded catalog template ships as a
	// templated config.
	ts, err := catalog.Templates()
	if err != nil {
		return err
	}
	for _, t := range ts {
		shipped[t.Name] = true
		src := t.Source()
		if !exists(root, t.Name) {
			// Reconcile(nil) is the schema's normalized default bag (repeat
			// group seeded with its Min default rows) — the fresh default
			// preset.
			if err := createSource(root, t.Name, src, t.Reconcile(nil, StorageDir(root)), Meta{
				PristineSHA256: hashOf(src),
			}); err != nil {
				return err
			}
			continue
		}
		info, _, err := buildInfo(root, t.Name)
		if err != nil {
			return err
		}
		if info.Provenance != "shipped" || info.Modified {
			continue // the user's now (or their edit); never overwritten
		}
		if cur, ok := readSource(root, t.Name); ok && cur == src {
			continue // nothing to upgrade
		}
		if err := applySource(root, t.Name, src, info.Meta); err != nil {
			if state.IsBadRequest(err) {
				// A render this config's presets cannot satisfy: leave the
				// config as it is rather than brick every startup — the next
				// materialize retries.
				continue
			}
			return err
		}
	}

	// Retire arm: a shipped-provenance config that is unmodified, NOT the
	// active config, and no longer has a shipped default of its name.
	var s state.Settings
	if data, err := os.ReadFile(filepath.Join(root, "settings.json")); err == nil {
		_ = json.Unmarshal(data, &s) // unreadable settings: nothing active
	}
	infos, err := List(root)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if shipped[info.Name] || info.Provenance != "shipped" ||
			info.Modified || info.Name == s.ActiveConfig {
			continue
		}
		if err := Delete(root, info.Name); err != nil {
			return err
		}
	}
	return nil
}
