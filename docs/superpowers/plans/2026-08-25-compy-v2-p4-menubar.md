# compy v2 P4 — Menu Bar v3 + Backlog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The spec's menu bar v3 (stats block, variable-set picker, gated distro swap already done) plus the recorded P2/P3 backlog.

**Spec:** docs/superpowers/specs/2026-08-25-compy-v2-configs-design.md § Menu bar. Design language for any UI copy: Direction B (dense utility).

## Global Constraints
Same as P1–P3: gofmt/vet/gofix clean; full `go test ./...` green; drift test green (routes unchanged unless stated); GOOS=linux CGO_ENABLED=0 build green; COMPY_HOME temp in tests; launchctl only via stubs in tests; NO live activation without a launchctl shim (standing rule).

### Task 1: core + CLI backlog
**Files:** internal/app (+tests), cmd/compy/main.go, internal/webui (marker usage only), internal/app/webui_wiring_test.go
- `app.LogStats(lines int) (errors, warnings int, err error)`: counts collector log lines whose level field is error/warn within the last `lines` lines (tab- or space-delimited level token; test with a synthetic log incl. mixed levels and a message containing the word "error" that must NOT count).
- CLI parity: `compy config set-distro <config> <distro|-->` and `compy config set-url <config> <url|-->` (`--` clears) → UpdateConfigMeta; `compy settings` (prints grpc/http ports + menu-distro-swap) and `compy settings set [--grpc-port N] [--http-port N] [--menu-distro-swap on|off]` → GetSettings/PutSettings. Usage block updated; no drift vs dispatch.
- SetDistroPath validation errors (path missing/not executable, unknown name) wrapped in webui.BadRequest → PUT /api/distros/{name} returns 400 (handler already maps the marker via RemoveDistro's path — verify; add 400 to that route's openapi responses).
- webui_wiring_test: add one sets/rename round-trip through the real WebUIAPI.
- [ ] TDD; commit `feat: P4 core/CLI backlog (log stats, meta+settings CLI, distro 400s)`.

### Task 2: tray v3
**Files:** internal/tray/tray.go (+slots_test.go if helpers grow), cmd wiring unchanged.
- Status block = two disabled lines: "running — <config> (<set>)" / "grpc :14317 · http :14318 · N err · M warn" (counts from app.LogStats(500); omit the err/warn tail when both 0; refresh with the 5s sync under m.mu).
- Variable-set picker: a "Variable set" submenu directly under the config slots, visible ONLY when the active config has ≥2 sets; items radio-checked (active set), click → Activate(activeConfig, thatSet) via act(). Reuse the add/Remove/setChecked item-map pattern.
- [ ] Pure helpers (e.g. statusLines(status, stats) (string, string)) unit-tested; build + `compy tray` process smoke; commit `feat: menu bar v3 (stats block, variable-set picker)`.

### Task 3: UI polish backlog
**Files:** internal/webui/static/app.js only.
- Ports form: blank/non-numeric input → inline error via showMessage (400-style copy), never a silent no-op.
- Remote edit-confirm wording: replace the circular "until you discard them" sentence with: "Editing detaches this configuration from its remote source. Sync will stop; 'Discard local edits & re-sync' brings it back."
- [ ] Screenshot the ports error state (throwaway serve, read-only); commit `fix: ports feedback + remote confirm copy`.

## Execution notes
T1 first (T2 consumes LogStats). T2 // T3 parallel after (disjoint files; T3 worktree). Reviews per task (T3 may take haiku). Final: no whole-branch fable pass needed at this scale — one sonnet branch review, fix wave if needed, merge, rebuild, tray reinstall (codesigning), live check incl. menu-bar screenshot impossible headlessly — verify via `launchctl print` + process + user's own eyes.
OTELCOL_BIN: /private/tmp/claude-501/-Users-severin-Projects-local-collector/05e1307f-c061-4fa0-aa7f-31358b0cef49/scratchpad/otelcol
