# compy

A telemetry switchboard for the local dev loop. Point locally-run apps at
one stable local OTLP endpoint once; from then on, which backends telemetry
goes to is changed with this tool — CLI or web UI — in seconds.

compy manages an OpenTelemetry Collector as an OS-supervised service (a
per-user macOS LaunchAgent). It is a proxy and a control plane.

**compy never displays telemetry.** It is not a trace viewer, not a log
tail, not a metrics UI. Other non-goals for this version: runtime download
of collector distributions, fleet/remote management beyond what the
collector's own confmap URIs support, team/profile sharing, Keychain-backed
secrets, and a Windows implementation.

## Install

Requires Go 1.24+. compy does not bundle a collector binary — bring your
own `otelcol` (core or contrib) build.

```
go build -o compy ./cmd/compy
```

## Quickstart

```
./compy distro add core /path/to/otelcol
./compy backend add mydebug --kind debug
./compy backend enable mydebug
./compy service install
eval "$(./compy env)"
```

`backend enable` already applies and starts the collector; `service install`
makes that explicit for first-time setup, and again after a `service
uninstall` — switching distros needs no reinstall, `compy distro use`
already re-applies. `eval "$(./compy env)"` exports
`OTEL_EXPORTER_OTLP_ENDPOINT` / `OTEL_EXPORTER_OTLP_PROTOCOL` into your
shell. For a single command instead: `./compy run -- <cmd>`.

Check it worked:

```
./compy status
```

## Command surface

```
compy status [--json]
compy apply | rollback | validate
compy backend list
compy backend add <name> --kind otlp-grpc|otlp-http|bronto|debug [--endpoint URL] [--api-key KEY]
compy backend remove|enable|disable|edit <name>
compy distro list
compy distro add <name> <path>
compy distro use <name>
compy service install|uninstall|status
compy env [--shell sh|fish|pwsh]
compy env set-os | unset-os
compy run -- <cmd...>
compy raw on|off|edit
compy ui [--port N]
compy tray
```

`compy ui` serves a localhost-only web UI (status, backend list/add/edit,
raw-mode toggle). `compy tray` is a macOS menu-bar icon; every capability it
exposes also exists in the CLI and web UI. Backends are additive — multiple
named backends can be enabled at once. Default OTLP ports are 14317 (gRPC)
and 14318 (HTTP), configurable via `settings.json`.
