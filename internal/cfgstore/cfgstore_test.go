package cfgstore

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/catalog"
	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/vars"
)

func TestCreateGetListDelete(t *testing.T) {
	root := t.TempDir()

	if err := Create(root, "myconfig", "receivers: {}\n"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Create(root, "myconfig", "receivers: {}\n"); err == nil {
		t.Fatal("Create over existing config: want error, got nil")
	}

	info, yaml, err := Get(root, "myconfig")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if yaml != "receivers: {}\n" {
		t.Fatalf("Get yaml = %q", yaml)
	}
	if info.Name != "myconfig" || info.Provenance != "local" || info.Modified {
		t.Fatalf("Get info = %+v", info)
	}

	if err := Create(root, "another", "receivers: {}\n"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	list, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Name != "another" || list[1].Name != "myconfig" {
		t.Fatalf("List = %+v, want sorted [another myconfig]", list)
	}

	if err := Delete(root, "another"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err = List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "myconfig" {
		t.Fatalf("List after delete = %+v", list)
	}

	if _, _, err := Get(root, "another"); err == nil {
		t.Fatal("Get deleted config: want error, got nil")
	}
}

func TestCopyDropsProvenance(t *testing.T) {
	root := t.TempDir()

	fetch := func(url string) ([]byte, error) { return []byte("content: v1\n"), nil }
	if err := CreateFromURL(root, "src", "https://example.com/c.yaml", fetch); err != nil {
		t.Fatalf("CreateFromURL: %v", err)
	}
	if err := SetVar(root, "src", "default", "KEY", "value"); err != nil {
		t.Fatalf("SetVar: %v", err)
	}
	if err := UsePreset(root, "src", "default"); err != nil {
		t.Fatalf("UsePreset: %v", err)
	}

	if err := Copy(root, "src", "dst"); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	info, yaml, err := Get(root, "dst")
	if err != nil {
		t.Fatalf("Get dst: %v", err)
	}
	if yaml != "content: v1\n" {
		t.Fatalf("dst yaml = %q", yaml)
	}
	if info.Provenance != "local" {
		t.Fatalf("dst provenance = %q, want local", info.Provenance)
	}
	if info.Meta.RemoteURL != "" {
		t.Fatalf("dst RemoteURL = %q, want empty", info.Meta.RemoteURL)
	}
	if info.Meta.PristineSHA256 != "" {
		t.Fatalf("dst PristineSHA256 = %q, want empty", info.Meta.PristineSHA256)
	}
	if info.Meta.Presets["default"]["KEY"] != "value" {
		t.Fatalf("dst presets = %+v, want copied KEY=value", info.Meta.Presets)
	}
	if info.Meta.ActivePreset != "default" {
		t.Fatalf("dst ActivePreset = %q, want default", info.Meta.ActivePreset)
	}

	// src untouched
	srcInfo, _, err := Get(root, "src")
	if err != nil {
		t.Fatalf("Get src: %v", err)
	}
	if srcInfo.Provenance != "remote" {
		t.Fatalf("src provenance = %q, want remote (unaffected by copy)", srcInfo.Provenance)
	}
}

func TestCreateFromURLFailedFetchSelfHeals(t *testing.T) {
	root := t.TempDir()

	failing := func(url string) ([]byte, error) { return nil, errors.New("dial tcp: no such host") }
	if err := CreateFromURL(root, "bad", "https://typo.example/c.yaml", failing); err == nil {
		t.Fatal("CreateFromURL with failing fetch: want error, got nil")
	}

	// A failed fetch must not leave a half-created configs/bad/ behind: List
	// still works and doesn't show "bad".
	list, err := List(root)
	if err != nil {
		t.Fatalf("List after failed CreateFromURL: %v", err)
	}
	for _, info := range list {
		if info.Name == "bad" {
			t.Fatalf("List includes %q from a failed CreateFromURL", info.Name)
		}
	}

	// Retry with a working fetch succeeds cleanly.
	working := func(url string) ([]byte, error) { return []byte("receivers: {}\n"), nil }
	if err := CreateFromURL(root, "bad", "https://typo.example/c.yaml", working); err != nil {
		t.Fatalf("CreateFromURL retry: %v", err)
	}
	info, _, err := Get(root, "bad")
	if err != nil {
		t.Fatalf("Get after retry: %v", err)
	}
	if info.Provenance != "remote" {
		t.Fatalf("provenance = %q, want remote", info.Provenance)
	}
}

func TestCreateFromURLAndSync(t *testing.T) {
	root := t.TempDir()

	content := "receivers: {}\n"
	calls := 0
	fetch := func(url string) ([]byte, error) {
		calls++
		return []byte(content), nil
	}
	if err := CreateFromURL(root, "remote1", "https://example.com/c.yaml", fetch); err != nil {
		t.Fatalf("CreateFromURL: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	info, _, err := Get(root, "remote1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Provenance != "remote" || info.Modified {
		t.Fatalf("info = %+v", info)
	}

	// Locally modify, then Sync should error.
	if err := WriteYAML(root, "remote1", "receivers: {}\nextra: true\n"); err != nil {
		t.Fatalf("WriteYAML: %v", err)
	}
	err = Sync(root, "remote1", fetch)
	if err == nil {
		t.Fatal("Sync after local edit: want error, got nil")
	}
	if !strings.Contains(err.Error(), "locally modified") {
		t.Fatalf("Sync error = %q, want mention of 'locally modified'", err.Error())
	}

	// Resync discards local edits and clears Modified.
	content = "receivers: {}\nupstream: changed\n"
	if err := Resync(root, "remote1", fetch); err != nil {
		t.Fatalf("Resync: %v", err)
	}
	info, yaml, err := Get(root, "remote1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Modified {
		t.Fatalf("info.Modified = true after Resync, want false")
	}
	if yaml != content {
		t.Fatalf("yaml after Resync = %q, want %q", yaml, content)
	}

	// Now unmodified: Sync should succeed.
	content = "receivers: {}\nupstream: changed again\n"
	if err := Sync(root, "remote1", fetch); err != nil {
		t.Fatalf("Sync on unmodified config: %v", err)
	}
	_, yaml, err = Get(root, "remote1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if yaml != content {
		t.Fatalf("yaml after Sync = %q, want %q", yaml, content)
	}
}

func TestVariableSets(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "cfg", "receivers: {}\n"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := SetVar(root, "cfg", "prod", "HOST", "prod.example.com"); err != nil {
		t.Fatalf("SetVar: %v", err)
	}
	info, _, err := Get(root, "cfg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.Meta.Presets["prod"]["HOST"] != "prod.example.com" {
		t.Fatalf("Presets = %+v", info.Meta.Presets)
	}

	if err := UsePreset(root, "cfg", "nonexistent"); err == nil {
		t.Fatal("UsePreset unknown set: want error, got nil")
	}

	if err := UsePreset(root, "cfg", "prod"); err != nil {
		t.Fatalf("UsePreset: %v", err)
	}

	if err := DeletePreset(root, "cfg", "prod"); err == nil {
		t.Fatal("DeletePreset active set: want error, got nil")
	}

	if err := SetVar(root, "cfg", "staging", "HOST", "staging.example.com"); err != nil {
		t.Fatalf("SetVar: %v", err)
	}
	if err := DeletePreset(root, "cfg", "staging"); err != nil {
		t.Fatalf("DeletePreset non-active: %v", err)
	}
	info, _, err = Get(root, "cfg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := info.Meta.Presets["staging"]; ok {
		t.Fatalf("staging set still present after delete: %+v", info.Meta.Presets)
	}
}

func TestRenameSet(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "cfg", "receivers: {}\n"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SetVar(root, "cfg", "prod", "HOST", "prod.example.com"); err != nil {
		t.Fatalf("SetVar: %v", err)
	}
	if err := UsePreset(root, "cfg", "prod"); err != nil {
		t.Fatalf("UsePreset: %v", err)
	}
	if err := SetVar(root, "cfg", "staging", "HOST", "staging.example.com"); err != nil {
		t.Fatalf("SetVar: %v", err)
	}

	// Renaming the active set follows it.
	if err := RenamePreset(root, "cfg", "prod", "production"); err != nil {
		t.Fatalf("RenamePreset: %v", err)
	}
	info, _, err := Get(root, "cfg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := info.Meta.Presets["prod"]; ok {
		t.Fatalf("old set name still present: %+v", info.Meta.Presets)
	}
	if info.Meta.Presets["production"]["HOST"] != "prod.example.com" {
		t.Fatalf("renamed set missing values: %+v", info.Meta.Presets)
	}
	if info.Meta.ActivePreset != "production" {
		t.Fatalf("ActivePreset = %q, want it to follow the rename", info.Meta.ActivePreset)
	}

	if err := RenamePreset(root, "cfg", "nonexistent", "x"); err == nil {
		t.Fatal("RenamePreset from a nonexistent set: want error, got nil")
	}
	if err := RenamePreset(root, "cfg", "staging", "production"); err == nil {
		t.Fatal("RenamePreset onto an existing set name: want error, got nil")
	}
	if err := RenamePreset(root, "cfg", "staging", ""); err == nil {
		t.Fatal("RenamePreset to an empty name: want error, got nil")
	}
}

func TestWriteSetCreatesAndReplaces(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "cfg", "receivers: {}\n"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := WritePreset(root, "cfg", "prod", map[string]any{"HOST": "prod.example.com"}); err != nil {
		t.Fatalf("WritePreset: %v", err)
	}
	info, _, err := Get(root, "cfg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(info.Meta.Presets["prod"]) != 1 || info.Meta.Presets["prod"]["HOST"] != "prod.example.com" {
		t.Fatalf("Presets after create = %+v", info.Meta.Presets)
	}

	// A second WritePreset replaces the whole set, not merges into it.
	if err := WritePreset(root, "cfg", "prod", map[string]any{"PORT": "443"}); err != nil {
		t.Fatalf("WritePreset (replace): %v", err)
	}
	info, _, err = Get(root, "cfg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := info.Meta.Presets["prod"]["HOST"]; ok {
		t.Fatalf("WritePreset merged instead of replacing: %+v", info.Meta.Presets["prod"])
	}
	if info.Meta.Presets["prod"]["PORT"] != "443" {
		t.Fatalf("Presets after replace = %+v", info.Meta.Presets)
	}

	if err := WritePreset(root, "missing", "prod", map[string]any{}); err == nil {
		t.Fatal("WritePreset on missing config: want error, got nil")
	}
}

func TestMaterializeDefaultsUpgradeRules(t *testing.T) {
	root := t.TempDir()

	// Fresh root: materializing creates the shipped otlp-basic config.
	if err := MaterializeDefaults(root); err != nil {
		t.Fatalf("MaterializeDefaults: %v", err)
	}
	info, yaml, err := Get(root, "otlp-basic")
	if err != nil {
		t.Fatalf("Get otlp-basic: %v", err)
	}
	if info.Provenance != "shipped" {
		t.Fatalf("provenance = %q, want shipped", info.Provenance)
	}
	if info.Modified {
		t.Fatalf("freshly materialized config reports Modified")
	}
	original := yaml

	// Idempotent: materializing again with no local edits and unchanged
	// embed leaves the config alone.
	if err := MaterializeDefaults(root); err != nil {
		t.Fatalf("MaterializeDefaults (2nd): %v", err)
	}
	info2, yaml2, err := Get(root, "otlp-basic")
	if err != nil {
		t.Fatalf("Get otlp-basic: %v", err)
	}
	if yaml2 != original || info2.Meta.PristineSHA256 != info.Meta.PristineSHA256 {
		t.Fatalf("unchanged re-materialize altered config: %+v", info2)
	}

	// Locally modify the config: hash no longer matches pristine.
	if err := WriteYAML(root, "otlp-basic", original+"# local edit\n"); err != nil {
		t.Fatalf("WriteYAML: %v", err)
	}
	modInfo, modYAML, err := Get(root, "otlp-basic")
	if err != nil {
		t.Fatalf("Get otlp-basic: %v", err)
	}
	if !modInfo.Modified {
		t.Fatalf("expected Modified=true after local edit")
	}

	// Materializing again must leave the modified config untouched (the
	// "leave untouched" branch of the upgrade rule).
	if err := MaterializeDefaults(root); err != nil {
		t.Fatalf("MaterializeDefaults (3rd): %v", err)
	}
	afterInfo, afterYAML, err := Get(root, "otlp-basic")
	if err != nil {
		t.Fatalf("Get otlp-basic: %v", err)
	}
	if afterYAML != modYAML {
		t.Fatalf("modified config was overwritten by MaterializeDefaults")
	}
	if !afterInfo.Modified {
		t.Fatalf("modified config lost Modified flag after MaterializeDefaults")
	}
}

func TestGetParsesVars(t *testing.T) {
	root := t.TempDir()
	if err := MaterializeDefaults(root); err != nil {
		t.Fatalf("MaterializeDefaults: %v", err)
	}
	info, _, err := Get(root, "otlp-basic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	byName := map[string]string{}
	for _, v := range info.Vars {
		byName[v.Name] = v.Description
	}
	want := map[string]string{
		"COMPY_GRPC_PORT": "compy's local gRPC port",
		"COMPY_HTTP_PORT": "compy's local HTTP port",
		"OTLP_ENDPOINT":   "where to send (base URL; the collector appends /v1/traces etc.)",
	}
	for name, desc := range want {
		got, ok := byName[name]
		if !ok {
			t.Errorf("var %s not parsed; got %+v", name, info.Vars)
			continue
		}
		if got != desc {
			t.Errorf("var %s description = %q, want %q", name, got, desc)
		}
	}
}

// Regression: every exported function that maps a config name to a path
// must reject path traversal before touching the filesystem.

// TestDeleteRejectsPathTraversal plants a sentinel *directory that looks
// like a real config* (config.yaml present) at the path a traversal name
// would resolve to, so that the pre-fix code's exists()-based guard would
// have been satisfied and os.RemoveAll would have fired on it. It proves
// validateName rejects the name before any filesystem access, not just
// incidentally via a "not found" on an unrelated path shape.
func TestDeleteRejectsPathTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "state")
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// "../../sentinel" from root/configs resolves to base/sentinel.
	sentinelDir := filepath.Join(base, "sentinel")
	if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinelYAML := filepath.Join(sentinelDir, "config.yaml")
	if err := os.WriteFile(sentinelYAML, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Delete(root, "../../sentinel"); err == nil {
		t.Fatal("Delete with traversal name: want error, got nil")
	}
	data, err := os.ReadFile(sentinelYAML)
	if err != nil {
		t.Fatalf("sentinel did not survive Delete: %v", err)
	}
	if string(data) != "keep me\n" {
		t.Fatalf("sentinel content changed: %q", data)
	}
}

// TestWriteYAMLRejectsPathTraversal plants a pre-existing file at the
// resolved traversal path (so the pre-fix exists()-based guard would have
// let the write through) and asserts the fixed code rejects the name before
// writing anything.
func TestWriteYAMLRejectsPathTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "state")
	if err := os.MkdirAll(filepath.Join(root, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// "../../outside" from root/configs resolves to base/outside.
	outsideDir := filepath.Join(base, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideYAML := filepath.Join(outsideDir, "config.yaml")
	if err := os.WriteFile(outsideYAML, []byte("original: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteYAML(root, "../../outside", "malicious: true\n"); err == nil {
		t.Fatal("WriteYAML with traversal name: want error, got nil")
	}
	data, err := os.ReadFile(outsideYAML)
	if err != nil {
		t.Fatalf("outside file vanished: %v", err)
	}
	if string(data) != "original: true\n" {
		t.Fatalf("traversal WriteYAML overwrote a file outside configs/: got %q", data)
	}
}

func TestSnapshotRestoreActive(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "settings.json")

	if err := RestoreActive(root); err == nil {
		t.Fatal("RestoreActive without a snapshot: want error, got nil")
	}

	if err := Create(root, "prod", "good: true\n"); err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "prod", "default", "KEY", "good"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"grpc_port":1,"active_config":"prod"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotActive(root, "prod"); err != nil {
		t.Fatalf("SnapshotActive: %v", err)
	}

	// Break both the config and settings after the snapshot.
	if err := WriteYAML(root, "prod", "bad: true\n"); err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "prod", "default", "KEY", "bad"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"grpc_port":2,"active_config":"prod"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RestoreActive(root); err != nil {
		t.Fatalf("RestoreActive: %v", err)
	}
	info, yaml, err := Get(root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if yaml != "good: true\n" {
		t.Errorf("yaml = %q, want the snapshotted content", yaml)
	}
	if info.Meta.Presets["default"]["KEY"] != "good" {
		t.Errorf("presets = %v, want the snapshotted values", info.Meta.Presets)
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"grpc_port":1`) {
		t.Errorf("settings.json = %s, want the snapshotted content", data)
	}
}

func TestRestoreActiveRejectsTraversalName(t *testing.T) {
	root := t.TempDir()
	snap := filepath.Join(root, "last-good")
	if err := os.MkdirAll(snap, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "settings.json"), []byte(`{"active_config":"../evil"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreActive(root); err == nil {
		t.Fatal("RestoreActive with a traversal active_config: want error, got nil")
	}
}

func TestAllNamedFunctionsRejectTraversal(t *testing.T) {
	root := t.TempDir()
	const bad = "../x"
	fetch := func(url string) ([]byte, error) { return []byte("x: 1\n"), nil }

	checks := []struct {
		name string
		call func() error
	}{
		{"Get", func() error { _, _, err := Get(root, bad); return err }},
		{"Create", func() error { return Create(root, bad, "x: 1\n") }},
		{"CreateFromURL", func() error { return CreateFromURL(root, bad, "https://example.com/x.yaml", fetch) }},
		{"Copy(src)", func() error { return Copy(root, bad, "dst") }},
		{"Copy(dst)", func() error { return Copy(root, "src", bad) }},
		{"Delete", func() error { return Delete(root, bad) }},
		{"WriteYAML", func() error { return WriteYAML(root, bad, "x: 1\n") }},
		{"WriteMeta", func() error { return WriteMeta(root, bad, Meta{}) }},
		{"Sync", func() error { return Sync(root, bad, fetch) }},
		{"Resync", func() error { return Resync(root, bad, fetch) }},
		{"Reset", func() error { return Reset(root, bad) }},
		{"Rename(from)", func() error { return Rename(root, bad, "dst") }},
		{"Rename(to)", func() error { return Rename(root, "src", bad) }},
		{"SetVar", func() error { return SetVar(root, bad, "set", "K", "V") }},
		{"WritePreset", func() error { return WritePreset(root, bad, "set", map[string]any{"K": "V"}) }},
		{"DeletePreset", func() error { return DeletePreset(root, bad, "set") }},
		{"UsePreset", func() error { return UsePreset(root, bad, "set") }},
		{"RenamePreset", func() error { return RenamePreset(root, bad, "set", "set2") }},
		{"SnapshotActive", func() error { return SnapshotActive(root, bad) }},
	}
	for _, c := range checks {
		if err := c.call(); err == nil {
			t.Errorf("%s(%q): traversal name accepted, want error", c.name, bad)
		}
	}
}

func TestResetRestoresShippedYAML(t *testing.T) {
	root := t.TempDir()
	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	_, shipped, err := Get(root, "otlp-basic")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "otlp-basic", "prod", "K", "V"); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAML(root, "otlp-basic", "edited: true\n"); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := Get(root, "otlp-basic"); !info.Modified {
		t.Fatal("setup: otlp-basic should be modified after WriteYAML")
	}

	if err := Reset(root, "otlp-basic"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	info, yaml, err := Get(root, "otlp-basic")
	if err != nil {
		t.Fatal(err)
	}
	if yaml != shipped {
		t.Fatalf("yaml after Reset = %q, want the shipped default", yaml)
	}
	if info.Modified {
		t.Fatal("Reset config still reports modified; pristine hash not recomputed")
	}
	if info.Provenance != "shipped" {
		t.Fatalf("provenance after Reset = %q, want shipped", info.Provenance)
	}
	if info.Meta.Presets["prod"]["K"] != "V" {
		t.Fatalf("presets after Reset = %+v, want K=V kept", info.Meta.Presets)
	}
}

func TestResetRefusals(t *testing.T) {
	root := t.TempDir()
	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}

	// Unmodified builtin: nothing to reset.
	if err := Reset(root, "debug"); err == nil || !state.IsBadRequest(err) {
		t.Fatalf("Reset unmodified builtin = %v, want BadRequest", err)
	}

	// Local config: not a builtin.
	if err := Create(root, "mine", "x: 1\n"); err != nil {
		t.Fatal(err)
	}
	if err := Reset(root, "mine"); err == nil || !state.IsBadRequest(err) {
		t.Fatalf("Reset local config = %v, want BadRequest", err)
	}

	// Remote config, even modified: resync is its path, not reset.
	fetch := func(url string) ([]byte, error) { return []byte("r: 1\n"), nil }
	if err := CreateFromURL(root, "linked", "https://example.com/c.yaml", fetch); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAML(root, "linked", "r: 2\n"); err != nil {
		t.Fatal(err)
	}
	if err := Reset(root, "linked"); err == nil || !state.IsBadRequest(err) {
		t.Fatalf("Reset remote config = %v, want BadRequest", err)
	}

	// Missing config.
	if err := Reset(root, "ghost"); err == nil {
		t.Fatal("Reset missing config: want error, got nil")
	}
}

func TestRenameConfig(t *testing.T) {
	root := t.TempDir()
	fetch := func(url string) ([]byte, error) { return []byte("r: 1\n"), nil }
	if err := CreateFromURL(root, "old", "https://example.com/c.yaml", fetch); err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "old", "prod", "K", "V"); err != nil {
		t.Fatal(err)
	}

	if err := Rename(root, "old", "new"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	info, yaml, err := Get(root, "new")
	if err != nil {
		t.Fatalf("Get renamed: %v", err)
	}
	if yaml != "r: 1\n" || info.Provenance != "remote" || info.Meta.Presets["prod"]["K"] != "V" {
		t.Fatalf("renamed config = %+v yaml %q, want provenance and presets kept", info, yaml)
	}
	if _, _, err := Get(root, "old"); err == nil {
		t.Fatal("Get old name after rename: want error, got nil")
	}

	// Collision: refused, both sides untouched.
	if err := Create(root, "other", "o: 1\n"); err != nil {
		t.Fatal(err)
	}
	if err := Rename(root, "new", "other"); err == nil || !state.IsBadRequest(err) {
		t.Fatalf("Rename onto existing = %v, want BadRequest", err)
	}
	if _, _, err := Get(root, "new"); err != nil {
		t.Fatalf("source gone after refused rename: %v", err)
	}
	if _, y, _ := Get(root, "other"); y != "o: 1\n" {
		t.Fatalf("collision target clobbered, yaml = %q", y)
	}

	// Missing source.
	if err := Rename(root, "ghost", "x"); err == nil || !state.IsBadRequest(err) {
		t.Fatalf("Rename missing source = %v, want BadRequest", err)
	}
}

// TestRenameShippedBecomesLocal: shipped identity is name-bound (Reset and
// the upgrade path read defaults/<name>.yaml), so a renamed builtin must
// become local — carrying the pristine hash to another name would reset or
// upgrade it against the wrong shipped YAML.
func TestRenameShippedBecomesLocal(t *testing.T) {
	root := t.TempDir()
	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "debug", "prod", "K", "V"); err != nil {
		t.Fatal(err)
	}
	_, shipped, err := Get(root, "debug")
	if err != nil {
		t.Fatal(err)
	}

	if err := Rename(root, "debug", "my-debug"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	info, yaml, err := Get(root, "my-debug")
	if err != nil {
		t.Fatal(err)
	}
	if info.Provenance != "local" || info.Modified {
		t.Errorf("renamed builtin = provenance %q modified %v, want a local config", info.Provenance, info.Modified)
	}
	if yaml != shipped || info.Meta.Presets["prod"]["K"] != "V" {
		t.Errorf("yaml/presets not kept across rename")
	}
	if err := Reset(root, "my-debug"); err == nil || !state.IsBadRequest(err) {
		t.Errorf("Reset on the renamed config = %v, want BadRequest (it is local now)", err)
	}
}

// TestMaterializeDefaultsLeavesNonShippedOnBuiltinName: a local (or remote)
// config occupying a builtin name — freed by delete or rename — is the
// user's; the upgrade path must never overwrite it.
func TestMaterializeDefaultsLeavesNonShippedOnBuiltinName(t *testing.T) {
	root := t.TempDir()
	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	if err := Delete(root, "otlp-basic"); err != nil {
		t.Fatal(err)
	}
	if err := Create(root, "otlp-basic", "receivers: {}\n# mine\n"); err != nil {
		t.Fatal(err)
	}

	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "otlp-basic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yaml, "# mine") || info.Provenance != "local" {
		t.Errorf("materialize overwrote the user's config on a builtin name: provenance %q yaml %q", info.Provenance, yaml)
	}
}

// TestReadMetaAcceptsLegacyKeys proves a v2 meta.json (variable_sets /
// active_set, plus a per-config distro that v3 dropped) still yields its
// presets, and that the distro key is silently ignored rather than
// resurrected on the next write.
func TestReadMetaAcceptsLegacyKeys(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "legacy", "x: 1\n"); err != nil {
		t.Fatal(err)
	}
	legacy := `{"distro":"contrib","variable_sets":{"prod":{"K":"V"}},"active_set":"prod"}`
	if err := os.WriteFile(filepath.Join(Dir(root), "legacy", "meta.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	info, _, err := Get(root, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.ActivePreset != "prod" || info.Meta.Presets["prod"]["K"] != "V" {
		t.Fatalf("meta = %+v, want active preset prod with K=V", info.Meta)
	}

	// Any write re-emits the file under the new key names, with no distro.
	if err := SetVar(root, "legacy", "prod", "K2", "V2"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(Dir(root), "legacy", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); !strings.Contains(s, `"presets"`) || strings.Contains(s, "distro") || strings.Contains(s, "variable_sets") {
		t.Fatalf("rewritten meta.json = %s, want presets/active_preset and no distro", s)
	}
}

func TestMissingRequired(t *testing.T) {
	// The activation pre-flight rule: required means has_default false,
	// not COMPY_-prefixed, and no non-empty value in the resolved preset.
	//
	// internal/webui/static/helpers.test.js mirrors this table verbatim
	// against the JS missingRequired — keep these tables identical.
	root := t.TempDir() // no config on disk: the tier-2 path
	info := Info{
		Meta: Meta{
			ActivePreset: "staging",
			Presets: map[string]map[string]any{
				"staging": {"BRONTO_KEY": "bro_live_1"},
				"empty":   {"BRONTO_KEY": "   "}, // whitespace is not a value
				"full":    {"BRONTO_KEY": "k", "OTLP_ENDPOINT": "e"},
			},
		},
		Vars: []vars.Var{
			{Name: "BRONTO_KEY"},    // required
			{Name: "OTLP_ENDPOINT"}, // required
			{Name: "DATASET", Default: "default", HasDefault: true}, // yaml fallback
			{Name: "COMPY_HTTP_PORT"},                               // compy-injected
		},
	}
	cases := []struct {
		name, preset string
		want         []string
	}{
		{"explicit preset with one value", "staging", []string{"OTLP_ENDPOINT"}},
		{"whitespace values count as missing", "empty", []string{"BRONTO_KEY", "OTLP_ENDPOINT"}},
		{"all values present", "full", nil},
		{"empty preset resolves to the active one", "", []string{"OTLP_ENDPOINT"}},
		{"unknown preset has no values", "nope", []string{"BRONTO_KEY", "OTLP_ENDPOINT"}},
	}
	for _, tc := range cases {
		got := MissingRequired(root, info, tc.preset)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: MissingRequired(%q) = %v, want %v", tc.name, tc.preset, got, tc.want)
		}
	}

	// No presets at all: nothing has a value, everything required is missing.
	bare := Info{Vars: info.Vars, Meta: Meta{Presets: map[string]map[string]any{}}}
	if got := MissingRequired(root, bare, ""); strings.Join(got, ",") != "BRONTO_KEY,OTLP_ENDPOINT" {
		t.Errorf("no presets: got %v, want BRONTO_KEY and OTLP_ENDPOINT", got)
	}
}

// TestMissingRequiredTier3: a config with a template source answers from
// its SCHEMA — required fields (secrets included) absent or blank in the
// preset's bag, named as field paths.
func TestMissingRequiredTier3(t *testing.T) {
	root := t.TempDir()
	src := `{"name": "t", "description": "d",
 "fields": [{"name": "endpoint", "type": "url", "label": "E"},
            {"name": "key", "type": "secret", "label": "K"}]}
---
a: {{.endpoint}}
`
	if err := CreateWithSource(root, "tpl", src, map[string]any{"endpoint": "https://x.example"}); err != nil {
		t.Fatal(err)
	}
	info, _, err := Get(root, "tpl")
	if err != nil {
		t.Fatal(err)
	}
	if got := MissingRequired(root, info, ""); strings.Join(got, ",") != "key" {
		t.Errorf("MissingRequired = %v, want the unset secret", got)
	}
	if err := SetVar(root, "tpl", info.Meta.ActivePreset, "key", "s3cret"); err != nil {
		t.Fatal(err)
	}
	info, _, _ = Get(root, "tpl")
	if got := MissingRequired(root, info, ""); got != nil {
		t.Errorf("filled bag still missing: %v", got)
	}
}

// Every creation path yields a config with the default preset present and
// active — the every-config-has-a-preset invariant.
func TestCreationPathsWriteDefaultPreset(t *testing.T) {
	wantDefault := func(t *testing.T, root, name string) {
		t.Helper()
		info, _, err := Get(root, name)
		if err != nil {
			t.Fatalf("Get %s: %v", name, err)
		}
		if len(info.Meta.Presets) == 0 {
			t.Fatalf("%s has no presets: %+v", name, info.Meta)
		}
		if _, ok := info.Meta.Presets[info.Meta.ActivePreset]; !ok {
			t.Fatalf("%s active preset %q does not exist: %+v", name, info.Meta.ActivePreset, info.Meta.Presets)
		}
	}

	root := t.TempDir()
	if err := Create(root, "local", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}
	wantDefault(t, root, "local")
	info, _, _ := Get(root, "local")
	if len(info.Meta.Presets) != 1 || len(info.Meta.Presets["default"]) != 0 || info.Meta.ActivePreset != "default" {
		t.Fatalf("Create meta = %+v, want just an empty active default preset", info.Meta)
	}

	fetch := func(url string) ([]byte, error) { return []byte("content: v1\n"), nil }
	if err := CreateFromURL(root, "remote", "https://example.com/c.yaml", fetch); err != nil {
		t.Fatal(err)
	}
	wantDefault(t, root, "remote")

	if err := Copy(root, "local", "copied"); err != nil {
		t.Fatal(err)
	}
	wantDefault(t, root, "copied")

	// Copy guarantees the invariant even from a hand-broken source.
	if err := os.WriteFile(metaPath(root, "local"), []byte(`{"presets":{},"active_preset":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Copy(root, "local", "copied-from-broken"); err != nil {
		t.Fatal(err)
	}
	wantDefault(t, root, "copied-from-broken")

	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"debug", "otlp-basic", "otlp-forward"} {
		wantDefault(t, root, name)
	}
}

// EnsurePresets repairs configs written before the invariant: zero presets
// gain {"default": {}} with active_preset "default", a dangling
// active_preset is repointed, and healthy configs are left byte-identical.
// Running it twice changes nothing (idempotent).
func TestEnsurePresetsBackfill(t *testing.T) {
	root := t.TempDir()

	write := func(name, meta string) {
		t.Helper()
		if err := os.MkdirAll(configDir(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(yamlPath(root, name), []byte("receivers: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if meta != "" {
			if err := os.WriteFile(metaPath(root, name), []byte(meta), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("empty", `{"presets":{},"active_preset":""}`)
	write("nometa", "")
	write("dangling", `{"presets":{"prod":{"K":"v"}},"active_preset":"gone"}`)
	write("healthy", `{"presets":{"prod":{"K":"v"}},"active_preset":"prod"}`)
	healthyBefore, err := os.ReadFile(metaPath(root, "healthy"))
	if err != nil {
		t.Fatal(err)
	}

	if err := EnsurePresets(root); err != nil {
		t.Fatalf("EnsurePresets: %v", err)
	}

	for _, name := range []string{"empty", "nometa"} {
		info, _, err := Get(root, name)
		if err != nil {
			t.Fatal(err)
		}
		if len(info.Meta.Presets) != 1 || len(info.Meta.Presets["default"]) != 0 || info.Meta.ActivePreset != "default" {
			t.Errorf("%s after backfill = %+v, want an empty active default preset", name, info.Meta)
		}
	}
	info, _, err := Get(root, "dangling")
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.ActivePreset != "prod" || info.Meta.Presets["prod"]["K"] != "v" {
		t.Errorf("dangling after backfill = %+v, want active repointed to prod", info.Meta)
	}
	if after, _ := os.ReadFile(metaPath(root, "healthy")); string(after) != string(healthyBefore) {
		t.Errorf("healthy meta rewritten by backfill:\n%s", after)
	}

	// Idempotent: a second run changes no file.
	before := map[string]string{}
	for _, name := range []string{"empty", "nometa", "dangling", "healthy"} {
		data, err := os.ReadFile(metaPath(root, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = string(data)
	}
	if err := EnsurePresets(root); err != nil {
		t.Fatalf("EnsurePresets (second run): %v", err)
	}
	for name, want := range before {
		if got, _ := os.ReadFile(metaPath(root, name)); string(got) != want {
			t.Errorf("%s changed on the second run:\n%s", name, got)
		}
	}
}

// The last preset cannot be deleted: a configuration keeps at least one.
func TestDeleteLastPresetRefused(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "cfg", "receivers: {}\n"); err != nil {
		t.Fatal(err)
	}

	err := DeletePreset(root, "cfg", "default")
	if err == nil || !strings.Contains(err.Error(), "at least one preset") {
		t.Fatalf("DeletePreset last = %v, want the keeps-at-least-one refusal", err)
	}
	if !state.IsBadRequest(err) {
		t.Errorf("DeletePreset last: not marked BadRequest: %v", err)
	}

	// With a second preset the non-active one deletes fine, and the survivor
	// is then refused again.
	if err := WritePreset(root, "cfg", "extra", map[string]any{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	if err := DeletePreset(root, "cfg", "extra"); err != nil {
		t.Fatalf("DeletePreset non-last: %v", err)
	}
	if err := DeletePreset(root, "cfg", "default"); err == nil {
		t.Fatal("DeletePreset survivor: want refusal, got nil")
	}
}

// HTTPFetch is the production Fetch behind CreateFromURL/Sync/SyncAll; its
// caps have never run under test: the happy path, a non-200 answer, and a
// body past the 5MB limit (which must be refused, not slurped).
func TestHTTPFetch(t *testing.T) {
	const limit = 5 * 1024 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.yaml":
			fmt.Fprint(w, "receivers: {}\n")
		case "/huge.yaml":
			w.Write(bytes.Repeat([]byte("a"), limit+1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	data, err := HTTPFetch(srv.URL + "/ok.yaml")
	if err != nil || string(data) != "receivers: {}\n" {
		t.Fatalf("HTTPFetch ok = %q, %v", data, err)
	}

	if _, err := HTTPFetch(srv.URL + "/gone.yaml"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("HTTPFetch 404 = %v, want an HTTP 404 error", err)
	}

	if _, err := HTTPFetch(srv.URL + "/huge.yaml"); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("HTTPFetch oversized = %v, want the 5MB-limit refusal", err)
	}
}

// An empty variable name — the CLI shape `compy presets set cfg prod
// =value` — is the caller's mistake (400-marked), never a nameless entry in
// the LaunchAgent environment (G2). WritePreset (the REST body path) guards
// the same way.
func TestEmptyVarKeyRefused(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "cfg", "receivers: {}\n"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, key := range []string{"", "  ", "\t"} {
		err := SetVar(root, "cfg", "prod", key, "value")
		if err == nil || !state.IsBadRequest(err) {
			t.Errorf("SetVar key %q = %v, want a BadRequest-marked error", key, err)
		}
	}
	err := WritePreset(root, "cfg", "prod", map[string]any{"": "value"})
	if err == nil || !state.IsBadRequest(err) {
		t.Errorf("WritePreset with empty key = %v, want a BadRequest-marked error", err)
	}
	info, _, err := Get(root, "cfg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := info.Meta.Presets["prod"]; ok {
		t.Fatalf("refused writes still created the preset: %+v", info.Meta.Presets)
	}
}

// testSource is a minimal tier-3 config source: one defaulted string knob.
const testSource = `{"name": "t", "description": "d",
 "fields": [{"name": "greeting", "type": "string", "label": "G", "default": "hello"}]}
---
a: {{.greeting}}
`

// TestCreateDetectsSource: content with template front matter creates a
// tier-3 config — source stored, yaml rendered with defaults, knobs
// recorded — while plain yaml stays tier 1. Pasting source into the yaml
// box IS how a templated config is born.
func TestCreateDetectsSource(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "tpl", testSource); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "tpl")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || info.Provenance != "local" || info.Modified {
		t.Errorf("info = %+v, want has_template local unmodified", info)
	}
	if yaml != "a: hello\n" {
		t.Errorf("rendered yaml = %q", yaml)
	}
	// The fresh default preset IS the schema defaults (Amendment 4).
	if info.Meta.Presets["default"]["greeting"] != "hello" || info.Meta.Knobs != nil {
		t.Errorf("meta = %+v, want the defaults seeded into the default preset", info.Meta)
	}
	src, ok, err := Source(root, "tpl")
	if err != nil || !ok || src != testSource {
		t.Errorf("Source = %q %v %v, want the stored source", src, ok, err)
	}

	if err := Create(root, "plain", "a: 1\n"); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := Get(root, "plain"); info.HasTemplate {
		t.Error("plain yaml misdetected as template source")
	}
	if _, ok, _ := Source(root, "plain"); ok {
		t.Error("plain config claims a source")
	}

	// A broken source creates nothing (parse/render run before any mkdir).
	if err := Create(root, "broken", "{\"name\": \"x\"\n---\nbody"); err == nil {
		t.Fatal("broken source created anyway")
	}
	if _, _, err := Get(root, "broken"); err == nil {
		t.Error("broken source left a config behind")
	}
}

// TestWriteYAMLDemotesTier3: plain yaml written over a templated config
// drops the source — editing is editing, no lock-in. The preset bags stay:
// they are the user's values, and a demotion must never delete a secret.
func TestWriteYAMLDemotesTier3(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "tpl", testSource); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAML(root, "tpl", "b: 2\n"); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "tpl")
	if err != nil {
		t.Fatal(err)
	}
	if info.HasTemplate || yaml != "b: 2\n" {
		t.Errorf("demote: has_template=%v yaml=%q", info.HasTemplate, yaml)
	}
	if info.Meta.Presets["default"]["greeting"] != "hello" {
		t.Errorf("preset bag lost in the demotion: %v", info.Meta.Presets)
	}
	if _, err := os.Stat(SourcePath(root, "tpl")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("config.tmpl survived the demotion: %v", err)
	}
}

// TestWriteSourceRestoresOnRenderedWriteFailure: the failure-atomicity
// promise — when the rendered write fails after the source landed, the
// prior source is put back, so a config never holds a source its yaml
// doesn't derive from.
func TestWriteSourceRestoresOnRenderedWriteFailure(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "tpl", testSource); err != nil {
		t.Fatal(err)
	}
	// Make the rendered write fail: a directory where config.yaml goes —
	// WriteFileAtomic's rename cannot replace it.
	if err := os.Remove(yamlPath(root, "tpl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(yamlPath(root, "tpl"), 0o755); err != nil {
		t.Fatal(err)
	}
	newSrc := strings.Replace(testSource, "hello", "goodbye", 1)
	if err := WriteSource(root, "tpl", newSrc, "a: goodbye\n"); err == nil {
		t.Fatal("WriteSource with unwritable yaml = nil, want error")
	}
	if src, _ := readSource(root, "tpl"); src != testSource {
		t.Errorf("source not restored after rendered-write failure: %q", src)
	}
	m, err := readMeta(root, "tpl")
	if err != nil {
		t.Fatal(err)
	}
	if m.Presets["default"]["greeting"] != "hello" {
		t.Errorf("presets changed despite the failed save: %v", m.Presets)
	}
}

// TestCopyCarriesSourcePair: Copy duplicates the source along with yaml
// and presets (bags included); provenance is dropped as ever.
func TestCopyCarriesSourcePair(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "tpl", testSource); err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "tpl", "prod", "K", "v"); err != nil {
		t.Fatal(err)
	}
	if err := Copy(root, "tpl", "dup"); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "dup")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || info.Provenance != "local" {
		t.Errorf("copy info = %+v, want has_template local", info)
	}
	if yaml != "a: hello\n" || info.Meta.Presets["default"]["greeting"] != "hello" {
		t.Errorf("copy pair: yaml=%q presets=%v", yaml, info.Meta.Presets)
	}
	if src, ok, _ := Source(root, "dup"); !ok || src != testSource {
		t.Errorf("copy source = %q %v", src, ok)
	}
	if info.Meta.Presets["prod"]["K"] != "v" {
		t.Errorf("presets lost: %v", info.Meta.Presets)
	}
}

// TestSyncRemoteTier3 walks the remote-source lifecycle: a fetched body
// with front matter creates/updates the SOURCE (pristine hash covers it),
// local knobs survive a schema change (removed pruned, new defaulted), and
// a remote that turns plain demotes the config.
func TestSyncRemoteTier3(t *testing.T) {
	root := t.TempDir()
	remote := testSource
	fetch := func(url string) ([]byte, error) { return []byte(remote), nil }

	if err := CreateFromURL(root, "r", "https://example.com/c", fetch); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "r")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || info.Provenance != "remote" || info.Modified {
		t.Errorf("info = %+v, want has_template remote unmodified", info)
	}
	if yaml != "a: hello\n" {
		t.Errorf("rendered = %q", yaml)
	}

	// A local preset value change does NOT make it modified: the SOURCE
	// carries the pristine hash, and preset bags are local state a sync must
	// respect.
	if err := SetVar(root, "r", "default", "greeting", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "r", "other", "greeting", "yo"); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := Get(root, "r"); info.Modified {
		t.Error("preset-only change flagged the source as modified")
	}

	// The remote's schema evolves: greeting is gone, shout arrives. Sync
	// reconciles EVERY preset's bag — the stored greeting pruned, the new
	// field defaulted, per preset — and re-renders from the active bag.
	remote = `{"name": "t", "description": "d",
 "fields": [{"name": "shout", "type": "toggle", "label": "S", "default": true}]}
---
b: {{.shout}}
`
	if err := Sync(root, "r", fetch); err != nil {
		t.Fatalf("Sync of tier-3 remote: %v", err)
	}
	info, yaml, err = Get(root, "r")
	if err != nil {
		t.Fatal(err)
	}
	if yaml != "b: true\n" {
		t.Errorf("synced render = %q", yaml)
	}
	if info.Modified {
		t.Error("fresh sync reports modified — pristine hash not moved to the new source")
	}
	for _, preset := range []string{"default", "other"} {
		bag := info.Meta.Presets[preset]
		if _, has := bag["greeting"]; has {
			t.Errorf("%s: removed field survived the sync: %v", preset, bag)
		}
		if bag["shout"] != true {
			t.Errorf("%s: new field not defaulted: %v", preset, bag)
		}
	}

	// An edited SOURCE is a modified config: sync refuses, resync discards.
	edited := strings.Replace(remote, "b: ", "c: ", 1)
	if err := WriteSource(root, "r", edited, "c: true\n"); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := Get(root, "r"); !info.Modified {
		t.Fatal("edited source not reported modified")
	}
	if err := Sync(root, "r", fetch); err == nil || !state.IsBadRequest(err) {
		t.Errorf("Sync of modified source = %v, want BadRequest", err)
	}
	if err := Resync(root, "r", fetch); err != nil {
		t.Fatal(err)
	}
	if src, _, _ := Source(root, "r"); src != remote {
		t.Error("resync did not restore the remote source")
	}

	// The remote turns plain: the source goes, the config is tier 1 again.
	remote = "plain: true\n"
	if err := Sync(root, "r", fetch); err != nil {
		t.Fatal(err)
	}
	info, yaml, err = Get(root, "r")
	if err != nil {
		t.Fatal(err)
	}
	if info.HasTemplate || yaml != "plain: true\n" || info.Modified || info.Meta.Knobs != nil {
		t.Errorf("plain sync: %+v yaml=%q", info, yaml)
	}
}

// TestSnapshotRestoreTier3: last-good captures the whole pair — source,
// rendered yaml, preset bags — and restore brings all of it back.
func TestSnapshotRestoreTier3(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "tpl", testSource); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"active_config":"tpl"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotActive(root, "tpl"); err != nil {
		t.Fatal(err)
	}
	// Wreck it: demote to plain, different yaml.
	if err := WriteYAML(root, "tpl", "wrecked: true\n"); err != nil {
		t.Fatal(err)
	}
	if err := RestoreActive(root); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "tpl")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || yaml != "a: hello\n" || info.Meta.Presets["default"]["greeting"] != "hello" {
		t.Errorf("restore lost the pair: %+v yaml=%q", info, yaml)
	}
	if src, ok, _ := Source(root, "tpl"); !ok || src != testSource {
		t.Errorf("restored source = %q %v", src, ok)
	}
}

