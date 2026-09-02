# Config templates — design note

Owner-ruled design, settled 2026-08-30 in conversation. This note is the
build brief. Vendor facts referenced here come from the OTLP vendor
landscape research (kept local, not committed — owner ruling 2026-08-30).

## The three-tier ladder

1. **Plain config pasted** — runs. No concepts.
2. **Config with `${env:VAR}` references pasted** — the existing
   machinery lights up OOTB: value cards from the vars parser,
   comment-derived descriptions, presets, pre-flight, drop diagnosis.
   Compatibility tier; already fully shipped.
3. **Template-born** — a creation form renders a template ONCE into
   plain collector YAML. Values bake; only secrets survive as
   `${env:KEY}` references (with emitted trailing comments, so tier 2's
   comment parser gives the cards names and descriptions for free).

**Invariant that future rounds must not break: tier N output is always
valid tier N−1 input.** A rendered config is indistinguishable from a
well-written pasted one; deleting a template strands nothing.

## What exists (one template, no vendor files)

- ONE catalog template, "custom endpoints": N OTLP backends plus the
  recommended local pipeline. Vendor knowledge lives in the research
  DOC (linked from the form's helper text), never in a vendor data
  file — rot lands on documentation, not on shipped behavior.
- No runtime rendering. No binding modes in the schema. The single
  special rule: `type: secret` fields render as env references and
  become preset cards; everything else bakes as literals.
- Presets thereby keep their honest core job (keys per environment,
  switched from the menu bar); an author MAY hand-write an
  env-with-default in the template body where a value is genuinely
  environmental — an authoring choice, not a schema concept.

## Template file format

`catalog/<name>.tmpl`: YAML front matter (schema) + `---` + Go
text/template body.

Schema:
- `name`, `description` — the catalog entry.
- `sections`: named groups, `label`, optional `collapsed: true`.
- `fields`: ordered map — **declaration order IS form order**. Field:
  `type` (slug | url | string | choice | multi | toggle | secret),
  `label`, `description`, `default`, `options` (choice/multi),
  `optional`, `section`, `advanced` (per-repeat-row disclosure).
- `backends` (repeat group): `repeat: 1..8` + `fields` (same shape).
- **Advanced rule (lintable): a field may be `advanced`/in a collapsed
  section only if its default is correct for the common case.**
  Collapsed must mean safely ignorable.

Template body:
- Go text/template, **boring rule**: `if` and `range` only. Anything
  needing logic is computed in Go and handed to the template as a flat
  value (`needs_delta`, `metrics_groups` — backends grouped by
  temporality into split metrics pipelines — `procs`, `exps` helpers:
  canonical processor order, per-signal exporter lists incl. the debug
  tee). Helper funcs: `upper`, `slug`.
- Secrets render as `${env:<UPPER name>_API_KEY}  # <desc>` — the
  trailing comment feeds tier 2's cards.
- The reference template ships these fields: per backend — name,
  endpoint, auth header, api key (primary); auth scheme prefix
  (choice: none/Bearer/Basic/Api-Token/ApiKey — the observed zoo),
  extra header+value, signals multi, temporality choice (as-is /
  to-delta / to-cumulative) as advanced. Config-level, all in a
  collapsed "pipeline options" section: memory limiter (on), batching
  (on, sized under the 1MB portable cap), host/env detection (on,
  `override: false`), offline queue (off; file_storage + persistent
  sending_queue + retry forever), local debug tee (off). No sampling,
  ever, per the research canon.

## Lifecycle

- Create: new configuration → "custom endpoints" → form → render →
  `cfgstore.Create` (plain YAML, pristine hash, default preset).
  `meta.json` gains `template` (name) + `knobs` (the chosen values,
  secrets excluded — they never had values at render time).
- **Change options**: template-born configs get a re-render action in
  the sync slot (third occupant of the source-refresh idiom, beside
  sync/reset). Semantics are resync's: unmodified re-renders cleanly;
  modified warns "your edits are lost". Re-render replaces YAML +
  updates knobs in meta; presets untouched.
- Hand-editing a template-born config just makes it tier 2 (modified,
  like any shipped/remote config). No lock-in, no template mode at
  runtime.

## Form UX

- Generated from the schema. Thirty-second path: name + per-backend
  {name, endpoint, auth header, api key} + create. Two disclosures:
  per-row "more options" and the collapsed "pipeline options" section
  (house collapsed idiom). Repeat rows: "+ add backend", ✕ per row.
- After create: land in the editor; preset cards already named and
  described; play → pre-flight collects the keys.
- Helper text links the research doc's vendor tables.

## Non-goals

- No vendor data file. No runtime rendering. No schema binding modes.
- No pongo2/Jinja unless authoring friction proves real (text/template
  is the Helm-familiar dialect; swap is mechanical if ever needed).
- No template features beyond the boring rule — a variant needing real
  logic becomes a separate catalog entry or a hand-managed config.

## Surfaces (parity law)

REST: list templates (schemas included), create-from-template with
knob values, re-render. CLI: `compy templates`, create `--template`
with a knobs file, re-render command. UI: the form + change-options.
openapi + drift test as always.

---

## Amendment: as shipped (backend round, 2026-08-30)

The backend half is live; the UI round only consumes. What shipped, where
it deviates, and the exact shapes:

### Module placement

`internal/catalog` (new leaf package: schema types, order-preserving
parse, knob validation, render; imports only `state`). `cfgstore` gained
the meta fields + two writes (`CreateFromTemplate`, `SetRendered`);
`app` orchestrates (`Templates`, `CreateFromTemplate`, `ReRender`,
`ReRenderForce`). Template files: `internal/catalog/catalog/<name>.tmpl`.

### One judged deviation: front matter is the JSON subset of YAML

compy is stdlib-only (no YAML parser exists in the module), so the
schema front matter is written as JSON — which IS valid YAML — and
fields are **arrays**, making declaration-order preservation free.
Everything else is per this note.

### Second deviation, forced by the collector: processor type names

v0.159 names them `resource_detection`, `cumulative_to_delta`,
`delta_to_cumulative` (verified against the built binary's
`components`). The note's `resourcedetection`/`cumulativetodelta`
spellings do not validate. Also: `resourcedetectionprocessor` was
missing from the manifest entirely — added; **run
`packaging/collector/build.sh` before the next rollout** (the repo-root
otelcol-compy predates the manifest change; renders with host/env
detection off validate against it, the full render was validated
against a fresh build).

### Surfaces

- REST: `GET /api/templates` (full schemas), `POST
  /api/configs/from-template` `{name, template, knobs}`, `POST
  /api/configs/{name}/re-render` `{knobs}` (refuses a hand-edited
  config, 400 "locally modified"), `POST
  /api/configs/{name}/re-render-force` (discards edits — resync's
  sibling-route shape). Omitted `knobs` = re-render with the stored
  ones. Schemas in api/openapi.json: `Template`, `TemplateField`,
  `Knobs`.
- CLI: `compy templates`; `compy config create <name> --template
  custom-endpoints --knobs file.json`; `compy config re-render <name>
  [--knobs file.json] [--discard-edits]`. (Resync is a CLI verb of its
  own; "re-re-render" isn't a word, so the discard flow is a flag.)
- Knob files are JSON objects (JSON ⊂ YAML).

### Shapes the UI consumes

`GET /api/templates` → array of:

```json
{"name": "custom-endpoints", "description": "…",
 "sections": [{"id": "backends", "label": "Backends"},
              {"id": "pipeline", "label": "Pipeline options", "collapsed": true}],
 "backends": {"min": 1, "max": 8, "fields": [
   {"name": "name", "type": "slug", "label": "Name", "description": "…"},
   {"name": "endpoint", "type": "url", "label": "Endpoint", "description": "…"},
   {"name": "auth_header", "type": "string", "label": "Auth header", "optional": true},
   {"name": "api_key", "type": "secret", "label": "API key", "description": "…"},
   {"name": "auth_scheme", "type": "choice", "options": ["none","Bearer","Basic","Api-Token","ApiKey"], "default": "none", "advanced": true},
   {"name": "extra_header", "type": "string", "optional": true, "default": "", "advanced": true},
   {"name": "extra_value", "type": "string", "optional": true, "default": "", "advanced": true},
   {"name": "signals", "type": "multi", "options": ["traces","metrics","logs"], "default": ["traces","metrics","logs"], "advanced": true},
   {"name": "temporality", "type": "choice", "options": ["as-is","to-delta","to-cumulative"], "default": "as-is", "advanced": true}]},
 "fields": [
   {"name": "memory_limiter", "type": "toggle", "default": true, "section": "pipeline"},
   {"name": "batch", "type": "toggle", "default": true, "section": "pipeline"},
   {"name": "resource_detection", "type": "toggle", "default": true, "section": "pipeline"},
   {"name": "offline_queue", "type": "toggle", "default": false, "section": "pipeline"},
   {"name": "debug_tee", "type": "toggle", "default": false, "section": "pipeline"}]}
```

(labels/descriptions elided here; the API carries them in full.)

Knobs (request AND what meta.json's `knobs` stores back, normalized,
secrets never present — the change-options form seeds from
`info.meta.knobs` and the config shows `provenance: "template"`):

```json
{"backends": [{"name": "honeycomb", "endpoint": "https://api.honeycomb.io",
               "auth_header": "x-honeycomb-team", "auth_scheme": "none",
               "extra_header": "", "extra_value": "",
               "signals": ["traces","metrics","logs"], "temporality": "as-is"}],
 "memory_limiter": true, "batch": true, "resource_detection": true,
 "offline_queue": false, "debug_tee": false}
```

Validation errors are 400s naming the field ("backends[0].endpoint:
required"). The advanced rule is a Go test
(`catalog.TestAdvancedRuleLint`); the golden render lives at
`internal/catalog/testdata/custom-endpoints-golden.yaml`.

### Semantics notes

- Provenance gained a fourth value: `"template"`. Copy demotes to
  local (template+knobs dropped); rename keeps template identity;
  Reset/Sync refuse template-born configs; hand-editing just flips
  `modified` (tier 2), exactly as for shipped/remote.
- meta.json records `pristine_sha256` for template-born configs too —
  it IS the shared modified-detection mechanism re-render's
  sync-semantics ride on. The YAML itself is byte-identical to the
  same YAML pasted (locked by test).
- The offline queue bakes `file_storage.directory` as a literal path
  under the state dir (`<COMPY_HOME>/storage`, `create_directory:
  true`) — an env ref would surface as a bogus required-var card.

---

## Amendment: UI round (as shipped, 2026-08-30)

The web UI half is live; built generically from the schema (no field name
from the template appears in app.js — a second catalog entry gets its
strip button and form for free).

- **Where things live**: the new-config strip lists one button per
  template (label/tooltip from the schema). Creation renders the form
  above the configs table; change-options renders the SAME form under its
  config's row (the inline-under-the-row idiom), seeded from
  `meta.knobs`. Pure logic (`seedKnobs`, `knobProblems`,
  `parseFieldErr`, template `originOf`) is in helpers.js under
  `node --test`.
- **Controls**: slug/url/string → inputs, choice → select, multi →
  checkbox row, toggle → the settings switch idiom; collapsed sections
  and per-row advanced fields use chevron disclosures. Secrets render as
  dashed, disabled placeholder cards ("collected in the preset after
  create" / "lives in the preset — unchanged here") — no secret value is
  ever collected in the form.
- **Errors**: light client checks and the server's field-naming 400s
  share one key space (`backends[0].endpoint`) and land field-adjacent;
  a group-level 400 (`backends: …`) lands on the section head; anything
  else uses the errbar. A re-render's "locally modified" 400 arms
  resync's discard confirm; its verb POSTs re-render-force.
- **Origin**: template rows/editors show lucide's layout-template glyph
  (vendored as `template`); the sync slot's third occupant is a sliders
  icon titled "change options and re-render", always live. The editor
  guards template yaml like shipped/remote (collapsed + unlock-ask with
  its own copy). A config whose template left the catalog gets a plain
  errbar sentence; the config keeps working (tier invariant).
- **One judged deviation**: the form's vendor-tables helper line IS the
  template's own `description` (which names the research doc's path) —
  a fixed UI-side sentence would hardcode one template's docs into every
  template's form.

---

## Amendment 3 — THE CORRECTED MODEL (owner-confirmed, supersedes the wizard framing above)

Templating is a property of the config source, available to users as
authors — tier 3 of the ladder, not a creation wizard and not a special
object. Anyone can WRITE a templated config, exactly as anyone can write
`${env:VAR}` and get cards. No detachment exists: editing is editing the
template source; the form is a second view of the same file.

- **Tier detection is textual**: a config whose source opens with the
  schema front matter (fields/sections, JSON-subset YAML) + `---` +
  template body is tier 3.
- **Storage**: the SOURCE lives at `configs/<name>/config.tmpl`; the
  rendered output is written to `configs/<name>/config.yaml` on every
  successful save — so the collector's `--config` path, the plist, and
  everything downstream stay untouched, and what runs is a real file.
  The pristine hash covers the SOURCE (it is the config); sync/remote
  transfer the source — publishing templated configs needs zero new
  machinery.
- **Save pipeline**: parse schema → apply stored knob values (defaults
  for new fields) → render → validate rendered (validate-or-restore,
  same honesty as YAML saves) → store source + rendered + knobs.
  Form and source feed the SAME save; both dirty → source applies
  first, then knobs.
- **Editor**: both views, both editable, always visible for tier 3 —
  form (from the source's own schema + meta knobs) above, source below.
  Validation failures show the rendered excerpt around the error
  (rendered→source line mapping deferred, recorded friction).
- **Cards/presets**: derived from the RENDERED yaml (that is what
  runs); the secret rule stands (type: secret → `${env:}` + comment).
- **The catalog entry is a starter**: creating from "custom endpoints"
  copies its SOURCE into the new config — immediately user-editable
  tier 3, nothing special about it afterward.
- **Boring-template rule** downgrades from law to authoring guidance
  for user templates; the provided helpers (procs, exps,
  metrics_groups, upper, slug) remain the vocabulary.
- Recorded frictions (accepted): two expansion syntaxes in one file;
  rendered-line error reporting; hand-written schema at tier 3; the
  dual-dirty save rule.
- Existing template-born configs from the wizard era hold rendered
  plain YAML — they simply ARE tier 1/2 configs; no migration.

---

## Amendment: tier-3 editor UI round (as shipped, 2026-08-30)

The web UI half of Amendment 3 is live; the wizard-era creation form and
the interim disabled change-options slot are gone.

- **The editor**: a has_template config shows both views, always — the
  form (generated client-side from the CONFIG'S OWN front matter via
  `parseSourceSchema`, values from `meta.knobs` through `seedKnobs`)
  above, the source in the surviving CodeMirror pane below, sharing one
  scroller (`.ed-scroll`; the pane grows with its text). No collapse, no
  unlock-ask, whatever the provenance. The pane is the plain-yaml pane's
  same slot, relabeled "config source"; yaml highlighting serves the
  source in v1 (the schema is the JSON subset, the body yaml-shaped) — a
  dedicated mode is future polish, not a gap.
- **One save**: either view dirty → the header button goes amber; the
  save routes by pane CONTENT exactly as the backend does
  (`isSourceText` mirrors `catalog.IsSource`): plain yaml → the yaml
  flow (a demotion when the config was templated — and a REJECTED
  demotion restores the source pair via `PUT …/source?validate=false`,
  since plain prev-yaml would have demoted it anyway); source and/or
  knobs → one `PUT …/source` (`source` only when the pane is dirty,
  `knobs` only when the form is — source-first is the server's order).
  After every successful save the config is reloaded and the form
  re-derived from the new schema; promotion and demotion are just what
  the reload shows.
- **Failures**: a 400 naming a form field lands field-adjacent (gated by
  `knownKnobPath` so a collector diagnostic that merely looks
  field-shaped stays in the panel). The panel's headline says WHO
  rejected: "the template source doesn't parse" (engine) vs "the
  rendered config was rejected" (render/collector). When the diagnostic
  names a line, the panel shows ±3 lines of the RENDERED yaml, labeled
  "in the rendered config" — from the stored render, since the server
  restored it (nothing-was-saved), so line numbers can drift by exactly
  the failed edit; rendered→source mapping stays deferred. The
  ?validate=false escape is offered whenever the source parses; it still
  parses/renders server-side, only the collector's verdict is skipped.
- **A schema that fails the loose client parse** quietly steps the form
  aside ("the schema doesn't parse — fix the front matter in the source
  below"); saving is never blocked on the client parse.
- **Creation**: the new-config strip's per-template buttons are name +
  create (`POST /api/configs/from-catalog`), disabled until the name is
  valid. The backend refuses knobs that leave required fields empty, so
  the client sends the schema's defaults plus neutral TYPE-derived
  placeholders (`placeholderKnobs`: required slug → "backend", url →
  "https://api.example.com", multi → all options) — no template field
  name appears in app.js; the user reshapes everything in the editor.
- **Rows**: the template glyph stays; the sync slot needs nothing for a
  local tier-3 config ("yours from the start") — remote+templated has
  remote provenance and syncs as any remote.
- Recorded frictions (accepted): the excerpt's line drift above; pasting
  a source whose schema has required repeat-group fields into a plain
  config 400s honestly ("backends: need 1 to 8 entries, got 0" — the
  stored knobs a plain config doesn't have), because a knob-less
  `PUT /yaml` can't invent values; creating from the catalog first is
  the path that seeds knobs.

---

## Amendment 4 — presets own everything (owner-confirmed final model)

Owner: "the user selects a config + preset and activates it, compy
renders it and runs it." From the user's perspective render-time vs
run-time binding does not exist; the options/presets split was
implementation leakage. Supersedes Amendment 3's knob storage.

- **A preset holds ALL of a config's values** — options, repeat groups,
  toggles, keys: one typed bag (`meta.presets[name]` becomes
  map[string]any). Tier-2 presets are the degenerate case (env values
  only). `meta.knobs` is deleted.
- **Activation = render(source, selected preset) → validate → run.**
  The rendered config.yaml is a derived artifact, regenerated at
  activation and at save (save keeps validate-or-restore +
  save-anyway). Switching presets may switch structure — activation
  restarts the collector anyway.
- **One invisible rule survives**: `type: secret` values travel via the
  environment, never baked into rendered yaml/snapshots/synced files.
  Internal rule, not a UI seam.
- **Editor**: one values surface — the form edits the SELECTED preset's
  bag (secret fields are real masked inputs in the form); the separate
  preset card band disappears for tier-3 configs; preset chips remain
  as the switcher. Source pane unchanged.
- Mental model, one line: **configs describe, presets fill in,
  activation runs.**

---

## Amendment 4 — as shipped (backend round, 2026-08-30)

The backend half of Amendment 4 is live; the editor UI round only
consumes. Shapes and judged calls:

- **Storage**: `meta.presets[name]` is `map[string]any`. Tier-2 bags hold
  env strings (the degenerate case); tier-3 bags hold typed schema values
  keyed by field name (`backends` an array of row objects), secrets
  included as strings. `meta.knobs` is gone: `cfgstore.EnsurePresets`
  merges it into every existing preset (preset values win — they are
  newer) and deletes it, idempotently, at `app.New`. A fresh tier-3
  config's `default` preset is seeded with the creation values, normalized
  (defaults filled).
- **Activation**: `app.Activate` re-renders (source, SELECTED preset's
  bag) into config.yaml before validating — same path, same plist; a
  rejected render/validation restores the previous yaml (nothing
  changed). `restorePrevious` replays the snapshot pair untouched.
- **Env split** (`catalog.SecretEnv`): a tier-3 activation's environment
  is ONLY the bag's secret values plus `COMPY_*`. Names derive as the
  vocabulary derives them: top-level secret F → `UPPER(F)`; row secret F
  in row named N → `UPPER(N_F)` (dashes → underscores) — so
  `${env:HONEYCOMB_API_KEY}` and the plist agree by construction (locked
  by test). `Render` strips secrets from its inputs: a body referencing
  one fails loudly instead of baking it.
- **Judged surface**: values travel through the EXISTING preset routes.
  `PUT /configs/{name}/presets/{preset}` accepts the typed bag and, for
  tier-3, validates schema-then-collector in a scratch file BEFORE
  storing (nothing-was-saved without a restore); `?validate=false` skips
  only the collector's verdict and answers `running_stale`.
  `PUT /source` keeps only `source` and reconciles EVERY preset's bag
  with the new schema (prune unknown, fill newly defaulted — lenient per
  preset; the strict answer lands at that preset's own next write or
  activation). Sync of a templated remote reconciles the same way, per
  preset.
- **Pre-flight**: `cfgstore.MissingRequired` (now root-aware) generalizes
  — tier 3 answers from the schema (required fields, secrets AND
  non-secrets, empty in the bag) as field paths (`backends[0].api_key`);
  tier 2 unchanged.
- **CLI**: `compy presets set` parses per the schema (toggles from
  true/false, `multi`/`backends` from JSON literals, string-shaped fields
  raw); `compy vars` on a tier-3 config prints the schema-field table per
  preset, secrets only ever as `(set)`/`-`.
- **Recorded frictions** (accepted): a source edit that adds a
  required-no-default field can't be saved atomically with its value
  (save the field with a default first, or set the value after — the
  source save no longer carries values); the embedded web UI's tier-3
  form still speaks the knobs shapes until the editor UI round lands
  (secret cards for tier-3 configs are the interim gap); demotion to
  plain yaml keeps the typed bag in place (tier-2 env export skips
  non-string values).

---

## Amendment 4 — editor UI round (as shipped, 2026-08-30)

The web UI half of Amendment 4 is live; the interim knobs-speaking form
and the tier-3 value-card band are gone.

- **The form edits the SELECTED preset's bag**: the presets are the
  form's switcher; switching re-seeds the form from the new preset's
  bag — row counts and all, per-preset structure is real. Switching
  with unsaved form edits arms the inline confirmBar ("switch to X?
  unsaved edits to Y are lost" / keep editing / discard & switch —
  Escape cancels). **Secrets are real masked inputs in the form**
  (`type=password`, the value cards' reveal/hide idiom); the dashed
  placeholder cards died with the tier-3 card band. Tier-2 editors keep
  their cards and autosave. *(Presentation follow-up, same day: the
  switcher shipped as a standalone "presets" chip band above a separate
  values surface — the owner read that as two editing surfaces. The
  presets now render as file-tabs on the top edge of the values card
  itself — tier 3's form card, tier 2's value-card grid — selected tab
  connected to the body and carrying the actions (rename input,
  duplicate, delete), the running preset marked by the accent dot, and
  the "values · X" form header dropped (the tab says it). One preset →
  no tabs at all, just a quiet + in the card's corner. Behavior —
  handlers, dirty guard, autosave, last-preset rule — unchanged.)*
- **Save**: form dirty → amber → `PUT presets/{selected}` with the whole
  bag. Both views dirty → TWO requests, source first (the server's
  order), and the result panel reflects the pair honestly: both landing
  says "saved the source and the X values"; the source landing and the
  values being refused keeps the source (it IS stored), re-derives the
  form from the new schema, puts the unsaved draft back, and the panel
  says "the source half of this save landed — only the values were
  refused"; the source being refused says the values were never sent.
  A 400 naming a form field still lands field-adjacent (with a note
  when the source half landed); anything else gets the rendered-excerpt
  panel and the save-anyway escape, which now re-runs the SAME
  sequenced save with `?validate=false` for whatever is still unsaved.
- **Pre-flight and diagnosis speak people**: `prettyMissing` (helpers)
  turns field paths into prose — "backend honeycomb's api key" from the
  bag's own row name and the schema's label; an unnamed row counts from
  1; a tier-2 UPPER_SNAKE var name passes verbatim (one function serves
  the pre-flight, the editor's warn line, and the drop diagnosis, which
  has no schema at hand and humanizes the field name instead). The
  configs-screen pre-flight on a tier-3 config fetches the source once
  on play (mirroring the Go rule via `missingRequiredT3`, kept in
  lockstep); an unparseable source defers to the server. The JS
  `missingRequired` mirror also gained Go's non-string guard (demoted
  bags).
- **The pencil judgment**: the inline card editor speaks env strings, so
  every tier-3 path into it (row chip pencil, menu pencil, row plus,
  pre-flight "add values", the dropping strip) routes to the editor
  with that preset selected instead — the simplest option won. The row
  plus still works with zero typing: it creates `preset-N` as a copy of
  the selected bag first, then lands on it. New/duplicated presets
  write with `?validate=false` — a verbatim copy of a stored bag has
  nothing new to prove (and a bag saved via the escape hatch stays
  copyable); the editor's chip-add seeds tier-3 from the selected bag
  (an empty bag would fail the repeat-group minimum).
- Help strip updated: "a preset holds all of a config's values".
- Drive-proven in the sandbox (throwaway COMPY_HOME, shimmed launchctl,
  real otelcol-compy): activating preset B rendered B's two-backend
  pipeline into config.yaml and the plist environment carried exactly
  the two secrets plus COMPY ports; a rejected plain-yaml demotion
  restored the templated pair via the source-only body.

---

## Amendment 5 — YAML front matter (owner request, as shipped)

Owner: "for the templates can the front matter not also be YAML." The
schema front matter now comes in two first-class forms, both forever, no
migration:

- **JSON** (the original): first non-blank byte `{`, then the `---`
  separator line. Detection stays purely textual, unchanged.
- **YAML** (the standard front-matter convention): `---` line, YAML
  schema, `---` line, body. Decoded strictly with `gopkg.in/yaml.v3`
  (`KnownFields`, mirroring the JSON path's `DisallowUnknownFields`)
  into the same schema structs — fields stay arrays, so declaration
  order is still form order. The dependency is an owner ruling
  (2026-08-29): a solid library over hand-crafting a parser; the OTel
  ecosystem itself runs on yaml.v3.

**Detection** (`catalog.IsSource`) is the careful part: a plain
collector config may legally open with `---` (the YAML document
marker), so shape alone cannot commit. A YAML candidate (opening AND
closing `---` lines) commits only when the between-text strictly
decodes into the schema struct and carries a `name` — otherwise it is a
plain config and falls through QUIETLY on the paste/demote path. The
source-save route (`PUT /source`) instead uses the new
`catalog.LooksLikeSource` (shape only), so a broken YAML schema on an
already-templated config errors LOUDLY with the real diagnostic — the
same asymmetry broken JSON front matter always had. Schema errors carry
line numbers relative to the FULL source (the decoder is fed
position-preserving blank-line padding). Once either form commits,
broken details (bad field types, an uncompilable body) error loudly in
`ParseSource`, as ever.

**The client parses nothing**: config detail
(`GET /api/configs/{name}`) now carries `template`, the server-parsed
schema, and the editor's form and tier-3 pre-flight derive from it —
`parseSourceSchema` is deleted (the form was only ever rebuilt from
STORED source, so no live-typing nicety was lost). `isSourceText`
mirrors detection with a documented cheap sniff (`---` markers plus a
top-level `name:` line); misrouting is self-correcting because the
server re-judges on either route.

**Serialization**: nothing regenerates front matter anywhere — sources
are owner-authored text stored and synced verbatim — so a YAML-fronted
source stays YAML by construction.

**The shipped starter** (`custom-endpoints.tmpl`) converted to YAML
front matter (it is the reference users copy); its body is
byte-identical. Existing configs that copied the JSON-fronted source
own their copies and simply stay JSON — the catalog is a starter,
nothing upgrades template copies.

---

## Amendment 6 — free vars: tier 3 contains tier 2 (as shipped)

Owner-found gap: a hand-written `${env:ASDF}` in a tier-3 body was dead
capability — not a schema field so not in the form, and the tier-3 env
split exported only secrets, so the var could never be set. That broke
the ladder ruling (tier N contains tier N−1's capability) and the
Amendment 4 model (presets own ALL of a config's values). Fixed:

- **Discovery** (`catalog.FreeVars(rendered, bag)`): the tier-2 vars
  parser runs over a preset's RENDER; free vars = the extracted `${env:}`
  refs minus `COMPY_*`, minus the bag's derived secret env names
  (`SecretEnv`'s walk, unset secrets included), minus any name colliding
  with a top-level schema key (**schema wins** — that is the collision
  rule; field names are conventionally lower_snake and env vars
  UPPER_SNAKE, so it never bites in practice). Trailing-comment
  descriptions and `:-defaults` come free from tier-2 machinery.
  Discovery is per-preset: a ref inside an `{{if}}` that didn't render
  doesn't exist for that preset.
- **Bag membership**: a free var is an ordinary STRING in the preset
  bag, keyed by the var's own name, verbatim. `NormalizeBag` and
  `PruneUnknown` pass unknown top-level strings through (unknown
  non-strings still 400/prune — the typo guard survives for structured
  values). `Reconcile` (source save, sync) additionally prunes a stored
  free-var value whose name that bag's own render no longer references
  — the removed-field rule applied to free vars; they are not secrets
  (secrets are schema fields, always kept), so pruning is safe. An
  unrenderable bag keeps its free values (lenient, as ever).
- **Env split becomes** (`catalog.EnvFor`): secrets + free-var values +
  `COMPY_*` (compy's ports win, added last). The render still never
  bakes a free var — the ref stays in the yaml and the collector
  expands it, exactly as tier 2; schema non-secret values still never
  travel via env (locked by test).
- **Pre-flight**: `cfgstore.MissingRequired` adds the tier-2 rule over
  the preset's render — a free var with no `:-default` and no bag value
  is missing (by var name, next to the schema's field paths). The web
  client's `missingRequiredT3` mirror carries the same rule (its list is
  the config detail's `free_vars[preset]`), and the tier-3 editor form
  renders the selected preset's free vars as tier-2 value cards under a
  "variables" section — values are ordinary members of the form's bag
  draft, riding the same dirty/save flow and whole-bag PUT as the schema
  fields.
- **Surfaces**: `GET /api/configs/{name}` gains `free_vars`
  ({preset → [Var]}, the openapi `ConfigDetail` schema documents it);
  values ride the existing preset routes (`PUT presets/{p}` bags,
  `compy presets set CFG P ASDF=v` — unknown keys already fell through
  as strings and now stick). `compy vars` appends free-var rows (type
  `env`) to the tier-3 table, unioned across presets.
- **No migration**: free vars are computed at read/activation time from
  source + bag; existing tier-3 configs light up on their next GET.

## Amendment 7 — shipped templated defaults: the four-config set (owner-approved, 2026-08-31)

Owner-approved redo of the out-of-the-box configurations. The shipped set
is exactly FOUR — `debug`, `otlp-basic`, `otlp-forward`, `bronto` — and
the `custom-endpoints` catalog template is gone (its engine coverage
survives as a test fixture). Three of the four are tier-3 templated
configs, so the shipped-defaults machinery learned to materialize
templated defaults:

- **The set**: `debug` (tier 3: one `verbosity` choice baked into the
  debug exporter), `otlp-basic` (tier 2, the one plain default: otlp out
  with a single `${env:OTLP_ENDPOINT:-…}` free var, no auth), `otlp-forward`
  (tier 3: the simplest receive→export fan-out — backends 1..8 with
  name/endpoint/optional auth header/secret, no processors, no toggles),
  `bronto` (tier 3: backends 1..4 with name/region choice (eu|us — the
  endpoint derives in the body)/secret/optional collection+dataset
  headers; memory_limiter + batch, file_storage-backed sending queue and
  retry-forever always on — fixed, not toggles).
- **One copy of each source**: tier-3 defaults live in
  `internal/catalog/catalog/*.tmpl` (they ARE the catalog — "new
  configuration from template" lists exactly these three); the plain
  default stays in `internal/cfgstore/defaults/otlp-basic.yaml`.
- **Materialization** (`cfgstore.MaterializeDefaults`): every embedded
  catalog template ships as a templated config — source copied to
  `config.tmpl`, the `default` preset seeded with the schema's normalized
  defaults (`Reconcile(nil)`; the repeat group seeds its Min rows from
  the row fields' defaults, which is why the shipped templates default
  `name` and `endpoint`/`region` — a shipped template must render with
  no user in the loop, locked by test), `config.yaml` rendered from it,
  provenance "shipped", pristine hash on the SOURCE (tier-3 sync
  semantics).
- **Upgrade in place**: an existing shipped config that is UNMODIFIED
  (hash vs pristine — against its source for a templated one, against
  its yaml for a still-plain one whose default turned templated) gets
  the new source via the same `applySource` path Sync uses: every
  preset's bag reconciled, render regenerated from the active preset,
  hash moved to the source. `Reconcile` additionally seeds a missing
  repeat group with Min default rows (the repeat-group version of
  "fields the schema defaults are filled in") so a plain-era empty bag
  upgrades cleanly. Modified configs and non-"shipped" provenance stay
  untouched, exactly as before; a bag the new schema cannot render is
  skipped (BadRequest), never bricking startup.
- **Retire rule**: a shipped-provenance config that is unmodified, NOT
  the active config, and no longer among the shipped defaults (the old
  `otlp`) is deleted at materialization. Modified or active → left
  alone (the old `debug`/`bronto` names are re-shipped, so only `otlp`
  retires in practice).
- **Reset** grows the templated arm: a shipped templated config resets
  source + render + pristine hash from the embedded template (again
  `applySource`; presets kept, reconciled) — the plain arm still reads
  `defaults/<name>.yaml` and clears any pasted source.
- **Vocabulary**: `ceBackend` gains `Row map[string]any` — the raw
  normalized row (secrets stripped) — as the ONE generic escape hatch
  for per-row fields the flat vocabulary doesn't model
  (`{{.Row.region}}`, `{{.Row.collection}}`). The Go side stays
  vendor-neutral; bronto's endpoint derivation lives in its template
  body.

## Amendment 8 — author-defined groups (owner-approved, 2026-09-01)

Owner ruling: "backends seems to be hardcoded — what if someone wants to
name it differently, or wants other lists: receivers, OTTL configurations?
This is highly limiting." And: "if such an item is named, I don't want a
`name` attribute — like the presets it should be possible to edit the label
at the top." Plus, on compatibility: "The user should be able to define
them, this is an infinite list, we do not want to limit them. But yes,
clean break."

So the repeat group stops being a feature of the engine and becomes a
feature of the SCHEMA.

- **`groups:` replaces `backends:`** — a list, not a singleton. Each entry
  is `{id, label?, item?, min?, max?, fields}`. The id is the bag key AND
  the render data key (`{{range .backends}}`, `{{range .ottl_statements}}`),
  must be `[a-z][a-z0-9_]*`, and may not collide with a field name or
  another group. `label` (the form heading) and `item` (one row: "+ add
  backend") derive from the id when omitted; `min` defaults to 0 and `max`
  to 16, the engine's per-group row cap. Groups themselves are unlimited.
  There is no back-compatibility shim: a source still saying `backends:`
  fails to parse, loudly, like any other unknown schema key.
- **Row identity is a LABEL, not a field**, and the rows ARE tabs. Every row
  carries the reserved key `_label` (`catalog.LabelKey`) and the group
  renders as the preset strip reused wholesale — same look, same gestures:
  the selected tab renames in place, the copy icon duplicates (with the
  first free "<label> 2"), the trash deletes down to `min`, the `+` adds,
  and the selected row's fields sit in a panel under the strip. A row is
  the same kind of thing a preset is: a named item you switch between.
  Schema fields may not use `_`-prefixed names, so nothing can collide with
  the key. A tab whose fields carry errors is marked, and a field 400 on a
  hidden row brings that row's tab forward. An absent label
  defaults by position ("backend 1"). Its SLUG is the row's identity in the
  rendered yaml — the exporter id and the secret env var names
  (`EU_PROD_API_KEY` for label "EU prod", field `api_key`) — so a rename
  moves the derived names while the secret VALUE stays in the row. Two rows
  may not slug to the same thing; that is a 400 (and a field-adjacent error
  in the form) rather than a silently collapsed exporter.
- **The Go vocabulary is gone.** `customendpoints.go` — `ceBackend`,
  `ceMetricsGroup`, `vocabulary()`, the `Backends`/`MetricsGroups`/
  `TracesExps`/`AnyProcs` value set — is deleted. It could only ever
  describe the shapes it knew, which is exactly the limitation. Render data
  is now just the normalized bag: every field under its own name, every
  group's rows under its own id, each row carrying `_label`, `_slug` and
  `_env` (field name → derived env var name, so a body writes
  `${env:{{._env.api_key}}}` and can never bake a value). Top-level secrets
  get the same `_env` map; `StorageDir` stays.
- **Bodies assemble their own lists** with three new template funcs beside
  `upper`/`slug`: `list`, `append`, `join`.

      {{$e := list}}{{range .backends}}{{$e = append $e (printf "otlp_http/%s" ._slug)}}{{end}}
      exporters: [{{join $e ", "}}]

  That is the whole replacement for `TracesExps` and friends — and it works
  for a group the engine has never heard of.
- **Optional fields now normalize to their type's zero** rather than
  staying absent: `missingkey=error` would otherwise turn an untouched
  optional field into a render failure the moment a body reads it directly
  (the old vocabulary hid this behind safe Go lookups).

### The OOTB set, round 3

Three shipped configs, not four — `bronto` is dropped entirely (the
retire rule removes an unmodified, inactive installed copy on first run;
a modified or active one stays and, since it references the deleted
vocabulary, will fail to re-render until reset — the accepted cost of the
clean break).

- **`otlp-basic`** (tier 2) gains an always-present auth header:
  `Authorization: Bearer ${env:OTLP_KEY:-}`. Empty sends an empty header;
  owner's call over a conditional the tier-2 model cannot express.
- **`otlp-forward`** (tier 3) drops the `name` field entirely — the row
  label replaced it — and its auth rule is one line: `auth_header`
  defaults to `Authorization` and sends `Bearer <value>`; ANY other header
  name sends the value bare; an emptied header name sends no auth block at
  all. `api_key` is optional, so the shipped default (localhost:4318, no
  key) opens with no warning — locked by test, for every shipped default.
