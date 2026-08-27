# compy

A telemetry switchboard for the local dev loop. Point locally-run apps at
one stable local OTLP endpoint once; from then on you switch which backends
telemetry goes to with this tool (CLI or web UI) in seconds.

compy manages an OpenTelemetry Collector as an OS-supervised service (a
per-user macOS LaunchAgent). It is a proxy and a control plane.

**compy never displays telemetry.** It is not a trace viewer, not a log
tail, not a metrics UI. Other non-goals for this version: runtime download
of arbitrary (non-pinned) collector distributions, fleet/remote management
beyond what the collector's own confmap URIs support, team/profile sharing,
Keychain-backed secrets, and a Windows implementation.

## Install

Requires Go 1.24+.

```
go build -o compy ./cmd/compy
```

### Bundled collector (otelcol-compy)

`sh packaging/collector/build.sh` builds `otelcol-compy`, compy's own
collector distribution (curated in `packaging/collector/manifest.yaml`),
next to the compy binary via the OpenTelemetry Collector Builder. It is a
separate, slow step (OCB downloads a large module graph), deliberately not
part of `go build`. When it sits next to the compy executable it is the
default collector; without it, compy falls back to downloading the pinned
`contrib` build (checksum-verified) the first time anything needs one.
Switch to another pinned build with `compy distro use core|contrib|otlp`,
pull a newer upstream release of one with `compy distro update <name>`, or
point compy at your own `otelcol` build with `compy distro add`.

### macOS app bundle

```
sh packaging/macos/make-app.sh ./compy
```

assembles a `compy.app` next to the binary so the standalone window
(`compy window`, and the tray's "Open compy") shows the right identity
(app menu says compy, Dock shows the dino icon) instead of a bare
executable's generic name. `open compy.app` opens the window directly.
Optional; everything works without it. See `packaging/macos/README.md`.

## Configuration model

compy manages **configurations**: whole collector `config.yaml` documents,
each with named **presets** (e.g. `default`, `staging`) of values for
the `${VAR}` / `${VAR:-default}` / `${env:VAR}` / `${env:VAR:-default}`
references it contains. Exactly one configuration, and one of its presets,
is active at a time. Activating installs and restarts the collector
with that preset's values in its environment, so the collector expands them
natively. If activation succeeds but the collector then fails to start,
compy automatically restores the previously running configuration and
preset. Three configurations are built in to compy (`debug`, `otlp`,
`bronto`). Edit one and it becomes "locally modified": future compy
upgrades leave it alone. A configuration created `--from-url` can `sync`
to pull in changes from its source; sync refuses a locally modified
config, and `resync` discards your edits and pulls anyway.

## Quickstart

```
./compy use debug
eval "$(./compy env)"
```

`compy use <config>` validates, installs, and starts the collector on
compy's standard ports. It runs the bundled `otelcol-compy` when one is
built next to the compy binary (see Install); otherwise the first run
downloads compy's pinned `otelcol-contrib` build (checksum-verified,
~90MB). Pick a different pinned build with `compy distro use core|otlp`,
or bring your own binary with `compy distro add <name> /path/to/otelcol`. `eval "$(./compy env)"` exports
`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, and
`OTEL_TRACES_EXPORTER` / `OTEL_METRICS_EXPORTER` / `OTEL_LOGS_EXPORTER`
(all pinned to `otlp` — some zero-code agents default logs to `none`) into
your current shell. Add that `eval` line to `~/.zshrc` (or your shell's rc)
to wire every new shell, or for a single command instead:
`./compy run -- <cmd>`. A process's own environment always wins over
anything compy sets.

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

## The advertised endpoint

compy's advertised ports are the contract: `compy env`, `compy run`, and
the OS-level env all export
`OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<http_port>` (protocol
`http/protobuf`, and the traces/metrics/logs exporters pinned to `otlp`)
from the ports in settings (14318 HTTP / 14317 gRPC by default), stable
across config switches. The advertised protocol is configurable
(`compy settings set --protocol grpc|http/protobuf|http/json`,
or the settings screen): `http/json` shares the HTTP port, `grpc` points the
endpoint at the gRPC port instead (still in `http://host:port` form: the
`http` scheme means plaintext for OTLP/gRPC, so no extra insecure flag is
needed) and the conformance warning below rides the gRPC port. Switching is
advertisement-only; the collector's receivers serve every protocol
regardless. The shipped configurations bind
their receivers to `${env:COMPY_GRPC_PORT}` / `${env:COMPY_HTTP_PORT}`, so
they always conform. A configuration owns its receivers and may bind
anywhere. But when its detected listeners don't include the advertised
port, apps that followed compy's env would silently miss it, so every
surface says so (`compy status`, the window sidebar, the menu bar's
"ports mismatch"). Either make the config conform (bind the `COMPY_*_PORT`
variables) or run `compy adopt-ports` (also in the window's warning) to
re-point the advertisement at the config's actual OTLP ports. compy probes
grpc and http apart automatically and refuses anything ambiguous, naming
the candidates; `--grpc N --http N` assigns them explicitly.

## Command surface

```
compy status [--json]
compy apply | validate | stop | start
compy adopt-ports [--grpc N] [--http N]
compy config list
compy config show|edit|delete|sync|resync|reset <name>
compy config create <name> [--from-url URL]
compy config copy <src> <dst>
compy config rename <old> <new>
compy config sync-all
compy config set-url <config> <url|-->
compy use <config> [<preset>]
compy vars <config>
compy presets set <config> <preset> KEY=VALUE
compy presets use|delete <config> <preset>
compy presets rename <config> <from> <to>
compy settings
compy settings set [--grpc-port N] [--http-port N] [--protocol grpc|http/protobuf|http/json]
compy factory-reset --yes
compy distro list
compy distro add <name> <path>
compy distro set-path <name> <path>
compy distro remove <name>
compy distro use|fetch <name>
compy distro update [--check] <name>
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
