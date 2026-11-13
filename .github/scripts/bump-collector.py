#!/usr/bin/env python3
"""bump-collector.py NEW_VERSION

Retargets compy's pinned collector distributions at a newer upstream
release: rewrites internal/distro/defs.go (versions, URLs, and per-asset
sha256 values fetched from the release's published .sha256 assets).

Run by .github/workflows/collector-bump.yml; safe to run locally. Stdlib
only, edits files in place, exits non-zero without touching anything when
the new version does not check out.
"""

import pathlib
import re
import sys
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
DEFS = ROOT / "internal" / "distro" / "defs.go"


def fetch(url):
    with urllib.request.urlopen(url) as resp:
        return resp.read().decode()



def main():
    if len(sys.argv) != 2 or not re.fullmatch(r"\d+\.\d+\.\d+", sys.argv[1]):
        sys.exit(__doc__.splitlines()[0])
    new = sys.argv[1]

    defs = DEFS.read_text()
    m = re.search(r'^\s*Version:\s+"(\d+\.\d+\.\d+)",$', defs, re.M)
    if not m:
        sys.exit(f"no Version in {DEFS}")
    old = m.group(1)
    if old == new:
        print(f"already at {new}, nothing to do")
        return
    defs = defs.replace(old, new)

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

    DEFS.write_text("".join(out))
    print(f"bumped {old} -> {new}")


if __name__ == "__main__":
    main()
