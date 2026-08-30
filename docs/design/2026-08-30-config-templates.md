# Config templates — design note

Owner-ruled design, settled 2026-08-30 in conversation. This note is the
build brief. Vendor facts referenced here come from
docs/research/2026-08-30-otlp-vendor-landscape.md.

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
