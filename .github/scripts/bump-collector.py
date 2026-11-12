#!/usr/bin/env python3
"""bump-collector.py NEW_VERSION

Retargets compy at a newer upstream collector release: rewrites every
component pin in packaging/collector/manifest.yaml from upstream's actual
manifests, pins OCB from upstream's Makefile, and updates internal/distro/defs.go
with the binary release version and published per-asset sha256 values.
Binary patch releases do not necessarily publish matching Go module tags.

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
BUILDER_VERSION = ROOT / "packaging" / "collector" / "builder-version"
UPSTREAM = (
    "https://raw.githubusercontent.com/open-telemetry/"
    "opentelemetry-collector-releases/v{}/{}"
)
GOMOD = re.compile(r"^([ \t]*- gomod: )(\S+) v(\d+\.\d+\.\d+)([ \t]*(?:#.*)?)$", re.M)


def fetch(url):
    request = urllib.request.Request(url, headers={"User-Agent": "compy-collector-bump"})
    with urllib.request.urlopen(request, timeout=30) as resp:
        return resp.read().decode()


def upstream_pins(new):
    pins = {}
    for distro in ("otelcol", "otelcol-contrib"):
        text = fetch(UPSTREAM.format(new, f"distributions/{distro}/manifest.yaml"))
        version = re.search(r"^  version: (\S+)$", text, re.M)
        if not version or version.group(1) != new:
            raise ValueError(f"upstream {distro} manifest does not describe {new}")
        for _, module, version, _ in GOMOD.findall(text):
            if module in pins and pins[module] != version:
                raise ValueError(f"upstream manifests disagree about {module}")
            pins[module] = version
    makefile = fetch(UPSTREAM.format(new, "Makefile"))
    builder = re.search(r"^OTELCOL_BUILDER_VERSION\s*\?=\s*(\d+\.\d+\.\d+)\s*$", makefile, re.M)
    if not builder:
        raise ValueError("upstream Makefile has no OCB version")
    return pins, builder.group(1)


def rewrite_manifest(text, new, pins):
    text = re.sub(r"^(  version: )\d+\.\d+\.\d+$", lambda m: m[1] + new, text, count=1, flags=re.M)

    def replace(m):
        module = m[2]
        if module not in pins:
            raise ValueError(f"{module} is absent from upstream {new}; review component changes before bumping")
        return m[1] + module + " v" + pins[module] + m[4]

    return GOMOD.sub(replace, text)


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

    pins, builder = upstream_pins(new)
    manifest = rewrite_manifest(manifest, new, pins)
    defs = DEFS.read_text().replace(old, new)

    # Refresh the compiled-in checksums from the release's .sha256 assets.
    # In defs.go each distro block lists URLs before SHA256, so the last
    # asset seen for a platform key is the one its sha entry belongs to.
    match = re.search(r'^const releaseBase = "(.*)"$', defs, re.M)
    if not match:
        raise ValueError("no releaseBase in distro definitions")
    base = match.group(1)
    asset_for = {}
    out = []
    for line in defs.splitlines(keepends=True):
        u = re.search(r'"(\w+)":\s+releaseBase \+ "([^"]+)"', line)
        if u:
            asset_for[u.group(1)] = u.group(2)
        s = re.search(r'"(\w+)":\s+"([0-9a-f]{64})"', line)
        if s:
            plat = s.group(1)
            if plat not in asset_for:
                raise ValueError(f"no release asset for {plat}")
            fields = fetch(base + asset_for[plat] + ".sha256").split()
            if not fields or not re.fullmatch(r"[0-9a-f]{64}", fields[0]):
                raise ValueError(f"malformed .sha256 asset for {asset_for[plat]}")
            sha = fields[0]
            line = line.replace(s.group(2), sha)
        out.append(line)

    MANIFEST.write_text(manifest)
    DEFS.write_text("".join(out))
    BUILDER_VERSION.write_text(builder + "\n")
    print(f"bumped release {old} -> {new} (OCB {builder}; component pins from upstream manifests)")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError) as exc:
        sys.exit(str(exc))
