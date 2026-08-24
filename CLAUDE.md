# compy

Local OpenTelemetry Collector manager: CLI + web UI + tray around a
launchd-supervised `otelcol` process. See README.md for what it does and the
non-goals.

- Spec: `docs/superpowers/specs/2026-08-24-local-collector-design.md`
- Plan: `.superpowers/sdd/2026-08-24-compy-v1/`

## Build / test

```
go build ./cmd/compy
go vet ./...
gofmt -l .
go test ./...
go test -tags=integration ./integration/         # needs OTELCOL_BIN=/path/to/otelcol (real collector binary)
```

## Dependencies

Stdlib only, except `fyne.io/systray` (tray icon; darwin-only build, stubbed
out on other GOOS in `internal/tray`).

## Module layout (`internal/*`)

- `app` — orchestrates everything else: turns settings + backend fragments
  into a validated, installed, running collector; the one API the CLI, web
  UI, and tray all call into.
- `collector` — runs and probes a local OpenTelemetry Collector binary
  (validate, start via launchd, health probe, log tail).
- `config` — collector configuration: base template, per-backend fragments,
  presets, collector arg construction (`--config` per enabled backend),
  last-good snapshot/restore.
- `envvars` — computes the `OTEL_*` vars compy exposes; emits them as shell
  scripts (`compy env`), subprocess environments (`compy run`), or OS-level
  (`launchctl setenv`) settings.
- `launchd` — macOS LaunchAgent management: renders the plist, installs /
  uninstalls / kickstarts / inspects it via `launchctl`.
- `state` — on-disk state: settings, distros, state directory layout
  (`COMPY_HOME`, below).
- `tray` — macOS menu-bar icon (status, enable/disable toggles, Open UI,
  Quit); non-darwin build is a no-op stub.
- `webui` — localhost-only web UI: JSON API plus an embedded (`go:embed`)
  single-page app; no internal dependencies, the caller wires behavior in
  via a closure struct.

## COMPY_HOME

State directory defaults to `~/Library/Application Support/compy` on
macOS (`$XDG_DATA_HOME/compy` or `~/.local/share/compy` elsewhere), overridable
via the `COMPY_HOME` env var. `internal/state.Dir()` resolves and creates it
(`config/backends/`, `logs/`, `last-good/`). Tests set `COMPY_HOME` to
`t.TempDir()` rather than touching the real state dir.

## Ports

Default OTLP ports: 14317 gRPC, 14318 HTTP (standard ports + 10000).
Configurable per-install in `settings.json`.
