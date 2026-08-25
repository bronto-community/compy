# compy v2 — Configurations Design

2026-08-25. Supersedes the backend-fragment model of the v1 spec
(2026-08-24-local-collector-design.md); everything not mentioned here
(launchd supervision, no-daemon architecture, state-dir layout principles,
env wiring, tray-as-login-agent, brand rules) carries over.

## The model shift

v1: base.yaml + additive backend fragments merged by the collector.
v2: **Configurations** are first-class, whole collector-config documents.
**Exactly one configuration (+ one of its variable sets) is active at a
time.** Sending to several vendors at once = several exporters inside one
configuration's YAML. The fragment/merge-append machinery is deleted.

The stable-endpoint *promise* is dropped as a hard guarantee: shipped
default configs listen on compy's standard ports out of the box, but users
own their YAML and may change receivers freely.

## Configuration

A configuration is a directory `configs/<name>/` in the state dir:

```
configs/<name>/config.yaml     # the collector YAML (user-editable)
configs/<name>/meta.json       # properties + variable sets + provenance
```

meta.json:
```json
{
  "remote_url": "https://raw.githubusercontent.com/.../collector.yaml",
  "distro": "core",                     // default collector binary for this config
  "pristine_sha256": "...",            // hash of last synced/shipped config.yaml
  "variable_sets": {
    "default": {"HONEYCOMB_KEY": "...", "OTLP_ENDPOINT": "..."},
    "staging": {...}
  },
  "active_set": "default"
}
```

- **Operations:** create (blank / from template / from URL), edit, copy,
  delete, sync.
- **Variables:** any `${VAR}`, `${VAR:-default}`, `${env:VAR}`,
  `${env:VAR:-default}` reference in config.yaml is a variable. compy parses
  them out (with defaults) to drive the variable UI/CLI. A YAML comment on
  the same line is the variable's description. All other confmap schemes
  (`${file:...}`, `${secretsmanager:...}`, …) pass through untouched — the
  collector owns them.
- **Injection:** variable values are NOT text-substituted. The active set's
  values go into the LaunchAgent plist's `EnvironmentVariables` dict; the
  collector expands them natively. `${VAR:-default}` therefore works with no
  compy code. Values are plaintext on disk for now — `${secretsmanager:...}`
  and keychain are the later escape hatches (deliberate deferral).
- **Provenance / edit protection (ONE mechanism for two features):**
  `pristine_sha256` records the content as shipped (default configs) or as
  last synced (remote configs). Current hash == pristine → "unmodified":
  remote configs show Sync / Sync All; default configs update in place on
  compy upgrades. Current hash != pristine → "locally modified": sync
  button disappears (replaced by "discard local edits & re-sync" for remote
  configs), and shipped-config upgrades keep the user's version. Editing a
  default config requires an explicit confirmation ("future updates will
  keep your version"); its YAML editor is collapsed by default → unhide →
  explicit "Edit" unlock.
- **Remote sources v1: plain HTTP(S) URLs only** (GitHub raw URLs are just
  HTTP). OTelBin and friends later. Manual sync only (button / sync-all).

## Shipped default configurations

Embedded in the binary (go:embed), materialized into `configs/` on first
run and on upgrade (subject to the edit-protection rule): `debug`, `otlp`,
`bronto`, …. They use compy's standard ports in their receivers and declare
variables (e.g. `${OTLP_ENDPOINT}`, `${API_KEY}  # vendor API key`) instead
of hardcoded credentials.

## Distributions

Definition-driven, nothing bundled. compy ships **definitions** for:

| name | source | darwin_arm64 approx | note |
|---|---|---|---|
| core | otelcol v0.135.0 | 37 MB gz | |
| contrib | otelcol-contrib v0.135.0 | 87 MB gz | the big one |
| otlp | otelcol-otlp v0.135.0 | 10 MB gz | minimal middle ground |
| ebpf-profiler | opentelemetry-ebpf-profiler | — | linux-only, NO upstream binary releases yet → definition marked unavailable; hidden on darwin |

A definition = pinned version, per-platform URL template, sha256. First use
of a distro downloads to `distros/<name>/`, verifies the checksum, and runs
it (curl-style download carries no quarantine xattr; Go binaries are ad-hoc
signed). Users may override a definition's path (with a warning, same
edit-protection spirit) or add their own binaries. `settings.json` keeps the
registry; per-config `distro` picks the default binary, falling back to the
global selection.

## Views (P3 UI)

1. **Configurations list** — rows: name, provenance badge (shipped /
   remote / local), modified state, active marker; actions: edit, copy,
   delete, sync; "New configuration" (blank / from URL).
2. **Configuration editor** — three sections:
   - top: name, remote URL, default collector binary, (room for more);
   - middle: variables table parsed from the YAML (name, description from
     trailing comment, default), with named variable sets as columns/tabs —
     create/rename/delete sets, pick active;
   - bottom: YAML editor — CodeMirror, vendored (MIT, one minified file, no
     build step, no CDN), syntax highlight, line numbers, validation
     feedback via `otelcol validate`. For unmodified shipped configs the
     editor is hidden → unhide (read-only) → explicit Edit unlock +
     confirmation.
3. **Collector view** — status, ports, and the collector log, searchable
   (client-side filter over the tail; this is operational output, not
   telemetry — the non-goal stands).
4. **Settings** — distro definitions/paths (edit with warning, add custom),
   the menu-bar distro-swap toggle (OFF by default), ports, OS-env toggle,
   shell wiring reference.

## Menu bar (P4)

Top: status block — running/stopped, open ports, error/warning count from
the current collector log (since last apply). Middle: pick the active
configuration; beneath the active one, pick its variable set. Optional
distro swap section, only when enabled in Settings. Then Open compy / Quit.

## API: REST + OpenAPI (P2)

No daemon (unchanged): the REST API is served by whatever UI process is up
(window/tray/`compy ui`); the CLI links the same Go core directly — feature
equivalence by construction. The REST surface is defined in a hand-written
`api/openapi.yaml` committed to the repo; tests assert every route in the
spec exists in the mux and every mux route is in the spec (drift test), plus
handler tests per route. CRUD configs, variable sets, activation, sync,
distros, service ops, status, log.

## Migration

On first v2 run, if legacy `config/backends/*.yaml` exist: create one
configuration per enabled set? No — simpler and honest: create a single
configuration named `migrated` whose YAML is the *rendered effective config*
of the old model (base + enabled fragments, which the old code can still
produce once during migration), select it, then archive the legacy files to
`legacy-v1/`. One-way, logged, no ongoing compatibility.

## Phasing

- **P1 — core + CLI:** config store (CRUD/copy), variable parsing +
  variable sets, activation (plist env injection), shipped defaults +
  edit-protection hashing, remote add/sync (HTTP), distro definitions +
  on-demand download w/ checksum, migration, CLI surface for all of it.
- **P2 — REST + OpenAPI:** api/openapi.yaml, handlers over the core,
  drift + handler tests; window/tray/ui serve it.
- **P3 — UI:** the four views, vendored CodeMirror.
- **P4 — menu bar v3 + polish:** stats block, config/set pickers, gated
  distro swap; sync-all; docs.

Good test coverage is an explicit requirement at every phase (unit +
route-level + the existing e2e pattern updated to the config model).

## Deliberate deferrals

Plaintext variable values (keychain / secretsmanager later); OTelBin and
authenticated remote sources; polling/auto-sync; ebpf-profiler until
upstream publishes binaries; Windows/Linux service layers (unchanged).
