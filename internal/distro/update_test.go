package distro

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bronto-io/compy/internal/state"
)

func jsonBody(s string) (io.ReadCloser, int64, error) {
	return io.NopCloser(strings.NewReader(s)), int64(len(s)), nil
}

func TestLatestVersionSkipsToolTagsAndPrereleases(t *testing.T) {
	// Real tag scheme (live API, 2026-08-27): the repo interleaves
	// cmd/builder/v0.x.y and cmd/opampsupervisor/v0.x.y tags with the
	// collector's own v0.x.y, newest first.
	fetch := func(url string) (io.ReadCloser, int64, error) {
		if url != releasesAPI {
			t.Fatalf("unexpected url %q", url)
		}
		return jsonBody(`[
			{"tag_name":"cmd/builder/v0.161.0","prerelease":false},
			{"tag_name":"v0.161.0","prerelease":true},
			{"tag_name":"cmd/opampsupervisor/v0.160.0","prerelease":false},
			{"tag_name":"v0.160.0","prerelease":false},
			{"tag_name":"v0.159.0","prerelease":false}
		]`)
	}
	v, err := LatestVersion(fetch)
	if err != nil {
		t.Fatal(err)
	}
	if v != "0.160.0" {
		t.Fatalf("latest = %q, want 0.160.0", v)
	}
}

