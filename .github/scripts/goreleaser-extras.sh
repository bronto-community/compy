#!/bin/sh
# goreleaser-extras.sh <compy-binary> <target>: GoReleaser post-build hook
# (see .goreleaser.yaml) that produces the release artifacts GoReleaser's
# own build matrix cannot: the otelcol-compy collector tarballs and the
# compy.app zip. The hook fires once per darwin build; everything happens
# on the darwin_arm64 invocation and the others exit immediately.
#
# Writes into dist/extra/:
#   compy.app.zip                                (bundle for the arm64 binary;
#                                                 expects a compy binary next
#                                                 to it when extracted)
#   otelcol-compy_<ver>_<os>_<arch>.tar.gz       (binary + .version stamp)
#   otelcol-compy_<ver>_<os>_<arch>.tar.gz.sha256 ("<hex>  <name>", the same
#                                                 format as upstream's
#                                                 per-asset .sha256 files)
set -eu

bin=$1
target=$2
# GoReleaser suffixes the target with the micro-architecture (GOARM64), so
# darwin/arm64 arrives as "darwin_arm64_v8.0".
case $target in
darwin_arm64*) ;;
*) exit 0 ;;
esac

root=$(cd "$(dirname "$0")/../.." && pwd)
extra="$root/dist/extra"
mkdir -p "$extra"

# App bundle, assembled next to the dist binary so nothing near the repo
# root (a live ./compy, ./compy.app) gets touched. zip -y keeps the
# bundle's relative symlink to the compy binary beside it.
sh "$root/packaging/macos/make-app.sh" "$bin"
(cd "$(dirname "$bin")" && rm -f "$extra/compy.app.zip" && zip -qry "$extra/compy.app.zip" compy.app)

version=$(sed -n 's/^  version: //p' "$root/packaging/collector/manifest.yaml")
for t in darwin_arm64 darwin_amd64 linux_amd64 linux_arm64; do
  goos=${t%_*}
  goarch=${t#*_}
  work=$(mktemp -d)
  # Throwaway GOCACHE per platform: each collector build writes a few GB of
  # objects that no other platform reuses, so a shared cache just stacks
  # them up until the disk fills. Dies with $work below.
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 GOCACHE="$work/gocache" \
    sh "$root/packaging/collector/build.sh" "$work"
  name="otelcol-compy_${version}_${t}.tar.gz"
  tar -czf "$extra/$name" -C "$work" otelcol-compy otelcol-compy.version
  (cd "$extra" && shasum -a 256 "$name" > "$name.sha256")
  rm -rf "$work"
done
