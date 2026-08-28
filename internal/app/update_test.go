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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/distro"
	"github.com/bronto-community/compy/internal/state"
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

// installDistro fakes an installed pinned distro: the binary dropped where
// a verified download would land it, and the version recorded in settings
// the way a pull records it.
func installDistro(t *testing.T, a *app.App, name, binary, version string) string {
	t.Helper()
	dir := filepath.Join(a.Dir, "distros", name+"-"+version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, binary)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.DistroVersions == nil {
		s.DistroVersions = map[string]string{}
	}
	s.DistroVersions[name] = version
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	return bin
}

func tarGzWith(t *testing.T, name string) []byte {
	return tarGzScript(t, name, "#!/bin/sh\nexit 0\n")
}

// tarGzScript is tarGzWith with the archived collector's script under the
// caller's control — a rejecting binary ("exit 1") is how a pulled update
// that the active configuration does not run with is staged.
func tarGzScript(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
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
	installDistro(t, a, "otlp", "otelcol-otlp", "0.159.0")

	current, latest, err := a.CheckDistroUpdate("otlp")
	if err != nil {
		t.Fatal(err)
	}
	if current != "0.159.0" || latest != "0.160.0" {
		t.Fatalf("check = (%q, %q), want (0.159.0, 0.160.0)", current, latest)
	}

	current, latest, updated, err := a.UpdateDistro("otlp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !updated || current != "0.159.0" || latest != "0.160.0" {
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

// countingFetch wraps fetch, counting GitHub releases-listing calls.
func countingFetch(fetch distro.Fetch, calls *int) distro.Fetch {
	return func(url string) (io.ReadCloser, int64, error) {
		if strings.Contains(url, "api.github.com") {
			*calls++
		}
		return fetch(url)
	}
}

func TestUpdateCheckPersistsAndShowsOnRows(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	a.Fetch = countingFetch(updateFetch(t, "0.160.0", tarGzWith(t, "otelcol-otlp")), &calls)
	installDistro(t, a, "otlp", "otelcol-otlp", "0.150.0")

	// The on-demand check persists its result.
	if _, _, err := a.CheckDistroUpdate("otlp"); err != nil {
		t.Fatal(err)
	}
	chk, err := state.LoadUpdateCheck()
	if err != nil || chk.Latest != "0.160.0" || chk.CheckedAt.IsZero() {
		t.Fatalf("persisted check = %+v (%v), want latest 0.160.0 with checked_at", chk, err)
	}
	if calls != 1 {
		t.Fatalf("listing calls = %d, want 1", calls)
	}

	// One listing covers every pinned distro, with no further network: the
	// installed-and-older row claims availability; an undownloaded row never
	// does — it advertises what a download would fetch instead.
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		t.Errorf("unexpected network call %q while listing distros", url)
		return nil, 0, fmt.Errorf("offline")
	}
	fakeDistro(t, "exit 0") // a user-managed row must never claim
	rows, err := a.Distros()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, r := range rows {
		byName[r["name"].(string)] = r
	}
	if byName["otlp"]["latest_available"] != "0.160.0" {
		t.Errorf("otlp latest_available = %v, want 0.160.0", byName["otlp"]["latest_available"])
	}
	if s, _ := byName["otlp"]["checked_at"].(string); s == "" {
		t.Errorf("otlp checked_at missing")
	}
	for _, name := range []string{"core", "contrib"} {
		r := byName[name]
		if v, ok := r["latest_available"]; ok {
			t.Errorf("%s claims %v; undownloaded rows have nothing to update", name, v)
		}
		if r["fetch_version"] != "0.160.0" {
			t.Errorf("%s fetch_version = %v, want 0.160.0", name, r["fetch_version"])
		}
		if _, ok := r["fetch_pinned"]; ok {
			t.Errorf("%s fetch_pinned set despite a check result", name)
		}
		if r["version"] != "" {
			t.Errorf("%s version = %v, want empty (nothing installed)", name, r["version"])
		}
	}
	for _, name := range []string{"compy", "fake"} {
		for _, k := range []string{"latest_available", "fetch_version"} {
			if v, ok := byName[name][k]; ok {
				t.Errorf("%s %s = %v; bundled/user rows must not claim", name, k, v)
			}
		}
	}
}

func TestMaybeCheckUpdatesCadenceAndUpdateClears(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	working := countingFetch(updateFetch(t, "0.160.0", tarGzWith(t, "otelcol-otlp")), &calls)
	a.Fetch = working

	// No persisted result: due — checks and records.
	a.MaybeCheckUpdates()
	if calls != 1 {
		t.Fatalf("listing calls = %d, want 1", calls)
	}
	if chk, err := state.LoadUpdateCheck(); err != nil || chk.Latest != "0.160.0" {
		t.Fatalf("persisted check = %+v (%v)", chk, err)
	}

	// Fresh result: declines without network.
	a.MaybeCheckUpdates()
	if calls != 1 {
		t.Fatalf("fresh result still checked: %d calls", calls)
	}

	// Stale result: due again.
	if err := state.SaveUpdateCheck(state.UpdateCheck{Latest: "0.150.0", CheckedAt: time.Now().Add(-13 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a.MaybeCheckUpdates()
	if calls != 2 {
		t.Fatalf("stale result not rechecked: %d calls", calls)
	}

	// A failed check keeps the previous result and its honest checked_at —
	// silent, retried next tick, never a stale claim erased.
	stale := state.UpdateCheck{Latest: "0.160.0", CheckedAt: time.Now().Add(-13 * time.Hour).UTC()}
	if err := state.SaveUpdateCheck(stale); err != nil {
		t.Fatal(err)
	}
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		return nil, 0, fmt.Errorf("rate limited")
	}
	a.MaybeCheckUpdates()
	if chk, err := state.LoadUpdateCheck(); err != nil || chk.Latest != stale.Latest || !chk.CheckedAt.Equal(stale.CheckedAt) {
		t.Fatalf("failed check touched the record: %+v (%v), want %+v", chk, err, stale)
	}

	// UpdateAvailable counts only INSTALLED outdated rows: with nothing
	// downloaded there is nothing to update, whatever upstream says. An
	// installed older distro claims, and a pulled update clears its claim
	// (its version in effect caught up) — undownloaded siblings still don't
	// count.
	a.Fetch = working
	if v, err := a.UpdateAvailable(); err != nil || v != "" {
		t.Fatalf("UpdateAvailable with nothing installed = (%q, %v), want none", v, err)
	}
	installDistro(t, a, "otlp", "otelcol-otlp", "0.150.0")
	if v, err := a.UpdateAvailable(); err != nil || v != "0.160.0" {
		t.Fatalf("UpdateAvailable = (%q, %v), want 0.160.0", v, err)
	}
	if _, _, updated, err := a.UpdateDistro("otlp", nil); err != nil || !updated {
		t.Fatalf("update otlp = (%v, %v)", updated, err)
	}
	rows, err := a.Distros()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		switch r["name"] {
		case "otlp":
			if v, ok := r["latest_available"]; ok {
				t.Errorf("otlp still claims %v after updating to it", v)
			}
		case "contrib":
			if v, ok := r["latest_available"]; ok {
				t.Errorf("undownloaded contrib claims %v", v)
			}
		}
	}
	if v, _ := a.UpdateAvailable(); v != "" {
		t.Errorf("UpdateAvailable after otlp update = %q, want none (nothing else installed)", v)
	}
}

// otlpDef returns the shipped otlp definition.
func otlpDef(t *testing.T) distro.Def {
	t.Helper()
	for _, d := range distro.Defs() {
		if d.Name == "otlp" {
			return d
		}
	}
	t.Fatal("no shipped otlp definition")
	return distro.Def{}
}

// TestDistroRowStates is the row-state matrix: undownloaded rows advertise
// what a download would fetch (never an update claim), installed rows claim
// only when older than the persisted latest.
func TestDistroRowStates(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	pin := otlpDef(t).Version
	row := func(name string) map[string]any {
		t.Helper()
		rows, err := a.Distros()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r["name"] == name {
				return r
			}
		}
		t.Fatalf("no %s row", name)
		return nil
	}

	// Undownloaded, no check result yet: the compiled-in pin, stated as such.
	r := row("otlp")
	if r["version"] != "" || r["fetch_version"] != pin || r["fetch_pinned"] != true {
		t.Fatalf("undownloaded unchecked row = %v, want empty version, fetch_version %s (pinned)", r, pin)
	}
	if _, ok := r["latest_available"]; ok {
		t.Fatalf("undownloaded row claims an update: %v", r)
	}

	// Undownloaded with a persisted latest: that is what a download fetches.
	if err := state.SaveUpdateCheck(state.UpdateCheck{Latest: "0.160.0", CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	r = row("otlp")
	if r["fetch_version"] != "0.160.0" {
		t.Fatalf("undownloaded checked row fetch_version = %v, want 0.160.0", r["fetch_version"])
	}
	if _, ok := r["fetch_pinned"]; ok {
		t.Fatalf("fetch_pinned set despite a check result: %v", r)
	}
	if _, ok := r["latest_available"]; ok {
		t.Fatalf("undownloaded row claims an update: %v", r)
	}

	// Downloaded and current: a version, no claims of any kind.
	installDistro(t, a, "otlp", "otelcol-otlp", "0.160.0")
	r = row("otlp")
	if r["version"] != "0.160.0" {
		t.Fatalf("installed row version = %v, want 0.160.0", r["version"])
	}
	for _, k := range []string{"latest_available", "fetch_version", "fetch_pinned"} {
		if v, ok := r[k]; ok {
			t.Errorf("current installed row carries %s=%v", k, v)
		}
	}

	// Downloaded and older: the one case that claims an update.
	installDistro(t, a, "contrib", "otelcol-contrib", "0.150.0")
	r = row("contrib")
	if r["version"] != "0.150.0" || r["latest_available"] != "0.160.0" {
		t.Fatalf("installed older row = %v, want version 0.150.0 claiming 0.160.0", r)
	}
}

// TestEnsureDistroFetchesLatestKnownRelease: a first download goes straight
// to the persisted latest release, verified via its published .sha256 asset,
// recorded in settings — and an installed binary is never silently upgraded.
func TestEnsureDistroFetchesLatestKnownRelease(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	inner := updateFetch(t, "0.160.0", tarGzWith(t, "otelcol-otlp"))
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		urls = append(urls, url)
		return inner(url)
	}
	if err := state.SaveUpdateCheck(state.UpdateCheck{Latest: "0.160.0", CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	path, err := a.EnsureDistro("otlp", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(a.Dir, "distros", "otlp-0.160.0", "otelcol-otlp")
	if path != want {
		t.Fatalf("EnsureDistro = %q, want %q (one download, straight to the latest)", path, want)
	}
	var gotTar, gotSHA bool
	for _, u := range urls {
		switch {
		case strings.Contains(u, "api.github.com"):
			t.Errorf("fetch ran a live release check: %s", u)
		case strings.HasSuffix(u, "0.160.0_"+runtime.GOOS+"_"+runtime.GOARCH+".tar.gz"):
			gotTar = true
		case strings.HasSuffix(u, ".sha256"):
			gotSHA = true
		}
	}
	if !gotTar || !gotSHA {
		t.Fatalf("fetched %v, want the 0.160.0 tarball and its .sha256 asset", urls)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.DistroVersions["otlp"] != "0.160.0" {
		t.Fatalf("DistroVersions = %v, want otlp 0.160.0", s.DistroVersions)
	}

	// Installed: idempotent, offline, and never a silent upgrade — a newer
	// persisted latest is UpdateDistro's business.
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		t.Errorf("unexpected fetch %q with otlp installed", url)
		return nil, 0, fmt.Errorf("offline")
	}
	if err := state.SaveUpdateCheck(state.UpdateCheck{Latest: "0.161.0", CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if path, err := a.EnsureDistro("otlp", nil); err != nil || path != want {
		t.Fatalf("EnsureDistro installed = (%q, %v), want %q untouched", path, err, want)
	}
}

// TestEnsureDistroPinFallbackUsesCompiledSHA: with no release-check result,
// a download targets the compiled-in pin and verifies against the
// compiled-in sha256 — no .sha256 asset is consulted.
func TestEnsureDistroPinFallbackUsesCompiledSHA(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	d := otlpDef(t)
	plat := runtime.GOOS + "_" + runtime.GOARCH
	if _, ok := d.URLs[plat]; !ok {
		t.Skipf("no otlp build pinned for %s", plat)
	}
	var urls []string
	tarGz := tarGzWith(t, "otelcol-otlp") // sha256 won't match the pin — that's the point
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		urls = append(urls, url)
		return io.NopCloser(bytes.NewReader(tarGz)), int64(len(tarGz)), nil
	}
	_, err = a.EnsureDistro("otlp", nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch: expected "+d.SHA256[plat]) {
		t.Fatalf("pin fallback err = %v, want mismatch against the compiled-in sha256 %s", err, d.SHA256[plat])
	}
	if len(urls) != 1 || urls[0] != d.URLs[plat] {
		t.Fatalf("fetched %v, want exactly the pinned URL %s (no .sha256 asset)", urls, d.URLs[plat])
	}
}

// selectDistro makes name the settings-selected default distro.
func selectDistro(t *testing.T, name string) {
	t.Helper()
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.Distro = name
	if err := state.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateDistroSwapsRunningCollector: updating the distro in use while
// the collector runs re-applies the active configuration onto the new
// binary — launchd sees a fresh bootstrap, the plist points into the new
// versioned dir, and settings record the pulled version.
func TestUpdateDistroSwapsRunningCollector(t *testing.T) {
	calls := setup(t, "state = running")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	a.Fetch = updateFetch(t, "0.160.0", tarGzWith(t, "otelcol-otlp"))
	installDistro(t, a, "otlp", "otelcol-otlp", "0.159.0")
	selectDistro(t, "otlp")
	if err := a.Activate("debug", ""); err != nil {
		t.Fatalf("Activate(debug) on otlp 0.159.0: %v", err)
	}

	*calls = nil
	current, latest, updated, err := a.UpdateDistro("otlp", nil)
	if err != nil || !updated || current != "0.159.0" || latest != "0.160.0" {
		t.Fatalf("UpdateDistro = (%q, %q, %v, %v), want a 0.159.0→0.160.0 update", current, latest, updated, err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("running collector was not re-applied onto the new version: %v", *calls)
	}
	if plist := readPlist(t); !strings.Contains(plist, filepath.Join("distros", "otlp-0.160.0", "otelcol-otlp")) {
		t.Errorf("plist does not point at the new binary:\n%s", plist)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.DistroVersions["otlp"] != "0.160.0" {
		t.Errorf("DistroVersions = %v, want otlp 0.160.0", s.DistroVersions)
	}
}

// TestUpdateDistroRejectedConfigKeepsVersion pins applyDistroUpdate's first
// failure branch: the new collector REJECTS the active configuration —
// nothing reached launchd, the old binary keeps running, and the recorded
// version honestly stands (the next restart uses it). The error names the
// new version, carries the collector's own diagnostic, and stays a 400.
func TestUpdateDistroRejectedConfigKeepsVersion(t *testing.T) {
	calls := setup(t, "state = running")
	listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	a.Fetch = updateFetch(t, "0.160.0", tarGzScript(t, "otelcol-otlp", "#!/bin/sh\necho 'unknown type' >&2\nexit 1\n"))
	installDistro(t, a, "otlp", "otelcol-otlp", "0.159.0")
	selectDistro(t, "otlp")
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	_, _, updated, err := a.UpdateDistro("otlp", nil)
	if err == nil || !updated {
		t.Fatalf("UpdateDistro = (updated %v, err %v), want the rejected-config error", updated, err)
	}
	if !strings.Contains(err.Error(), "otlp is now 0.160.0") || !strings.Contains(err.Error(), "does not run with it") {
		t.Errorf("error = %q, want it to say the update stands but the config does not run with it", err)
	}
	if !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("error = %q, want the collector's own diagnostic", err)
	}
	if !state.IsBadRequest(err) {
		t.Errorf("a config the new collector rejects is the caller's to fix; error not BadRequest-marked: %v", err)
	}
	if called(*calls, "bootstrap") {
		t.Errorf("a rejected config still reached launchd: %v", *calls)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.DistroVersions["otlp"] != "0.160.0" {
		t.Errorf("DistroVersions = %v, want the update to stand at 0.160.0", s.DistroVersions)
	}
}

// TestUpdateDistroStartFailureRollsBack pins the second failure branch: the
// new collector VALIDATES but will not start. The last-good restore puts
// settings.json — version record included — back, and the error names both
// versions so the user knows what actually runs.
func TestUpdateDistroStartFailureRollsBack(t *testing.T) {
	// launchd: up for the initial activation, up for applyDistroUpdate's
	// am-I-running check, down at the failing activation's check, back up
	// once the previous setup is restored.
	setupStaged(t, "state = running", "state = running", "", "state = running")
	port := listenPort(t)

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	a.Fetch = updateFetch(t, "0.160.0", tarGzWith(t, "otelcol-otlp"))
	installDistro(t, a, "otlp", "otelcol-otlp", "0.159.0")
	selectDistro(t, "otlp")
	if err := a.Activate("debug", ""); err != nil {
		t.Fatal(err)
	}

	// Nothing answers the probe from here on: the new collector never
	// comes up, and the restore path runs.
	closeListener(t, port)
	_, _, updated, err := a.UpdateDistro("otlp", nil)
	if err == nil || !updated {
		t.Fatalf("UpdateDistro = (updated %v, err %v), want the did-not-start error", updated, err)
	}
	if !strings.Contains(err.Error(), "otlp 0.160.0 did not start") || !strings.Contains(err.Error(), "still 0.159.0") {
		t.Errorf("error = %q, want it to name both versions (0.160.0 failed, 0.159.0 restored)", err)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.DistroVersions["otlp"] != "0.159.0" {
		t.Errorf("DistroVersions = %v, want the last-good restore to put 0.159.0 back", s.DistroVersions)
	}
}

// TestStartUpdateDistroAsync: the REST update answers at once, downloads in
// the background reporting through DownloadProgress, and a second Start
// while one is in flight joins it instead of racing a second extract into
// the same directory (beginDownload's dedup).
func TestStartUpdateDistroAsync(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	unblock := make(chan struct{})
	tarFetches := 0
	inner := updateFetch(t, "0.160.0", tarGzWith(t, "otelcol-otlp"))
	a.Fetch = func(url string) (io.ReadCloser, int64, error) {
		if strings.HasSuffix(url, ".tar.gz") { // not the tarball's .sha256 asset
			tarFetches++
			<-unblock
		}
		return inner(url)
	}
	installDistro(t, a, "otlp", "otelcol-otlp", "0.159.0")

	current, latest, started, err := a.StartUpdateDistro("otlp")
	if err != nil || !started || current != "0.159.0" || latest != "0.160.0" {
		t.Fatalf("StartUpdateDistro = (%q, %q, %v, %v), want a started 0.159.0→0.160.0 update", current, latest, started, err)
	}
	// In flight: a second Start reports started without spawning another
	// download.
	if _, _, started, err := a.StartUpdateDistro("otlp"); err != nil || !started {
		t.Fatalf("second StartUpdateDistro = (started %v, err %v), want (true, nil) — join, not refuse", started, err)
	}
	close(unblock)

	deadline := time.Now().Add(10 * time.Second)
	for {
		p, err := a.DownloadProgress("otlp")
		if err != nil {
			t.Fatal(err)
		}
		st := p.(map[string]any)["status"]
		if st == "done" {
			break
		}
		if st == "failed" {
			t.Fatalf("download failed: %v", p)
		}
		if time.Now().After(deadline) {
			t.Fatalf("download never finished: %v", p)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tarFetches != 1 {
		t.Errorf("tarball fetched %d times, want 1 (the second Start must not race a second download)", tarFetches)
	}
	s, err := state.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if s.DistroVersions["otlp"] != "0.160.0" {
		t.Errorf("DistroVersions = %v, want otlp 0.160.0 once the async update lands", s.DistroVersions)
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
	// Undownloaded: nothing installed to update — a download fetches the
	// newest release directly, and the refusal says so.
	if _, _, err := a.CheckDistroUpdate("otlp"); !state.IsBadRequest(err) || !strings.Contains(fmt.Sprint(err), "not downloaded") {
		t.Fatalf("check undownloaded otlp: %v, want not-downloaded 400", err)
	}
	// A network failure is an honest error, never a version claim.
	installDistro(t, a, "otlp", "otelcol-otlp", "0.159.0")
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
