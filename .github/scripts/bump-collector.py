#!/usr/bin/env python3
"""bump-collector.py NEW_VERSION

Retargets compy at a newer upstream collector release: rewrites every
component pin in packaging/collector/manifest.yaml (including the paired
1.x confmap-provider line, read from the upstream otelcol manifest at the
new tag) and internal/distro/defs.go (versions, URLs, and per-asset sha256
values fetched from the release's published .sha256 assets).

Run by .github/workflows/collector-bump.yml; safe to run locally. Stdlib
only, edits files in place, exits non-zero without touching anything when
the new version does not check out.
"""

import pathlib
import re
import sys
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
MANIFEST = ROOT / "packaging" / "collector" / "manifest.yaml"
DEFS = ROOT / "internal" / "distro" / "defs.go"
UPSTREAM_MANIFEST = (
    "https://raw.githubusercontent.com/open-telemetry/"
    "opentelemetry-collector-releases/v{}/distributions/otelcol/manifest.yaml"
)


def fetch(url):
    with urllib.request.urlopen(url) as resp:
        return resp.read().decode()


def provider_version(manifest_text):
    m = re.search(r"confmap/provider/envprovider v(\d+\.\d+\.\d+)", manifest_text)
    if not m:
        sys.exit("no envprovider pin found in manifest")
    return m.group(1)


def main():
    if len(sys.argv) != 2 or not re.fullmatch(r"\d+\.\d+\.\d+", sys.argv[1]):
        sys.exit(__doc__.splitlines()[0])
    new = sys.argv[1]

    manifest = MANIFEST.read_text()
    m = re.search(r"^  version: (\d+\.\d+\.\d+)$", manifest, re.M)
    if not m:
        sys.exit(f"no version in {MANIFEST}")
    old = m.group(1)
    if old == new:
        print(f"already at {new}, nothing to do")
        return

    # The 0.x collector line and the 1.x confmap line move together; the
    # upstream otelcol manifest at the new tag says which 1.x pairs with it.
    prov_old = provider_version(manifest)
    prov_new = provider_version(fetch(UPSTREAM_MANIFEST.format(new)))

    manifest = manifest.replace(old, new).replace(prov_old, prov_new)
    defs = DEFS.read_text().replace(old, new)

    # Refresh the compiled-in checksums from the release's .sha256 assets.
    # In defs.go each distro block lists URLs before SHA256, so the last
    # asset seen for a platform key is the one its sha entry belongs to.
    base = re.search(r'^const releaseBase = "(.*)"$', defs, re.M).group(1)
    asset_for = {}
    out = []
    for line in defs.splitlines(keepends=True):
        u = re.search(r'"(\w+)":\s+releaseBase \+ "([^"]+)"', line)
        if u:
            asset_for[u.group(1)] = u.group(2)
        s = re.search(r'"(\w+)":\s+"([0-9a-f]{64})"', line)
        if s:
            plat = s.group(1)
            sha = fetch(base + asset_for[plat] + ".sha256").split()[0]
            if not re.fullmatch(r"[0-9a-f]{64}", sha):
                sys.exit(f"malformed .sha256 asset for {asset_for[plat]}")
            line = line.replace(s.group(2), sha)
        out.append(line)

    MANIFEST.write_text(manifest)
    DEFS.write_text("".join(out))
    print(f"bumped {old} -> {new} (providers {prov_old} -> {prov_new})")


if __name__ == "__main__":
    main()
