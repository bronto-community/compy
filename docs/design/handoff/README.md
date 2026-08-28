# Handoff: compy — five surfaces

## Overview

compy is a macOS menu-bar app (plus window and CLI) that runs a local OpenTelemetry
Collector for a developer. Apps on the machine send telemetry to compy; compy forwards
it wherever the active configuration says. This handoff covers the redesign of all five
surfaces: Configurations (home), Configuration editor, Collector, Settings, and the
menu bar.

Design decisions in here were made against a written product brief and iterated with the
product owner. Several of the brief's "product truths" were deliberately changed — see
**Departures from the brief** below. Those are decisions, not oversights.

## About the design files

`compy.dc.html` in this folder is a **design reference written in HTML**, not production
code. It is a working prototype: real state, real latencies, real failure paths, so
behaviour can be verified by clicking rather than by reading. Do **not** port the HTML.

The task is to recreate these designs in compy's own environment (SwiftUI / AppKit for a
native macOS app, or whatever the existing codebase uses), following that codebase's
established patterns, components and idioms. Where this document and the prototype
disagree, the prototype wins for behaviour and this document wins for intent.

Open it in a browser. Everything described below is clickable.

## Fidelity

**High-fidelity.** Final colours, typography, spacing, iconography, copy and interaction
behaviour. Recreate faithfully. Two caveats:

- The YAML pane is a static render, not a working code editor. Syntax colouring and line
  numbers are illustrative of the intended treatment; use a real editor component.
- Health numbers, log lines, timestamps, pids and uptimes are fixed sample data.

## Design language

Dense utility. Monospace throughout (JetBrains Mono), IBM Plex Sans only for explanatory
sentences. Tables over cards. Quiet text and icon actions rather than buttons. One accent
colour, reserved for the single active thing — never used decoratively.

Palette is Bronto's brand palette. Both a dark and a light theme are defined; the app
follows macOS by default.

### Colour tokens

Every colour is a CSS custom property in the prototype so both themes stay in lockstep.
Reproduce as two theme dictionaries.

| token | dark | light | used for |
|---|---|---|---|
| desk | #131312 | #E2DECC | area behind the window |
| window | #1b1b1a | #FDFCFA | window body, input fills |
| raise | #212120 | #F1EFE7 | sidebar, presets band, row hover |
| panel | #262625 | #F8F7F3 | tiles, menus, cards, active row fill |
| logbg | #171716 | #F8F7F3 | log surface |
| divider | #2b2b28 | #E9E5D9 | row separators |
| border | #363631 | #D8D2C0 | default borders |
| border2 | #302f2b | #E2DECC | soft card borders |
| border3 | #4b4a44 | #C3BC9F | strong borders, focus |
| text | #F1EFE7 | #444441 | primary text |
| text2 | #E2DECC | #4E4C46 | menu items, code |
| text3 | #C3BC9F | #5C5A51 | URLs, secondary values |
| muted | #9E9782 | #6E6B5E | secondary text, quiet actions |
| dim | #857F6B | #857F6B | icon actions, hints |
| dim2 | #6C6759 | #7C7767 | placeholders, meta |
| faint | #57544A | #8A8474 | column headers, disabled icons |
| accent | #ECAA0D | #99753F | THE ACTIVE THING, only |
| accentLine | #99753F | #C9A45E | borders on accent controls |
| accentBg | #2E2A1A | #F5EEDD | accent control hover fill |
| accentHi | #FFC839 | #ECAA0D | link hover |
| ok | #53DFA9 | #17885B | running, valid |
| okBg | #0A3424 | #E4F7EE | success strip fill |
| err | #C1583B | #C1583B | failures, destructive |
| errText | #FC9F85 | #A6412A | failure headline |
| diag | #D2A392 | #7A3E2C | collector diagnostic body |
| errBg | #241715 | #FCEDE7 | failure panel fill |
| errLine | #4A241C | #E8BEAE | failure panel border |
| errDiv | #3A211A | #EFD6CA | failure panel divider |
| errHover | #2E1A15 | #F7E0D6 | destructive hover |
| info | #859EFF | #0129CC | variable names |
| string | #B3B310 | #17885B | YAML string values |
| warnText | #D8A21A | #6F5410 | log warn level |
| warnBg | #2A2617 | #FBF3DC | unlock confirmation fill |

