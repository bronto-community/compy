# compy self-observability — design (2026-08-31)

Status: PROPOSAL — nothing here is built. Owner decisions pending are
marked ⚖.

compy emits its own telemetry — OTel on the wire, sent to Bronto — so we
learn how it is used and where it breaks in the field. This is product
telemetry in the RUM sense: it runs inside an end user's environment, so
consent, transparency, and data minimization are design constraints, not
afterthoughts.

## Goals

- Adoption and usage: which surfaces (tray / window / web UI / CLI),
  which features (configurations, presets, distros, templates), version
  and platform spread, upgrade lag.
- Field failures: collector validate/start failures, migration errors,
  activation rollbacks, panics — by error class, not by error text.
- Dogfooding: compy is an OpenTelemetry tool; its own telemetry should
  be clean OTLP that lands in a Bronto dashboard the way we tell users
  their telemetry will.

## Non-goals

- Never any of the USER's telemetry: nothing that flows through their
  collector is read, sampled, or counted beyond compy's own process
  boundaries.
- No PII and no user content: no usernames, hostnames, paths, config
  contents, endpoints, env var names or values, API keys, or raw error
  strings from the user's environment.

## Consent model ⚖

The decision: opt-in (nothing sent until the user says yes) versus
opt-out (on by default, loud first-run notice, easy off).

- **Opt-in, ask-first (recommended).** The first tray/window launch
  shows a one-time card: what is collected (link to TELEMETRY.md),
  yes/no. CLI-only users get a one-line notice on first run pointing at
  `compy telemetry on`. Until answered, nothing is sent. This costs
  data volume but fits the audience (observability engineers notice
  telemetry) and the GDPR posture of an EU-maintained project.
- **Opt-out with notice** (Homebrew/VS Code model): more data, more
  goodwill risk; a dev-tool RUM analogy supports it, but the OTel
  community skews privacy-sensitive.

Independent of the choice, all of these hold:

- `settings.json` field `telemetry: "on" | "off"`, unset = undecided
  (undecided sends nothing in the opt-in model).
- `compy telemetry status|on|off` in the CLI; a toggle in the settings
  UI; both show WHAT is collected, not just the switch.
- `COMPY_TELEMETRY=off` and the cross-tool `DO_NOT_TRACK=1` env vars
  always win over settings. CI environments (`CI=true`) never send.
- From-source builds send nothing (see key handling below) — telemetry
  is a property of released binaries.

## What is emitted

Identity: a random instance UUID, generated on consent, stored in
settings, shown by `compy telemetry status`, regenerated or deleted by
`compy telemetry reset`/`off`. Not derived from hardware. Resource
attributes: `service.name=compy`, instance id, compy version, os/arch,
macOS major version, install method (brew / source), bundled collector
version.

- **Metrics** (counters/gauges, coarse): command invocations by command
  name; activations by config identity — shipped names (`debug`,
  `otlp-basic`, `otlp-forward`, `bronto`) verbatim, anything user-made
  as `custom`, remote as `remote`; preset counts (bucketed); distro in
  use (shipped names only); UI opens by surface; collector
  running/uptime; upgrade events (version from→to).
- **Events/logs**: first-run, consent changes, failure events by CLASS
  (`validate_failed`, `start_failed`, `rollback`, `migration_failed`,
  `panic`) with no free-text from the user's machine — a stack hash for
  panics, never a message.
- **Traces**: deferred. A span tree of the activation flow
  (validate→install→start→health) is attractive diagnostics but not
  needed for v1; metrics + failure events carry the same signal at far
  lower cost.

## Transport and architecture

- **Direct OTLP/HTTP to Bronto ingestion** (`ingestion.eu.bronto.io`),
  no intermediary service. The user's collector pipeline is never
  touched — injecting compy's telemetry into their pipeline would ship
  our data to their backend.
- **Spool, then upload.** CLI commands are short-lived and must never
  block on the network: every process appends events to a local spool
  (`COMPY_HOME/telemetry/`), and the long-lived processes (tray, ui,
  window) flush it periodically as OTLP batches — fail-silent, short
  timeouts, spool capped and self-pruning. No tray, no long-lived
  process → the next CLI invocation flushes opportunistically in the
  background. (This is the Go-toolchain telemetry shape: local first,
  network later, never in the hot path.)
- **Ingestion key** ⚖: baked into RELEASED binaries at build time
  (ldflags), scoped to `IngestionApi` only — a public write-only key,
  which is exactly what browser/mobile RUM ships. Absent in from-source
  builds, so those are silent by construction. Rotation rides releases.
