# compy

Local OpenTelemetry Collector manager: CLI + web UI + tray around a
launchd-supervised `otelcol` process. See README.md for what it does and the
non-goals.

- Specs: `docs/superpowers/specs/2026-08-24-local-collector-design.md` (v1,
  still authoritative for launchd/no-daemon/state-dir/tray/brand rules),
  `docs/superpowers/specs/2026-08-25-compy-v2-configs-design.md` (v2,
  supersedes the v1 backend-fragment model with configurations)
- Plan: `.superpowers/sdd/2026-08-25-compy-v2-p1-core/`

## Build / test

```
go build ./cmd/compy
go vet ./...
gofmt -l .
go test ./...
go test -tags=integration ./integration/         # needs OTELCOL_BIN=/path/to/otelcol (real collector binary)
GOOS=linux CGO_ENABLED=0 go build -o /dev/null ./cmd/compy   # cross-build gate
```

The `-o /dev/null` on the linux gate is load-bearing: `./compy` in the repo
root is the LIVE binary — the user's LaunchAgents and menu-bar tray execute
that exact path. Never overwrite it with a non-darwin build; rebuild it only
as `go build -o compy ./cmd/compy` when deliberately rolling out.

## Dependencies

Stdlib only, except `fyne.io/systray` (tray icon; darwin-only build, stubbed
out on other GOOS in `internal/tray`) and `github.com/webview/webview_go`
(native window; darwin-only build, stubbed out on other GOOS in
`internal/window`).

## Module layout (`internal/*`)

- `app` — orchestrates everything else: turns the active configuration + its
  preset into a validated, installed, running collector; the one API
  the CLI, web UI, and tray all call into.
- `collector` — runs and probes a local OpenTelemetry Collector binary
  (validate, health probe, log tail); starting it is `launchd`'s job, via
  `app.Apply`.
- `cfgstore` — configurations: `configs/<name>/` (config.yaml + meta.json),
  CRUD/copy, presets, provenance hashing, shipped defaults, remote
  sync, last-good snapshot/restore.
- `vars` — extracts `${VAR}` / `${env:VAR:-default}` references (and their
  trailing-comment descriptions) from collector YAML.
- `distro` — pinned collector-distribution definitions, checksum-verified
  on-demand download, and the distro registry.
- `envvars` — computes the `OTEL_*` vars compy exposes; emits them as shell
  scripts (`compy env`), subprocess environments (`compy run`), or OS-level
  (`launchctl setenv`) settings.
- `launchd` — macOS LaunchAgent management: renders the plist, installs /
  uninstalls / kickstarts / inspects it via `launchctl`.
- `state` — on-disk state: settings, distros, state directory layout
  (`COMPY_HOME`, below). Also home to `BadRequest`/`IsBadRequest`, the
  marker that says an error is the caller's mistake (400) rather than ours
  (500): it lives in this leaf package so `cfgstore` and `app` can mark
  errors without importing the HTTP layer back.
- `tray` — macOS menu-bar icon (status, Open UI, Quit — deliberately no
  per-backend toggles); non-darwin build is a no-op stub.
- `webui` — localhost-only web UI: JSON API plus an embedded (`go:embed`)
  single-page app; no internal dependencies (it recognises `state`'s
  bad-request marker structurally, by its `BadRequest() bool` method), the
  caller wires behavior in via a closure struct. A 5xx is the only thing
  the page appends a collector log tail to, so a user mistake answered 500
  buries its own message — mark it.
- `window` — the native window wrapper `compy window` runs.

## api/

`api/openapi.json` is the committed, authoritative REST contract (served by
whatever UI process is running — no daemon). `internal/webui`'s `routes()`
table and the spec must agree: adding, removing, or renaming a route means
updating BOTH, or `TestOpenAPIDriftAgainstRoutes` fails.

## Configurations

A configuration (`internal/cfgstore`) is a whole collector `config.yaml` +
`meta.json` (provenance, presets) under `configs/<name>/`. Exactly one
configuration, and one of its presets, is active at a time;
activating (`app.Activate`) puts that preset's values into the LaunchAgent's
environment so the collector expands its own `${VAR}` / `${env:VAR:-def}`
references — no text substitution in compy. Three shipped defaults
(`debug`, `otlp`, `bronto`, embedded via `internal/cfgstore/defaults/*.yaml`)
are materialized into `configs/` on first run. Edit-protection and sync
share one mechanism: a config's current YAML hash vs. its recorded
`pristine_sha256` — matching means "unmodified" (shipped configs upgrade in
place, remote configs may `sync`), differing means "locally modified"
(upgrades/`sync` leave it alone; `resync` force-discards local edits). On
first v2 run, a v1 state dir's rendered effective config becomes a new
`migrated` configuration (activated if anything was enabled) and the old
`config/` tree is archived to `legacy-v1/` — one-way, logged to stderr.

## COMPY_HOME

State directory defaults to `~/Library/Application Support/compy` on
macOS (`$XDG_DATA_HOME/compy` or `~/.local/share/compy` elsewhere), overridable
via the `COMPY_HOME` env var. `internal/state.Dir()` resolves and creates it
(`configs/`, `logs/`, `last-good/`). Tests set `COMPY_HOME` to
`t.TempDir()` rather than touching the real state dir.

## Ports

Default OTLP ports: 14317 gRPC, 14318 HTTP (standard ports + 10000).
Configurable per-install in `settings.json`.
