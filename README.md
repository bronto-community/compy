# compy

compy is a local OpenTelemetry Collector manager for the dev loop on macOS.
Point your locally-run apps at one stable local OTLP endpoint once; from
then on you switch what happens to their telemetry (which config, which
backend) with a CLI, a web UI, or the menu bar, in seconds.

Under the hood it runs a collector as a per-user macOS LaunchAgent, so the
OS supervises the process and there is no daemon of compy's own.

## Install

```
brew install bronto-community/tap/compy
compy tray install
```

That's the whole setup: the cask installs compy with its bundled collector
distribution (`otelcol-compy`) and the `compy.app` identity for the native
window, and `compy tray install` puts the compy icon in the menu bar as a
login item. Click the icon and activate a configuration — the first
activation just works, nothing to download.

While this repo is private, `brew` additionally needs a
`HOMEBREW_GITHUB_API_TOKEN` that can read it.

**Upgrading.** `brew upgrade compy` is the whole upgrade: the menu bar
restarts itself onto the new version, and a running collector keeps
running the old one until you restart it — compy says so (a "restart
needed" note in the menu bar, window, and `compy status`) and the restart
from any of them finishes the upgrade. **Uninstalling.** `brew uninstall
compy` stops the collector and the menu bar; add `--zap` to also remove
the LaunchAgents and compy's state directory (`~/Library/Application
Support/compy` — your configurations included).

Building from source instead is covered under
[Development](#development).

## Quickstart

```
compy use debug
eval "$(compy env)"
```

The first line validates, installs, and starts the collector with the
shipped `debug` configuration on compy's standard ports (14318 HTTP, 14317
gRPC). The second exports `OTEL_EXPORTER_OTLP_ENDPOINT` and friends into
your current shell, so anything you start from it sends telemetry to
compy. `compy status` confirms both.

To send somewhere real, e.g. the shipped `bronto` configuration:

```
compy presets set bronto default BRONTO_API_KEY=...
compy use bronto default
```

## What it does

**Configurations and presets.** compy manages whole collector
`config.yaml` documents, each with named presets (e.g. `default`,
`staging`) of values for the `${VAR}` / `${env:VAR:-default}` references
the YAML contains. Presets are how values reach a configuration: every
configuration keeps at least one — a new one starts with an empty
`default` preset, and the last preset can't be deleted. Exactly one
configuration and one of its presets is active at a time. Activating puts
the preset's values into the collector's environment and restarts it; the
collector expands its own variables, compy never rewrites the YAML. If the new configuration fails to start,
compy restores the one that was running.

**Shipped, remote, and your own configs.** Three configurations ship with
compy (`debug`, `otlp`, `bronto`). `compy config create --from-url` pulls
one from a URL and can later `sync` to its source. Edit any config
(`compy config edit`, or the editor in the UI) and it becomes "locally
modified": upgrades and `sync` leave it alone, `resync` discards your
edits and pulls anyway, `reset` restores a shipped config.

**A stable advertised endpoint.** `compy env`, `compy run`, and the
OS-level env all advertise the same endpoint from the ports in settings,
stable across config switches. The advertised protocol is configurable
(`compy settings set --protocol grpc|http/protobuf|http/json`); the
collector's receivers serve every protocol regardless. The shipped
configurations bind their receivers to `${env:COMPY_GRPC_PORT}` /
`${env:COMPY_HTTP_PORT}`, so they always match. A config of your own may
bind anywhere; when its detected listeners miss the advertised port, every
surface warns (`compy status`, the window sidebar, the menu bar), and
`compy adopt-ports` re-points the advertisement at the config's actual
OTLP ports.

**Env wiring, three ways.** `eval "$(compy env)"` for the current shell
(put it in your shell rc to wire every new shell; `--shell fish|pwsh` for
others), `compy run -- <cmd>` for a single command, `compy env set-os`
for OS-level (`launchctl setenv`) variables that GUI apps see too. A
process's own environment always wins over anything compy sets.

**Collector distributions.** `compy distro list` shows a table of the
known collector builds: the bundled `otelcol-compy` (when built), the
pinned `core`/`contrib`/`otlp` upstream builds (downloaded on demand,
checksum-verified), and any binary you register with `compy distro add
<name> <path>`. `compy distro use` switches, `compy distro update` pulls a
newer upstream release of an installed one.

**UI surfaces.** `compy ui` serves a localhost-only web UI, `compy window`
opens the same UI in a native window, and `compy tray` puts a status icon
in the menu bar (`compy tray install` registers it as a login item).
Everything they expose also exists in the CLI.

**Factory reset.** `compy factory-reset --yes` uninstalls the
LaunchAgent and wipes compy's state directory (`~/Library/Application
Support/compy` by default, overridable with `COMPY_HOME`): all
configurations, presets, downloaded collectors, logs, and settings. The
shipped configs come back fresh.

The full command surface is in `compy` (no arguments).

## Non-goals

**compy never displays telemetry.** It is not a trace viewer, not a log
tail, not a metrics UI. Also out of scope for this version: runtime
download of arbitrary (non-pinned) collector distributions, fleet/remote
management beyond what the collector's own confmap URIs support,
team/profile sharing, Keychain-backed secrets, and a Windows
implementation.

## HTTP API

Whatever UI process is running (`compy ui`, `compy window`, or the menu
bar's "Open compy") serves the same localhost-only REST API; there is no
separate daemon. The full contract is `api/openapi.json`.

```
compy ui --port 8080 &
curl http://localhost:8080/api/status
```

## Migrating from v1

On first v2 run, a v1 state directory (`config/base.yaml` + enabled
`config/backends/*.yaml`) is rendered once into a new configuration named
`migrated` (activated if anything was enabled) and the old `config/` tree
is archived to `legacy-v1/`. One-way, logged to stderr.

## Development

Building from source requires Go 1.25+:

```
CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -o compy ./cmd/compy
```

The tags are required by Wails (the native-window library): an untagged
build compiles and works fully except `compy window`, which errors at
runtime by Wails' design. The CGO_LDFLAGS links a framework Wails
references but does not link itself. Neither applies to non-darwin builds.

That is enough: on first use compy downloads a pinned, checksum-verified
collector build (~90MB). Two optional extras (the Homebrew install ships
both):

- **Bundled collector.** `sh packaging/collector/build.sh` builds
  `otelcol-compy`, compy's own curated collector distribution (components
  pinned in `packaging/collector/manifest.yaml`), next to the compy binary
  via the OpenTelemetry Collector Builder. It is a separate, slow step (OCB
  downloads a large module graph). When it sits next to the compy
  executable it is the default collector and no download happens.
- **macOS app bundle.** `sh packaging/macos/make-app.sh ./compy` assembles
  a `compy.app` next to the binary so the standalone window (`compy
  window`, and the menu bar's "Open compy") shows the right identity in
  the app menu and Dock. Everything works without it. See
  `packaging/macos/README.md`.

The gates:

```
go build -o /dev/null ./cmd/compy
go vet ./...
gofmt -l .
go test ./...
GOOS=linux CGO_ENABLED=0 go build -o /dev/null ./cmd/compy   # cross-build stays green
CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags desktop,production -o /dev/null ./cmd/compy
go test -tags=integration ./integration/   # needs OTELCOL_BIN=/path/to/otelcol
```

Unit tests live next to their packages under `internal/`; the
`integration/` suite drives a real collector binary and only runs with the
`integration` tag and `OTELCOL_BIN` set. `packaging/` holds the OCB
manifest and build script for `otelcol-compy` and the macOS app-bundle
tooling. `CLAUDE.md` has the module map.

CI runs these same gates on every PR (see `.github/workflows/README.md`
for the full workflow list). Releases are cut by pushing a `vX.Y.Z` tag,
which GoReleaser turns into GitHub release artifacts and the Homebrew
cask.

## License

Apache-2.0 (see `LICENSE` and `NOTICE`). The web UI vendors CodeMirror
(MIT), Lucide icons (ISC), and the IBM Plex Sans and JetBrains Mono fonts
(SIL OFL 1.1); their license texts are under
`internal/webui/static/vendor/`.