- **Wire format** ⚖: hand-rolled OTLP/HTTP+JSON encoder for logs and
  metrics (small, stable structs) rather than the OTel Go SDK — the SDK
  is a large dependency tree against compy's stdlib-only rule, and we
  need a fraction of it. The wire stays standard OTLP either way; if
  the owner prefers the SDK, that needs a dependency ruling like
  yaml.v3 got.

## Transparency

`TELEMETRY.md` in the repo root: every attribute and event enumerated,
the consent mechanics, the retention story, and how to verify (point
compy's own spool at a debug config and look). Linked from the README,
the first-run prompt, and `compy telemetry status`. The sanitization
rules above are enforced in one place in code so the doc stays true.

## Bronto side

Collection `compy`, datasets per signal via the standard
`service.namespace`/`service.name` routing. One adoption dashboard:
weekly active instances, version spread, activations by config, failure
rate by class, upgrade lag — the compy sibling of the Claude Code
dashboard.

## Phasing

1. Consent plumbing: settings field, CLI verbs, env overrides, UI
   toggle + first-run card, TELEMETRY.md. Nothing sent yet.
2. Spool + OTLP JSON exporter + first metrics (invocations, version,
   platform) behind consent.
3. Tray uploader cadence, failure-class events, the Bronto dashboard.
4. Later, if wanted: activation-flow spans.

## Open decisions ⚖

1. Opt-in ask-first (recommended) or opt-out with notice?
2. Embedded write-only ingestion key in released binaries — acceptable?
   Which Bronto org/collection does this land in?
3. Hand-rolled OTLP JSON (recommended, keeps the stdlib rule) or an
   OTel Go SDK dependency ruling?

---

## Amendment 1 — what actually shipped (owner-directed, 2026-09-02)

The proposal above is PRODUCT telemetry: compy phoning home to Bronto so we
learn how it is used. What the owner asked to build first is a different
thing wearing the same words — **observability of compy, for the person
running compy** — and it resolves most of the ⚖ decisions by removing them.

Shipped:

- **A toggle** (`settings.json` `tracing`, a switch on the settings screen,
  `compy settings set --tracing`). Off by default.
- **OpenTelemetry tracing**, real spans from the real SDK, over compy's own
  operations: `compy.activate` with `compy.render`, `compy.validate`,
  `compy.launchd.install` and `compy.probe` beneath it, plus `compy.apply`
  and `compy.stop`. Instrumented in `internal/app`, so the same spans
  appear whether the operation came from the CLI, the tray, or the web UI;
  `compy.surface` on the resource says which.
- **Default destination: compy's own collector** (`127.0.0.1:<http_port>`).
  Compy's telemetry travels the path a user's applications do and lands
  wherever their active configuration sends it. An endpoint + headers in
  settings bypasses it for a backend directly.

What that changes about the decisions above:

1. **Consent** is no longer a question. Nothing is collected until the user
   turns it on, and what is collected goes to THEIR pipeline, on their
   machine, by default. There is no first-run prompt to design because
   there is no default collection. `TELEMETRY.md` is not needed for this;
   it would be needed for the Bronto-facing proposal, which is untouched.
2. **The embedded ingestion key** is moot for this feature: there is no
   compy-owned destination. It stays open for the product-telemetry
   proposal, if that is ever built.
3. **The wire format** was ruled: the stock OTel Go SDK with its OTLP/HTTP
   protobuf exporter, +17.5 MB, over a hand-written JSON exporter at
   +5.4 MB (see CLAUDE.md's Dependencies). An OpenTelemetry tool ships the
   OpenTelemetry SDK.

Deliberately still absent: metrics and logs (traces first), sampling
configuration (a local tool at this volume does not need it), and any
attribute carrying a config's contents, endpoints, or values — a span says
WHICH configuration by name and nothing about what is in it.

The two failure modes worth knowing:

- The default destination is a collector the user can stop, and compy
  traces its own `compy stop`. Export timeout and shutdown are short
  (2s/3s) and retry is OFF, so a stopped collector costs a dropped span,
  never a hanging CLI. The OTLP defaults — 10s timeout, 5s initial backoff,
  retry on — would have put a ten-second pause on every command.
- `otlptracehttp.WithEndpointURL` takes its URL literally. The setting is a
  BASE (like otlp-forward's endpoint field), so `tracing.TracesURL` appends
  `/v1/traces`; without it every span POSTs to `/`, gets a 404, and
  disappears silently. Caught only by asserting the path in a test — the
  first end-to-end run "passed" against a handler that accepted anything.
