# compy

[![CI](https://github.com/bronto-community/compy/actions/workflows/ci.yml/badge.svg)](https://github.com/bronto-community/compy/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/bronto-community/compy)](https://github.com/bronto-community/compy/releases)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/bronto-community/compy/badge)](https://scorecard.dev/viewer/?uri=github.com/bronto-community/compy)

compy is a local OpenTelemetry Collector manager for the dev loop on macOS.
Point your locally-run apps at one stable local OTLP endpoint once; from
then on you switch what happens to their telemetry (which config, which
backend) with a CLI, a web UI, or the menu bar, in seconds.

Under the hood it runs a collector as a per-user macOS LaunchAgent, so the
OS supervises the process and there is no daemon of compy's own.

The name is short for
[Compsognathus](https://en.wikipedia.org/wiki/Compsognathus), one of the
smallest known dinosaurs — about the size of a chicken, and quick on its
feet. That is the idea here too: a small, fast companion that runs
alongside your dev loop and stays out of the way.

compy is an open-source project from [Bronto](https://bronto.io),
maintained as a community artifact: free to use, contributions welcome —
but not covered by Bronto's product support. Bugs and feature requests are
handled best-effort through
[GitHub issues](https://github.com/bronto-community/compy/issues).

## Install

```sh
brew trust --tap bronto-community/tap
brew install bronto-community/tap/compy
compy tray install
```

Homebrew requires explicit trust for third-party taps and silently ignores
casks from untrusted ones, hence the first line.

The install brings the compy binary, its bundled collector distribution
(`otelcol-compy`) and the `compy.app` identity for the native window. The
menu bar item — compy's main interface — is a login item, so adding it is
the third line rather than something `brew install` does for you. Take it
out again with "Remove from Menu Bar" in its menu, or `compy tray
uninstall`.

Building from source instead is covered in
[CONTRIBUTING.md](CONTRIBUTING.md); `compy tray install` works the same way
there.

## Getting started

Once `compy tray install` has run, the compy icon sits in your menu bar.
Click it and activate the shipped `debug` configuration — the collector starts
(supervised by launchd) on compy's standard ports, 14318 HTTP and 14317
gRPC, and prints everything it receives to the collector log. "Open
compy" opens the window: switch configurations, fill in keys and
endpoints, watch the log.

Your apps find compy through the standard `OTEL_*` environment variables:

```sh
eval "$(compy env)"   # this shell (put it in your shell rc for every shell)
```

or `compy run -- <cmd>` for a single command, or `compy env set-os` for
OS-level variables that GUI apps see too. Run your app and its telemetry
shows up in the collector log.

To send somewhere real, switch configurations in the window. Three ship
with compy:

- `debug` — print everything to the collector log
- `otlp-basic` — pass everything through to one OTLP endpoint, with a
  bearer token when your backend wants one
- `otlp-forward` — fan out to as many OTLP/HTTP backends as you like, each
  with its own endpoint and auth, plus batching and an optional on-disk
  retry queue

The menu bar and the window are the main interface; everything they do
also exists as CLI commands for scripting and the terminal-inclined —
`compy` with no arguments lists them. The same first run from the shell:

```sh
compy use debug
eval "$(compy env)"
compy status
compy log
```

**Upgrading.** `brew upgrade compy` replaces the binary. Two things keep
running the old version until they are restarted:

- The menu bar. Its login item points at the stable
  `$(brew --prefix)/bin/compy` symlink, so the next login picks up the new
  version by itself; to switch over immediately, quit it from its menu and
  run `compy tray install`.
- A running collector. compy tells you (a "restart needed" note in the menu
  bar, window, and `compy status`), and the restart from any of them
  finishes the job.

**Uninstalling.** `brew uninstall compy` stops the collector and the menu
bar; add `--zap` to also remove the LaunchAgents and compy's state
directory (`~/Library/Application Support/compy` — your configurations
included).

## What it does

**Configurations and presets.** compy manages whole collector
`config.yaml` documents, each with named presets (e.g. `default`,
`staging`) of values for the `${VAR}` / `${env:VAR:-default}` references
the YAML contains. Exactly one configuration and one of its presets is
active at a time. Activating puts the preset's values into the collector's
environment and restarts it; the collector expands its own variables,
compy never rewrites the YAML. If the new configuration fails to start,
compy restores the one that was running.

**Templated configurations.** A configuration whose text opens with a
schema block is templated: the UI renders a form (dropdowns, toggles,
secrets, and repeat groups the template itself names — backends,
receivers, whatever your config is a list of) and compy renders the
collector YAML from the template and the selected preset. `debug` and
`otlp-forward` ship this way; the plain-YAML and env-var tiers stay
available for configs you write yourself.

**Shipped, remote, and your own configs.** `compy config create
--from-url` pulls a configuration from a URL and can later `sync` to its
source. Edit any config (`compy config edit`, or the editor in the UI) and
it becomes locally modified: upgrades and `sync` leave it alone, `resync`
discards your edits and pulls anyway, `reset` restores a shipped config.

**A stable advertised endpoint.** `compy env`, `compy run`, and the
OS-level env all advertise the same endpoint from the ports in settings,
stable across config switches. The shipped configurations bind their
receivers to `${env:COMPY_GRPC_PORT}` / `${env:COMPY_HTTP_PORT}`, so they
always match. A config of your own may bind anywhere; when its detected
listeners miss the advertised port, every surface warns, and `compy
adopt-ports` re-points the advertisement at the config's actual OTLP
ports.

**Env wiring, three ways.** `eval "$(compy env)"` for the current shell
(put it in your shell rc to wire every new shell; `--shell fish|pwsh` for
others), `compy run -- <cmd>` for a single command, `compy env set-os`
for OS-level (`launchctl setenv`) variables that GUI apps see too. A
process's own environment always wins over anything compy sets.

**Collector distributions.** `compy distro list` shows the known
collector builds: the bundled `otelcol-compy`, the pinned
`core`/`contrib`/`otlp` upstream builds (downloaded on demand,
checksum-verified), and any binary you register with `compy distro add`.
`compy distro use` switches, `compy distro update` pulls a newer upstream
release of an installed one.

**UI surfaces.** `compy ui` serves a localhost-only web UI, `compy window`
opens the same UI in a native window, and `compy tray` puts a status icon
in the menu bar. Everything they expose also exists in the CLI. The menu
bar is fully keyboard-drivable: Ctrl+F8 reaches the icon, and while the
menu is open, `1`–`9` activate configurations, `s` stops/starts, `r`
restarts, `o` opens the window, ⌘Q quits.

**Factory reset.** `compy factory-reset --yes` uninstalls the LaunchAgent
and wipes compy's state directory: all configurations, presets, downloaded
collectors, logs, and settings. The shipped configs come back fresh.

The full command surface is in `compy` (no arguments).

## HTTP API

Whatever UI process is running (`compy ui`, `compy window`, or the menu
bar's "Open compy") serves the same localhost-only REST API; there is no
separate daemon. The full contract is [`api/openapi.json`](api/openapi.json).

```sh
compy ui --port 8080 &
curl http://localhost:8080/api/status
```

## Contributing

Build-from-source instructions, the test gates, the module map, and the
release process are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md) for how to report vulnerabilities and how
releases can be verified.

## License

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). The web UI
vendors CodeMirror (MIT), Lucide icons (ISC), and the IBM Plex Sans and
JetBrains Mono fonts (SIL OFL 1.1); their license texts are under
`internal/webui/static/vendor/`.
