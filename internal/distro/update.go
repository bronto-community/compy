package distro

// Pulling a more recent release of a pinned distro (core, contrib, otlp).
//
// Trust model: the PINNED version in defs.go ships with a compiled-in
// sha256. A PULLED update is verified against the <asset>.sha256 checksum
// asset published in the same upstream release (TLS + same-origin GitHub
// release assets) — newer releases publish per-asset .sha256 files instead
// of the old aggregate checksums.txt (verified against v0.159.0's asset
// list, 2026-08-27).

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// releasesAPI lists collector releases, newest first; a var so tests can
// point it at a stub server.
var releasesAPI = "https://api.github.com/repos/open-telemetry/opentelemetry-collector-releases/releases?per_page=30"

// LatestVersion returns the newest collector release version (e.g.
// "0.159.0") from the GitHub API. The repo tags collector releases as
// v0.x.y and also tags cmd/builder/v0.x.y and cmd/opampsupervisor/v0.x.y
// releases (verified against the live API, 2026-08-27) — only plain v tags
// count. One release carries the assets of all three distros.
func LatestVersion(fetch Fetch) (string, error) {
	rc, _, err := fetch(releasesAPI)
	if err != nil {
		return "", fmt.Errorf("release check: %w", err)
	}
	defer rc.Close()
	// The listing is huge — every release carries hundreds of assets — so
	// stream the array and stop at the first collector tag instead of
	// buffering the whole response.
	dec := json.NewDecoder(rc)
	if _, err := dec.Token(); err != nil { // opening [
		return "", fmt.Errorf("release check: %w", err)
	}
	for dec.More() {
		var r struct {
			TagName    string `json:"tag_name"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := dec.Decode(&r); err != nil {
			return "", fmt.Errorf("release check: %w", err)
		}
		if r.Prerelease || !strings.HasPrefix(r.TagName, "v") {
			continue
		}
		return strings.TrimPrefix(r.TagName, "v"), nil
	}
	return "", errors.New("release check: no collector release in the GitHub API response")
}

// DefForVersion returns d retargeted at another upstream version, following
// the release-asset naming <binary>_<version>_<os>_<arch>.tar.gz. SHA256 is
// left empty: EnsureVersion fills it from the release's .sha256 asset.
func DefForVersion(d Def, version string) Def {
	nd := Def{Name: d.Name, Version: version, Binary: d.Binary,
		URLs: make(map[string]string, len(d.URLs)), SHA256: map[string]string{}}
	for plat := range d.URLs {
		nd.URLs[plat] = fmt.Sprintf(
			"https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v%s/%s_%s_%s.tar.gz",
			version, d.Binary, version, plat)
	}
	return nd
}

// EnsureVersion is Ensure for an effective (possibly pulled) version of
// base: the pinned version uses its compiled-in sha256, any other version
// is verified against the .sha256 asset published next to the tarball in
// the same upstream release. Idempotent like Ensure.
func EnsureVersion(root string, base Def, version string, fetch Fetch, progress Progress) (string, error) {
	if version == base.Version {
		return Ensure(root, base, fetch, progress)
	}
	d := DefForVersion(base, version)
	plat := platformKey()
	url, ok := d.URLs[plat]
	if !ok {
		return "", fmt.Errorf("distro %s: no download URL for platform %s", d.Name, plat)
	}
	sum, err := fetchSHA(fetch, url+".sha256")
	if err != nil {
		return "", fmt.Errorf("distro %s %s: checksum asset: %w", d.Name, version, err)
	}
	d.SHA256[plat] = sum
	return Ensure(root, d, fetch, progress)
}

// fetchSHA reads a published .sha256 asset: bare hex, or "hex  filename".
func fetchSHA(fetch Fetch, url string) (string, error) {
	rc, _, err := fetch(url)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 256))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return "", errors.New("empty checksum file")
	}
	sum := strings.ToLower(fields[0])
	if raw, err := hex.DecodeString(sum); err != nil || len(raw) != 32 {
		return "", fmt.Errorf("malformed checksum %q", fields[0])
	}
	return sum, nil
}
