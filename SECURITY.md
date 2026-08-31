# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities **privately** via GitHub's security
advisory flow: [Report a vulnerability](https://github.com/bronto-community/compy/security/advisories/new).
Do not open a public issue for security reports.

You can expect an initial response within a few business days. Please
include a description, reproduction steps, and the affected version
(`compy version`).

## Supported versions

Only the latest release receives security fixes. Update with
`brew upgrade compy` or from the
[releases page](https://github.com/bronto-community/compy/releases).

| Version | Supported |
| --- | --- |
| latest release | yes |
| older releases | no |

## Verifying releases

Every release attaches a `compy_<version>_checksums.txt` covering all
archives:

```sh
shasum -a 256 --check --ignore-missing compy_<version>_checksums.txt
```

Release binaries are not yet Developer ID-signed or notarized; the
Homebrew cask clears macOS's quarantine flag on install, a manually
downloaded archive needs `xattr -d com.apple.quarantine` (after verifying
its checksum).

## Scope notes

- The web UI and REST API bind to localhost only; there is no daemon and
  nothing listens on external interfaces. The shipped collector
  configurations bind their OTLP receivers to `127.0.0.1` as well.
- Downloaded collector distributions are pinned: a shipped version is
  verified against a compiled-in sha256, a pulled update against the
  `.sha256` asset published in the same upstream release.
- Preset values (API keys included) are stored in compy's state directory
  (`~/Library/Application Support/compy`) and travel only into the local
  collector's process environment — compy itself sends them nowhere.
  Keychain-backed secret storage is a known non-goal for now.
- Dependencies are watched by Dependabot, and `govulncheck` runs on every
  PR plus a weekly sweep.
