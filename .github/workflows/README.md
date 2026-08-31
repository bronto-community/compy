# Workflows

| Workflow | Triggers | What it does |
|---|---|---|
| `ci.yml` | PR, push to main, dispatch | gofmt, vet, tests, linux cross-build on ubuntu; vet + tests + tagged window build on macOS |
| `integration.yml` | nightly, dispatch | downloads the pinned otlp collector (URL and sha256 parsed from `internal/distro/defs.go`, checksum verified) and runs `go test -tags=integration ./integration/` |
| `security.yml` | PR, weekly, dispatch | govulncheck |
| `collector-bump.yml` | weekly, dispatch | opens a PR bumping the pinned collector release via `.github/scripts/bump-collector.py` when upstream has a newer one |
| `release.yml` | tags `v*` | GoReleaser: compy archives, compy.app zip, GitHub release, Homebrew cask; the otelcol-compy tarballs are fetched prebuilt (see `collector-build.yml`) |
| `collector-build.yml` | push to main touching `packaging/collector/`, dispatch | prebuilds the four otelcol-compy tarballs and publishes them under a content-addressed tag (`collector-<version>-<manifest sha8>`), with provenance attestation; the release workflow downloads these instead of compiling |

Dependabot (`.github/dependabot.yml`) watches gomod and github-actions
weekly.

## Secrets

- `BRONTO_COMMUNITY_BOT_APP_ID` / `BRONTO_COMMUNITY_BOT_PRIVATE_KEY`
  (releases): the Bronto Community Bot GitHub App. The release workflow
  mints a short-lived installation token from it per run — scoped to
  contents:write on `bronto-community/homebrew-tap` only — and hands it
  to GoReleaser as `HOMEBREW_TAP_TOKEN` to commit the generated cask to
  `Casks/compy.rb`. No long-lived cross-repo token exists. The same app
  releases bronto-cli.
- The release job runs in the protected `release` environment (required
  reviewer), so a pushed `v*` tag waits for a human approval before any
  release credential is minted.
- `BUMP_PR_TOKEN` (optional): a PAT with contents and pull-requests write.
  PRs opened with the default `GITHUB_TOKEN` do not trigger CI (GitHub
  blocks workflow recursion), so without this secret a collector-bump PR
  needs a manual nudge (close/reopen) before CI runs on it.

## Release integrity, and the Apple-signing limitation

Releases are supply-chain signed: cosign (keyless OIDC) signs the
checksums file, actions/attest-build-provenance attaches SLSA build
provenance, and syft SBOMs accompany every archive — SECURITY.md carries
the verification commands.

What they are NOT is Apple-signed: the binaries (compy, otelcol-compy,
compy.app) are ad-hoc signed at best — no Developer ID signing or
notarization in the pipeline. The cask compensates by stripping
`com.apple.quarantine` recursively from the staged install in its
postflight; a manually downloaded archive needs the same
`xattr -dr com.apple.quarantine` by hand. Proper signing needs an Apple
Developer account and certificate secrets in this repo.

## Upgrade / uninstall behavior of the cask

The generated cask carries the lifecycle: its postflight strips
quarantine and runs `compy tray install` via the stable
`${HOMEBREW_PREFIX}/bin/compy` symlink — a fresh install puts compy in
the menu bar (the main interface), and an upgrade moves the menu bar
onto the new binary immediately. The collector job
is deliberately left alone — its plist bakes the resolved (versioned
Caskroom) binary path, so after an upgrade compy surfaces "restart the
collector to run the new version" (`stale_binary` in `/api/status`) and
the next restart re-resolves it. `uninstall` boots both launchd labels
out; `zap` additionally trashes the two LaunchAgents plists and
`~/Library/Application Support/compy`. To inspect the rendered cask
without the slow collector builds:
`HOMEBREW_TAP_TOKEN=dummy SKIP_COLLECTOR=1 go run
github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean
--skip=publish,sign,sbom` (the token template needs a value even for a
render), then read
`dist/homebrew/Casks/compy.rb`.

