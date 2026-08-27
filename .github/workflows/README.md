# Workflows

| Workflow | Triggers | What it does |
|---|---|---|
| `ci.yml` | PR, push to main, dispatch | gofmt, vet, tests, linux cross-build on ubuntu; vet + tests on macOS (push/dispatch only while the repo is private, macOS minutes cost 10x there) |
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

## Private-repo note for Homebrew

The tap is public but the cask downloads its archive from this repo's
GitHub release. While the repo is private, `brew install
bronto-community/tap/compy` only works with a `HOMEBREW_GITHUB_API_TOKEN`
environment variable holding a token that can read this repo. That caveat
disappears when the repo goes public; at the same time, widen `ci.yml`'s
macOS job to pull requests.
