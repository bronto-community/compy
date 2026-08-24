# Local OTel Collector Manager — Design

2026-08-24. Open source, owned by bronto.io. Name: **compy** (after
Compsognathus, the tiny dinosaur — a small local companion, on brand for
bronto.io). Name-conflict sweep done 2026-08-24: no dev-tool or observability
collisions; major registries squatted by dead projects (irrelevant for a Go
binary), Homebrew free.

## What it is

A telemetry switchboard for the local dev loop. Developers point locally-run
apps at one stable local OTLP endpoint once; from then on, which backends
telemetry goes to is changed in this tool — UI or CLI — in seconds. It manages
an OpenTelemetry Collector as an OS-supervised service. It is a proxy and a
control plane. **It never displays telemetry** (no trace viewer, no log tail,
no metrics UI).

## Decisions already made (from brainstorming)

- Backend changes are **additive**: multiple named backends enabled at once,
  add/remove independently. Not an A/B switch.
- A backend change restarts the collector. Brief restart pause and possible
  in-flight data loss are **acceptable**. No zero-loss front proxy.
- Ships with core and contrib distributions out of the box; user can also
  register locally installed/built collector binaries.
- Config errors: show the collector's own error, offer rollback to
  last-known-good. No custom validation engine; "Open in OTelBin" link instead.
- Env vars: per-shell/per-process pickup is the headline mode (wrapping
  OTel-capable tools like Claude Code); OS-level injection is in, opt-in.
- Remote config: only what the collector's confmap already supports
  (file/env/yaml/http/https URIs). Nothing custom.
- Single developer, own machine. No team/sharing features.
- Nonstandard OTLP ports by default: standard ports + 10000, i.e. 14317 gRPC /
  14318 HTTP, configurable. Keeps the familiar 4317/4318 mnemonic.
- Stack: **one Go binary** — CLI, tray icon, embedded localhost web UI
  (the Datadog Agent interaction model). macOS first; Linux must be fully
  usable with no tray (CLI + browser URL).
- Config composition: **collector-native multi-`--config` merge** of a base
  file plus one fragment per enabled backend (see risk R1).

## Architecture

### Process topology: no daemon of our own

The OS service manager is the supervisor. On macOS a per-user LaunchAgent with
`KeepAlive` runs the collector binary directly: start at login, restart on
death, both for free. (Linux: systemd user unit. Windows: service, later.)

The `compy` binary is a pure control plane. GUI, tray, and CLI all operate on
the same state directory and poke launchd; files are the single source of
truth, so there is no IPC protocol and no way for CLI and UI to disagree.

```
apps ──OTLP──▶ collector (supervised by launchd) ──▶ enabled backends
                    ▲ config files + plist
compy CLI ──┐        │
compy UI  ──┼──▶ state dir ──▶ launchctl kickstart on apply
compy tray ─┘
```

### State directory

`~/Library/Application Support/compy/` (XDG dirs on Linux):

```
config/base.yaml            # receivers (stable OTLP ports), shared processors,
                            # service skeleton (telemetry, extensions)
config/backends/<name>.yaml # one fragment per named backend: exporter defs
                            # + pipeline membership
config/custom.yaml          # only in raw mode (see Config model)
last-good/                  # snapshot of the last config set that ran healthy
settings.json               # ports, enabled backend set, selected distro, mode
distros.json                # registered collector binaries
logs/collector.log          # collector stderr via plist StandardErrorPath
```

All writes are atomic (write temp + rename). `settings.json` is the record of
intent; the rendered plist is derived from it.

### Config model

**Managed mode (default).** The collector is launched as:

```
otelcol --config config/base.yaml --config config/backends/a.yaml --config config/backends/b.yaml ...
```

one `--config` per enabled backend, relying on confmap merge. Fragments are
plain collector YAML — portable, pasteable into OTelBin, no template language.
A named backend is a first-class object: a fragment file plus an
enabled/disabled bit in `settings.json`. Disabling a backend removes its flag;
the fragment file stays.

Backends are created two ways: a **preset form** (vendor presets — OTLP
generic, Bronto, and a handful of common vendors — asking only for endpoint +
API key, generating the fragment) or **raw YAML** (write the fragment
yourself). A preset-generated fragment the user hand-edits simply becomes a
raw fragment; nothing breaks, the form just no longer round-trips it.

Remote config: a backend (or the base) may be a confmap URI
(`https://…`, `env:…`) instead of a local file — passed through verbatim.

**Raw mode.** An explicit toggle: the user owns `config/custom.yaml`, launched
as the single `--config`. Backend toggles are disabled while raw mode is on.
Switching back re-renders from base + fragments. No silent detaching.

**Apply flow** (`compy apply`, also triggered by every UI toggle):
1. Run `<selected distro binary> validate --config …` with the exact flag set.
   This gives distribution-aware validation for free — a contrib-only
   component fails validation the moment core is selected.