func TestLatestVersionHonestErrors(t *testing.T) {
	if _, err := LatestVersion(func(string) (io.ReadCloser, int64, error) {
		return nil, 0, errors.New("rate limited")
	}); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("want fetch error surfaced, got %v", err)
	}
	if _, err := LatestVersion(func(string) (io.ReadCloser, int64, error) {
		return jsonBody(`[{"tag_name":"cmd/builder/v0.161.0","prerelease":false}]`)
	}); err == nil {
		t.Fatal("want error when no collector release is present")
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.161.0", "0.135.0", true},  // newer
		{"0.135.0", "0.135.0", false}, // equal
		{"0.135.0", "0.161.0", false}, // older
		{"1.0.0", "0.161.0", true},    // major beats minor
		{"0.135.1", "0.135.0", true},  // patch bump
		{"0.161", "0.135.0", true},    // short form, missing part = 0
		{"0.135.0", "0.135", false},   // equal via missing part
		{"0.161.0", "unknown", false}, // bundled stamp fallback: no claim
		{"0.161.0", "", false},        // user-managed rows have no version
		{"", "0.135.0", false},        // no check result yet
		{"garbage", "0.135.0", false}, // malformed latest
		{"0.161.0", "v0.135.0", false},
		{"0.-1.0", "0.135.0", false},
	}
	for _, c := range cases {
		if got := NewerVersion(c.latest, c.current); got != c.want {
			t.Errorf("NewerVersion(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestDefForVersion(t *testing.T) {
	base := Def{Name: "otlp", Version: "0.135.0", Binary: "otelcol-otlp",
		URLs: map[string]string{"darwin_arm64": "x", "linux_amd64": "y"}}
	d := DefForVersion(base, "0.160.0")
	want := "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v0.160.0/otelcol-otlp_0.160.0_darwin_arm64.tar.gz"
	if d.URLs["darwin_arm64"] != want {
		t.Fatalf("url = %q, want %q", d.URLs["darwin_arm64"], want)
	}
	if d.Version != "0.160.0" || len(d.SHA256) != 0 {
		t.Fatalf("bad def: %+v", d)
	}
}

func TestEnsureVersionVerifiesAgainstReleaseChecksum(t *testing.T) {
	tarGz := buildTarGz(t, "otelcol-otlp", "#!/bin/sh\necho v2\n")
	plat := runtime.GOOS + "_" + runtime.GOARCH
	base := Def{Name: "otlp", Version: "0.135.0", Binary: "otelcol-otlp",
		URLs: map[string]string{plat: "https://example.invalid/old.tar.gz"}}
	tarURL := DefForVersion(base, "0.160.0").URLs[plat]

	fetch := func(url string) (io.ReadCloser, int64, error) {
		switch url {
		case tarURL + ".sha256":
			return jsonBody(sha256Hex(tarGz)) // bare hex, as upstream publishes
		case tarURL:
			return io.NopCloser(bytes.NewReader(tarGz)), int64(len(tarGz)), nil
		}
		t.Fatalf("unexpected url %q", url)
		return nil, 0, nil
	}
	root := t.TempDir()
	path, err := EnsureVersion(root, base, "0.160.0", fetch, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "distros", "otlp-0.160.0", "otelcol-otlp")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	// A tampered checksum asset must refuse the download.
	badFetch := func(url string) (io.ReadCloser, int64, error) {
		if strings.HasSuffix(url, ".sha256") {
			return jsonBody(strings.Repeat("0", 64))
		}
		return io.NopCloser(bytes.NewReader(tarGz)), int64(len(tarGz)), nil
	}
	if _, err := EnsureVersion(t.TempDir(), base, "0.160.0", badFetch, nil); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}

	// A malformed checksum asset (an HTML error page, say) is refused too.
	if _, err := EnsureVersion(t.TempDir(), base, "0.160.0", func(url string) (io.ReadCloser, int64, error) {
		return jsonBody("<html>not found</html>")
	}, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want malformed checksum error, got %v", err)
	}

	// The pinned version takes the compiled-in path (no .sha256 fetch).
	pinned := Def{Name: "otlp", Version: "0.135.0", Binary: "otelcol-otlp",
		URLs:   map[string]string{plat: "https://example.invalid/old.tar.gz"},
		SHA256: map[string]string{plat: sha256Hex(tarGz)}}
	path, err = EnsureVersion(t.TempDir(), pinned, "0.135.0", func(url string) (io.ReadCloser, int64, error) {
		if url != pinned.URLs[plat] {
			t.Fatalf("pinned ensure fetched %q", url)
		}
		return io.NopCloser(bytes.NewReader(tarGz)), int64(len(tarGz)), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "otlp-0.135.0") {
		t.Fatalf("pinned path = %q", path)
	}
}

func TestBundled(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "compy")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := bundledExe
	bundledExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { bundledExe = orig })

	if p, _ := Bundled(); p != "" {
		t.Fatalf("no binary: path = %q, want empty", p)
	}

	bin := filepath.Join(dir, "otelcol-compy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, v := Bundled()
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r // macOS: /var is a symlink to /private/var
	}
	if p != bin || v != "unknown" {
		t.Fatalf("no stamp: got (%q, %q), want (%q, unknown)", p, v, bin)
	}

	if err := os.WriteFile(bin+".version", []byte("0.159.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, v := Bundled(); v != "0.159.0" {
		t.Fatalf("version = %q, want 0.159.0", v)
	}
}

func TestRegistryIncludesBundledAndEffectiveVersions(t *testing.T) {
	t.Setenv("COMPY_HOME", t.TempDir())
	dir := t.TempDir()
	exe := filepath.Join(dir, "compy")
	bin := filepath.Join(dir, "otelcol-compy")
	for _, f := range []string{exe, bin} {
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	orig := bundledExe
	bundledExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { bundledExe = orig })

	root := t.TempDir()
	// A pulled otlp update on disk, recorded in settings.
	newBin := filepath.Join(root, "distros", "otlp-0.160.0", "otelcol-otlp")
	if err := os.MkdirAll(filepath.Dir(newBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveSettings(state.Settings{DistroVersions: map[string]string{"otlp": "0.160.0"}}); err != nil {
		t.Fatal(err)
	}

	reg, err := Registry(root)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r // macOS: /var is a symlink to /private/var
	}
	if reg[0].Name != BundledName || reg[0].Path != bin {
		t.Fatalf("first row = %+v, want bundled %q", reg[0], bin)
	}
	byName := map[string]string{}
	for _, d := range reg {
		byName[d.Name] = d.Path
	}
	if byName["otlp"] != newBin {
		t.Fatalf("otlp path = %q, want pulled version %q", byName["otlp"], newBin)
	}
}
