# compy v3 — Acceptance checklist (transcribed from prototype)

Source of truth for **behaviour**: `compy.dc.html`, driven live via CDP (Chrome, headless,
the artboard executing in its sandboxed iframe) and cross-checked against the component's
own source where the DOM/automation was ambiguous or clipboard-gated. Source of truth for
**intent**: `README.md` in this folder. Where they disagree, the prototype's behaviour is
the requirement — see **Prototype↔README disagreements** at the end.

Every item is Given/When/Then, one observable behaviour, with exact copy in quotes.
Numbering: C1 = Configurations (home), C2 = Configuration editor, C3 = Collector,
C4 = Settings, C5 = Menu bar, C6 = Cross-cutting.

---

## C1. Configurations (home)

**C1.1 — Header sentence.** Given the configs screen, the header reads "configurations"
with sentence "activating restarts the collector" underneath, always, regardless of state.

**C1.2 — Table columns.** Given the table, the header row shows blank / "NAME" / "PRESET"
/ blank in 10px uppercase faint labels, over a `62px minmax(170px,1fr) 330px 112px` grid.

**C1.3 — Status + type icons.** Given any row, the first icon is circle-dot (amber) if that
config is the running one, else a plain grey circle, titled exactly "running now" or
"not running". The second icon is package/user/link with title exactly "built in to
compy", "yours", or "fetched from otel.acme.dev" (never a generic word like "remote").

**C1.4 — Active row.** Given the running config's row, its name renders amber and the row
carries panel fill with a left accent bar; there is no separate "running" badge or column —
the colour is the only signal.

**C1.5 — Sorting.** Given any number of configs, the table is alphabetical by name always
(confirmed by name after creating/duplicating entries — new rows insert in place, not at
the end).

**C1.6 — Play tooltip states.** Given a non-running config's play icon, title is exactly
"activate `<name>` · `<preset>`" (config-selected preset). Given the running config's play
icon, title is exactly "already running" (icon inert).

**C1.7 — Preset selector, single preset.** Given a config with exactly one preset, the
preset control shows the preset name with **no chevron** (confirmed: 11 of the 12 built-in
seed configs have a single "default" preset and render no chevron; `otlp-to-bronto`, the
only multi-preset seed config, does).

