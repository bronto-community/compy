# Workflows

| Workflow | Triggers | What it does |
|---|---|---|
| `ci.yml` | PR, push to main, dispatch | gofmt, vet, tests, linux cross-build on ubuntu; vet + tests + tagged window build on macOS |
| `integration.yml` | nightly, collector-related PR, dispatch | downloads the pinned contrib collector (URL and sha256 parsed from `internal/distro/defs.go`, checksum verified) and runs `go test -tags=integration ./integration/` |
| `security.yml` | PR, weekly, dispatch | govulncheck |
| `collector-bump.yml` | manual dispatch only; disabled on GitHub for now | optional PR helper; recurring update checks are tracked in issue #29 |
| `release.yml` | tags `v*` | GoReleaser: compy archives, compy.app zip, GitHub release, Homebrew cask; the otelcol-compy tarballs are fetched prebuilt (see `collector-build.yml`) |
| `collector-build.yml` | collector-related PR, push to main touching `packaging/collector/`, dispatch | PRs build and integration-test the bundled collector; main/dispatch builds four platforms and publishes content-addressed tarballs (`collector-<version>-<manifest+builder sha8>`) with provenance attestation |

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
- `BUMP_PR_TOKEN` (collector-bump): optional bot PAT with contents and
  pull-requests write for this repository. It allows PR creation and lets
  PR checks start automatically. Without it, the workflow uses
  `GITHUB_TOKEN`: the repository must permit Actions to create PRs, and
  resulting PR checks can require a maintainer to approve workflow runs.

## Collector update tracking

Use [issue #29](https://github.com/bronto-community/compy/issues/29) as the
recurring maintenance task. Ask a coding agent to pick it up periodically;
the issue contains the version check, update, build, test, and reporting
runbook. Leave it open after each check and record dated results there.
Scheduled bump PRs are deferred, and the existing bump workflow is disabled
on GitHub. No repository permission or bot-token changes are needed for
this manual approach.

The optional, manual bump helper compares stable numeric tags, skips
drafts and prereleases, and prepares one PR. It updates the
binary release pins/checksums, each selected component's exact upstream
module version, and the separate OCB pin from upstream's Makefile. A
binary patch such as 0.163.2 can use 0.163.0 components and OCB: replacing
every version string with the binary release is not valid.

The collector PR check builds the actual curated distribution and runs the
real-collector integration suite before merge. Main builds use current
stable Go and also exercise the Linux binary before publishing the
prebuilt artifacts. A subsequent Compy release ships those artifacts;
neither an update check nor a PR changes a running local collector.

If automated PR creation is requested later, re-enable the workflow and
either configure `BUMP_PR_TOKEN`, or enable
**Settings → Actions → General → Workflow permissions → Allow GitHub
Actions to create and approve pull requests**. Keep the default token
permission read-only; the workflow grants its own contents/PR write scopes.
If using the default token, approve the PR's workflow runs when GitHub
requires it. See [GitHub's workflow triggering rules](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow).

The setting was disabled and no repository-level `BUMP_PR_TOKEN` was
configured when investigated on 2026-10-26. The scheduled runs had been
failing at PR creation, including
[the October 26 run](https://github.com/bronto-community/compy/actions/runs/40279559906).
Changing the cron or version pins alone does not fix that permission.

Those automation prerequisites are deferred. No auto-merge is configured.

Local update/build/guest-test commands are in
[packaging/collector/README.md](../../packaging/collector/README.md).

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

The generated cask's postflight strips quarantine and nothing else. It
deliberately does **not** run `compy tray install`: that writes a login
LaunchAgent under `~/Library`, which is not something `brew install`
should do unasked, and Homebrew's structured `postflight_steps` — the
replacement for these legacy Ruby flight blocks — run sandboxed "without
network or general home-directory access", so it would stop working
anyway. The cask's caveat tells the user to run `compy tray install`.

The tray's own plist points at the stable `${HOMEBREW_PREFIX}/bin/compy`
symlink, so an upgrade is picked up at the next login without
reinstalling the agent (`compy tray install` again switches over
immediately). The collector job is deliberately left alone — its plist
bakes the resolved (versioned Caskroom) binary path, so after an upgrade
compy surfaces "restart the collector to run the new version"
(`stale_binary` in `/api/status`) and the next restart re-resolves it.

Both launchd labels are **zap-only**. `brew upgrade` runs the old
version's `uninstall` stanza first, and Homebrew's `uninstall launchctl:`
removes the job *and deletes its plist* — when the labels sat under
`uninstall`, every upgrade stopped the collector and deleted the tray's
login item, contradicting both paragraphs above. Homebrew cannot skip it
on upgrade (`on_upgrade` covers `signal` only). So `zap` boots both labels
out and trashes their plists and `~/Library/Application Support/compy`,
while a plain `brew uninstall` leaves the jobs behind; the caveat says to
use `--zap`, or `compy stop` and `compy tray uninstall` first.

CI renders and checks the cask on every PR (the `cask` job in ci.yml), so
a template change cannot reach the tap unchecked. To inspect the rendered
cask locally without the slow collector builds:
`HOMEBREW_TAP_TOKEN=dummy SKIP_COLLECTOR=1 go run
github.com/goreleaser/goreleaser/v2@v2.17.0 release --snapshot --clean
--skip=publish,sign,sbom` (the token template needs a value even for a
render), then read
`dist/homebrew/Casks/compy.rb`.