Light mode is not an inversion: accent becomes Bronto Gold (Lemon Lime is invisible on
paper white), ok becomes Tropical Mint 60%, strings become green, variable names become
Sapphire 60%. The two smallest-type tokens (faint, dim2) are darkened in light mode to
clear 3:1 contrast at 10–11.5px.

### Typography

| role | font | size | notes |
|---|---|---|---|
| screen title | JetBrains Mono | 17px | lowercase |
| body / rows | JetBrains Mono | 13px | |
| secondary / actions | JetBrains Mono | 12px | |
| meta, state | JetBrains Mono | 11.5px | |
| column headers | JetBrains Mono | 10px | uppercase, letter-spacing .14em, faint |
| explanatory sentences | IBM Plex Sans | 12px / 11.5px | the only non-mono text |
| code / log | JetBrains Mono | 12.5px / 11.5px | line-height 1.85 / 1.75 |

All UI copy is lowercase except native menu-bar items, which follow macOS convention
(Title case: "Restart collector", "Open compy", "Quit").

### Spacing, radius, elevation

- Screen padding 20–24px; header padding 16–20px 24px.
- Table rows: 10–11px vertical padding, 47–48px effective row height.
- Card padding 8px 10px; grid gaps 8px; control gaps 8–14px.
- Radius: 4px controls, 5px cards, 6px panels/tiles, 8–10px window.
- Only elevation: dropdowns — `0 14px 30px -12px rgba(0,0,0,.65)`, submenus
  `0 16px 34px -14px rgba(0,0,0,.7)`, window `0 30px 70px -20px rgba(0,0,0,.85)`.
- Active row marker: `box-shadow: inset 2px 0 0 0 accent` plus panel fill.

### Iconography

Lucide (ISC), stroke 1.9, 12–14px, `currentColor`, round caps. Icons used: circle,
circle-dot, package, user, link, play (filled), pencil, copy, refresh-cw, undo, trash-2,
plus, chevron-down, chevron-right, check, download, folder, ban, search, list, activity,
sliders. Use the real Lucide package (or the platform equivalent) rather than the inlined
path data in the prototype.

Semantics that must hold:
- **circle-dot amber = running.** circle grey = not running.
- **package / user / link** = built in to compy / yours / fetched from a URL.
- **play** = activate. **pencil** = edit. **copy** = duplicate. **trash** = delete.
- **refresh-cw / undo** = return to source (re-sync from URL / reset to shipped).

## Vocabulary (settled — use these words everywhere)

| concept | word | notes |
|---|---|---|
| bundled with compy | **built in to compy** | shown as the package icon; the phrase appears in tooltips |
| created by the user | *no label* | the normal case earns no word |
| fetched from a URL | **the host itself** (`otel.acme.dev`) | shown with the link icon; never a category word like "remote" |
| named bundle of values | **preset** | never "variable set", "profile" |
| collector binary | **collector** | four known ones; states: installed / available to download / added by you / not available on macOS |
| user has edited a protected config | *no badge* | expressed only by the sync/reset icon being live |

Names become folder names and CLI arguments, so they are lowercase-digits-dashes.
Typing converts as you go ("My Collector" → `my-collector`) and the field says
"saved as my-collector". Collisions are rejected, not auto-suffixed.

## Departures from the brief (decisions, not omissions)

1. **No per-config collector binary.** The brief allows each configuration to pin a
   binary. Dropped. One collector, chosen once in Settings, runs everything. Rationale:
   removes a config×binary matrix nobody can hold in their head, and "which binary was
   this running under" stops being a question. Cost: comparing a config on `otelcol` vs
   `otelcol-contrib` means switching globally and back.
2. **No rollback / "restore last working setup".** The brief keeps the last
   provably-working configuration and restores it in one action. Dropped after review:
   "provably worked" cannot be defined without defining healthy, and a config can start
   cleanly and still be wrong. If it returns, the honest version is "go back to the setup
   you were on before this one" (config + preset), labelled with its target, one step
   only — not a history.
3. **Menu-bar collector switcher removed**, along with its Settings toggle. Choosing a
   binary is a settings-level decision, not a per-session one.

Two questions were left open on purpose: what "healthy" means beyond "the process
started", and whether editing a built-in should fork a copy instead of taking it over.

---

# Screens

## 1. Configurations (home)

**Purpose.** See what is running, switch in one gesture, manage the list.

**Layout.** Window 1240×838. Sidebar 214px fixed, content fills. Header (title, sentence,
find field, "sync all", "new configuration"), then optional strips (note / nothing-active
/ activation failure), then the table in a scroll area with 24px side padding.

