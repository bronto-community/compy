// Package distro manages pinned collector-distribution definitions and
// their on-demand, checksum-verified download.
package distro

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bronto-io/compy/internal/state"
)

// Def describes one pinned collector distribution.
type Def struct {
	Name    string // "core", "contrib", "otlp"
	Version string
	URLs    map[string]string // "darwin_arm64" -> tar.gz URL; missing platform = unavailable
	SHA256  map[string]string // per platform, of the tar.gz
	Binary  string            // path inside the archive, e.g. "otelcol"
}

// Defs returns the shipped table of pinned distribution definitions.
func Defs() []Def {
	return defs
}

// platformKey returns the GOOS_GOARCH key used in Def.URLs/SHA256 for the
// running platform.
func platformKey() string {
	return runtime.GOOS + "_" + runtime.GOARCH
}

// Available reports whether d has a download URL for the running platform.
func Available(d Def) bool {
	_, ok := d.URLs[platformKey()]
	return ok
}

// Fetch retrieves the content at url (e.g. an http.Get wrapper), with the
// total size in bytes when the server declares one and -1 when it does not.
type Fetch func(url string) (io.ReadCloser, int64, error)

// Progress is called as a download proceeds, with the bytes read so far and
// the total (-1 when unknown). A nil Progress reports nothing.
type Progress func(done, total int64)

// counter turns writes into Progress calls; it is the write half of a
// TeeReader wrapped around the download.
type counter struct {
	done, total int64
	report      Progress
}

func (c *counter) Write(p []byte) (int, error) {
	c.done += int64(len(p))
	c.report(c.done, c.total)
	return len(p), nil
}

// Ensure makes sure d's binary is installed under root, returning its path.
// Idempotent: if distros/<name>-<version>/<binary> already exists, it's
// returned without invoking fetch (and without reporting progress).
// Otherwise the tar.gz is downloaded via fetch — reporting bytes to progress
// as it arrives — its sha256 verified against d.SHA256, the binary extracted
// and chmod 0755. On checksum mismatch, no partial files are left on disk.
func Ensure(root string, d Def, fetch Fetch, progress Progress) (string, error) {
	dir := filepath.Join(root, "distros", d.Name+"-"+d.Version)
	binPath := filepath.Join(dir, d.Binary)
	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}

	plat := platformKey()
	url, ok := d.URLs[plat]
	if !ok {
		return "", fmt.Errorf("distro %s: no download URL for platform %s", d.Name, plat)
	}
	wantSHA, ok := d.SHA256[plat]
	if !ok {
		return "", fmt.Errorf("distro %s: no sha256 for platform %s", d.Name, plat)
	}

	rc, total, err := fetch(url)
	if err != nil {
		return "", fmt.Errorf("distro %s: fetch: %w", d.Name, err)
	}
	defer rc.Close()

	var body io.Reader = rc
	if progress != nil {
		body = io.TeeReader(rc, &counter{total: total, report: progress})
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("distro %s: read: %w", d.Name, err)
	}

	sum := sha256.Sum256(data)
	gotSHA := hex.EncodeToString(sum[:])
	if gotSHA != wantSHA {
		return "", fmt.Errorf("distro %s: checksum mismatch: expected %s, got %s", d.Name, wantSHA, gotSHA)
	}

	// Verified: safe to create the destination directory and extract.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("distro %s: %w", d.Name, err)
	}
	if err := extractBinary(data, d.Binary, binPath); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("distro %s: extract: %w", d.Name, err)
	}
	return binPath, nil
}

// extractBinary finds the regular file named binName in the tar.gz data
// and writes it to dest with mode 0755.
func extractBinary(tarGz []byte, binName, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(tarGz))
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("binary %q not found in archive", binName)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Name != binName {
			continue
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Chmod(dest, 0o755) // ensure perm regardless of umask
	}
}

// EffectiveVersion returns the version of d in effect: the pulled-update
// version recorded in settings' DistroVersions when present, else the
// pinned one.
func EffectiveVersion(d Def, s state.Settings) string {
	if v := s.DistroVersions[d.Name]; v != "" {
		return v
	}
	return d.Version
}

// Registry returns the bundled distro (BundledName, Path "" when not built
// next to the executable) followed by state.LoadDistros() merged with
// Defs(): definition entries appear with Path set to their installed path
// (or "" if not yet downloaded into root), at the version in effect per
// settings; a user entry (state.Distro) with the same name as a definition
// or the bundled distro overrides it entirely.
func Registry(root string) ([]state.Distro, error) {
	user, err := state.LoadDistros()
	if err != nil {
		return nil, err
	}
	s, err := state.LoadSettings()
	if err != nil {
		return nil, err
	}
	overrides := make(map[string]state.Distro, len(user))
	for _, u := range user {
		overrides[u.Name] = u
	}

	out := make([]state.Distro, 0, len(defs)+len(user)+1)
	seen := map[string]bool{BundledName: true}
	if u, ok := overrides[BundledName]; ok {
		out = append(out, u)
	} else {
		path, _ := Bundled()
		out = append(out, state.Distro{Name: BundledName, Path: path})
	}
	for _, d := range Defs() {
		seen[d.Name] = true
		if u, ok := overrides[d.Name]; ok {
			out = append(out, u)
			continue
		}
		ver := EffectiveVersion(d, s)
		path := filepath.Join(root, "distros", d.Name+"-"+ver, d.Binary)
		if _, err := os.Stat(path); err != nil {
			path = ""
		}
		out = append(out, state.Distro{Name: d.Name, Path: path})
	}
	for _, u := range user {
		if !seen[u.Name] {
			out = append(out, u)
		}
	}
	return out, nil
}
