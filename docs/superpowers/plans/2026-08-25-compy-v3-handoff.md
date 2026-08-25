# compy v3 — Design-Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the user's Claude Design handoff exactly: docs/design/handoff/README.md is the governing spec (intent), docs/design/handoff/compy.dc.html the behavioral prototype (behavior wins on conflict). Vocabulary, removals, and renames go ALL the way through UI, CLI, and API.

**Acceptance:** an acceptance checklist transcribed from the live prototype (produced in parallel; lands at docs/design/handoff/ACCEPTANCE.md) — every item click-verified in the real app before final review.

## Global Constraints
- Handoff README's tokens/type/spacing/icons/vocabulary are law. "preset" everywhere ("variable set"/"set" dies); "built in to compy"; origin shown as host, never "remote"; lowercase UI copy (Title case only in the native menu).
- Departures are decisions: NO per-config collector binary; NO rollback; NO menu-bar collector switcher.
- Gates as always: gofmt/vet, full go test, drift test, GOOS=linux build, COMPY_HOME temp in tests, launchctl shim for any live activation, interactive click-verification for UI claims.
- Icons: Lucide subset inlined as SVG (ISC — vendor LICENSE-lucide + VERSIONS entry, ~25 icons listed in README). Fonts: vendor JetBrains Mono (OFL, latin, 400/500/700) like prior fonts; IBM Plex Sans already stacked.

### Task 1: backend/product surgery (Go + API + CLI)
**Removals:** `/api/configs/{name}/meta` loses `distro` (route stays for remote_url; meta.Distro tolerated on disk, ignored); DELETE `compy config set-distro`; DELETE rollback everywhere (route POST /api/service/rollback, CLI `compy rollback`, tray item); DELETE menu_distro_swap setting + its API field + CLI flag (menu-bar switcher is gone).
**Renames (API+CLI+openapi+wiring test):** `/api/configs/{name}/sets/{set}*` → `/presets/{preset}*`; CLI `compy sets …` → `compy presets …`, `compy set <config> <set> K=V` → `compy presets set <config> <preset> K=V`; error copy s/variable set/preset/.
**Failure guarantee** (README: "on failure the previously active configuration keeps running"): Activate's post-install probe failure auto-restores the PREVIOUS config+preset (reuse SnapshotActive/RestoreActive internally — snapshot before install, restore on startup-failure; the machinery stays, the user-facing rollback dies). Error shape gains `"still_running": "<prev-config> · <prev-preset>"` for the failure panel. Tests: validate-failure (no change, as today) + startup-failure (auto-restore, prev running, marked 500 with diagnostic).
**Stop/Start:** `app.Stop()` (bootout collector job; settings gains nothing — stopped = job absent), `app.Start()` = Apply. Routes POST /api/service/stop|start (restart = existing apply). CLI `compy stop|start`. Status: `running:false` + active config still named (prototype shows config tiles dimmed when stopped).
**Health:** GET /api/collector/health → scrape http://localhost:8888/metrics (2s timeout) for received/exported/queue/dropped (otelcol_receiver_accepted_*, otelcol_exporter_sent_*, otelcol_exporter_queue_size, *_refused/_dropped — verify real metric names against the running collector and DOCUMENT which were chosen); `{"available":false}` when stopped/unscrapable. Shipped configs: add service::telemetry metrics exposure on :8888 if not default (verify against otelcol v0.135; adjust debug/otlp/bronto yamls + TestDefaultsValidate).
**Download progress:** distro.Ensure gains a progress func (bytes/total); app tracks in-memory per-name; GET /api/distros/{name}/progress → {status: idle|downloading|done|failed, pct, error}; POST fetch becomes async (starts download, UI polls). CLI `compy distro fetch` keeps blocking with a simple printed percent.
**Recency:** settings.Recent []string (most-recent-first, cap 20), updated on successful Activate; exposed in GET /api/status as `recent`. Tray/menu consume it.
- [ ] TDD throughout; commit granularity by area (renames+removals / failure+stop / health / progress+recency).

### Task 2: UI rebuild (static/) — the big one
Recreate all five... four window surfaces exactly from the prototype: Configurations home (table grid 62px/minmax(170px,1fr)/330px/112px, preset selector-in-row + inline preset editor, find, new-config strip w/ slug note + URL fetch paths, activation-failure panel = THE home for diagnostics, inline destructive confirms, nothing-active strip), Editor (one-line header w/ inline-editable name + URL field + save; presets band: chips w/ amber dot + inline rename + copy/trash + dashed "+", value cards 3-per-row w/ reveal toggles + origin hints, warning sentence row; save results at screen level; YAML collapsed-by-default for protected, gutter + highlight, "edit anyway"→"make it mine"), Collector (header w/ restart/stop|start, four tiles, health strip w/ :8888 note, log toolbar: filter/level chips/count/copy/tail indicator, 3-col log w/ level colors, min-width max-content), Settings (app section: appearance segmented control (localStorage, follows macOS live) + os-env toggle + CLI sentence; collector table w/ per-row play/download/folder/trash, progress bar, ban row for ebpf, add row). Tokens/type/spacing/animations (compyPulse, compyBar only) per README tables verbatim. Vendor JetBrains Mono + Lucide subset first.
- [ ] Iterate against the prototype side-by-side (screenshots for look, CDP drives for behavior); acceptance checklist items for these screens must pass before task-done.

### Task 3: menu bar v4 (tray)
One list: status line (state · config · preset), ports + warnings line, CONFIGURATION header, ≤10 recency-ordered configs — click activates single-preset configs; >1 preset → submenu where PICKING A PRESET ACTIVATES; More… submenu (rest, alphabetical); Restart collector; Open compy (existing single-window); Quit. Title case per macOS. Remove rollback + distro submenu. systray constraint check: nested submenu-in-submenu for More…>config>presets may not be supported — if not, flatten More… entries to "config · preset" items and record the deviation.
- [ ] Helpers unit-tested (recency ordering, More… split, submenu decision); build + smoke.

### Task 4: docs + acceptance + finish
README/CLAUDE.md (v3 CLI surface, vocabulary, removed commands); run the FULL acceptance checklist (ACCEPTANCE.md) against the real app interactively; whole-branch review (fable — this is architectural scale); fix wave; merge; live rollout (tray reinstall); live click-through on real machine.

## Execution notes
- Parallel now: acceptance-transcription agent (prototype → ACCEPTANCE.md) ∥ T1 (backend). T2 after T1 (needs endpoints), T3 parallel with T2 (tray vs static/), T4 last.
- OTELCOL_BIN: /private/tmp/claude-501/-Users-severin-Projects-local-collector/05e1307f-c061-4fa0-aa7f-31358b0cef49/scratchpad/otelcol
- Prototype seeding for drives: design-skill helper (seed-canvas.mjs) wraps compy.dc.html into a runnable page.
