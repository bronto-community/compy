package distro

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/state"
)

// buildTarGz packs a single regular file named binName with the given
// content into an in-memory tar.gz, mirroring the layout of the real
// upstream release archives (binary at the archive root).
func buildTarGz(t *testing.T, binName, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: binName,
		Mode: 0o755,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestEnsureDownloadsVerifiesExtracts(t *testing.T) {
	tarGz := buildTarGz(t, "otelcol-fake", "#!/bin/sh\necho fake\n")
	sha := sha256Hex(tarGz)
	plat := runtime.GOOS + "_" + runtime.GOARCH
	d := Def{
		Name:    "fake",
		Version: "1.0.0",
		Binary:  "otelcol-fake",
		URLs:    map[string]string{plat: "https://example.invalid/fake.tar.gz"},
		SHA256:  map[string]string{plat: sha},
	}

	calls := 0
	fetch := func(url string) (io.ReadCloser, int64, error) {
		calls++
		if url != d.URLs[plat] {
			t.Fatalf("fetch called with unexpected url %q", url)
		}
		return io.NopCloser(bytes.NewReader(tarGz)), int64(len(tarGz)), nil
	}

	root := t.TempDir()
	var lastDone, lastTotal int64
	path, err := Ensure(root, d, fetch, func(done, total int64) { lastDone, lastTotal = done, total })
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	wantPath := filepath.Join(root, "distros", "fake-1.0.0", "otelcol-fake")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat installed binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary not executable: mode %v", info.Mode())
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times, want 1", calls)
	}
	if lastDone != int64(len(tarGz)) || lastTotal != int64(len(tarGz)) {
		t.Fatalf("progress ended at %d/%d, want %d/%d", lastDone, lastTotal, len(tarGz), len(tarGz))
	}

	// Second call: idempotent, no fetch — and no progress to report.
	path2, err := Ensure(root, d, fetch, nil)
	if err != nil {
		t.Fatalf("Ensure (2nd): %v", err)
	}
	if path2 != wantPath {
		t.Fatalf("2nd path = %q, want %q", path2, wantPath)
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times after 2nd Ensure, want still 1", calls)
	}
}

func TestEnsureChecksumMismatch(t *testing.T) {
	tarGz := buildTarGz(t, "otelcol-fake", "content")
	plat := runtime.GOOS + "_" + runtime.GOARCH
	d := Def{
		Name:    "fake",
		Version: "1.0.0",
		Binary:  "otelcol-fake",
		URLs:    map[string]string{plat: "https://example.invalid/fake.tar.gz"},
		SHA256:  map[string]string{plat: strings.Repeat("0", 64)},
	}
	fetch := func(url string) (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(tarGz)), int64(len(tarGz)), nil
	}

	root := t.TempDir()
	_, err := Ensure(root, d, fetch, nil)
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	wantExpected := d.SHA256[plat]
	gotSHA := sha256Hex(tarGz)
	if !strings.Contains(err.Error(), wantExpected) {
		t.Errorf("error %q does not contain expected sha %q", err, wantExpected)
	}
	if !strings.Contains(err.Error(), gotSHA) {
		t.Errorf("error %q does not contain got sha %q", err, gotSHA)
	}

	dir := filepath.Join(root, "distros", "fake-1.0.0")
	if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected no partial files at %q, stat err = %v", dir, statErr)
	}
}

func TestAvailable(t *testing.T) {
	plat := runtime.GOOS + "_" + runtime.GOARCH
	withCurrent := Def{Name: "x", URLs: map[string]string{plat: "https://example.invalid/x.tar.gz"}}
	if !Available(withCurrent) {
		t.Error("expected Available true for def with current platform URL")
	}

	withoutCurrent := Def{Name: "y", URLs: map[string]string{"nonexistent_arch": "https://example.invalid/y.tar.gz"}}
	if Available(withoutCurrent) {
		t.Error("expected Available false for def without current platform URL")
	}

	empty := Def{Name: "no-binaries", URLs: map[string]string{}}
	if Available(empty) {
		t.Error("expected Available false for def with no URLs")
	}
}

func TestRegistryMergesUserOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPY_HOME", home)

	// Override "core" (a real Defs() entry) with a user path, and add a
	// user-only entry not in Defs().
	overridePath := filepath.Join(home, "custom-otelcol")
	userOnly := state.Distro{Name: "my-custom", Path: "/opt/my-custom/bin"}
	override := state.Distro{Name: "core", Path: overridePath}
	if err := state.SaveDistros([]state.Distro{override, userOnly}); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	got, err := Registry(root)
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}

	byName := make(map[string]state.Distro, len(got))
	for _, d := range got {
		byName[d.Name] = d
	}

	if d, ok := byName["core"]; !ok || d.Path != overridePath {
		t.Errorf("core entry = %+v, want Path %q", d, overridePath)
	}
	if d, ok := byName["my-custom"]; !ok || d.Path != "/opt/my-custom/bin" {
		t.Errorf("my-custom entry = %+v", d)
	}
	// contrib is a Def not overridden by the user: should appear with
	// Path == "" since nothing has been downloaded into root.
	if d, ok := byName["contrib"]; !ok || d.Path != "" {
		t.Errorf("contrib entry = %+v, want empty Path (not downloaded)", d)
	}
}
