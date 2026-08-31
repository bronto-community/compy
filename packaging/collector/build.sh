#!/bin/sh
# build.sh [output-dir] — build otelcol-compy, the collector distribution
# shipped with compy, from manifest.yaml using the OpenTelemetry Collector
# Builder (OCB) pinned to the manifest's collector version.
#
# NOT part of the normal go-build gates: OCB downloads a large module graph
# and takes minutes. Run it when the manifest changes or for a release.
#
# Output: <output-dir>/otelcol-compy (default: next to this script's repo
# root, i.e. where a compy binary built with `go build ./cmd/compy` lands)
# plus <output-dir>/otelcol-compy.version holding the collector version, which
# compy's distro registry reads for display.
#
# Cross-compiles: set GOOS/GOARCH (the collector is pure Go, so pair them
# with CGO_ENABLED=0). The builder itself always compiles for the host.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$(cd "$here/../.." && pwd)}
out=$(cd "$out" && pwd)

version=$(sed -n 's/^  version: //p' "$here/manifest.yaml")
[ -n "$version" ] || { echo "no version in manifest.yaml" >&2; exit 1; }

# The builder is versioned in lockstep with the collector: the v0.x.y
# collector line tags the builder module as cmd/builder/v0.x.y, importable
# as go.opentelemetry.io/collector/cmd/builder@v0.x.y. Installed for the
# host (GOOS/GOARCH cleared) so a cross-target build still gets a runnable
# builder; the builder's inner `go build` then picks up the caller's
# GOOS/GOARCH from the environment.
echo "building otelcol-compy (collector $version) via OCB…" >&2
builder_dir=$(mktemp -d)
trap 'rm -rf "$builder_dir"' EXIT
GOBIN="$builder_dir" GOOS= GOARCH= go install go.opentelemetry.io/collector/cmd/builder@v"$version"

# Build inside the throwaway dir: the manifest's output_path (./_build) is
# relative to the CWD, and a shared packaging/collector/_build lets two
# concurrent invocations (goreleaser runs the darwin post hooks in
# parallel) delete each other's output mid-copy — that broke the v0.1.0
# release. Also keeps generated builder sources away from `gofmt -l .`.
cp "$here/manifest.yaml" "$builder_dir/manifest.yaml"
cd "$builder_dir"
"$builder_dir/builder" --config manifest.yaml

# Install atomically via rename: overwriting the file in place while a
# running collector executes it taints the inode — macOS then SIGKILLs
# every fresh exec of the path ("file changed while mapped").
cp "$builder_dir/_build/otelcol-compy" "$out/otelcol-compy.tmp"
mv "$out/otelcol-compy.tmp" "$out/otelcol-compy"
printf '%s\n' "$version" > "$out/otelcol-compy.version"
echo "built $out/otelcol-compy ($version)" >&2
