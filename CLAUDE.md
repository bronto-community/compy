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
out on other GOOS in `internal/tray`), `github.com/wailsapp/wails/v2`
(native window, used as a library — no wails CLI, wails.json, or node;
darwin-only build, stubbed out on other GOOS in `internal/window`),
`gopkg.in/yaml.v3` (YAML front matter in tier-3 config sources; owner
ruling 2026-08-29 — the OTel ecosystem itself runs on it), and the
**OpenTelemetry Go SDK** (`go.opentelemetry.io/otel`, `otel/sdk`,
`exporters/otlp/otlptrace/otlptracehttp`) for compy's own tracing.

The OTel ruling is owner-made and deliberate (2026-09-02), against a
measured alternative: the stock OTLP/HTTP exporter is protobuf, so it drags
in grpc, protobuf, grpc-gateway and x/net — **+17.5 MB**, roughly doubling
the binary, versus +5.4 MB for the same SDK behind a hand-written OTLP/JSON
exporter. The owner chose the stock exporter: an OpenTelemetry tool should
ship the OpenTelemetry SDK unmodified, and nothing in compy is hand-rolled
where upstream has the real thing. Do not "optimise" this back into a
custom encoder without a new ruling.

## Module layout (`internal/*`)

- `app` — orchestrates everything else: turns the active configuration + its
  preset into a validated, installed, running collector; the one API
  the CLI, web UI, and tray all call into.
- `collector` — runs and probes a local OpenTelemetry Collector binary
  (validate, health probe, log tail); starting it is `launchd`'s job, via
  `app.Apply`.
- `cfgstore` — configurations: `configs/<name>/` (config.yaml + meta.json,
  plus config.tmpl for templated tier-3 configs — the SOURCE the yaml is
  rendered from), CRUD/copy, presets, provenance hashing, shipped defaults
  (plain from `defaults/*.yaml`, templated from the embedded catalog —
  materialize, upgrade-in-place, retire-when-dropped, reset), remote sync,
  last-good snapshot/restore.
- `catalog` — the config-template engine (tier 3): parses template sources
  (front-matter schema — YAML between `---` marker lines, or the original
  JSON form — + `---` + Go text/template body), validates
  knob values, renders to plain collector YAML (only `type: secret` fields
  survive as `${env:}` refs). Repeat groups are AUTHOR-DEFINED (Amendment
  8): a schema declares any number under `groups:`, each id being both the
  bag key and the render data key; a row's identity is the reserved
  `_label` (`catalog.LabelKey`), edited in place like a preset tab, whose
  slug derives exporter ids and secret env names. Ships the embedded
  catalog — `debug`, `otlp-forward` — which double as the tier-3 shipped
  defaults; a config created from a catalog entry COPIES the source and
  owns it (docs/design/2026-08-30-config-templates.md, Amendment 3).
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
  Every pinned distro is on the same collector version, which is what lets
  the shipped configs use current component names — the exporter is
  `otlp_http`, not the deprecated `otlphttp` alias (renamed in collector
  v0.144.0; the alias still works but warns on every start). A user-managed
  binary older than that is the one case where they would not load.
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
- `tracing` — compy's OWN OpenTelemetry tracing (opt-in, off by default):
  builds the global TracerProvider from settings and returns the flush the
  entry point defers. The default destination is compy's own collector, so
  compy's spans travel the path a user's applications do and land wherever
  the active configuration sends them; an endpoint + headers in settings
  bypasses it. Off installs nothing, so `internal/app`'s `op()` calls run
  unguarded on OTel's no-op global — nothing in compy may depend on a span
  existing. Export timeout and shutdown are short and retry is OFF: the
  default destination is a collector the user can stop, and a CLI command
  must not wait on it.
- `window` — the native window wrapper `compy window` runs.

## api/

`api/openapi.json` is the committed, authoritative REST contract (served by
whatever UI process is running — no daemon). `internal/webui`'s `routes()`
table and the spec must agree: adding, removing, or renaming a route means
updating BOTH, or `TestOpenAPIDriftAgainstRoutes` fails.

## Configurations

