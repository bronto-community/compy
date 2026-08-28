#!/bin/sh
# goreleaser-extras.sh <compy-binary> <target>: GoReleaser post-build hook
# (see .goreleaser.yaml) that produces what GoReleaser's own build matrix
# cannot: the otelcol-compy collector tarballs, the compy.app zip, and the
# per-arch staging dir the darwin archives (and thus the Homebrew cask)
# fold in. The hook fires once per darwin build; each invocation handles
# its own architecture, and the darwin_arm64 one additionally produces the
# linux collector tarballs and the compy.app zip, so the two can run in
# parallel without racing on a file.
#
# Writes into dist/stage/<os>_<arch>/ (folded into that arch's archive by
# .goreleaser.yaml, so the cask's Caskroom staging dir holds them next to
# the compy binary):
#   otelcol-compy, otelcol-compy.version   (bundled collector + stamp)
#   compy.app                              (bundle with a REAL copy of the
#                                           compy binary: a distributed
#                                           bundle can't rely on make-app.sh's
#                                           dev-loop symlink surviving
#                                           archive round-trips)
#
# Writes into dist/extra/ (published as release assets):
#   compy.app.zip                          (bundle for the arm64 binary;
#                                           keeps the relative symlink, so it
#                                           expects a compy binary next to it
#                                           when extracted)
#   otelcol-compy_<ver>_<os>_<arch>.tar.gz (binary + .version stamp)
#   otelcol-compy_<ver>_<os>_<arch>.tar.gz.sha256  ("<hex>  <name>", the same
#                                           format as upstream's per-asset
#                                           .sha256 files)
set -eu

bin=$1
target=$2
# GoReleaser suffixes the target with the micro-architecture (GOARM64), so
# darwin/arm64 arrives as "darwin_arm64_v8.0".
case $target in
darwin_arm64*) t=darwin_arm64 ;;
darwin_amd64*) t=darwin_amd64 ;;
*) exit 0 ;;
esac

root=$(cd "$(dirname "$0")/../.." && pwd)
extra="$root/dist/extra"
stage="$root/dist/stage/$t"
mkdir -p "$extra"
rm -rf "$stage"
mkdir -p "$stage"

version=$(sed -n 's/^  version: //p' "$root/packaging/collector/manifest.yaml")

# collector <os_arch> [stage-dir]: build otelcol-compy for the platform,
# tarball it into dist/extra, and optionally leave a copy in stage-dir.
collector() {
  goos=${1%_*}
  goarch=${1#*_}
  work=$(mktemp -d)
  # Throwaway GOCACHE per platform: each collector build writes a few GB of
  # objects that no other platform reuses, so a shared cache just stacks
  # them up until the disk fills. Dies with $work below.
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 GOCACHE="$work/gocache" \
    sh "$root/packaging/collector/build.sh" "$work"
  name="otelcol-compy_${version}_$1.tar.gz"
  tar -czf "$extra/$name" -C "$work" otelcol-compy otelcol-compy.version
  (cd "$extra" && shasum -a 256 "$name" > "$name.sha256")
  if [ -n "${2:-}" ]; then
    cp "$work/otelcol-compy" "$work/otelcol-compy.version" "$2/"
  fi
  rm -rf "$work"
}

collector "$t" "$stage"

# App bundle, assembled next to the dist binary so nothing near the repo
# root (a live ./compy, ./compy.app) gets touched.
sh "$root/packaging/macos/make-app.sh" "$bin"
if [ "$t" = darwin_arm64 ]; then
  # zip -y keeps the bundle's relative symlink to the compy binary beside it.
  (cd "$(dirname "$bin")" && rm -f "$extra/compy.app.zip" && zip -qry "$extra/compy.app.zip" compy.app)
fi
# Staged copy: swap the symlink for a real copy of this arch's binary.
cp -R "$(dirname "$bin")/compy.app" "$stage/compy.app"
rm "$stage/compy.app/Contents/MacOS/compy"
cp "$bin" "$stage/compy.app/Contents/MacOS/compy"

if [ "$t" = darwin_arm64 ]; then
  collector linux_amd64
  collector linux_arm64
fi