// TestEnsurePresetsStripsWizardMeta: a wizard-era template-born config
// (meta.template + knobs + pristine hash, plain rendered yaml, no
// config.tmpl) is simply a tier-1/2 config now — the backfill strips the
// dead marker so no UI shows dead affordances.
func TestEnsurePresetsStripsWizardMeta(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "wiz", "a: 1\n"); err != nil {
		t.Fatal(err)
	}
	meta := `{"pristine_sha256": "abc", "template": "custom-endpoints",
 "knobs": {"debug_tee": true},
 "presets": {"default": {}}, "active_preset": "default"}`
	if err := os.WriteFile(metaPath(root, "wiz"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePresets(root); err != nil {
		t.Fatal(err)
	}
	info, _, err := Get(root, "wiz")
	if err != nil {
		t.Fatal(err)
	}
	if info.Provenance != "local" || info.Modified || info.HasTemplate {
		t.Errorf("wizard config after backfill = %+v, want plain local", info)
	}
	if info.Meta.Template != "" || info.Meta.Knobs != nil || info.Meta.PristineSHA256 != "" {
		t.Errorf("wizard meta not stripped: %+v", info.Meta)
	}
}

// TestEnsurePresetsMigratesKnobs is the Amendment 4 migration: a tier-3
// config's options-era knobs merge into EVERY preset's bag — existing
// preset values win — and the knobs key is deleted. Idempotent: the second
// run rewrites nothing.
func TestEnsurePresetsMigratesKnobs(t *testing.T) {
	root := t.TempDir()
	if err := Create(root, "tpl", testSource); err != nil {
		t.Fatal(err)
	}
	meta := `{"presets": {"default": {}, "prod": {"greeting": "kept", "KEY": "s"}},
 "active_preset": "default",
 "knobs": {"greeting": "hello"}}`
	if err := os.WriteFile(metaPath(root, "tpl"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePresets(root); err != nil {
		t.Fatal(err)
	}
	info, _, err := Get(root, "tpl")
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.Knobs != nil {
		t.Errorf("knobs survived the migration: %v", info.Meta.Knobs)
	}
	if info.Meta.Presets["default"]["greeting"] != "hello" {
		t.Errorf("knobs not merged into default: %v", info.Meta.Presets)
	}
	if info.Meta.Presets["prod"]["greeting"] != "kept" || info.Meta.Presets["prod"]["KEY"] != "s" {
		t.Errorf("existing preset values did not win: %v", info.Meta.Presets["prod"])
	}
	before, err := os.ReadFile(metaPath(root, "tpl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsurePresets(root); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(metaPath(root, "tpl")); string(after) != string(before) {
		t.Errorf("second run rewrote meta:\n%s", after)
	}
}

// plantShipped writes a config exactly as an older compy's plain
// materialization did: yaml + a pristine hash over it, provenance shipped.
func plantShipped(t *testing.T, root, name, yaml string, presets map[string]map[string]any) {
	t.Helper()
	if err := createDir(root, name); err != nil {
		t.Fatal(err)
	}
	if err := writeYAMLFile(root, name, yaml); err != nil {
		t.Fatal(err)
	}
	m := withDefaultPreset(Meta{PristineSHA256: hashOf(yaml), Presets: presets})
	if err := writeMeta(root, name, m); err != nil {
		t.Fatal(err)
	}
}

// TestMaterializeTemplatedDefaults: a shipped catalog template materializes
// as a shipped TEMPLATED config — source copied in, default preset seeded
// with the schema's normalized defaults, config.yaml a render of them,
// pristine hash on the SOURCE. Idempotent.
func TestMaterializeTemplatedDefaults(t *testing.T) {
	root := t.TempDir()
	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "otlp-forward")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || info.Provenance != "shipped" || info.Modified {
		t.Fatalf("otlp-forward = %+v, want an unmodified shipped templated config", info)
	}
	entry, err := catalog.Get("otlp-forward")
	if err != nil {
		t.Fatal(err)
	}
	if src, ok := readSource(root, "otlp-forward"); !ok || src != entry.Source() {
		t.Error("source is not a copy of the embedded template")
	}
	bag := info.Meta.Presets[DefaultPreset]
	rows, _ := bag["backends"].([]any)
	if len(rows) != 1 {
		t.Fatalf("default preset backends = %v, want the one seeded row", bag)
	}
	row, _ := rows[0].(map[string]any)
	if row[catalog.LabelKey] != "backend 1" || row["auth_header"] != "Authorization" {
		t.Errorf("seeded row = %v, want the schema defaults", row)
	}
	for _, want := range []string{"otlp_http/backend-1", "${env:BACKEND_1_API_KEY:-}", "memory_limiter"} {
		if !strings.Contains(yaml, want) {
			t.Errorf("rendered yaml missing %q:\n%s", want, yaml)
		}
	}

	// Idempotent: a second materialize changes nothing.
	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	info2, yaml2, err := Get(root, "otlp-forward")
	if err != nil {
		t.Fatal(err)
	}
	if yaml2 != yaml || info2.Meta.PristineSHA256 != info.Meta.PristineSHA256 {
		t.Error("re-materialize altered the templated config")
	}
}

// TestMaterializeUpgradesPlainShippedToTemplated: an UNMODIFIED plain
// shipped config whose default is now templated (the old bronto) upgrades
// in place — source written, every preset's bag reconciled (repeat group
// seeded from defaults), render regenerated, hash moved to the source. A
// modified one is left exactly as it is.
func TestMaterializeUpgradesPlainShippedToTemplated(t *testing.T) {
	root := t.TempDir()
	plantShipped(t, root, "otlp-forward", "receivers: {}\n# the old plain forward\n",
		map[string]map[string]any{"default": {}, "prod": {"OLD": "v"}})

	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "otlp-forward")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || info.Provenance != "shipped" || info.Modified {
		t.Fatalf("upgraded otlp-forward = %+v, want an unmodified shipped templated config", info)
	}
	if !strings.Contains(yaml, "otlp_http/backend-1") {
		t.Errorf("upgrade did not re-render:\n%s", yaml)
	}
	for _, preset := range []string{"default", "prod"} {
		rows, _ := info.Meta.Presets[preset]["backends"].([]any)
		if len(rows) != 1 {
			t.Errorf("%s: bag not reconciled with the schema: %v", preset, info.Meta.Presets[preset])
		}
	}

	// Modified: untouched, still plain.
	root2 := t.TempDir()
	edited := "receivers: {}\n# the old plain forward\n"
	plantShipped(t, root2, "otlp-forward", edited, nil)
	if err := WriteYAML(root2, "otlp-forward", edited+"# my edit\n"); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeDefaults(root2); err != nil {
		t.Fatal(err)
	}
	info, yaml, err = Get(root2, "otlp-forward")
	if err != nil {
		t.Fatal(err)
	}
	if info.HasTemplate || !info.Modified || !strings.Contains(yaml, "# my edit") {
		t.Errorf("modified plain otlp-forward was touched: %+v yaml=%q", info, yaml)
	}
}

// TestMaterializeRetiresDroppedShipped: a shipped-provenance config that is
// unmodified, inactive, and no longer among the shipped defaults (the old
// otlp) is deleted; the ACTIVE one stays, as does a modified one.
func TestMaterializeRetiresDroppedShipped(t *testing.T) {
	root := t.TempDir()
	old := "receivers: {}\n# the old otlp\n"
	plantShipped(t, root, "otlp", old, nil)
	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Get(root, "otlp"); err == nil {
		t.Error("retired otlp still exists")
	}

	// Active: kept.
	root2 := t.TempDir()
	plantShipped(t, root2, "otlp", old, nil)
	if err := os.WriteFile(filepath.Join(root2, "settings.json"),
		[]byte(`{"active_config": "otlp"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeDefaults(root2); err != nil {
		t.Fatal(err)
	}
	if _, yaml, err := Get(root2, "otlp"); err != nil || yaml != old {
		t.Errorf("active old otlp was touched: %v %q", err, yaml)
	}

	// Modified: kept.
	root3 := t.TempDir()
	plantShipped(t, root3, "otlp", old, nil)
	if err := WriteYAML(root3, "otlp", old+"# my edit\n"); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeDefaults(root3); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Get(root3, "otlp"); err != nil {
		t.Errorf("modified old otlp was retired: %v", err)
	}
}

// TestResetTemplatedShipped: Reset on a shipped templated config restores
// source + render + pristine hash from the embedded template — here after a
// demotion (plain yaml pasted over it), the strongest modification — with
// presets kept (reconciled).
func TestResetTemplatedShipped(t *testing.T) {
	root := t.TempDir()
	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	_, shipped, err := Get(root, "otlp-forward")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "otlp-forward", "prod", "K", "V"); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAML(root, "otlp-forward", "edited: true\n"); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := Get(root, "otlp-forward"); info.HasTemplate || !info.Modified {
		t.Fatalf("setup: otlp-forward should be a demoted, modified config: %+v", info)
	}

	if err := Reset(root, "otlp-forward"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	info, yaml, err := Get(root, "otlp-forward")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || info.Modified || info.Provenance != "shipped" {
		t.Errorf("reset otlp-forward = %+v, want an unmodified shipped templated config", info)
	}
	if yaml != shipped {
		t.Errorf("yaml after Reset = %q, want the shipped render", yaml)
	}
	if _, ok := info.Meta.Presets["prod"]; !ok {
		t.Errorf("presets lost in Reset: %v", info.Meta.Presets)
	}
}
