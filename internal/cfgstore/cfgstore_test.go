package cfgstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bronto-io/compy/internal/state"
	"github.com/bronto-io/compy/internal/vars"
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

	if err := WritePreset(root, "cfg", "prod", map[string]string{"HOST": "prod.example.com"}); err != nil {
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
	if err := WritePreset(root, "cfg", "prod", map[string]string{"PORT": "443"}); err != nil {
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

	if err := WritePreset(root, "missing", "prod", map[string]string{}); err == nil {
		t.Fatal("WritePreset on missing config: want error, got nil")
	}
}

func TestMaterializeDefaultsUpgradeRules(t *testing.T) {
	root := t.TempDir()

	// Fresh root: materializing creates the shipped debug config.
	if err := MaterializeDefaults(root); err != nil {
		t.Fatalf("MaterializeDefaults: %v", err)
	}
	info, yaml, err := Get(root, "debug")
	if err != nil {
		t.Fatalf("Get debug: %v", err)
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
	info2, yaml2, err := Get(root, "debug")
	if err != nil {
		t.Fatalf("Get debug: %v", err)
	}
	if yaml2 != original || info2.Meta.PristineSHA256 != info.Meta.PristineSHA256 {
		t.Fatalf("unchanged re-materialize altered config: %+v", info2)
	}

	// Locally modify the config: hash no longer matches pristine.
	if err := WriteYAML(root, "debug", original+"# local edit\n"); err != nil {
		t.Fatalf("WriteYAML: %v", err)
	}
	modInfo, modYAML, err := Get(root, "debug")
	if err != nil {
		t.Fatalf("Get debug: %v", err)
	}
	if !modInfo.Modified {
		t.Fatalf("expected Modified=true after local edit")
	}

	// Materializing again must leave the modified config untouched (the
	// "leave untouched" branch of the upgrade rule).
	if err := MaterializeDefaults(root); err != nil {
		t.Fatalf("MaterializeDefaults (3rd): %v", err)
	}
	afterInfo, afterYAML, err := Get(root, "debug")
	if err != nil {
		t.Fatalf("Get debug: %v", err)
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
	info, _, err := Get(root, "debug")
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
		"DEBUG_VERBOSITY": "basic | normal | detailed",
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
		{"WritePreset", func() error { return WritePreset(root, bad, "set", map[string]string{"K": "V"}) }},
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
	_, shipped, err := Get(root, "debug")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetVar(root, "debug", "prod", "K", "V"); err != nil {
		t.Fatal(err)
	}
	if err := WriteYAML(root, "debug", "edited: true\n"); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := Get(root, "debug"); !info.Modified {
		t.Fatal("setup: debug should be modified after WriteYAML")
	}

	if err := Reset(root, "debug"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	info, yaml, err := Get(root, "debug")
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
	if err := Delete(root, "otlp"); err != nil {
		t.Fatal(err)
	}
	if err := Create(root, "otlp", "receivers: {}\n# mine\n"); err != nil {
		t.Fatal(err)
	}

	if err := MaterializeDefaults(root); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := Get(root, "otlp")
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
	info := Info{
		Meta: Meta{
			ActivePreset: "staging",
			Presets: map[string]map[string]string{
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
		got := MissingRequired(info, tc.preset)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: MissingRequired(%q) = %v, want %v", tc.name, tc.preset, got, tc.want)
		}
	}

	// No presets at all: nothing has a value, everything required is missing.
	bare := Info{Vars: info.Vars, Meta: Meta{Presets: map[string]map[string]string{}}}
	if got := MissingRequired(bare, ""); strings.Join(got, ",") != "BRONTO_KEY,OTLP_ENDPOINT" {
		t.Errorf("no presets: got %v, want BRONTO_KEY and OTLP_ENDPOINT", got)
	}
}
