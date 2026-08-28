# Quality review — 2026-08-28

Four independent read-only reviews of branch `main` (at `a0746fe`): Go quality
and architecture, testing and coverage, security, and frontend. This report
merges their findings and ends in a ranked action list. Nothing was changed
in this pass.

## Verdict

The codebase is in unusually good shape for ~25 feature rounds in four days.
The house rules held under pressure: error-marker discipline (400 vs 500) is
consistent, exec argv is safe everywhere, path traversal is closed and
regression-tested across all 17 name-taking store functions, the CSRF and
host-check middleware provably wraps every route including late additions,
theme tokens are complete in all blocks, and no innerHTML or native dialog
ever slipped in. `go vet`, `gofmt`, staticcheck (one trivial finding),
and govulncheck are clean; the full test suite passes.

The debt is concentrated, not smeared: one unbounded HTTP path, one oversized
file per language (`app.go` 1707 lines, `app.js` 2475 lines), a REST handler
layer whose bodies mostly never ran under test, zero continuous JS tests, and
a handful of consistency drifts (five confirm idioms, three error surfaces
for name collisions).

## Security posture

Nothing exploitable in the threat model (malicious web pages vs the
localhost API, hostile config content, compromised downloads). Three LOW
findings, each requiring a compromised upstream:

- **S1** The collector-bump workflow interpolates an upstream tag into a
  shell line (`${{ }}` before parsing) — classic Actions injection; fix is
  env-var indirection. (S)
- **S2** The explicit distro-update path trusts the upstream `tag_name` into
  filesystem paths and URLs without the `versionParts`/`NewerVersion` guard
  the auto-fetch path already has; also permits a silent downgrade. One
  guard before `applyDistroUpdate`. (S)
- **S3** The otelbin import validates only the first URL; redirect hops are
  followed blind (up to 5), so a hostile otelbin.io could bounce compy into
  blind GETs at internal hosts. Check each hop. (S)

Accepted and on record: pulled updates verify against same-origin release
checksums (TLS-anchored; pinned versions keep compiled-in hashes); preset
secrets are readable in the LaunchAgent env and `GET /api/configs`
(single-user localhost ruling — revisit with keychain); 5xx bodies echo
internal paths (needed by the log-tail design; localhost only).

## Correctness risks (from the Go review)

- **G1** `httpFetch` (`app.go:873`) has no timeout at any level — the only
  unbounded HTTP path. A stalled connection wedges an activation's
  auto-download and permanently stalls the tray's hourly update loop.
  Transport-level deadlines (dial/TLS/response-header), not an overall
  timeout (archives are large). (S)
- **G2** `cfgstore.SetVar` accepts an empty variable key, which flows into
  the LaunchAgent environment. One guard. (S)
- **G3** GitHub-check failures return unmarked 500s, so the UI staples a
  collector log tail onto errors that have nothing to do with the collector;
  activation failures carry a double tail (embedded + UI-appended). (S)
- **G4** `Recent` is a maintained, API-exposed vestige — nothing consumes it
  since the menu went alphabetical, and the openapi description is now
  false. Decide: delete the chain or keep the field and fix the spec. (S)

## Test coverage (61% overall; the gaps that matter)

