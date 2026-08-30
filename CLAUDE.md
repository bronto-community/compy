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
go build -o /dev/null ./cmd/compy
go vet ./...
gofmt -l .
go test ./...
go test -tags=integration ./integration/         # needs OTELCOL_BIN=/path/to/otelcol (real collector binary)
GOOS=linux CGO_ENABLED=0 go build -o /dev/null ./cmd/compy   # cross-build gate
```

Shippable darwin binaries need Wails' own build tags — `compy window` in an
untagged build compiles but errors at runtime (Wails gates wails.Run behind
`desktop,production` by design; the untagged `go build ./cmd/compy` stays a
compile gate only). The CGO_LDFLAGS is because Wails' production asset
handler references UTType without linking its framework:

```
CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -o /dev/null ./cmd/compy
```

Every `-o /dev/null` above is load-bearing: `./compy` in the repo root is
the LIVE binary — the user's LaunchAgents and menu-bar tray execute that
exact path, and a bare `go build ./cmd/compy` from the repo root would
overwrite it with an untagged build whose `compy window` errors at runtime.
Never overwrite it with a non-darwin or untagged build; rebuild it only as

```
CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -o compy ./cmd/compy
```

when deliberately rolling out.

`sh packaging/collector/build.sh` builds `otelcol-compy` (the bundled
distro, from `packaging/collector/manifest.yaml` via OCB, builder pinned to
the manifest's collector version) next to the compy binary, plus its
`otelcol-compy.version` stamp. Slow (big module graph) and deliberately NOT
part of the gates above; run it when the manifest changes.

## Dependencies

Stdlib only, except `fyne.io/systray` (tray icon; darwin-only build, stubbed
out on other GOOS in `internal/tray`) and `github.com/wailsapp/wails/v2`
(native window, used as a library — no wails CLI, wails.json, or node;
darwin-only build, stubbed out on other GOOS in `internal/window`).

## Module layout (`internal/*`)

- `app` — orchestrates everything else: turns the active configuration + its
  preset into a validated, installed, running collector; the one API
  the CLI, web UI, and tray all call into.
- `collector` — runs and probes a local OpenTelemetry Collector binary
  (validate, health probe, log tail); starting it is `launchd`'s job, via
  `app.Apply`.
- `cfgstore` — configurations: `configs/<name>/` (config.yaml + meta.json,
  plus config.tmpl for templated tier-3 configs — the SOURCE the yaml is
  rendered from), CRUD/copy, presets, provenance hashing, shipped defaults,
  remote sync, last-good snapshot/restore.
- `catalog` — the config-template engine (tier 3): parses template sources
  (JSON front-matter schema + `---` + Go text/template body), validates
  knob values, renders to plain collector YAML (only `type: secret` fields
  survive as `${env:}` refs). Ships the embedded starter catalog; a config
  created from it COPIES the source and owns it
  (docs/design/2026-08-30-config-templates.md, Amendment 3).
- `vars` — extracts `${VAR}` / `${env:VAR:-default}` references (and their
  trailing-comment descriptions) from collector YAML.
- `distro` — pinned collector-distribution definitions, checksum-verified
  on-demand download, the distro registry, the bundled `otelcol-compy`
  (resolved next to the compy executable, never downloaded, updates with
  compy releases), and pulling newer upstream releases of the pinned
  distros. Trust model: a PINNED version ships with a compiled-in sha256; a
  PULLED update is verified against the `.sha256` asset published in the
  same upstream release (TLS + same-origin release assets). Default distro
  chain: explicit `settings.Distro` → bundled-if-present → `contrib`.
  Pulled versions are recorded in settings.json (`distro_versions`) so the
  last-good restore rolls a bad update back. `contrib` is the implicit
  default: an empty `settings.Distro` resolves to it, auto-downloaded by
  the first operation that needs a collector binary (`app.DefaultDistro`).
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
`meta.json` (provenance, presets) under `configs/<name>/`. Every config
keeps at least one preset (creation paths write an empty `default`;
`cfgstore.EnsurePresets` backfills older state at `app.New`; the last
preset can't be deleted). Exactly one
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

A TEMPLATED (tier-3) config additionally keeps its source at
`configs/<name>/config.tmpl`; `config.yaml` is its rendered output, written
on every successful save, so everything downstream (collector path, plist,
vars cards) stays plain. The SOURCE carries the pristine hash (it IS the
config; remote configs may be templated and sync their source). Tier
detection is textual (`catalog.IsSource`): front-matter text through the
yaml write routes to the source pipeline, plain yaml over a templated
config demotes it (source dropped). The save pipeline
(`app.WriteConfigSource`) parses, reconciles knobs (removed pruned, new
defaulted), renders, stores the pair, then collector-validates —
restoring everything on rejection (nothing-was-saved).

## COMPY_HOME

State directory defaults to `~/Library/Application Support/compy` on
macOS (`$XDG_DATA_HOME/compy` or `~/.local/share/compy` elsewhere), overridable
via the `COMPY_HOME` env var. `internal/state.Dir()` resolves and creates it
(`configs/`, `logs/`, `last-good/`). Tests set `COMPY_HOME` to
`t.TempDir()` rather than touching the real state dir.

## Ports

Default OTLP ports: 14317 gRPC, 14318 HTTP (standard ports + 10000).
Configurable per-install in `settings.json`.
