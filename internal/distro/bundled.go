package distro

import (
	"os"
	"path/filepath"
	"strings"
)

// BundledName is the distro shipped with compy itself: otelcol-compy, built
// from packaging/collector/manifest.yaml by packaging/collector/build.sh and
// resolved at runtime next to the compy executable — never downloaded. It
// updates with compy releases, not via `compy distro update`.
const BundledName = "compy"

// bundledExe is os.Executable, overridable in tests.
var bundledExe = os.Executable

// Bundled locates otelcol-compy next to the compy executable. path is ""
// when it is not there (a bare `go build` without packaging/collector/
// build.sh); version comes from the sibling otelcol-compy.version stamp the
// build script writes, "unknown" when the stamp is missing.
func Bundled() (path, version string) {
	exe, err := bundledExe()
	if err != nil {
		return "", ""
	}
	// Resolve symlinks so a symlinked compy (compy.app/Contents/MacOS) still
	// looks next to the real binary.
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	p := filepath.Join(filepath.Dir(exe), "otelcol-compy")
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
		return "", ""
	}
	version = "unknown"
	if b, err := os.ReadFile(p + ".version"); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			version = v
		}
	}
	return p, version
}