A configuration (`internal/cfgstore`) is a whole collector `config.yaml` +
`meta.json` (provenance, presets) under `configs/<name>/`. A preset is one
typed value bag (`map[string]any`) holding ALL of a config's values
(Amendment 4: configs describe, presets fill in, activation runs). Every
config keeps at least one preset (creation paths write a `default` — for a
templated config seeded with the schema's normalized values;
`cfgstore.EnsurePresets` backfills older state at `app.New` and merges the
options-era `knobs` key into every preset; the last preset can't be
deleted). Exactly one configuration, and one of its presets, is active at
a time. Activating (`app.Activate`) a plain (tier-2) config puts that
preset's string values into the LaunchAgent's environment so the collector
expands its own `${VAR}` / `${env:VAR:-def}` references — no text
substitution in compy. Activating a templated (tier-3) config RENDERS the
source with the selected preset's bag first (so switching presets may
switch pipeline structure), and its environment carries ONLY the bag's
`type: secret` values (under `catalog.SecretEnv`'s derived names) plus
`COMPY_*` — everything else is baked into the render. Three shipped defaults —
`debug`, `otlp-forward` (templated, embedded via
`internal/catalog/catalog/*.tmpl`) and `otlp-basic` (plain, via
`internal/cfgstore/defaults/otlp-basic.yaml`) — are materialized into
`configs/` on first run; a shipped config that is unmodified, inactive,
and no longer shipped (the old `otlp`, and now `bronto`) is retired. Edit-protection and sync
share one mechanism: a config's current YAML hash vs. its recorded
`pristine_sha256` — matching means "unmodified" (shipped configs upgrade in
place, remote configs may `sync`), differing means "locally modified"
(upgrades/`sync` leave it alone; `resync` force-discards local edits). On
first v2 run, a v1 state dir's rendered effective config becomes a new
`migrated` configuration (activated if anything was enabled) and the old
`config/` tree is archived to `legacy-v1/` — one-way, logged to stderr.

A TEMPLATED (tier-3) config additionally keeps its source at
`configs/<name>/config.tmpl`; `config.yaml` is a DERIVED render of
(source, active preset's bag), regenerated at activation and at save, so
everything downstream (collector path, plist) stays plain. The SOURCE
carries the pristine hash (it IS the config; remote configs may be
templated and sync their source — sync reconciles every preset's bag with
the fetched schema). Tier detection is textual (`catalog.IsSource`):
front-matter text through the yaml write routes to the source pipeline,
plain yaml over a templated config demotes it (source dropped, preset bags
kept — never delete a secret). The source save (`app.WriteConfigSource`)
carries the source ONLY: it parses, reconciles every preset's bag (removed
fields pruned, newly defaulted filled), renders with the active bag,
stores, then collector-validates — restoring everything on rejection
(nothing-was-saved). Values travel through the preset writes
(`app.ReplacePreset`/`SetVar`), which for tier-3 normalize the bag against
the schema and prove the render in a scratch file BEFORE storing anything
(`?validate=false` skips only the collector's verdict).

## COMPY_HOME

State directory defaults to `~/Library/Application Support/compy` on
macOS (`$XDG_DATA_HOME/compy` or `~/.local/share/compy` elsewhere), overridable
via the `COMPY_HOME` env var. `internal/state.Dir()` resolves and creates it
(`configs/`, `logs/`, `last-good/`). Tests set `COMPY_HOME` to
`t.TempDir()` rather than touching the real state dir.

## Ports

Default ports, all standard + 10000: 14317 gRPC, 14318 HTTP, 18888 the
collector's own telemetry.
Configurable per-install in `settings.json`, and injected into configs as
`${env:COMPY_GRPC_PORT}` / `${env:COMPY_HTTP_PORT}`.

The third is the collector's OWN telemetry — otelcol's Prometheus endpoint,
which `collector.ScrapePorts` reads for the health strip. otelcol defaults
it to `:8888`; compy moves it to `:18888` so it stops sitting on a port
other collectors and Prometheus examples routinely claim.
`settings.json`'s `metrics_port` moves it further (0 = let the OS pick), but
compy does NOT put it in anybody's config: `collector.OverlayYAML` is
passed as a SEPARATE `--config` source, ahead of the configuration
(`app.collectorArgs`). Order is the contract — confmap merges its sources
and the LAST wins, so overlay-first makes compy's block a DEFAULT a config's
own `service::telemetry` overrides, and hand-written configs get the
setting for free. Never edit a configuration to place it.

The Prometheus reader's bind is fatal (contrib/otelconf: plain `net.Listen`,
error returned, startup aborts), so `resolveMetricsPort` pre-flights the
port at activation and falls back to 0 rather than letting an unrelated
process on `:8888` take the collector down. Our own collector holding the
port is not "busy" (`collector.PortHeldBy`) — without that, every
re-activation would drift onto an OS-assigned port. A fallback is visible:
`/api/status`'s `metrics_port` (configured) differing from
`/api/collector/health`'s `port` (actual) is what the collector screen
turns amber.
