#!/bin/sh
# goreleaser-extras.sh <compy-binary> <target> [version]: GoReleaser
# post-build hook (see .goreleaser.yaml) that produces what GoReleaser's own
# build matrix cannot: the compy.app zip and the per-arch staging dir the
# darwin archives (and thus the Homebrew cask) fold in. The hook fires once
# per darwin build; each invocation handles its own architecture, and only
# the darwin_arm64 one writes the zip, so the two can run in parallel
# without racing on a file.
#
# Writes into dist/stage/<os>_<arch>/ (folded into that arch's archive by
# .goreleaser.yaml, so the cask's Caskroom staging dir holds it next to the
# compy binary):
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
set -eu

bin=$1
target=$2
# $3 is the compy release version, forwarded to make-app.sh so the app
# bundle's CFBundleShortVersionString matches the release. Optional: a bare
# local invocation still works.
compy_version=${3:-}
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

# App bundle, assembled next to the dist binary so nothing near the repo
# root (a live ./compy, ./compy.app) gets touched.
sh "$root/packaging/macos/make-app.sh" "$bin" "$compy_version"
if [ "$t" = darwin_arm64 ]; then
  # zip -y keeps the bundle's relative symlink to the compy binary beside it.
  (cd "$(dirname "$bin")" && rm -f "$extra/compy.app.zip" && zip -qry "$extra/compy.app.zip" compy.app)
fi
# Staged copy: swap the symlink for a real copy of this arch's binary.
cp -R "$(dirname "$bin")/compy.app" "$stage/compy.app"
rm "$stage/compy.app/Contents/MacOS/compy"
cp "$bin" "$stage/compy.app/Contents/MacOS/compy"
