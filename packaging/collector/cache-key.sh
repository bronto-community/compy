#!/bin/sh
# Shared by the prebuild and release consumer; independent of checkout path.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
[ -r "$here/manifest.yaml" ]
[ -r "$here/builder-version" ]
cat "$here/manifest.yaml" "$here/builder-version" | shasum -a 256 | cut -c1-8
