#!/bin/sh
# build.sh [output-dir] — build otelcol-compy, the collector distribution
# shipped with compy, from manifest.yaml using the OpenTelemetry Collector
# Builder (OCB) pinned separately in builder-version, following upstream.
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

# A binary patch release can retain earlier component and builder versions.
# bump-collector.py reads the OCB version from upstream's Makefile. Installed for the
# host (GOOS/GOARCH cleared) so a cross-target build still gets a runnable
# builder; the builder's inner `go build` then picks up the caller's
# GOOS/GOARCH from the environment.
builder_version=$(tr -d '\n\r' < "$here/builder-version")
echo "$builder_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || {
  echo "invalid OCB version in builder-version" >&2; exit 1;
}
echo "building otelcol-compy (release $version, OCB $builder_version)…" >&2
builder_dir=$(mktemp -d)
trap 'rm -rf "$builder_dir"' EXIT
GOBIN="$builder_dir" GOOS= GOARCH= go install go.opentelemetry.io/collector/cmd/builder@v"$builder_version"

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