**Table grid.** `62px minmax(170px,1fr) 330px 112px`, 14px column gap. Header row:
uppercase 10px faint labels — blank, name, preset, blank.

Row columns:

1. **Status + type**, two icons, 9px apart. circle-dot amber if running, circle dim
   otherwise (tooltip "running now" / "not running"); then package / user / link
   (tooltip "built in to compy" / "yours" / "fetched from otel.acme.dev").
2. **Name.** 13px, amber when running, otherwise text. Click opens the editor.
3. **Preset control.** A 148px-min bordered selector showing the selected preset with a
   chevron **only when the config has more than one**; then **play** (activate) and
   **pencil** (open the inline preset editor). While activating, the selector is followed
   by pulsing "restarting…" and a 44px indeterminate bar. The dropdown lists every
   preset, each with its own play and pencil, and ends with "+ add preset".
4. **Actions**, always all three, right-aligned, 4px apart: copy, refresh-cw/undo, trash.
   Greyed with an explaining tooltip when inapplicable — e.g. "yours from the start,
   nothing to return to", "this is the shipped version, nothing to reset", "can't delete
   the running config".

**Active row.** panel fill + `inset 2px 0 0 0 accent` + amber name and status icon.
Said once — there is no "running" text column, no badge.

**Sorting.** Alphabetical by name, everywhere in the window. (The menu bar sorts by
recency; see below.)

**Empty / first-run.** The true empty state cannot occur (fresh installs ship with
built-ins). The reachable one is *nothing active*: dashed strip, "nothing is running
yet", "no collector is running. activate a config and it starts.", plus one primary
action. Reach it by stopping the collector on the Collector screen.

**New configuration.** Inline strip, two fields only: name (with live slug note, red
"docker-stats already exists" on collision) and optional URL. With a URL: "fetching…"
for ~1.5s, then either a linked config appears with a quiet confirmation, or an inline
failure "404 · nothing at that URL. compy kept nothing."

**Activation failure.** Bordered panel above the table: err dot, "couldn't activate
ebpf-profiles", right-aligned reassurance "otlp-to-bronto still running", dismiss; then
the collector's own multi-line diagnostic in monospace `<pre>` with horizontal scroll,
then "open in editor" / "copy diagnostic". This is the home for collector text — never a
one-line strip.

**Destructive confirmations.** Single inline row, plain sentence + "keep it" + the verb
("delete", "discard & re-sync", "reset"). No modals.

**Find.** Field with search icon in the header, filters as you type; "no configuration
matches “x”" when empty.

## 2. Configuration editor

**Purpose.** Edit one configuration: presets and their values, its YAML, its origin.

**Header, one line.** status dot (if running) · type icon with the origin detail as
tooltip · name as an inline-editable field (17px, borderless until hover/focus; rename
rejects collisions) · for linked configs the URL as an inline field taking the row's free
space · right side: transient "asking the collector…" hint, a re-sync/reset button where
applicable, and **save**.

There is no second header line and no origin strip — that information lives on the type
icon, the URL field and the reset button.

**Presets band** (directly under the header, full width, raise fill):
- Row one: "presets" label, then one chip per preset. The selected chip carries an amber
  dot and its name is an **inline editable field** (dashed underline) — renaming happens
  here, there is no rename action. Every chip carries copy and trash icons. A dashed
  **+** chip at the end adds a preset, like a browser tab.
- Row two: value cards, **fixed 3 per row**, wrapping. Card: key name (bare —
  `BRONTO_KEY`, not `${env:BRONTO_KEY}`) with its description as tooltip, origin hint
  right ("line 17" / "default"), then the value input with a reveal/hide toggle for
  secrets (masked with bullets). Card ~65px tall; padding 8px 10px, gap 5px.
