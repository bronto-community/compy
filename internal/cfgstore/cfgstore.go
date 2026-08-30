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

	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/vars"
)

// Meta is the persisted metadata for a configuration (meta.json). There is
// deliberately no per-config collector binary: one collector, chosen once in
// settings, runs every configuration (docs/design/handoff/README.md,
// "Departures"). A "distro" key left in an older meta.json is ignored.
type Meta struct {
	RemoteURL      string                       `json:"remote_url,omitempty"`
	PristineSHA256 string                       `json:"pristine_sha256,omitempty"`
	Presets        map[string]map[string]string `json:"presets"`
	ActivePreset   string                       `json:"active_preset"`
	// Template + Knobs mark a template-born configuration: the catalog
	// template it was rendered from and the normalized knob values (secrets
	// excluded — they never had values at render time). They exist so
	// "change options" can re-render; the YAML itself is plain collector
	// YAML, indistinguishable from a pasted one (the tier invariant).
	Template string         `json:"template,omitempty"`
	Knobs    map[string]any `json:"knobs,omitempty"`
}

// Info describes a configuration for listing/inspection.
type Info struct {
	Name       string     `json:"name"`
	Provenance string     `json:"provenance"` // "shipped" | "remote" | "local" | "template"
	Modified   bool       `json:"modified"`   // hash != pristine (always false for "local")
	Meta       Meta       `json:"meta"`
	Vars       []vars.Var `json:"vars"`
}

// MissingRequired names the variables an activation of this config with
// this preset would leave without a value: no `${VAR:-default}` fallback in
// the yaml (vars.Var.HasDefault false), not compy-injected (COMPY_*), and
// empty or absent in the preset. An empty preset name resolves to the
// config's active preset, exactly as Activate does — and every config has
// one (EnsurePresets). Callers decide what to do with the answer: the
// window asks before activating, the CLI warns and proceeds.
func MissingRequired(info Info, preset string) []string {
	if preset == "" {
		preset = info.Meta.ActivePreset
	}
	values := info.Meta.Presets[preset]
	var missing []string
	for _, v := range info.Vars {
		if v.HasDefault || strings.HasPrefix(v.Name, "COMPY_") {
			continue
		}
		if strings.TrimSpace(values[v.Name]) == "" {
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

func metaPath(root, name string) string {
	return filepath.Join(configDir(root, name), "meta.json")
}

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func provenanceFor(m Meta) string {
	if m.Template != "" {
		return "template"
	}
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
		return Meta{Presets: map[string]map[string]string{}}, nil
	}
	if err != nil {
		return Meta{}, err
	}
	var raw struct {
		Meta
		LegacyPresets map[string]map[string]string `json:"variable_sets"`
		LegacyActive  string                       `json:"active_set"`
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
		m.Presets = map[string]map[string]string{}
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
		m.Presets = map[string]map[string]string{DefaultPreset: {}}
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
// active_preset "default". Deterministic and idempotent; a repair, not an
// event, so nothing is logged.
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
		if ensureDefaultPreset(&m) {
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
	info := Info{
		Name:       name,
		Provenance: provenanceFor(m),
		Modified:   modifiedFor(m, yaml),
		Meta:       m,
		Vars:       vars.Parse(yaml),
	}
	return info, yaml, nil
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

// Create makes a new local configuration with the given YAML content.
func Create(root, name, yaml string) error {
	if err := createDir(root, name); err != nil {
		return err
	}
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	return writeMeta(root, name, withDefaultPreset(Meta{}))
}

// CreateFromTemplate makes a new template-born configuration: rendered
// plain YAML plus the template name and its normalized knob values in
// meta.json. The pristine hash serves re-render exactly as it serves sync:
// matching means unmodified (re-render replaces cleanly), differing means
// hand-edited (re-render refuses unless forced). Apart from meta the config
// is indistinguishable from a pasted one.
func CreateFromTemplate(root, name, yaml, template string, knobs map[string]any) error {
	if err := createDir(root, name); err != nil {
		return err
	}
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	return writeMeta(root, name, withDefaultPreset(Meta{
		PristineSHA256: hashOf(yaml),
		Template:       template,
		Knobs:          knobs,
	}))
}

// SetRendered replaces a template-born configuration's YAML with a fresh
// render, updating the pristine hash and the recorded knobs; presets and
// the rest of meta are untouched. The modified-or-not decision is the
// caller's (app mirrors Sync/Resync there); this is the write both paths
// share, refusing only a config that isn't template-born at all.
func SetRendered(root, name, yaml string, knobs map[string]any) error {
	if err := validateName(name); err != nil {
		return err
	}
	info, _, err := buildInfo(root, name)
	if err != nil {
		return err
	}
	if info.Meta.Template == "" {
		return userErrf("config %q was not created from a template", name)
	}
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	m := info.Meta
	m.PristineSHA256 = hashOf(yaml)
	m.Knobs = knobs
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
	yaml := string(content)
	if err := createDir(root, name); err != nil {
		return err
	}
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	return writeMeta(root, name, withDefaultPreset(Meta{
		RemoteURL:      url,
		PristineSHA256: hashOf(yaml),
	}))
}

// Copy duplicates src's YAML and presets into a new local
// configuration dst; provenance (remote URL / pristine hash) is dropped.
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
	if err := createDir(root, dst); err != nil {
		return err
	}
	if err := writeYAMLFile(root, dst, yaml); err != nil {
		return err
	}
	presets := make(map[string]map[string]string, len(srcMeta.Presets))
	for name, kv := range srcMeta.Presets {
		presets[name] = maps.Clone(kv)
	}
	return writeMeta(root, dst, withDefaultPreset(Meta{
		Presets:      presets,
		ActivePreset: srcMeta.ActivePreset,
	}))
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

// WriteYAML overwrites a configuration's config.yaml. Modified status is
// derived from the pristine hash, not tracked separately.
func WriteYAML(root, name, yaml string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if !exists(root, name) {
		return userErrf("config %q not found", name)
	}
	return writeYAMLFile(root, name, yaml)
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
	yaml := string(content)
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	m.PristineSHA256 = hashOf(yaml)
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
	content, err := embeddedDefaults.ReadFile("defaults/" + name + ".yaml")
	if err != nil {
		return userErrf("config %q has no shipped default to reset to", name)
	}
	yaml := string(content)
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	m := info.Meta
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

// SetVar sets a key/value pair in a preset, creating the preset on first
// write.
func SetVar(root, name, preset, key, value string) error {
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
		m.Presets = map[string]map[string]string{}
	}
	if m.Presets[preset] == nil {
		m.Presets[preset] = map[string]string{}
	}
	m.Presets[preset][key] = value
	return writeMeta(root, name, m)
}

// WritePreset creates or replaces a preset's entire contents (the preset
// need not already exist).
func WritePreset(root, name, preset string, values map[string]string) error {
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
		m.Presets = map[string]map[string]string{}
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

// MaterializeDefaults creates or upgrades shipped default configurations
// from the embedded defaults/ directory. It is idempotent: missing configs
// are created (provenance "shipped"); existing, unmodified configs are
// overwritten when the embedded content has changed (the upgrade path);
// existing, modified configs are left untouched.
func MaterializeDefaults(root string) error {
	entries, err := embeddedDefaults.ReadDir("defaults")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()[:len(e.Name())-len(filepath.Ext(e.Name()))]
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
	return nil
}
