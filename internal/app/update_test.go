package app_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/distro"
	"github.com/bronto-io/compy/internal/state"
)

// placeBundled drops a fake otelcol-compy (plus version stamp) next to the
// running test binary — the real place Bundled() looks — and removes it
// again on cleanup so no other test sees a bundled collector.
func placeBundled(t *testing.T, version string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(filepath.Dir(exe), "otelcol-compy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := bin + ".version"
	if version != "" {
		if err := os.WriteFile(stamp, []byte(version+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.Remove(bin); os.Remove(stamp) })
	return bin
}

// updateFetch serves a stubbed GitHub: the releases listing, one release's
// tarball, and its .sha256 checksum asset — keyed purely on URL shape, the
// way the injected app.Fetch sees them.
func updateFetch(t *testing.T, latest string, tarGz []byte) distro.Fetch {
	t.Helper()
	sum := sha256.Sum256(tarGz)
	return func(url string) (io.ReadCloser, int64, error) {
		switch {
		case strings.Contains(url, "api.github.com"):
			body := fmt.Sprintf(`[
				{"tag_name":"cmd/builder/v%s","prerelease":false},
				{"tag_name":"v%s","prerelease":false},
				{"tag_name":"v0.135.0","prerelease":false}
			]`, latest, latest)
			return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
		case strings.HasSuffix(url, ".sha256"):
			s := hex.EncodeToString(sum[:])
			return io.NopCloser(strings.NewReader(s)), int64(len(s)), nil
		case strings.Contains(url, "/releases/download/v"+latest+"/"):
			return io.NopCloser(bytes.NewReader(tarGz)), int64(len(tarGz)), nil
		}
		t.Errorf("unexpected fetch %q", url)
		return nil, 0, fmt.Errorf("unexpected url %q", url)
	}
}

func tarGzWith(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := "#!/bin/sh\nexit 0\n"
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}); err != nil {
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

func TestUpdateDistroPullsRecordsAndResolves(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	a.Fetch = updateFetch(t, "0.160.0", tarGzWith(t, "otelcol-otlp"))

	current, latest, err := a.CheckDistroUpdate("otlp")
	if err != nil {
		t.Fatal(err)
	}
	if current != "0.135.0" || latest != "0.160.0" {
		t.Fatalf("check = (%q, %q), want (0.135.0, 0.160.0)", current, latest)
	}

	current, latest, updated, err := a.UpdateDistro("otlp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !updated || current != "0.135.0" || latest != "0.160.0" {
		t.Fatalf("update = (%q, %q, %v)", current, latest, updated)
	}
	want := filepath.Join(a.Dir, "distros", "otlp-0.160.0", "otelcol-otlp")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("pulled binary not installed at %s: %v", want, err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.DistroVersions["otlp"] != "0.160.0" {
		t.Fatalf("DistroVersions = %v, want otlp 0.160.0", s.DistroVersions)
	}

	// The recorded version is what EnsureDistro resolves from now on.
	path, err := a.EnsureDistro("otlp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != want {
		t.Fatalf("EnsureDistro = %q, want %q", path, want)
	}

	// And the row shows it.
	rows, err := a.Distros()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r["name"] == "otlp" && r["version"] != "0.160.0" {
			t.Fatalf("otlp row version = %v, want 0.160.0", r["version"])
		}
	}

	// Already newest: honest no-op.
	_, _, updated, err = a.UpdateDistro("otlp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("second update: want updated=false")
	}
	if _, _, started, err := a.StartUpdateDistro("otlp"); err != nil || started {
		t.Fatalf("StartUpdateDistro when newest = (started %v, err %v), want (false, nil)", started, err)
	}
}

func TestUpdateDistroRefusesBundledAndUserManaged(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.CheckDistroUpdate("compy"); !state.IsBadRequest(err) || !strings.Contains(fmt.Sprint(err), "compy releases") {
		t.Fatalf("check compy: %v, want bundled 400", err)
	}
	fakeDistro(t, "exit 0") // registers user distro "fake"
	if _, _, err := a.CheckDistroUpdate("fake"); !state.IsBadRequest(err) || !strings.Contains(fmt.Sprint(err), "user-managed") {
		t.Fatalf("check fake: %v, want user-managed 400", err)
	}
	if _, _, err := a.CheckDistroUpdate("nope"); !state.IsBadRequest(err) {
		t.Fatalf("check nope: %v, want 400", err)
	}
	// A network failure is an honest error, never a version claim.
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		return nil, 0, fmt.Errorf("HTTP 403 rate limited")
	}
	if _, _, err := a.CheckDistroUpdate("otlp"); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("check otlp offline: %v, want surfaced fetch error", err)
	}
}

func TestBundledDefaultResolution(t *testing.T) {
	setup(t, "")
	bin := placeBundled(t, "0.159.0")

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	// No explicit setting: the bundled collector is the default, no download.
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		t.Errorf("unexpected download of %q with the bundled collector present", url)
		return nil, 0, fmt.Errorf("no network")
	}
	path, err := a.EnsureDistro("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "otelcol-compy" {
		t.Fatalf("EnsureDistro(\"\") = %q, want the bundled binary (%s)", path, bin)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Distro != "compy" {
		t.Fatalf("Status.Distro = %q, want compy", st.Distro)
	}
	rows, err := a.Distros()
	if err != nil {
		t.Fatal(err)
	}
	if r := rows[0]; r["name"] != "compy" || r["bundled"] != true || r["selected"] != true ||
		r["downloaded"] != true || r["version"] != "0.159.0" {
		t.Fatalf("compy row = %v", r)
	}
	// An explicit setting still wins.
	fakeDistro(t, "exit 0")
	if st, err := a.Status(); err != nil || st.Distro != "fake" {
		t.Fatalf("Status.Distro = %q (%v), want fake", st.Distro, err)
	}
}

func TestNoBundledFallsBackToContrib(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Distro != "contrib" {
		t.Fatalf("Status.Distro = %q, want contrib fallback", st.Distro)
	}
	rows, err := a.Distros()
	if err != nil {
		t.Fatal(err)
	}
	if r := rows[0]; r["name"] != "compy" || r["downloaded"] != false || r["selected"] != false {
		t.Fatalf("absent compy row = %v", r)
	}
	// Selecting the unbuilt bundled collector is a caller mistake, said plainly.
	if _, err := a.EnsureDistro("compy", nil); !state.IsBadRequest(err) || !strings.Contains(fmt.Sprint(err), "build.sh") {
		t.Fatalf("EnsureDistro(compy) without the binary: %v", err)
	}
}
