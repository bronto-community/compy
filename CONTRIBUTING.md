# Contributing to compy

## 5-minute path

```sh
git clone https://github.com/bronto-community/compy
cd compy
go build -o compy-dev ./cmd/compy   # untagged dev build
go test ./...
./compy-dev status
```

The untagged build needs nothing but Go 1.25+ and works fully except
`compy window`, which errors at runtime by design of Wails (the
native-window library). The full native build is:

```sh
CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -o compy ./cmd/compy
```

The `desktop,production` tags are required by Wails; the `CGO_LDFLAGS`
links a framework Wails references but does not link itself. Neither
applies to non-darwin builds.

On first use compy downloads a pinned, checksum-verified collector build
(~90MB). Two optional extras (the Homebrew install ships both):

- **Bundled collector.** `sh packaging/collector/build.sh` builds
  `otelcol-compy`, compy's own curated collector distribution (components
  pinned in `packaging/collector/manifest.yaml`), next to the compy binary
  via the OpenTelemetry Collector Builder. It is a separate, slow step.
  When it sits next to the compy executable it is the default collector
  and no download happens.
- **macOS app bundle.** `sh packaging/macos/make-app.sh ./compy` assembles
  a `compy.app` next to the binary so the standalone window shows the
  right identity in the app menu and Dock. Everything works without it.
  See `packaging/macos/README.md`.

## The gates

Every change should pass these before a PR; CI runs the same set:

```sh
go build -o /dev/null ./cmd/compy
go vet ./...
gofmt -l .                                                   # must print nothing
go test ./...
GOOS=linux CGO_ENABLED=0 go build -o /dev/null ./cmd/compy   # cross-build stays green
CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -o /dev/null ./cmd/compy
node --test internal/webui/static/helpers.test.js            # web UI logic tests
go test -tags=integration ./integration/                     # needs OTELCOL_BIN=/path/to/otelcol
```

## Layout

- `cmd/compy` — the CLI entry point; one flat command table.
- `internal/` — everything else: `app` orchestrates, `collector` runs and
  probes the binary, `cfgstore` owns configurations and presets, `catalog`
  is the config-template engine, `distro` manages collector builds,
  `launchd` renders and drives the LaunchAgent, `webui` is the embedded
  single-page app plus the REST API, `tray`/`window` are the macOS
  surfaces. `CLAUDE.md` in the repo root carries the full module map and
  is kept current.
- `api/openapi.json` — the committed REST contract. Adding, removing, or
  renaming a route means updating both the spec and `internal/webui`'s
  route table, or `TestOpenAPIDriftAgainstRoutes` fails.
- `integration/` — black-box tests against a real collector binary; they
  only run with the `integration` tag and `OTELCOL_BIN` set.

Unit tests live next to their packages. Tests never touch your real state
directory — they set `COMPY_HOME` to a temp dir, and anything driving
launchd stubs it out.

## Conventional commits

Commit subjects follow
[Conventional Commits](https://www.conventionalcommits.org/): `feat:`,
`fix:`, `docs:`, `test:`, `chore:`, `refactor:`. Release notes are
generated from the commit history, so an accurate prefix files your change
where readers will look for it.

## Deferred work gets a fix or an issue

When you notice something out of scope while working: fix it now if it is
small and in code your change already touches, otherwise open an issue and
link it. A "we should probably…" line in a PR description is where work
goes to be forgotten.

## Releases

Pushing a `vX.Y.Z` tag runs the release workflow: GoReleaser builds the
compy archives (darwin arm64+amd64 with cgo, linux amd64+arm64 without),
the `otelcol-compy` collector tarballs, and the `compy.app` zip, then
publishes the GitHub release and the Homebrew cask in
`bronto-community/homebrew-tap`. `.github/workflows/README.md` documents
every workflow and the secrets involved.
