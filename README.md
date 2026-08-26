# compy

A telemetry switchboard for the local dev loop. Point locally-run apps at
one stable local OTLP endpoint once; from then on, which backends telemetry
goes to is changed with this tool — CLI or web UI — in seconds.

compy manages an OpenTelemetry Collector as an OS-supervised service (a
per-user macOS LaunchAgent). It is a proxy and a control plane.

**compy never displays telemetry.** It is not a trace viewer, not a log
tail, not a metrics UI. Other non-goals for this version: runtime download
of arbitrary (non-pinned) collector distributions, fleet/remote management
beyond what the collector's own confmap URIs support, team/profile sharing,
Keychain-backed secrets, and a Windows implementation.

## Install

Requires Go 1.24+. compy can download its own pinned collector binaries on
first use (`compy distro use core|contrib|otlp`), or you can point it at
your own `otelcol` build with `compy distro add`.

```
go build -o compy ./cmd/compy
```

### macOS app bundle

```
sh packaging/macos/make-app.sh ./compy
```

assembles a `compy.app` next to the binary so the standalone window
(`compy window`, and the tray's "Open compy") shows the right identity —
app menu says compy, Dock shows the dino icon — instead of a bare
executable's generic name. `open compy.app` opens the window directly.
Optional; everything works without it. See `packaging/macos/README.md`.

## Configuration model

compy manages **configurations**: whole collector `config.yaml` documents,
each with named **presets** (e.g. `default`, `staging`) of values for
the `${VAR}` / `${VAR:-default}` / `${env:VAR}` / `${env:VAR:-default}`
references it contains. Exactly one configuration, and one of its presets,
is active at a time — activating installs and restarts the collector
with that preset's values in its environment, so the collector expands them
natively. If activation succeeds but the collector then fails to start,
compy automatically restores the previously running configuration and
preset. compy ships three configurations built in to compy (`debug`,
`otlp`, `bronto`); edit any of them and it becomes "locally modified" and
is skipped by future compy upgrades, or `sync`/`resync` a configuration
created `--from-url` to pull in changes from its source (refused if locally
modified, unless you `resync` to discard your edits).

## Quickstart

```
./compy distro use core
./compy use debug
eval "$(./compy env)"
```

`compy distro use core` downloads compy's pinned `otelcol` build
(checksum-verified) on first use and makes it the global default; bring
your own binary instead with `compy distro add <name> /path/to/otelcol`.
`compy use <config>` validates, installs, and starts the collector on
compy's standard ports. `eval "$(./compy env)"` exports
`OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_PROTOCOL` into your
shell. For a single command instead: `./compy run -- <cmd>`.

Check it worked:

```
./compy status
```

Point it at a real backend instead of `debug`, e.g. the shipped `bronto`
configuration:

```
./compy presets set bronto default BRONTO_API_KEY=...
./compy use bronto default
```

(`compy presets set <config> <preset> KEY=VALUE` takes one variable per
call; `compy use <config> <preset>` both selects and activates a preset.)

## Command surface

```
compy status [--json]
compy apply | validate | stop | start
compy config list
compy config show|edit|delete|sync|resync <name>
compy config create <name> [--from-url URL]
compy config copy <src> <dst>
compy config sync-all
compy config set-url <config> <url|-->
compy use <config> [<preset>]
compy vars <config>
compy presets set <config> <preset> KEY=VALUE
compy presets use|delete <config> <preset>
compy presets rename <config> <from> <to>
compy settings
compy settings set [--grpc-port N] [--http-port N]
compy distro list
compy distro add <name> <path>
compy distro set-path <name> <path>
compy distro remove <name>
compy distro use|fetch <name>
compy service install|uninstall|status
compy env [--shell sh|fish|pwsh]
compy env set-os | unset-os
compy log [--lines N]
compy run -- <cmd...>
compy ui [--port N]
compy tray [install|uninstall]
compy window
```

`compy ui` serves a localhost-only web UI (status, configuration list,
activation). `compy tray` is a macOS menu-bar icon (`compy tray install`
registers it as a login LaunchAgent so it appears at every login;
`uninstall` removes it); every capability it exposes also exists in the CLI
and web UI. Default OTLP ports are 14317 (gRPC) and 14318 (HTTP),
configurable via `settings.json`.

## HTTP API

Whatever UI process is running (`compy ui`, `compy window`, or the tray's
"Open compy") serves the same localhost-only REST API `compy ui` does —
there's no separate daemon. The full contract is `api/openapi.json`.

```
compy ui --port 8080 &
curl http://localhost:8080/api/status
```

## Migrating from v1

If compy finds a v1 state directory (`config/base.yaml` + enabled
`config/backends/*.yaml` fragments) on first v2 run, it renders their
effective merged config once, saves it as a new configuration named
`migrated`, activates it if anything was enabled, and archives the old
`config/` tree to `legacy-v1/`. This is one-way and logged to stderr; the
old fragment model is gone from v2.
