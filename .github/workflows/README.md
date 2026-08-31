# Workflows

| Workflow | Triggers | What it does |
|---|---|---|
| `ci.yml` | PR, push to main, dispatch | gofmt, vet, tests, linux cross-build on ubuntu; vet + tests + tagged window build on macOS |
| `integration.yml` | nightly, dispatch | downloads the pinned otlp collector (URL and sha256 parsed from `internal/distro/defs.go`, checksum verified) and runs `go test -tags=integration ./integration/` |
| `security.yml` | PR, weekly, dispatch | govulncheck |
| `collector-bump.yml` | weekly, dispatch | opens a PR bumping the pinned collector release via `.github/scripts/bump-collector.py` when upstream has a newer one |
| `release.yml` | tags `v*` | GoReleaser: compy archives, otelcol-compy tarballs, compy.app zip, GitHub release, Homebrew cask |

Dependabot (`.github/dependabot.yml`) watches gomod and github-actions
weekly.

## Secrets

- `HOMEBREW_TAP_TOKEN` (releases): a token with push access to
  `bronto-community/homebrew-tap`, used by GoReleaser to commit the
  generated cask to `Casks/compy.rb`. When it is missing the release still
  succeeds; only the cask step is skipped.
- `BUMP_PR_TOKEN` (optional): a PAT with contents and pull-requests write.
  PRs opened with the default `GITHUB_TOKEN` do not trigger CI (GitHub
  blocks workflow recursion), so without this secret a collector-bump PR
  needs a manual nudge (close/reopen) before CI runs on it.

## Known limitation: no signing / notarization

Release binaries (compy, otelcol-compy, compy.app) are ad-hoc signed at
best — there is no Developer ID signing or notarization in the pipeline.
The cask compensates by stripping `com.apple.quarantine` recursively from
the staged install in its postflight; a manually downloaded archive needs
the same `xattr -dr com.apple.quarantine` by hand. Proper signing needs an
Apple Developer account and certificate secrets in this repo.

## Upgrade / uninstall behavior of the cask

The generated cask carries the lifecycle: its postflight strips quarantine
and, when the tray LaunchAgent is installed, bounces it so the menu bar
runs the new binary immediately after `brew upgrade` (the tray plist
points at the stable `/opt/homebrew/bin/compy` symlink). The collector job
is deliberately left alone — its plist bakes the resolved (versioned
Caskroom) binary path, so after an upgrade compy surfaces "restart the
collector to run the new version" (`stale_binary` in `/api/status`) and
the next restart re-resolves it. `uninstall` boots both launchd labels
out; `zap` additionally trashes the two LaunchAgents plists and
`~/Library/Application Support/compy`. To inspect the rendered cask
without the slow collector builds:
`SKIP_COLLECTOR=1 go run github.com/goreleaser/goreleaser/v2@latest
release --snapshot --clean --skip=publish`, then read
`dist/homebrew/Casks/compy.rb`.