- Row three: a sentence only when something is wrong ("customer-x has no ingest key.
  activating with it will fail.").

Deleting a preset is blocked when it is the running one ("this preset is running.
activate another one first.") and when it is the last one ("a config always keeps one
preset"). Duplicate names are rejected.

**Save results** sit at screen level, directly under the presets band — visible whether
or not the YAML is expanded. Success: quiet one-line ok strip. Failure: err panel,
"the collector rejected this config. nothing was saved.", copy + dismiss, and the
diagnostic in a scrollable `<pre>`, max-height 220px.

**YAML.** For built-in and linked configs it is **collapsed by default** to a single
line: "config.yaml" + "ships with compy. most people never open it." / "kept in sync with
otel.acme.dev" + "show yaml". User-owned configs open expanded. When expanded: uppercase
"config.yaml" bar, read-only notice + "edit anyway" for protected configs, then a
gutter of line numbers and the highlighted document (keys info, string values string,
comments faint).

**Protection flow.** "edit anyway" → one inline confirmation ("editing disconnects this
from otel.acme.dev. it stops re-syncing." / "your version stays through compy updates.")
→ "make it mine". Reset restores protection and read-only.

## 3. Collector

**Purpose.** Is telemetry flowing, and what is the collector saying?

- **Header**: state dot (mint running / amber restarting / grey stopped), state word,
  "pid 48213 · up 26m" (or "no process"), then **restart** (becomes **start** when
  stopped) and **stop**.
- **Four tiles**, 1px-gap grid: configuration (amber), preset, collector (underlined,
  clicks through to Settings, tooltip "every config runs on this one"), listening. When
  stopped: preset "—", listening "not listening", both dimmed.
- **Health strip**: received / exported / queue / dropped, with
  `localhost:8888/metrics` named on the right so the source of the numbers is explicit.
  Dropped turns amber when non-zero. All dashes and "no metrics while stopped" when
  stopped. Deliberately four numbers, no chart.
- **Log toolbar**: filter field, level chips (all / error / warn / info / debug, amber dot
  on the active one), line count, a copy icon (copies the filtered lines, confirms with
  "14 log lines copied"), and the tail indicator ("live tail" mint / "paused" grey /
  "no output" when stopped).
- **Log**: three columns `82px 56px 1fr`, `white-space: pre`, `min-width: max-content`
  so long lines scroll horizontally rather than wrap. Level colours: error err,
  warn warnText, info info, debug dim2.

## 4. Settings

Two sections, **app first**:

- **app** — appearance (segmented system / dark / light, system default, remembered,
  follows macOS live) and "set OTEL_* variables system-wide". A sentence notes that ports
  and shell wiring live in the CLI (`compy env`).
- **collector** — "one collector runs every configuration. compy ships with contrib; add
  others if you need them." Table shaped exactly like the configurations table:
  `54px minmax(170px,1fr) 1fr 132px`; status icon (circle-dot in use / circle installed /
  download available / ban blocked), name with its path as tooltip, one short state
  phrase, then four always-present icons — play ("run every config on otelcol"), download,
  folder (change path), trash. The in-use row carries the same amber edge and fill.
  Downloading shows "downloading… 51%" with a 2px progress bar; failure shows
  "download failed · checksum mismatch" in err with the download icon acting as retry.
  `ebpf-profiler` shows "not available on macOS" with the reason in its tooltip — never a
  dead row. Last row on the same grid: + , name field, path field, "add".

## 5. Menu bar

Native macOS menu, so this is ordering and wording:

```
● Running · otlp-to-bronto · staging
  :4317 :4318 · 2 warnings
  ────────
  CONFIGURATION
  ✓ otlp-to-bronto              ›     (submenu only when >1 preset)
    local-debug
    acme-standard               ›
    … up to 10, most recently used first
    More…                       ›     (the rest, alphabetical)
  ────────
  Restart collector
  ────────
  Open compy
  Quit
```

One list, not two pickers: a configuration with several presets opens a submenu and
**picking a preset is the activation**, so nothing can be half-chosen. Single-preset
configs activate on click. Ten most recent, then More… . Status line always names state,
config and preset. "Open compy" focuses the single existing window (never spawns one).

# Interactions & behaviour

**Slow actions** (activate, restart, save) take 1–10s and can fail. Everything else is
instant. Prototype timings: activate 2.3s (ebpf failure path 3.4s), restart 2.2s, save
1.6s, URL fetch 1.5s, download ~1.6s in ~17% steps.

**During activation**: the row shows pulsing "restarting…" plus an indeterminate bar
(`compyBar`, 1.2s linear, translateX(-100% → 320%)); the sidebar status reads
"restarting…" with an amber dot; further activations are ignored until it settles.

**On failure**: the previously active configuration keeps running and the panel says so.
Nothing is saved on a rejected save.

**Animations** are only these two: `compyPulse` (opacity 1 → .4 → 1, 1.1s ease-in-out
infinite) on in-flight labels, and `compyBar`. No transitions elsewhere.

**Hover** is a background lift to raise/panel for rows and menu items, and a colour lift
(dim → text, or → err for destructive) for icon actions. Focus on inputs is a border3
border plus a fill change.

**Disabled** never hides an action: the icon greys to faint, the cursor stays default,
and the tooltip explains why.

# State

```
theme            'system' | 'dark' | 'light'   persisted (localStorage in the prototype)
screen           'configs' | 'editor' | 'collector' | 'settings'
activeId         config id currently running
activePreset     preset name currently running
nothingActive    collector stopped
busyId           config id being activated
restarting       collector restart in flight
err / errName    activation failure diagnostic + subject
recent           config ids, most recent first (menu ordering)
presetSel        { configId: presetName }   per-row selection, independent of what runs
presetsOpenId    which row's preset dropdown is open
menubarId        which menu item's submenu is open ('__more' for More…)
inline           { id, preset, isNew } | null  inline preset editor
inlineName       draft preset name
editId           config open in the editor
name             draft config name
unlocked         protection lifted for the open config
unlockAsk        unlock confirmation showing
yamlOpen         YAML expanded for a protected config
saving / valErr / valOk        save in flight + result
preset           preset selected in the editor
values           { presetName: { KEY: value } }
reveal           { KEY: bool }  secret visibility
query / level / tail           log filter, level, tail state
find             configuration search
newOpen / newName / newUrl / newErr / fetching   new-configuration form
confirm / confirmVerb / confirmId                destructive confirmation
dl               { binaryName: { status, pct } } download progress
note             transient one-line confirmation, auto-clears after ~3s
```

Validation rules: names slugged to `[a-z0-9-]`; duplicate config names rejected;
duplicate preset names rejected; last preset undeletable; running preset undeletable;
running config undeletable; URL fetch failure creates nothing.

# Assets

No image assets. Icons are Lucide (ISC licence) — use the real package. Fonts are
JetBrains Mono and IBM Plex Sans (both open-licensed); substitute the codebase's mono and
sans if it already standardises on some.

# Files

- `compy.dc.html` — the complete interactive prototype: all five surfaces, both themes,
  every state described above. Open in any browser.

## Things worth clicking in the prototype

- Activate **ebpf-profiles** — platform failure with the collector's diagnostic.
- Activate **otlp-to-bronto · customer-x** — missing `BRONTO_KEY` failure.
- **save** in the editor twice — first the collector's complaint, then the quiet success.
- **stop** on the Collector screen — reaches the nothing-active first-run state.
- Create a config with a URL that is not `otel.acme.dev` — the 404 path.
- Download **otelcol-k8s** in Settings — progress, then checksum failure and retry.
- The **⟳ / undo** icon on `otlp-to-bronto` — reset to the shipped version.
- The menu bar's **More…** — overflow past ten.

## amendments (2026-08-26 feedback)

Design-owner review of the live v3 UI; these rulings win where they conflict
with the text above.

- A config with no presets shows **default** (muted) everywhere a preset is
  named — preset selector, sidebar status, collector tile, menu-bar status —
  never "—": it is the implicit default preset, activating with empty values.
- The configs-row icon next to play is a **plus** (add preset, opens the
  inline editor in new-preset mode), not a pencil — the pencil read as
  "edit the config". Per-preset pencils stay inside the dropdown.
- The whole config **row background opens the editor** (cursor: pointer);
  play, plus, selector, actions and the inline editor keep their own actions.
- A short dismissible **getting-started strip** sits above the configs table
  (pick a shipped config → add a preset via the plus → play; "new
  configuration" for your own). Dismissal is remembered in localStorage.
- **Disabled controls are unmistakably muted**: every disabled button-ish
  control fades (opacity .45), default cursor, no hover lift — the
  explaining tooltip stays.
- **Ports honesty**: compy only injects `COMPY_GRPC_PORT`/`COMPY_HTTP_PORT`.
  The sidebar, collector "listening" tile and menu-bar line claim the
  settings ports only for the ports the active config's YAML references;
  a partial reference shows just that port (":14317 grpc"), none shows
  "ports per config.yaml" (muted).
- **cmd/ctrl+S saves** in the editor (same save-and-validate as the button);
  elsewhere it is swallowed (preventDefault) and does nothing. No other
  shortcuts.
- The active row **says "running"** — a small pulsing accent word
  (compyPulse) after the name, rhyming with the sidebar status — on top of
  the amber name/dot. Stopped-but-active stays dimmed and wordless.
- Settings gains an **otlp ports row** (grpc + http) in the app section —
  the home of the global `COMPY_*` ports, which stay excluded from preset
  value cards. Nothing re-applies automatically: after a save the row says
  the new ports apply when the collector next restarts and offers the
  restart action. The "ports live in the CLI" sentence is now "shell wiring
  lives in the CLI".
- The **menu-bar configuration list is alphabetical** (case-insensitive),
  More… simply continuing it past ten — recency no longer orders the menu
  (supersedes the v4 "ten most recent first" rule; `recent` stays in
  status/API).
- The getting-started strip is a real **help affordance** (round 2): a
  circle-help icon leads it, and a help icon button in the configs header
  (title "help") reopens it when dismissed (or toggles it closed).
  Dismissal is still remembered in localStorage; the button un-dismisses.
  The configs subtitle "activating restarts the collector" is deleted —
  that fact lives in the strip's copy ("…press play — activating restarts
  the collector.").
- The otlp ports row is replaced by a **global variables section** in
  Settings, between app and collector (round 2): preset-style value cards
  (the editor's card grid) for `COMPY_GRPC_PORT` and `COMPY_HTTP_PORT`,
  subtitled with why they are global — available in every configuration's
  yaml, so not part of presets. The honest applies-on-next-restart note
  (with the restart action while running) stays. The grid is sized to take
  more cards later; user-defined variables are not built yet.
- **Real listening-port detection replaces the port guessing** (round 3):
  compy shows only the ports the collector process is ACTUALLY listening
  on, detected OS-side (launchd's pid + lsof), never claims derived from
  settings or YAML — "ports per config.yaml" and the `COMPY_*_PORT`
  reference-sniffing are deleted everywhere. Undetectable (lsof absent or
  failing, process gone) means no claim shown, never an error. Sidebar and
  menu-bar line show up to 4 ports as ":6000 :6001 :8888", more as
  "N ports open", nothing detected omits the segment; the sidebar also
  drops the distro name (it lives in settings). The collector "listening"
  tile shows the full detected list, labeling what we know — settings
  grpc/http ports and the port the health scrape actually answered on
  (`telemetry`) — unknown ports bare. Health tries :8888 first, then the
  detected ports; the strip names the port that answered.
- **The window opens at the design size** (round 4): `compy window` defaults
  to 1240×838 (was 960×680) and stays resizable; every screen must hold
  without collision or sideways scrolling down to ~900px wide.
- **The log message column wraps** (round 4 — supersedes the Collector
  section's `min-width: max-content` rule): time and level stay fixed-width
  and aligned, the message wraps within its column and long tokens (URLs,
  JSON blobs) break rather than stretch the row. The log never scrolls
  sideways at any width.
- Narrow-width fixes behind the 960×680 report (round 4): the settings
  pane's cards no longer flex-shrink — a short window used to compress them,
  and `overflow: hidden` clipped the table's last-row border, leaving the
  trash icon on the clipped corner and cutting the OTEL_* toggle row in
  half; the configs preset column yields 330px → 238px min at narrow widths
  so rows never overflow the pane sideways.
- **The log represents otelcol's structure** (round 5): zap console lines
  (`ts \t level \t [caller \t] message [\t {json}]` — the caller and the
  JSON tail are both optional in real output) parse client-side. The
  message cell shows the message text followed by the structured attrs as
  dimmed `key=value` pairs (top level flattened, nested values as compact
  JSON), each pair wrapping as a unit; the caller
  (`service@…/file.go:123`) is a row tooltip, not an inline cell — it
  earns no space at this density. Lines with no timestamp/level (the
  debug exporter's multi-line dumps) are continuation rows of the entry
  above: empty time/level cells, indented, dimmed, internal whitespace
  preserved; a continuation line that is itself a `{…}` object (the
  dump's trailing attrs) renders as pairs. Filters work on whole entries
  — a level chip keeps a dump with its parent line, the text filter
  matches the full raw text, continuations included — copy still copies
  the raw lines, and a malformed JSON tail stays raw in the message.
  Unknown shapes always fall back to a plain raw row.
- **Settings ends in a factory reset** (round 6): a "danger" section at the
  very bottom — muted err styling (err border and destructive verb only, no
  red carnival) — with one action, "reset compy to factory settings", and a
  sentence naming what it deletes (all configurations, presets, downloaded
  collectors, logs, and settings — the shipped configs come back fresh). It
  arms the usual inline confirm, hardened for wholesale data loss: the
  destructive verb ("reset compy") stays disabled until "compy" is typed
  into an inline field. Success reloads the client's entire state and notes
  "compy was reset". The reset uninstalls the collector job and wipes the
  state dir's contents (never the dir itself, never the tray's own agent);
  the CLI twin is `compy factory-reset`, which refuses without `--yes`.
- **The menu bar shows the designed icon, not the "compy" title** (round 7):
  the "track + signals" template icon from the icon handoff (vendored with
  its spec under `internal/tray/icons/`), icon-only per macOS convention,
  tooltip kept. State is shape, as the handoff requires: solid = running,
  hollow = stopped, dots collapsed into one heavy mark = running with
  errors in the log tail (the same error count `LogStats` already reports);
  shipped as per-state `.icns` (16 + 16@2x black-on-transparent rasters)
  via `systray.SetTemplateIcon`, switched only when the state changes.
- **The standalone window carries the app icon and name** (round 8): the
  designed app icon (dark variant, `packaging/macos/`, spec in its
  HANDOFF.md) rasterized to the full Apple set as `compy.icns`, and
  `packaging/macos/make-app.sh` assembles a `compy.app` next to the compy
  binary — `open compy.app` opens the window, the app menu says compy, the
  Dock shows the dino. The tray spawns the window through the bundle when
  it is present. The light appicon variant is vendored source-only
  (asset-catalogue light/dark variants need Xcode tooling). The Dock never
  shows the menu-bar glyph, nor the menu bar the app icon, per the handoff.
- **Menu rows show three-state indicator icons, not the checkmark** (round
  9): during an activation swap the old "checkmark stays + '— Activating…'
  title suffix" mechanic is rejected. Config rows AND preset submenu items
  carry per-item template icons (`internal/tray/icons/item-*`, drawn in the
  menu-bar glyph's family): a filled dot = active (what is RUNNING — the
  checkmark's old honesty rule), a down chevron = going down (the running
  side of an in-flight swap, stop included), an up chevron = going up (the
  activating side; start and restart show up only). Both sides of a swap
  show their transition at click time; the end-of-action sync repaints
  launchd truth — success or failure alike. A same-config preset swap
  transitions only the presets; the row keeps its active dot. The native
  Check()/Uncheck() and the title suffix are gone; stopped still shows no
  icons anywhere, and a transparent blank icon stands in for "none" (systray
  cannot clear a set icon) which also keeps titles aligned.
- **contrib is the out-of-the-box collector; ebpf-profiler is gone** (owner
  ruling, round 10): an empty `settings.Distro` now means contrib — the
  first operation that needs a collector binary downloads it automatically
  (checksum-verified, ~90MB, same progress machinery), so the quickstart's
  `compy distro use core` step no longer exists and the settings table
  shows contrib as "in use" from first launch. An explicitly selected
  distro is untouched. ebpf-profiler is removed entirely (no upstream
  binaries, cannot run on macOS) — the handoff's permanent "not available
  on macOS" ban row no longer appears; the ban treatment remains, generic,
  for any definition without a build for the running platform (e.g. Intel
  macs). `/api/distros` rows carry an optional `download` field so the
  settings screen's 3s refresh shows the progress bar for a download it
  did not start (an activation's auto-fetch).
- **Five OTEL_* vars, and the shell alternatives on the settings page**
  (owner rulings, 2026-08-27): the exposed env set grows to five —
  `OTEL_TRACES_EXPORTER` / `OTEL_METRICS_EXPORTER` / `OTEL_LOGS_EXPORTER`,
  all pinned to `otlp`, join endpoint+protocol (some zero-code agents
  default logs to "none"; a process's own env always shadows). Every
  surface (env, run, OS-level set/unset, reboot reapply, port-change
  refresh) derives from the one Vars function. Under the OS-env toggle a
  compact guidance block offers the shell-side alternatives — three ways,
  one line each, click-to-copy per the log-toolbar idiom: eval now, append
  to the shell rc, `compy run -- <cmd>` — closing with "an app's own
  environment always wins over the system-wide toggle". The app-section
  subtitle ("shell wiring lives in the CLI") folded into it.
- **Configurable advertised protocol** (owner ruling, 2026-08-27): the
  advertised OTLP protocol becomes a setting — `grpc` | `http/protobuf` |
  `http/json`, default unchanged (http/protobuf; empty in settings.json
  means the default). Both http flavors keep the endpoint on the HTTP
  port (one OTLP/HTTP receiver serves both); grpc points it at the gRPC
  port, still in `http://host:port` form — the OTLP exporter spec's own
  default shape, whose http scheme already means plaintext for gRPC, so
  `OTEL_EXPORTER_OTLP_INSECURE` is deliberately not set and the key set
  stays identical across protocols (locked by a test: that is what makes
  the OS-env refresh on a protocol switch stale-key-proof). The
  conformance verdict rides the port the advertised endpoint actually
  uses (grpc → gRPC port primary, the HTTP port missing becomes the soft
  addendum, and a grpc-only config can adopt); switching is
  advertisement-only — nothing restarts. Surfaces: settings app card gets
  a three-segment protocol row (appearance idiom), `compy settings set
  --protocol`, GET/PUT /api/settings, and `compy status`'s endpoint line.
- **Help on every screen, and a copy pass** (owner ruling, 2026-08-27):
  the configurations page's help idiom (dismissible strip, header help
  button to bring it back, dismissal in localStorage) extends to the
  collector, settings, and editor screens, one storage key per page.
  Each strip is two to three fact-dense lowercase sentences: the
  collector's says the numbers are the collector's own and restart/stop
  live there; settings names what the page holds down to the danger
  area; the editor's defines a configuration, states edit-protection in
  one sentence, and notes cmd+s. In the same round every user-facing
  string was reviewed against a prose standard: em dashes joining full
  clauses in flowing sentences (help copy, sans explainers, error
  sentences) became periods, commas, or colons; structural separators in
  status lines and state labels ("· ", "not built — build.sh") are
  design tokens and stay.
- **The nothing-active strip goes quiet** (owner ruling, 2026-08-27 —
  supersedes ruling D1, the prototype's boxed nothing-active strip): when
  nothing runs, the configurations screen shows one muted sentence above
  the table — "nothing active. press play on a config to start the
  collector." — no box, no dashed border, no suggestion button. A
  deliberately stopped collector is a state, not a nag; the sidebar's
  stopped card already carries it. The `.empty` idiom is gone.
- **Activation pre-flight for missing required values** (owner ruling,
  2026-08-27): a required variable is one whose yaml reference has no
  `:-fallback` (`has_default` false), is not COMPY_-prefixed, and has no
  non-empty value in the preset the activation would use. Every
  activation entry point on the configurations screen (play button,
  preset-menu play; the editor has no activate path) checks first: if
  anything is missing, an inline panel under the row — the
  inline-editor idiom, never a dialog — says "<config> needs <VARS>
  before it can send anywhere. add a preset with values, or activate
  anyway.", with "add values" (opens the inline preset editor — a new
  preset if none exist, else the effective one) and "activate anyway"
  (proceeds exactly as before); Escape/cancel collapses. Nothing missing
  → zero friction. The CLI warns and proceeds (`warning: no value for X
  (no default in the yaml)` per var, on stderr, no gating flag); the
  tray is unchanged (native menus can't host the flow; failure
  surfacing covers it). The rule is shared: `cfgstore.MissingRequired`
  and the window's `missingRequired` implement the same selection, and
  the editor's static missing-value warning now reuses it.
- **Help strips are opt-in, one slot everywhere** (owner ruling,
  2026-08-27 — supersedes the shown-by-default help ruling from the copy
  round): no help strip renders until the header's question-mark button
  opens it; the button or the ✕ closes it. Open state is in-memory only
  (a reload starts closed — simpler than persisting, and the strips are
  reference material now, not onboarding), and the old
  `compy.helpDismissed*` localStorage keys are ignored. The strip's slot
  is identical on all four screens: directly under the page's header
  line, above the content — which moved the collector screen's tiles
  out of its header block and the settings strip below the "app" title.

- 2026-08-28 (empty values never exported): a preset value saved empty (or
  whitespace-only) is omitted from the collector's environment — an
  exported-but-empty variable would defeat the yaml's own `${env:VAR:-default}`
  fallback, so a partially filled preset now lets the defaults fire instead of
  failing validation. The value card's "default" origin hint carries a tooltip
  when its value is empty — "empty — the yaml default applies" — no other copy
  or layout change.
- 2026-08-28 (upgrade smoothing): after a brew upgrade replaces the
  Caskroom, the app surfaces one line — "compy was upgraded — restart
  the collector to run the new version" — in the sidebar, settings, CLI
  status, and the tray's warnings segment ("restart needed"); the
  restart action heals the baked path. The cask's postflight relaunches
  the tray; the collector's restart stays the user's call.
