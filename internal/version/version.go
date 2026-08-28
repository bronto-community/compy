// Package version resolves what build of compy is running: a release
// version stamped by goreleaser (ldflags -X), a dev build described by its
// VCS revision, or "unknown" when neither is available (a bare `go run`).
package version

import "runtime/debug"

// release is stamped at build time:
//
//	-ldflags "-X github.com/bronto-community/compy/internal/version.release=0.1.0"
//
// (.goreleaser.yaml stamps {{.Version}}). Empty in every dev build.
var release string

// Release is the stamped release version, "" for a dev build.
func Release() string { return release }

// IsRelease reports whether this is a stamped release build — the only kind
// that may claim "a newer compy is available" (a dev build's version line
// already says dev, and semver comparison against a commit is meaningless).
func IsRelease() bool { return release != "" }

// String renders the running build's version the way every surface shows
// it: "0.1.0" for a release, "dev · 787da79a1b2c" (+dirty when the tree was
// modified) for a dev build, "unknown" without build info.
func String() string {
	rev, dirty := vcsInfo()
	return render(release, rev, dirty)
}

// render is the pure resolution order — release wins, then VCS revision
// (short, 12 chars), then "unknown" — separated so tests can feed synthetic
// inputs instead of faking runtime/debug.
func render(release, revision string, modified bool) string {
	if release != "" {
		return release
	}
	if revision == "" {
		return "unknown"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	s := "dev · " + revision
	if modified {
		s += "+dirty"
	}
	return s
}

// vcsInfo reads the VCS stamp the Go toolchain embeds in builds from a git
// checkout; both zero when there is none.
func vcsInfo() (revision string, modified bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, modified
}