| Area | Gap |
|---|---|
| webui handlers | ~14 handler bodies never execute under test (dispatch is tested, decode isn't) |
| distro updates | `applyDistroUpdate` on a running collector: swap + both rollback-honesty branches unproven; `StartUpdateDistro` async path never runs |
| sync | `SyncAll` 0%, `HTTPFetch` (the real fetch: 5MB cap, non-200) 0% |
| CLI | `cmd/compy` arg parsing ~0% — ~870 lines nothing would catch |
| failure spine | `restorePrevious`'s restore-failure branch untested |
| app.js | 2475 lines, zero continuous tests; `missingRequired` hand-mirrors the Go rule and can drift silently |

Quality notes: the suite pins behavior over prose almost everywhere (typed
markers, keyword substrings); the staged launchd shim is fine as-is; the
"at least one preset" sentence is pinned in three layers (should be one);
the integration suite runs nightly only, Linux only, and covers no launchd,
migration, sync, or gRPC ingest.

## Frontend

Structural: the render/in-place-flip split is real but undocumented (four
flip idioms); three S-mutations happen mid-render; the CodeMirror instance
is destroyed and rebuilt on every editor render (background refresh resets
scroll on an open clean YAML pane); `renderSidebar` re-parses the 500-line
log on every keystroke, twice; one shared autosave timer can silently drop
a pending value PUT when switching presets within 500ms; `distroRow` derives
seven outputs from eight booleans via nested ternaries; the destructive
confirm renders at the table's end instead of under its row.

Consistency: five confirm idioms (three share a shape and should share a
builder); name-collision errors use three different surfaces, including the
success-styled note; disabled glyphs are double-faded (faint color × .45
opacity — likely under 3:1); errbar inset is inconsistent.

Accessibility: the os-env `role="switch"` div is focusable but not
keyboard-operable (the one real a11y bug); segmented controls lack
`aria-pressed`; the preset menu lacks `aria-expanded`/Escape; focus-visible
styling covers 5 of ~18 interactive classes; no `aria-live` on notes/errors.

Hygiene: dead CSS (`.hidden`, `--desk`, `--accentHi`), two never-used icons
(VERSIONS says 22, map holds 24), two vendored font weights never fetched;
CodeMirror 5 is EOL (pinned + hashed — note only). Token discipline and
vendor licensing are exemplary; the CSS theme dictionaries are duplicated by
necessity and nothing guards lockstep.

## Action list

Batch 1 — correctness and security (all S, do first):
1. Transport timeouts in `httpFetch` (G1)
2. Actions-injection fix in collector-bump.yml (S1)
3. Version-format + newer-than guard on the update path (S2)
4. Host check on every otelbin redirect hop (S3)
5. Empty-key guard in `SetVar` (G2)
6. Mark upstream-check failures; stop the double log tail (G3)
7. Keyboard operability for the os-env switch (F: real bug)

Batch 2 — tests that close real risk:
8. Table-driven route smoke test over `routes()` (valid body, malformed
   body, closure error per route) — kills the handler gap in one test (S)
9. `applyDistroUpdate` running-collector swap + rollback branches (M);
   `StartUpdateDistro` async via progress polling (S)
10. `SyncAll` + real `HTTPFetch` against httptest servers (M)
11. Extract ~10 pure JS helpers to `static/helpers.js`, test with stdlib
    `node --test` (no npm, no build); mirror the Go `MissingRequired` table
    verbatim; add the CI step (M)
12. CLI `run()` tests for the main subcommands (M)
13. `restorePrevious` restore-failure branch; `DropDiagnosis` wiring (S)
14. Go lockstep test for the CSS theme dictionaries; de-duplicate the
    "at least one preset" prose pin to one layer (S)
15. Integration workflow: path-filtered PR trigger for collector-touching
    changes (S)

Batch 3 — structure (M, no behavior change):
16. Split `app.go` → `distros.go` / `health.go` / `wiring.go` (same package)
17. Keep the CodeMirror instance across renders (rebuild only on
    config/readonly change)
18. Memoize `logEntries`; move `renderSidebar`'s form out; fix the
    mid-render S mutations; per-target autosave timer flush
19. Collapse `confirm*` keys into one object; render the confirm under its
    row; one shared `confirmBar()` for the three same-shape confirms
20. `distroState()` decision ladder + shared `pollProgress()`
21. `Status.EndpointPort()`; collapse the duplicated ports-line helpers

Batch 4 — polish (S, batchable into one round):
22. Dead exports (`MetricsURL`, `webui.Serve`, `RenderPlist`, `PlistPath`),
    stale `webui.BadRequest` comment, staticcheck S1039
23. Decide `Recent` (G4)
24. Dead CSS/tokens/icons/fonts; VERSIONS count; un-stack disabled fading;
    errbar inset; name-collision errors through one surface
25. `aria-pressed` on segmented controls, `aria-expanded`+Escape on the
    preset menu, grouped focus-visible rule, `aria-live` on note/error
    strips; declare `S.inlineDraft`; render-rule comment at `render()`

Deliberate non-actions: tray/window glue coverage (needs a real Cocoa run
loop; the pure logic is tested), the App-level operation lock (single-user
localhost ceiling, documented), webui handler-boilerplate adapter (do only
if the surface keeps growing), CM6 migration (EOL noted; pin + hash is the
mitigation until the editor warrants the port).