2. On pass: snapshot current set to `last-good/` (only if the running
   collector is healthy), rewrite plist, `launchctl kickstart -k`.
3. Post-apply health probe (process up + OTLP port accepting). On failure:
   surface tail of `collector.log`, offer `compy rollback`.

`compy rollback` re-applies the `last-good/` snapshot.

### Distributions

- Release artifacts **bundle** core and contrib binaries for the platform
  alongside `compy` (inside the signed/notarized package — this sidesteps the
  unsigned-upstream-binary problem on macOS entirely).
- `compy distro add <path>` registers any local binary (vendor distro, custom
  build). `compy distro use <name>` switches; switching re-runs the apply flow
  (validate against the new binary first, so a bad switch is caught before
  the restart).
- Runtime download of distributions is a **non-goal for v1** (Gatekeeper +
  quarantine handling; revisit later).

### Environment variables

Vars managed: `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`,
and `OTEL_RESOURCE_ATTRIBUTES` if the user sets any. Values point at the
stable local endpoint, so they change only when ports change.

- `compy env [--shell sh|fish|pwsh]` — emits export lines for
  `eval "$(compy env)"`.
- `compy run -- <cmd>` — spawns the command with vars injected (the
  Claude Code / one-off tool case). Nothing touches the parent shell.
- OS-level (opt-in, explicit on/off in UI and CLI): macOS
  `launchctl setenv` per var, affecting newly launched GUI apps and login
  shells. Documented limitation: already-running processes don't see it.
  Linux/Windows equivalents are per-platform work, later.

### Web UI and tray

Single-page UI, embedded via `go:embed`, served on a localhost port only
while the tray app or `compy ui` is running — there is no always-on UI server;
the only long-lived process on the machine is the collector itself. Bound to `127.0.0.1` with a Host-header check (DNS
rebinding) — no auth beyond that; single-user machine.

v1 screens: status (service state, distro, endpoint, copy-paste env lines);
backend list with enable toggles, add-via-preset form, per-backend YAML
editor; base config editor; raw-mode toggle; "Open in OTelBin" link
(config in URL fragment — verify format, item R2); on failure, the
collector's own error output. That last part is operational output, not
telemetry viewing.

Tray (macOS menu bar first): status dot, backend enable/disable toggles,
"Open UI". Tray is a convenience layer only — every capability exists in
CLI + web UI, which is what keeps Linux-without-tray a non-event.

### CLI surface (v1)

```
compy service install|uninstall|status
compy status
compy backend list|add|remove|enable|disable|edit <name>
compy apply | rollback | validate
compy distro list|add <path>|use <name>
compy env [--shell …]
compy run -- <cmd>
compy ui
```

## Error handling

- Validation failure: collector's own stderr, verbatim. No apply happens.
- Collector fails after apply: health probe catches it, log tail shown,
  rollback offered. launchd keeps retrying meanwhile (KeepAlive throttled).
- Backend unreachable at runtime: collector's exporter retry/queue defaults
  apply; not our problem to surface beyond the log file.
- Secrets (API keys) live plaintext in fragment YAML — the collector reads
  them there regardless. Keychain integration deferred.

## Testing

- Unit: config render, fragment generation from presets, settings/state
  round-trips, env emission per shell.
- Integration: launch a real core collector binary with generated configs,
  send one OTLP span through, assert it reaches a debug/file exporter;
  exercise apply → break config → rollback. Runs in CI with a downloaded
  core binary.
- launchd layer: thin wrapper around `launchctl`, covered by a manual
  checklist per release, not automated CI (no launchd in CI).

## Risks / verify-first items

- **R1 (design-level):** confmap list-merge semantics. Additive backends
  require pipeline `exporters:` arrays to *append* across `--config` files;
  historically confmap replaces lists, and append behavior arrived behind a
  merge-append option gate in recent collector versions. **Verify before any
  other work.** Fallback if append isn't reliable across supported distros:
  fragments stay pure YAML, but compy renders the `service:` section itself
  into a small generated overlay file — contained change, same UX.
- **R2:** OTelBin URL-fragment format and size limits.
- **R3:** `launchctl setenv` behavior on current macOS (SIP-era quirks).
- **R4:** systray library choice for Go (fyne-io/systray vs alternatives)
  and its macOS notarization behavior.

## Milestones

1. **Core loop, no service:** state dir, config model, presets,
   `compy validate` + foreground run. (R1 verification is task zero.)
2. **Service:** launchd install, apply/kickstart, health probe, rollback.
3. **Env:** `compy env`, `compy run`, macOS `launchctl setenv` opt-in.
4. **UI + tray:** embedded web UI, menu-bar app.
5. **Packaging:** signing, notarization, bundled distros, brew tap.

Linux (systemd + XDG, no tray needed) and Windows are structured-for but not
built in these milestones.

## Non-goals (v1)

Telemetry viewing of any kind; runtime distro download; fleet/remote
management beyond confmap URIs; team profile sharing; keychain secrets;
Windows implementation.
