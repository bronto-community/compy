// Package cfgstore manages compy's Configurations: on-disk directories
// under configs/<name>/ holding a collector config.yaml + meta.json
// (provenance, variable sets). See
// docs/superpowers/specs/2026-08-25-compy-v2-configs-design.md.
package cfgstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/bronto-io/compy/internal/state"
	"github.com/bronto-io/compy/internal/vars"
)

// Meta is the persisted metadata for a configuration (meta.json).
type Meta struct {
	RemoteURL      string                       `json:"remote_url,omitempty"`
	Distro         string                       `json:"distro,omitempty"` // "" = global default
	PristineSHA256 string                       `json:"pristine_sha256,omitempty"`
	VariableSets   map[string]map[string]string `json:"variable_sets"`
	ActiveSet      string                       `json:"active_set"`
}

// Info describes a configuration for listing/inspection.
type Info struct {
	Name       string     `json:"name"`
	Provenance string     `json:"provenance"` // "shipped" | "remote" | "local"
	Modified   bool       `json:"modified"`   // hash != pristine (always false for "local")
	Meta       Meta       `json:"meta"`
	Vars       []vars.Var `json:"vars"`
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
			return "", fmt.Errorf("config %q not found", name)
		}
		return "", err
	}
	return string(data), nil
}

func readMeta(root, name string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(metaPath(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return Meta{VariableSets: map[string]map[string]string{}}, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.VariableSets == nil {
		m.VariableSets = map[string]map[string]string{}
	}
	return m, nil
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

func validateName(name string) error {
	if !state.ValidBackendName(name) {
		return fmt.Errorf("invalid config name %q", name)
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
	return buildInfo(root, name)
}

func createDir(root, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if exists(root, name) {
		return fmt.Errorf("config %q already exists", name)
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
	return writeMeta(root, name, Meta{VariableSets: map[string]map[string]string{}})
}

// CreateFromURL fetches yaml from url and creates a new remote configuration,
// recording the pristine hash for edit-protection.
func CreateFromURL(root, name, url string, fetch Fetch) error {
	if err := createDir(root, name); err != nil {
		return err
	}
	content, err := fetch(url)
	if err != nil {
		return err
	}
	yaml := string(content)
	if err := writeYAMLFile(root, name, yaml); err != nil {
		return err
	}
	return writeMeta(root, name, Meta{
		RemoteURL:      url,
		PristineSHA256: hashOf(yaml),
		VariableSets:   map[string]map[string]string{},
	})
}

// Copy duplicates src's YAML and variable sets into a new local
// configuration dst; provenance (remote URL / pristine hash) is dropped.
func Copy(root, src, dst string) error {
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
	sets := make(map[string]map[string]string, len(srcMeta.VariableSets))
	for setName, kv := range srcMeta.VariableSets {
		sets[setName] = maps.Clone(kv)
	}
	return writeMeta(root, dst, Meta{
		Distro:       srcMeta.Distro,
		VariableSets: sets,
		ActiveSet:    srcMeta.ActiveSet,
	})
}

// Delete removes a configuration entirely.
func Delete(root, name string) error {
	if !exists(root, name) {
		return fmt.Errorf("config %q not found", name)
	}
	return os.RemoveAll(configDir(root, name))
}

// WriteYAML overwrites a configuration's config.yaml. Modified status is
// derived from the pristine hash, not tracked separately.
func WriteYAML(root, name, yaml string) error {
	if !exists(root, name) {
		return fmt.Errorf("config %q not found", name)
	}
	return writeYAMLFile(root, name, yaml)
}

// WriteMeta overwrites a configuration's meta.json.
func WriteMeta(root, name string, m Meta) error {
	if !exists(root, name) {
		return fmt.Errorf("config %q not found", name)
	}
	return writeMeta(root, name, m)
}

func refetch(root, name string, fetch Fetch) error {
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if m.RemoteURL == "" {
		return fmt.Errorf("config %q has no remote URL configured", name)
	}
	content, err := fetch(m.RemoteURL)
	if err != nil {
		return err
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
	info, _, err := buildInfo(root, name)
	if err != nil {
		return err
	}
	if info.Modified {
		return fmt.Errorf("config %q is locally modified; use Resync to discard local edits", name)
	}
	return refetch(root, name, fetch)
}

// Resync forcibly refetches a remote configuration, discarding any local
// edits.
func Resync(root, name string, fetch Fetch) error {
	return refetch(root, name, fetch)
}

// SetVar sets a key/value pair in a variable set, creating the set on first
// write.
func SetVar(root, name, set, key, value string) error {
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if !exists(root, name) {
		return fmt.Errorf("config %q not found", name)
	}
	if m.VariableSets == nil {
		m.VariableSets = map[string]map[string]string{}
	}
	if m.VariableSets[set] == nil {
		m.VariableSets[set] = map[string]string{}
	}
	m.VariableSets[set][key] = value
	return writeMeta(root, name, m)
}

// DeleteSet removes a variable set. It errors if the set is the active set
// or does not exist.
func DeleteSet(root, name, set string) error {
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if !exists(root, name) {
		return fmt.Errorf("config %q not found", name)
	}
	if _, ok := m.VariableSets[set]; !ok {
		return fmt.Errorf("config %q has no variable set %q", name, set)
	}
	if set == m.ActiveSet {
		return fmt.Errorf("cannot delete active variable set %q", set)
	}
	delete(m.VariableSets, set)
	return writeMeta(root, name, m)
}

// UseSet makes set the active variable set. It errors if the set does not
// exist.
func UseSet(root, name, set string) error {
	m, err := readMeta(root, name)
	if err != nil {
		return err
	}
	if !exists(root, name) {
		return fmt.Errorf("config %q not found", name)
	}
	if _, ok := m.VariableSets[set]; !ok {
		return fmt.Errorf("config %q has no variable set %q", name, set)
	}
	m.ActiveSet = set
	return writeMeta(root, name, m)
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
			if err := writeMeta(root, name, Meta{
				PristineSHA256: hashOf(embedYAML),
				VariableSets:   map[string]map[string]string{},
			}); err != nil {
				return err
			}
			continue
		}

		info, currentYAML, err := buildInfo(root, name)
		if err != nil {
			return err
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
