package cfgstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := UseSet(root, "src", "default"); err != nil {
		t.Fatalf("UseSet: %v", err)
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
	if info.Meta.VariableSets["default"]["KEY"] != "value" {
		t.Fatalf("dst variable sets = %+v, want copied KEY=value", info.Meta.VariableSets)
	}
	if info.Meta.ActiveSet != "default" {
		t.Fatalf("dst ActiveSet = %q, want default", info.Meta.ActiveSet)
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
	if info.Meta.VariableSets["prod"]["HOST"] != "prod.example.com" {
		t.Fatalf("VariableSets = %+v", info.Meta.VariableSets)
	}

	if err := UseSet(root, "cfg", "nonexistent"); err == nil {
		t.Fatal("UseSet unknown set: want error, got nil")
	}

	if err := UseSet(root, "cfg", "prod"); err != nil {
		t.Fatalf("UseSet: %v", err)
	}

	if err := DeleteSet(root, "cfg", "prod"); err == nil {
		t.Fatal("DeleteSet active set: want error, got nil")
	}

	if err := SetVar(root, "cfg", "staging", "HOST", "staging.example.com"); err != nil {
		t.Fatalf("SetVar: %v", err)
	}
	if err := DeleteSet(root, "cfg", "staging"); err != nil {
		t.Fatalf("DeleteSet non-active: %v", err)
	}
	info, _, err = Get(root, "cfg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := info.Meta.VariableSets["staging"]; ok {
		t.Fatalf("staging set still present after delete: %+v", info.Meta.VariableSets)
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
	if info.Meta.VariableSets["default"]["KEY"] != "good" {
		t.Errorf("variable sets = %v, want the snapshotted values", info.Meta.VariableSets)
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
		{"SetVar", func() error { return SetVar(root, bad, "set", "K", "V") }},
		{"DeleteSet", func() error { return DeleteSet(root, bad, "set") }},
		{"UseSet", func() error { return UseSet(root, bad, "set") }},
		{"SnapshotActive", func() error { return SnapshotActive(root, bad) }},
	}
	for _, c := range checks {
		if err := c.call(); err == nil {
			t.Errorf("%s(%q): traversal name accepted, want error", c.name, bad)
		}
	}
}
