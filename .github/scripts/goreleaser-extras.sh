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
#
# SKIP_COLLECTOR=1 skips fetching the prebuilt collector tarballs (a
# config-inspection dry run — e.g. rendering the cask .rb via `release
# --snapshot`); the staged dirs and dist/extra then simply lack the
# otelcol-compy artifacts. Never set in the release workflow.
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

# collector <os_arch> [stage-dir]: fetch the PREBUILT otelcol-compy
# tarball for this platform into dist/extra, and optionally unpack a copy
# into stage-dir. The tarballs are built once per manifest change by
# .github/workflows/collector-build.yml and published under a
# content-addressed tag (collector-<version>-<manifest sha8>), so a
# release no longer spends ~25 minutes compiling four collectors. Trust
# model: same-origin release assets over TLS with a sha256 check, like
# compy's own pulled-distro updates; the compy release then folds the
# tarballs into its own signed checksums.txt.
collector() {
  mhash=$(shasum -a 256 "$root/packaging/collector/manifest.yaml" | cut -c1-8)
  tag="collector-${version}-${mhash}"
  name="otelcol-compy_${version}_$1.tar.gz"
  url="https://github.com/bronto-community/compy/releases/download/$tag/$name"
  work=$(mktemp -d)
  if ! curl -fsSL -o "$work/$name" "$url"; then
    echo "no prebuilt collector release for this manifest ($tag)." >&2
    echo "run the 'Collector build' workflow on main, wait for it, then retry." >&2
    rm -rf "$work"
    exit 1
  fi
  curl -fsSL -o "$work/$name.sha256" "$url.sha256"
  (cd "$work" && shasum -a 256 -c "$name.sha256" >/dev/null)
  cp "$work/$name" "$extra/$name"
  cp "$work/$name.sha256" "$extra/$name.sha256"
  if [ -n "${2:-}" ]; then
    tar -xzf "$work/$name" -C "$2"
  fi
  rm -rf "$work"
}

[ -n "${SKIP_COLLECTOR:-}" ] || collector "$t" "$stage"

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

if [ "$t" = darwin_arm64 ] && [ -z "${SKIP_COLLECTOR:-}" ]; then
  collector linux_amd64
  collector linux_arm64
fi
