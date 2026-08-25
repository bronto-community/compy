# compy v2 P2 — REST + OpenAPI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A complete, OpenAPI-documented REST surface over the v2 core — the contract P3's UI will call — with a drift test guaranteeing spec ↔ router agreement, plus the app-layer additions the surface needs.

**Architecture:** No daemon (unchanged): the API is served by whatever UI process runs (`compy ui` / window / tray's Open compy); the CLI keeps linking the core directly. `api/openapi.json` (OpenAPI 3.0, JSON so stdlib parses it) is committed and authoritative; `internal/webui` declares routes in an enumerable table consumed by both `Handler()` and the drift test. webui stays dependency-free: all behavior arrives via the `API` closure struct, wired by `app.WebUIAPI()`.

**Tech Stack:** Go stdlib only. Existing host/CSRF middleware wraps everything, untouched.

**Spec:** docs/superpowers/specs/2026-08-25-compy-v2-configs-design.md (§ "API: REST + OpenAPI").

## Global Constraints

- gofmt + `go vet ./...` clean; full `go test ./...` green; `GOOS=linux CGO_ENABLED=0 go build ./...` green; tests set COMPY_HOME to t.TempDir(); no network in unit tests; launchctl only via stubs.
- Route errors: JSON `{"error":"..."}`, closure error strings verbatim (collector output is the UX). 400 for malformed request bodies/params, 500 for closure errors.
- Config/set names in paths are URL-encoded by clients; handlers pass them through — name validation stays in the core (cfgstore/app), never duplicated in handlers.
- The stopgap index.html must keep working: it uses GET /api/status, GET /api/configs, POST /api/configs/{name}/activate, GET /api/log — these shapes are frozen.
- The existing security tests (host check, CSRF) must remain green unmodified.

## The REST surface (authoritative list; openapi.json mirrors exactly this)

```
GET    /api/status                              → app.Status JSON
GET    /api/log                                 → {"log": tail}
GET    /api/env                                 → {"vars": {..}, "script": "export ..."}
POST   /api/os-env                              {"on":bool}
GET    /api/settings                            → {"grpc_port","http_port","menu_distro_swap"}
PUT    /api/settings                            same shape (partial: absent field = unchanged); port changes take effect on next apply
POST   /api/service/apply
POST   /api/service/rollback
POST   /api/service/validate                    → 200 {"ok":true} | 500 {"error": collector output}
GET    /api/configs                             → []cfgstore.Info
POST   /api/configs                             {"name","yaml"?}  (blank template when yaml absent)
POST   /api/configs/from-url                    {"name","url"}
GET    /api/configs/{name}                      → {"info": Info, "yaml": "..."}
PUT    /api/configs/{name}/yaml                 text/plain body
PUT    /api/configs/{name}/meta                 {"distro"?, "remote_url"?} (partial)
DELETE /api/configs/{name}
POST   /api/configs/{name}/copy                 {"dst"}
POST   /api/configs/{name}/activate             {"set"?}
POST   /api/configs/{name}/sync
POST   /api/configs/{name}/resync
POST   /api/configs/sync-all                    → {"synced": []}
PUT    /api/configs/{name}/sets/{set}           {"values": {k:v}} (create/replace whole set)
DELETE /api/configs/{name}/sets/{set}
POST   /api/configs/{name}/sets/{set}/use
GET    /api/distros                             → [{"name","path","available","downloaded","selected","version"?}]
POST   /api/distros                             {"name","path"} (add user distro; warning field in response when overriding a definition)
POST   /api/distros/{name}/use
POST   /api/distros/{name}/fetch
```

---

### Task 1: route table + openapi.json + drift test (the contract)

**Files:**
- Create: `api/openapi.json`
- Modify: `internal/webui/webui.go` (+`internal/webui/webui_test.go`)

**Produces (T3 compiles against this):**

```go
// route is one API endpoint: the drift test compares this table to
// api/openapi.json, and Handler() builds the mux from it.
type route struct {
    Method  string
    Pattern string // mux pattern, e.g. "POST /api/configs/{name}/activate"
    H       func(API) http.HandlerFunc
}
func routes() []route   // the full table above
func Handler(api API) http.Handler // ranges routes(); static + middleware unchanged
```

- API struct: extend to the full closure set (typed fields; exact signatures listed in Task 3's Consumes block — define them HERE so T3 only implements): Status, Log, Env, SetOSEnv, GetSettings, PutSettings, Apply, Rollback, Validate, Configs, CreateConfig, CreateFromURL, GetConfig, PutConfigYAML, PutConfigMeta, DeleteConfig, CopyConfig, Activate, Sync, Resync, SyncAll, PutSet, DeleteSet, UseSet, Distros, AddDistro, UseDistro, FetchDistro.
- Every handler in T1 is the real signature but returns 501 `{"error":"not implemented"}` EXCEPT the four frozen stopgap routes (Status, Configs, Activate, Log) which keep their existing working implementations moved into the table.
- openapi.json: OpenAPI 3.0.3, info block, every path+method above with request/response schemas (pragmatic: named object schemas in components for Info/Status/Settings/Distro rows; free-form `additionalProperties` where the core returns maps).
- Drift test `TestOpenAPIDriftAgainstRoutes`: parse api/openapi.json with encoding/json; set(spec paths×methods) == set(routes() patterns) — convert `{name}` segments both ways; failure messages name the missing/extra entries on each side.

- [ ] Steps: failing drift test first (empty table) → write table + spec together → GREEN; existing webui tests stay green (stopgap routes still live); 501 smoke test hits one unimplemented route. Commit `feat: REST contract (route table, openapi.json, drift test)`.

---

### Task 2: app-layer additions

**Files:** Modify `internal/app/app.go` (+test), `internal/cfgstore/cfgstore.go` (+test)

**Produces:**
```go
// cfgstore
func WriteSet(root, name, set string, values map[string]string) error // create/replace whole set (validates name; set may be new)
// app
func (a *App) ReplaceSet(name, set string, values map[string]string) error // WriteSet + re-activate if name is active AND set is its active_set
func (a *App) UpdateConfigMeta(name string, distroP, remoteURLP *string) error // partial; nil = unchanged; distro must exist in Registry or be ""
func (a *App) GetSettings() (state.Settings, error)
func (a *App) PutSettings(grpcP, httpP *int, menuSwapP *bool) error // partial; ports validated 1-65535
func (a *App) EnvInfo() (map[string]string, string, error) // envvars.Vars + sh Script
func (a *App) SyncAll() ([]string, error)                  // exists — verify signature, else add
func (a *App) AddDistroWarning(name string) string          // "" or the shipped-definition-override warning text (extracted from the stderr print so REST can return it)
```
- [ ] Steps: TDD each (existing app_test patterns; ReplaceSet re-activation asserted via stubbed launchd like TestWriteYAMLReactivatesWhenActive). Commit `feat: app surface for REST (sets, meta, settings, env)`.

---

### Task 3: handlers + per-route tests + wiring

**Files:** Modify `internal/webui/webui.go` or new `internal/webui/handlers.go`, `internal/webui/webui_test.go`, `internal/app/app.go` (WebUIAPI), `internal/webui/static/index.html` (only if a frozen shape accidentally drifted — it must not).

- Replace every 501 with the real handler calling its closure; wire all closures in app.WebUIAPI() as one-liners.
- Tests: table-driven per-route handler tests with fake closures (existing fakeAPI pattern, extended): happy path + malformed-body 400 + closure-error 500 passthrough for at least: PutSettings, CreateConfig, CopyConfig, PutSet, UpdateMeta, AddDistro (warning field), Activate with set. Existing security tests untouched and green.
- [ ] Steps: RED (route tests against 501s) → implement → GREEN → full gates. Commit `feat: REST handlers over the v2 core`.

---

### Task 4: docs + finish

**Files:** README.md, CLAUDE.md
- README: short "HTTP API" section — where it's served, api/openapi.json pointer, one curl example.
- CLAUDE.md: api/ dir note + drift-test convention ("add a route = update table + openapi.json or TestOpenAPIDrift fails").
- [ ] Full gates + integration suite (OTELCOL_BIN) + commit `docs: HTTP API`.

## Execution notes (coordinator)
- T1 and T2 PARALLEL (disjoint packages) in worktrees; merge both; T3 sequential (opus or strong sonnet) in-tree; T4 tiny (can fold into T3's agent if convenient — keep as separate commit).
- Whole-branch review at the end (most capable model), one fix wave max, then merge to main, rebuild ./compy, `tray uninstall && tray install` (codesigning lesson), verify stopgap page + one curl against a live `compy ui`.
- OTELCOL_BIN: /private/tmp/claude-501/-Users-severin-Projects-local-collector/05e1307f-c061-4fa0-aa7f-31358b0cef49/scratchpad/otelcol