**C1.8 — Preset dropdown contents.** Given a multi-preset config, clicking its preset
selector opens a dropdown listing every preset as its own row (name + "activate this
preset" play icon, amber, + "edit this preset" pencil icon), ending in a dashed "+" row
titled exactly "add a preset".

**C1.9 — Row actions always present.** Given any row, three icons are always rendered
right-aligned regardless of applicability: copy (duplicate), refresh-cw/undo (sync/reset),
trash (delete). Disabled ones are greyed to faint, keep default cursor, and carry an
explaining title — never hidden. Exact strings captured:
  - duplicate: title "duplicate, including presets" (always enabled).
  - sync/reset when user-owned from the start: "yours from the start, nothing to return
    to" (disabled).
  - sync/reset on an unmodified built-in: "this is the shipped version, nothing to reset"
    (disabled).
  - sync/reset on a modified built-in: "reset to the version that ships with compy"
    (enabled).
  - sync/reset on an unmodified linked config: "in sync with otel.acme.dev" (disabled).
  - sync/reset on a linked config with an available upstream update: "update available
    from otel.acme.dev" (enabled) — distinct from the modified-locally case below.
  - sync/reset on a linked config the user has edited: "discard my edits and re-sync from
    otel.acme.dev" (enabled).
  - delete on the running config: "can't delete the running config" (disabled).
  - delete otherwise: "delete `<name>`" (enabled).

**C1.10 — Activation success.** Given clicking play on a config whose selected preset
succeeds, Then the row shows pulsing "restarting…" plus a 44px indeterminate bar for
**2.3s** (2300ms in source), after which the row becomes the new active row, sidebar/menu
state flips to "Running · `<name>` · `<preset>`", and the config moves to the front of the
menu-bar recency list.

**C1.11 — Activation ignored while in flight.** Given an activation in progress
(`busyId` set), Then clicking any other play/preset action is a no-op until the in-flight
one settles (source: `if (this.state.busyId) return;` guards every activate call) — this
literally implements the README's "further activations are ignored until it settles."

**C1.12 — ebpf-profiles failure (documented demo #1).** Given clicking play on
`ebpf-profiles`, Then after **3.4s** an err panel appears above the table reading exactly:
"couldn't activate ebpf-profiles" (no preset suffix — it has only one preset), right-aligned
"`<previously-active config>` still running" (or "collector still stopped" if nothing was
running), a "dismiss" action, then the collector's diagnostic in a scrollable monospace
`<pre>`:
```
Error: cannot start pipeline: failed to build pipelines: failed to create "profiles" receiver for data type "profiles":
  receiver "ebpf" is not supported on darwin/arm64
    github.com/open-telemetry/opentelemetry-collector-contrib/receiver/ebpfreceiver@v0.108.0/factory.go:71
2026/08/25 14:22:07 collector server run finished with error: cannot start pipeline
exit status 1
```
then "open in editor" / "copy diagnostic" actions. The previously-active config keeps
running; nothing else changes.

**C1.13 — otlp-to-bronto · customer-x failure (documented demo #2).** Given opening
`otlp-to-bronto`'s preset dropdown and clicking "activate this preset" on `customer-x`,
Then after **2.3s** the panel reads exactly "couldn't activate otlp-to-bronto ·
customer-x", right-aligned reassurance naming whichever config is actually still running,
then the diagnostic:
```
Error: invalid configuration: exporters::otlp/bronto: 'headers' has invalid value for key 'x-bronto-key':
  environment variable "BRONTO_KEY" is not set and has no default
    at config.yaml line 17, column 7
2026/08/25 14:22:07 collector server run finished with error: invalid configuration
exit status 1
```

**C1.14 — Other configs can also fail activation, deterministically.** Given the seed
data, activation fails (reusing the exact C1.13 diagnostic, substituting the config/preset
name) for **any config whose selected preset has an empty `BRONTO_KEY` value AND whose
config has more than one preset** — this is a generic rule in the prototype
(`needsKey = cfg.presets.length > 1 || id === "honeycomb"`), not something scoped to
`otlp-to-bronto`. Confirmed failing: `acme-standard` (presets: default, eu-region),
`trace-debug` (default, verbose), `prom-scrape-local` (default, high-cardinality) — all
because their non-otlp configs incidentally carry 2 seed presets. Confirmed succeeding:
`docker-stats`, `local-debug`, `k8s-attributes`, `metrics-only`, `vector-bridge`,
`tail-sampling`, `logs-to-file` (all single-preset). **This is prototype sample-data
shape, not a real per-config contract** — see Not-verifiable section; v3 should validate
against real collector output, not reuse a canned string for unrelated configs.

**C1.15 — New configuration, blank path.** Given clicking "new configuration", Then an
inline strip opens with exactly two fields: NAME (helper text "lowercase, digits, dashes")
and "FROM URL (OPTIONAL)" (helper text "empty means a blank config"), plus "cancel" /
"create". Typing "My Collector" live-slugs and the helper note becomes "saved as
my-collector" (dim2 colour). Typing an existing name changes the note to red (err colour)
"`<slug>` already exists" (confirmed exact: "docker-stats already exists") and (by source)
blocks creation.

**C1.16 — New configuration, URL path, success.** Given a name and a URL whose host is
`otel.acme.dev`, clicking "create" shows "fetching…" (create button also relabels to
"fetching…", disabled) for **1.5s**, then: the form closes, a new row appears in the table
(alphabetically placed, link icon, title "fetched from otel.acme.dev" / reset title "in
sync with otel.acme.dev"), and a transient note reading exactly "`<name>` fetched from
otel.acme.dev" shows for **3.2s** then auto-clears (confirmed via source; the automation's
polling cadence was too coarse to reliably screenshot the note itself — content confirmed
by reading the handler, not just by clicking).

**C1.17 — New configuration, URL path, 404.** Given a name and a URL whose host is not
`otel.acme.dev` (e.g. `https://example.com/config.yaml`), Then after 1.5s the form stays
open, the name is preserved, and the URL field area shows exactly "404 · nothing at that
URL. compy kept nothing." — no row is added, config count unchanged, "create" re-enabled.

**C1.18 — Duplicate.** Given clicking the copy icon on a row (e.g. `acme-standard`), Then
a new row `<name>-copy` appears immediately (confirmed convention: `acme-standard-copy`),
origin resets to user-owned regardless of the source's origin, presets are copied.

**C1.19 — Destructive: delete.** Given clicking the trash icon on a deletable row, Then an
inline row (no modal) replaces nothing — it appears directly above the table — reading
exactly "delete `<name>` and its presets?" with "keep it" / "delete". Confirming removes
the row immediately; count decrements.

**C1.20 — Destructive: reset (built-in, modified).** Given a modified built-in config's
sync icon, clicking it shows exactly "reset `<name>` to the version that ships with
compy? your changes are lost." with "keep it" / "reset". Confirming restores the shipped
version, re-locks it (read-only again), and the sync icon title reverts to "this is the
shipped version, nothing to reset". A transient note "`<name>` reset to the shipped
version" fires for 3.2s (source-confirmed).

**C1.21 — Destructive: discard & re-sync (linked, modified).** Given a modified linked
config's sync icon, clicking it shows exactly "re-syncing `<name>` throws away your
edits." with "keep it" / "discard & re-sync" (confirmed on `tail-sampling`).

**C1.22 — Nothing-active empty state.** Given the collector is stopped (reached via
Collector screen "stop"), Then the configs screen shows a dashed strip with headline
**"nothing active"** and body "no collector is running. activate a config and it starts."
plus one primary action button labelled "activate `<name>`" (a specific config, e.g.
"activate local-debug"). **Disagreement from README** — see final section: the README's
prose names the headline "nothing is running yet"; the shipped copy is "nothing active".

**C1.23 — Find.** Given the header find field (placeholder "find"), typing filters the
table live; per README, an empty result reads exactly `no configuration matches "x"`
(not independently re-typed to zero results during this pass, but the field, icon, and
live-filter behaviour were confirmed wired).

**C1.24 — "sync all" is a stub.** Given the header's "sync all" action, clicking it does
literally nothing in this build (`syncAll: () => {}` in source) — no confirmation, no
state change. Flag for v3: this is an unimplemented placeholder, not a documented no-op.

---

## C2. Configuration editor

**C2.1 — Header, one line.** Given any config's editor, the header is a single row:
status dot (only if this config is the running one) · type icon (title exactly
"fetched from `<host>`" / "built in to compy. updates with it until you edit." / "yours")
· name as an inline field (borderless until hover/focus) · for linked configs, the URL
occupying the row's free space · right-aligned: re-sync/reset control (label depends on
origin, see C2.2) · "save". There is no second header line.

**C2.2 — Re-sync/reset label by origin.** Exact labels confirmed from source:
built-in → "reset to shipped"; linked, unmodified → "re-sync"; linked, modified →
"discard edits & re-sync". Shown (`showReset`) only when `origin === "url"` or
(`origin === "builtin" && edited`) — i.e. an unmodified built-in shows no reset control at
all in the header (the row-level action in C1.9 is the one that's always present).

**C2.3 — Editor header re-sync/reset button did not respond to automation.** Clicking the
header's reset/re-sync control (as opposed to the table-row icon in C1.20/C1.21) produced
no visible effect in repeated attempts. Source confirms this is not an automation
artifact: `resync: () => {}` — the editor header's own handler is an **unwired stub** in
the prototype. The table-row equivalent (C1.20/C1.21) is fully wired and was verified end
to end. Treat the editor-header control's confirm copy as intent-only (same strings as
C1.20/C1.21 apply per README) but note it is unverified in this build.

**C2.4 — Presets band, row one.** Given the presets band (raise fill, full width, directly
under the header), row one reads "presets" (lowercase, CSS-uppercased) then one chip per
preset. The selected chip shows a 5px amber dot and its name as an inline input (dashed
underline, title "rename this preset"); unselected chips are plain clickable text. Every
chip carries "duplicate this preset" and "delete `<name>`" icons. A dashed "+" chip
(title "add a preset") is last.

**C2.5 — Selecting a preset is shared state.** Given clicking a chip in the editor, Then
the row-level preset control on the Configurations table (C1.6/C1.8) reflects the same
selection afterward — confirmed by activating via the row's play button and getting the
preset last selected in the editor (`prod-key`), not the config's original default. This
is the `presetSel` state described in the README, shared between the row control and the
editor band.

**C2.6 — Value cards.** Given the selected preset's values, each card shows the bare key
(e.g. "BRONTO_KEY", not `${env:BRONTO_KEY}`), an origin hint right ("line 17" / "default"),
then the value input. Secret fields render masked (bullets) with a "reveal"/"hide" toggle.
Confirmed: reveal state is keyed by KEY name and **persists across preset switches**
(switching from `staging` to `prod-key` kept BRONTO_KEY revealed, showing the different
underlying secret for that preset) — matches the README's `reveal { KEY: bool }` state
model exactly.

**C2.7 — Row three warning sentence.** Given the `customer-x` preset is selected, Then a
sentence appears reading exactly "customer-x has no ingest key. activating with it will
fail." (source: hardcoded to `s.preset === "customer-x"`, not a generic "is anything
required empty" computation — a newly-added draft preset with an empty required key does
**not** trigger this sentence). Flag for v3: implement this as a real "is any required
value missing" check, not a name-specific special case.

**C2.8 — Add preset.** Given clicking the "+" chip, Then a new preset named exactly
"preset" is created (next collision-avoided name is "preset-2", etc., per source), becomes
selected, with all its values empty (echoing each key's default via placeholder).

**C2.9 — Duplicate preset.** Given clicking a chip's duplicate icon, a new preset named
`<name>-copy` is created with that preset's values copied, and becomes selected.

**C2.10 — Delete preset: last preset undeletable.** Given a config with exactly one
preset, its delete icon's title is exactly "a config always keeps one preset" and the icon
is inert (disabled).

**C2.11 — Delete preset: running preset undeletable (when not last).** Given the running
config with 2+ presets and its currently-running preset selected, that preset's delete
icon title reads exactly "this preset is running. activate another one first." — other,
non-running presets in the same config delete immediately with no confirmation prompt
(deleting a preset is not one of the destructive-confirmation flows the README documents
for the configs table; behaviour confirmed: click removes it instantly, selection falls
back to a neighboring preset).

**C2.12 — Duplicate preset name rejected.** Given renaming a preset (via its inline input)
to a name another preset in the same config already has, Then a transient note reads
exactly "a preset called `<name>` already exists" for 3s, and the rename is rejected (input
reverts to its prior value).

**C2.13 — YAML collapsed by default (built-in/linked).** Given a built-in or linked
config's editor, the YAML pane starts collapsed to one line: "config.yaml" (lowercase) +
"ships with compy. most people never open it." (built-in) or "kept in sync with
otel.acme.dev" (linked) + "show yaml". User-owned configs open with YAML already expanded.

**C2.14 — YAML expanded.** Given "show yaml" clicked, the bar becomes uppercase
"CONFIG.YAML", a read-only notice + "edit anyway" appears for protected configs, then a
21-line gutter + highlighted document (this sample: OTLP receiver, batch processor, Bronto
OTLP exporter with `${env:...}` interpolation, one pipeline).

**C2.15 — Protection flow, built-in.** Given "edit anyway" clicked on a built-in config,
an inline confirmation shows exactly "your version stays through compy updates." with
"cancel" / "make it mine". Confirming removes the read-only notice; the config is now
editable.

**C2.16 — Protection flow, linked.** Given "edit anyway" clicked on a linked config, the
confirmation instead reads exactly "editing disconnects this from otel.acme.dev. it stops
re-syncing." (same "make it mine" verb) — the sentence is chosen purely by
`origin === "url"`.

**C2.17 — Save, first attempt fails.** Given clicking "save", the button reads "checking…"
and a hint "asking the collector…" appears for **1.6s**; Then (this is a **global** call
counter, not per-config — the very first save of the whole session always fails) an err
panel appears at screen level (under the presets band, visible whether YAML is expanded or
not) reading exactly "the collector rejected this config. nothing was saved." with
copy + dismiss, and the diagnostic in a scrollable `<pre>` (max-height 220px):
```
Error: invalid configuration: exporters::otlp/bronto: 'headers' has invalid keys: x-bronto-ket
  did you mean 'x-bronto-key'?
    at config.yaml line 18, column 7
2026/08/25 14:31:44 validation finished with error
```

**C2.18 — Save, subsequent attempts succeed.** Given saving again (on any config, since
the counter is global), Then after 1.6s a quiet one-line ok strip reads exactly "saved and
re-applied to the running collector in 2.4s". Every save after the first, ever, succeeds.

**C2.19 — Name field collision.** Given renaming a config (inline header field) to another
existing config's slug, Then a note reads exactly "`<slug>` already exists. name not
changed." and the rename is rejected.

---

## C3. Collector

**C3.1 — Header, running.** Given the collector is running, the header reads a mint dot,
state word "running", "pid 48213 · up 26m", then "restart" and "stop" actions.

**C3.2 — Header, stopped.** Given "stop" clicked, the dot turns grey, state word "stopped",
meta becomes "no process", and only a single "start" action remains (no separate
"restart"/"stop" pair) — confirmed: `restartLabel` is "restart" running, "restarting…"
mid-flight, "start" when stopped; `canStop` (`stop` button rendered at all) is
`!nothingActive && !restarting`.

**C3.3 — Four tiles.** Given the screen, tiles read CONFIGURATION (amber, config name or
"nothing active"), PRESET ("`<preset>`" or "—" stopped), COLLECTOR (underlined, click
navigates to Settings, title "every config runs on this one"), LISTENING (port summary or
"not listening" stopped, dimmed).

**C3.4 — Health strip.** Given running, four numbers: RECEIVED / EXPORTED / QUEUE /
DROPPED (e.g. "1.2k spans/min", "1.1k spans/min", "12%", "118"), with
"localhost:8888/metrics" named at right. Given stopped, all four show "—" and a note
"no metrics while stopped" replaces the metrics-source line.

**C3.5 — Log toolbar: level chips.** Given chips "all / error / warn / info / debug",
clicking one filters the log and updates the count line, e.g. clicking "error" produces
exactly "1 of 14 lines" (this fixture's single error line); clicking "warn" produces
exactly "2 of 14 lines".

**C3.6 — Log toolbar: filter field.** Given the filter input (placeholder "filter log…"),
typing e.g. "grpc" narrows the count to "1 of 14 lines" and shows only matching lines,
case-insensitively.

**C3.7 — Log toolbar: copy.** Given the copy icon (title "copy these `<N>` lines"),
clicking it writes the filtered/leveled lines to the clipboard and (per source) sets a
transient note reading exactly "`<N>` log lines copied" for 2.6s. **Not independently
re-observed via automated click** in this pass — see Not-verifiable section — but
confirmed present and wired via source (`copyLog`), unlike `copyDiag` (see C1.12/C4 note).

**C3.8 — Tail indicator.** Given running, the indicator reads "live tail" (mint); clicking
it toggles to "paused" (grey). Given stopped, it reads "no output" and is not clickable to
resume (nothing to tail).

**C3.9 — Log columns.** Three columns (time / level / message), `white-space: pre`,
horizontal scroll rather than wrap; level colours err / warnText / info / dim2 for error /
warn / info / debug respectively.

**C3.10 — Stop reaches nothing-active.** Given clicking "stop", Then immediately the
header flips to the stopped state (C3.2), tiles/health strip dim (C3.3/C3.4), and
navigating to Configurations shows the nothing-active strip (C1.22).

**C3.11 — Restart.** Given running, clicking "restart" pulses "restarting…" (amber dot)
for **2.2s** (2200ms in source) then returns to "running" with the same config/preset —
confirmed via source (`restart: () => { ...; setTimeout(() => this.set({restarting:
false}), 2200); }`); if nothing is active, "restart" instead re-runs `activate` on the
last known `activeId`/`activePreset` (i.e. behaves like "start").

---

## C4. Settings

**C4.1 — Section order.** Given the screen, "app" is first, "collector" second (matches
README's "app first").

**C4.2 — Appearance segmented control.** Given "system" / "dark" / "light" segments, the
selected one is amber text on accentBg fill. The note beside it reads exactly "following
macOS — currently `<dark|light>`" when "system" is selected, or exactly "always dark" /
"always light" when explicitly chosen. Selection persists via `localStorage` key
`compy.theme` (confirmed in source) and survives reload.

**C4.3 — OTEL_* toggle.** Given "set OTEL_* variables system-wide" with note "new shells
and apps point at compy automatically", the toggle renders as an on/off switch (amber when
on). **The toggle does not actually flip on click** — source: `flip: () => {}` — a stub;
its on/off render is hardcoded `const on = true`. Flag for v3: this control needs a real
handler.

**C4.4 — Collector table shape.** Grid `54px minmax(170px,1fr) 1fr 132px`; status icon
states confirmed: circle-dot amber "in use", circle "installed", download icon "available
to download", ban icon "not available on macOS" (with the *reason* in a separate, more
specific tooltip — confirmed exact: "needs Linux kernel probes. compy cannot run it
here." — distinct from the row-label tooltip "not available on macOS"). Name shows path as
tooltip; state phrase and four icons (play/download/folder/trash) always present.

**C4.5 — Download success.** Given clicking download on `otelcol-otlp` (title "download
and verify otelcol-otlp"), state cycles "downloading… 17%" → "34%" → "51%" → "68%" → "85%"
→ 100%, one step every **260ms** (confirmed in source: `pct += 17` every 260ms — six steps,
~1.56s total, matching the README's "~1.6s in ~17% steps"), landing on "installed".

**C4.6 — Download failure + retry.** Given clicking download on `otelcol-k8s`, the same
progress sequence runs, then lands on exactly "download failed · checksum mismatch" (err
colour). The download icon's title becomes "try again"; clicking it re-runs the identical
progress sequence and **fails identically again** — this pairing (`otelcol-k8s` always
fails, `otelcol-otlp` always succeeds) is fixed per-binary in the seed data, not
probabilistic.

**C4.7 — Add collector row.** Given the last table row, fields are "+", name (placeholder
"name"), path (placeholder e.g. "/usr/local/bin/otelcol-mine"), "add". **The play, folder,
and trash icons on the collector table are stubs** — source: `use: () => {}`,
`path: () => {}`, `remove: () => {}` for every collector row. Flag for v3: these three
actions are unwired placeholders in the prototype, not confirmed end-to-end behaviour.

---

## C5. Menu bar

**C5.1 — Status line.** Given running, line one reads exactly "Running · `<config>` ·
`<preset>`" then "`<ports>` · `<N> warnings`" (e.g. ":4317 :4318 · 2 warnings" — this count
is the log's warn-level line count specifically, confirmed 2 via the C3.5 filter, distinct
from the Configurations sidebar's "3 warn" badge which sums warn **and** error lines
together and always labels the total "warn" — a minor label looseness worth deciding
explicitly for v3, not a functional bug). Given stopped, line one reads exactly "Stopped"
with second line "no listeners".

**C5.2 — Ordering: recent-first, then alphabetical.** Given the CONFIGURATION section,
items are ordered by a `recent` list (ids in most-recently-**activated** order, updated
only by a successful `activate()` call — moves the activated id to the front); any config
never yet activated this session falls after all recent ones, and those fall-through
configs are sorted alphabetically among themselves (confirmed in source:
`byRecent.sort(...)`). Creating a new config does **not** touch `recent` — new,
never-activated configs surface only via this alphabetical fallback bucket, not at the
front, even though they are the newest thing on the machine.

**C5.3 — Top 10 + More…** Given more than 10 configs, only the first 10 of the ordered
list show inline; the remainder appear under "More…", themselves re-sorted alphabetically
(confirmed: with 14 configs, the 4 alphabetically-last, not-recently-activated ones landed
under "More…").

**C5.4 — Single-preset activates on click; multi-preset opens a submenu.** Given a
config with one preset, clicking its menu row activates it directly. Given a config with
2+ presets (only `otlp-to-bronto` in the seed data), clicking opens a submenu of its
presets (chevron-right shown); clicking a preset row is itself the activation (submenu
closes, `activate(configId, preset)` fires) — there is no separate "confirm" step,
matching README's "one list, not two pickers."

**C5.5 — Active marking.** Given the running config/preset, its menu row/submenu-row is
rendered amber with a check icon (Lucide `check`, not a literal "✓" character — the
README's ASCII art is illustrative of an icon, not literal text).

**C5.6 — Restart collector / Open compy / Quit.** Present, Title Case, in that order,
below a divider — per README's native-menu convention (all other UI copy is lowercase).

---

## C6. Cross-cutting

**C6.1 — Animations, exact values (confirmed in source CSS, not just visually).**
```css
@keyframes compyPulse { 0%,100% { opacity: 1 } 50% { opacity: .4 } }   /* 1.1s ease-in-out infinite */
@keyframes compyBar   { 0% { transform: translateX(-100%) } 100% { transform: translateX(320%) } }  /* 1.2s linear infinite */
```
These are the **only** two animations anywhere in the prototype — no other transitions
exist (confirmed: no other `@keyframes` or `transition:` blocks drive state changes).

**C6.2 — Slow-action timings (confirmed in source, milliseconds).**
| action | ms | notes |
|---|---|---|
| activate (normal) | 2300 | |
| activate (ebpf-profiles) | 3400 | only config with a distinct timing |
| restart | 2200 | |
| save | 1600 | both the fail and the success path |
| URL fetch (new config) | 1500 | |
| download, per step | 260 | steps of 17% each, ~6 steps to completion |
| note auto-clear | 2600 / 3000 / 3200 | varies by which note (copy=2600, duplicate-preset-name=3000, reset/fetch=3200) — all "~3s" per README, not a single constant |

**C6.3 — Themes.** Both dark (default) and light themes are live and swap via CSS custom
properties (confirmed visually: light theme renders cream/paper background `#FDFCFA`-class
tones, amber accent, gold toggle knob, matching the README's colour table — light is not a
naive inversion, exactly as documented). Theme choice persists in `localStorage` under
`compy.theme` and, on "system", the note text updates to reflect the OS setting.

**C6.4 — Disabled affordances.** Confirmed pattern holds everywhere checked (row actions,
preset delete, download-blocked rows): disabled icons are never removed, cursor stays
default (not `not-allowed` or hidden), and a `title` attribute explains why.

**C6.5 — Destructive confirmations are inline, not modal.** Confirmed for: delete config
(C1.19), reset config (C1.20), discard & re-sync config (C1.21). **Not** used for preset
deletion (C2.11) — that action is either blocked (disabled icon) or executes immediately,
no confirmation row, which is a real behavioural distinction worth keeping (or
deliberately overriding) in v3, since README's "Destructive confirmations" language
appears under the Configurations-screen section only and does not claim to cover presets.

---

## Prototype↔README disagreements

**D1 — Nothing-active headline text.** README (section 1, "Empty / first-run") states the
headline is **"nothing is running yet"**. The prototype renders **"nothing active"**
(confirmed both live and in source, line ~121). Behaviour wins: implement "nothing
active". The secondary sentence and primary action match the README exactly.

**D2 — "3 warn" badge semantics.** The README doesn't specify this badge's counting rule.
The prototype's sidebar/nav badge ("3 warn") sums warn-level **and** error-level log lines
and always labels the sum "warn" (source: `issueCount = warn-or-error count`). The menu
bar's own "N warnings" text (C5.1) counts warn-level lines only (2). These two numbers can
legitimately differ for the same log (as observed: 3 vs 2). Not a bug so much as an
underspecified label — v3 should pick one counting rule and word the label to match it.

---

## Not verifiable in prototype

**N1 — Two actions are unwired stubs in this build, not just automation misses (confirmed
by reading source, not by inference):**
- "sync all" (Configurations header) — `syncAll: () => {}`.
- "copy diagnostic" (activation-failure panel and save-failure panel) — `copyDiag: () =>
  {}`.
- The editor header's own re-sync/reset button — `resync: () => {}` (the row-level
  equivalent on the Configurations table is fully wired and was verified, see C1.20/C1.21).
- Settings collector-table play/folder/trash icons — `use`, `path`, `remove` are all
  `() => {}`.
- Settings "set OTEL_* variables system-wide" toggle — `flip: () => {}`; its rendered
  on/off state is a hardcoded `true`, not read from any real preference.

These should not be read as "this feature doesn't need to exist" — they're clearly
intended (titles, layout, and hover states are fully built) but left as no-ops in this
design-reference build. v3 needs real behaviour for all five.

**N2 — Clipboard-dependent confirmations.** The log-copy note ("`<N>` log lines copied",
C3.7) is wired in source (`copyLog`) but could not be independently re-triggered via
synthetic click in headless automation — most likely the sandboxed iframe
(`sandbox="allow-scripts"`, no `allow-same-origin`) denies `navigator.clipboard`, and/or
the synthetic click didn't land precisely enough on the icon. The note text and 2.6s
duration are taken from source, not from an observed toast.

**N3 — Find-field empty-result copy.** `no configuration matches "x"` is documented in the
README and the find field itself was confirmed present and live-filtering, but a
zero-result query was not separately driven to visually confirm this exact string appears
(it is a plausible, simply-interpolated template given the pattern of every other message
found, but wasn't independently re-observed rendering on screen in this pass).

**N4 — Menu-bar "Restart collector" click-through.** The item's presence, ordering, and
Title Case are confirmed; actually clicking it to observe the simulated menu bar's window
sync in real time was not separately exercised (Collector screen's own restart, C3.11, was
exercised instead and is the authoritative timing source for the identical action).
