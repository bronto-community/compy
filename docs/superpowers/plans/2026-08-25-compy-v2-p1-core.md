# compy v2 P1 — Configurations Core + CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the fragment model with first-class Configurations (variable sets, shipped defaults, remote sync, definition-driven distros) in the Go core and CLI, keeping the machine's installed tray/window functional via a stopgap.

**Architecture:** Per spec `docs/superpowers/specs/2026-08-25-compy-v2-configs-design.md` (READ IT FIRST). One active config + variable set; values injected via LaunchAgent EnvironmentVariables; pristine-hash edit protection shared by shipped defaults and remote sync; distros are pinned definitions downloaded on demand with sha256 verification.

**Tech Stack:** Go ≥1.24 stdlib (+ existing systray/webview deps untouched).

**Spec:** docs/superpowers/specs/2026-08-25-compy-v2-configs-design.md

## Global Constraints

- Module `github.com/bronto-community/compy`; gofmt + `go vet ./...` clean; `GOOS=linux CGO_ENABLED=0 go build ./...` stays green; full `go test ./...` green before every commit.
- Every test sets `COMPY_HOME` to `t.TempDir()`. No network in unit tests (download code takes an injectable fetch func).
- Atomic writes via `state.WriteFileAtomic`. Files with secrets (meta.json, config.yaml) 0o600.
- Config names: reuse `state.ValidBackendName` semantics (rename usages, keep regex `^[a-z0-9][a-z0-9-]*$`, ≤64).
- The feature gate `confmap.enableMergeAppendOption` is REMOVED from run/validate args (single-config model doesn't need it).
- No file may import a package from a higher-numbered task (T5 integrates).

---

### Task 1: `internal/vars` — variable extraction from collector YAML

**Files:** Create `internal/vars/vars.go`, `internal/vars/vars_test.go`

**Interfaces:**
- Consumes: nothing internal.
- Produces:

```go
package vars

type Var struct {
    Name        string `json:"name"`
    Default     string `json:"default"`     // from :-fallback, "" if none
    HasDefault  bool   `json:"has_default"`
    Description string `json:"description"` // trailing same-line YAML comment, "" if none
}

// Parse scans collector YAML text for ${NAME}, ${NAME:-def}, ${env:NAME},
// ${env:NAME:-def} references. Other schemes (${file:...}, ${secretsmanager:...},
// anything with a scheme: prefix other than env) are ignored. Deduplicated by
// name (first occurrence wins for default/description), sorted by name.
func Parse(yaml string) []Var
```

Name charset: `[A-Za-z_][A-Za-z0-9_]*`. A description is the `# ...` comment on the same line as the reference (after the value), trimmed.

- [ ] **Step 1: failing tests** — table-driven `TestParse`:

```go
cases := []struct{ name, yaml string; want []vars.Var }{
  {"bare", "endpoint: ${OTLP_ENDPOINT}", []vars.Var{{Name: "OTLP_ENDPOINT"}}},
  {"default", "endpoint: ${EP:-http://localhost:4318}", []vars.Var{{Name: "EP", Default: "http://localhost:4318", HasDefault: true}}},
  {"env form + desc", "key: ${env:API_KEY}  # vendor API key", []vars.Var{{Name: "API_KEY", Description: "vendor API key"}}},
  {"env with default", "x: ${env:A:-b}", []vars.Var{{Name: "A", Default: "b", HasDefault: true}}},
  {"other schemes ignored", "a: ${file:/etc/x}\nb: ${secretsmanager:arn}", nil},
  {"dedup first wins", "a: ${X:-1}  # first\nb: ${X:-2}  # second", []vars.Var{{Name: "X", Default: "1", HasDefault: true, Description: "first"}}},
  {"sorted", "a: ${B}\nb: ${A}", []vars.Var{{Name: "A"}, {Name: "B"}}},
  {"nested default kept verbatim", "a: ${E:-${F}}", []vars.Var{{Name: "E", Default: "${F}", HasDefault: true}}}, // ponytail: nested refs not recursed
}
```
- [ ] **Step 2:** run — FAIL. **Step 3:** implement (regex + line scan; no YAML parser). **Step 4:** PASS + vet. **Step 5:** Commit `feat: vars package (variable extraction)`

---

### Task 2: `internal/launchd` — EnvironmentVariables in the plist

**Files:** Modify `internal/launchd/launchd.go`, `internal/launchd/launchd_test.go`

**Interfaces (changed, callers updated in T5):**

```go
func RenderPlist(bin string, args []string, logPath string, env map[string]string) []byte
func Install(bin string, args []string, logPath string, env map[string]string) error
func InstallAgent(label, bin string, args []string, logPath string, keepAlive bool, env map[string]string) error
```

Plist gains, when env is non-empty (sorted keys, XML-escaped keys and values):
```xml
<key>EnvironmentVariables</key><dict>
  <key>K</key><string>V</string>
</dict>
```

- [ ] **Step 1: failing tests:** `TestRenderPlistEnvDict` (two vars incl. a value with `&`, assert sorted order + escaping + dict present; nil env → no EnvironmentVariables key). Update existing call sites in THIS package's tests with `nil`.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** package tests PASS (other packages break until T5 — that's expected; run `go test ./internal/launchd/` only). **Step 5:** Commit `feat: launchd env dict in plists` — NOTE: repo-wide build is broken until T5 lands; commit anyway (T2 runs on an isolated branch merged together with T5).

---

### Task 3: `internal/cfgstore` — configuration store

**Files:** Create `internal/cfgstore/cfgstore.go`, `internal/cfgstore/defaults.go`, `internal/cfgstore/cfgstore_test.go`, `internal/cfgstore/defaults/*.yaml` (embedded)

**Interfaces:**
- Consumes: `state.WriteFileAtomic`, `state.ValidBackendName` (as name validator), `internal/vars`.
- Produces:

```go
package cfgstore

type Meta struct {
    RemoteURL      string                       `json:"remote_url,omitempty"`
    Distro         string                       `json:"distro,omitempty"` // "" = global default
    PristineSHA256 string                       `json:"pristine_sha256,omitempty"`
    VariableSets   map[string]map[string]string `json:"variable_sets"`
    ActiveSet      string                       `json:"active_set"`
}

type Info struct {
    Name       string     `json:"name"`
    Provenance string     `json:"provenance"` // "shipped" | "remote" | "local"
    Modified   bool       `json:"modified"`   // hash != pristine (always false for "local")
    Meta       Meta       `json:"meta"`
    Vars       []vars.Var `json:"vars"`
}

func Dir(root string) string                                  // root/configs
func List(root string) ([]Info, error)                        // sorted by name
func Get(root, name string) (Info, string, error)             // info + config.yaml content
func Create(root, name, yaml string) error                    // provenance local; errors if exists
func CreateFromURL(root, name, url string, fetch Fetch) error // sets RemoteURL + pristine hash
func Copy(root, src, dst string) error                        // copies yaml + variable sets; drops RemoteURL/pristine (provenance local)
func Delete(root, name string) error
func WriteYAML(root, name, yaml string) error                 // just writes; Modified derives from hash
func WriteMeta(root, name string, m Meta) error
func Sync(root, name string, fetch Fetch) error               // errors "locally modified" if Modified; refetch + update pristine
func Resync(root, name string, fetch Fetch) error             // discard local edits: force refetch + update pristine
func SetVar(root, name, set, key, value string) error         // creates set on first write
func DeleteSet(root, name, set string) error
func UseSet(root, name, set string) error                     // errors if set missing
func MaterializeDefaults(root string) error                   // idempotent; see below
type Fetch func(url string) ([]byte, error)                   // injectable; prod = http.Get with 30s timeout, 5MB cap
```

Shipped defaults live in `defaults/<name>.yaml` (embedded FS). This task creates ONLY `debug.yaml` as a placeholder (T6 authors the real set):
```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:${COMPY_GRPC_PORT:-14317}  # compy's local gRPC port
      http:
        endpoint: 127.0.0.1:${COMPY_HTTP_PORT:-14318}  # compy's local HTTP port
exporters:
  debug:
    verbosity: ${DEBUG_VERBOSITY:-normal}  # basic | normal | detailed
service:
  pipelines:
    traces: {receivers: [otlp], exporters: [debug]}
    metrics: {receivers: [otlp], exporters: [debug]}
    logs: {receivers: [otlp], exporters: [debug]}
```

`MaterializeDefaults`: for each embedded default — if the config doesn't exist, create it (provenance shipped: pristine hash set, meta has no RemoteURL but pristine set ⇒ Provenance "shipped"; distinguish shipped-vs-remote by RemoteURL == ""). If it exists and is UNMODIFIED (hash == pristine) and embedded content differs: overwrite + update pristine (the upgrade path). If exists and MODIFIED: leave untouched. Provenance rules: pristine=="" ⇒ "local"; RemoteURL!="" ⇒ "remote"; else "shipped".

- [ ] **Step 1: failing tests** (each with temp root): `TestCreateGetListDelete`, `TestCopyDropsProvenance`, `TestCreateFromURLAndSync` (fake Fetch; sync after local edit → error mentioning "locally modified"; Resync succeeds and clears Modified), `TestVariableSets` (SetVar creates set; UseSet unknown → error; DeleteSet active → error), `TestMaterializeDefaultsUpgradeRules` (fresh → created; unmodified + changed embed (simulate by editing pristine content then re-materializing with a doctored embed? — instead: edit config.yaml, hash differs → materialize leaves it; restore original content → materialize overwrites when embed differs is untestable without doctored embed, so test the exported hash helper logic via WriteYAML+List Modified flag instead and the leave-untouched path), `TestGetParsesVars` (debug default → COMPY_GRPC_PORT, COMPY_HTTP_PORT, DEBUG_VERBOSITY with descriptions).
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS + vet. **Step 5:** Commit `feat: cfgstore (configurations, variable sets, provenance, sync)`

---

### Task 4: `internal/distro` — definitions + verified download

**Files:** Create `internal/distro/distro.go`, `internal/distro/defs.go`, `internal/distro/distro_test.go`

**Interfaces:**
- Consumes: `state` (registry file), stdlib.
- Produces:

```go
package distro

type Def struct {
    Name     string            // "core", "contrib", "otlp", "ebpf-profiler"
    Version  string
    URLs     map[string]string // "darwin_arm64" → tar.gz URL; missing platform = unavailable
    SHA256   map[string]string // per platform, of the tar.gz
    Binary   string            // path inside the archive, e.g. "otelcol"
}

func Defs() []Def                       // the shipped table (defs.go), version-pinned
func Available(d Def) bool              // has URL for runtime GOOS_GOARCH
type Fetch func(url string) (io.ReadCloser, error)
func Ensure(root string, d Def, fetch Fetch) (string, error)
// idempotent: distros/<name>-<version>/<binary> exists → return path;
// else download tar.gz via fetch, verify sha256, extract binary, chmod 0755, return path.
// checksum mismatch → error naming expected/got, partial files removed.
func Registry(root string) ([]state.Distro, error)
// state.LoadDistros merged with Defs(): definition entries appear with
// Path = installed path or "" (not downloaded); user entries (state.Distro)
// override a definition with the same name (the "change path" warning is UI/CLI copy, not enforced here).
```

defs.go pins v0.135.0 for core/contrib/otlp with real URLs (the release URL pattern already used in this repo) and sha256 values — **fill real checksums by downloading each darwin_arm64 + linux_amd64 asset's published `.sha256` or computing from the download at implementation time; do NOT invent values.** ebpf-profiler: present with empty URLs (upstream has no binary releases) so it lists as unavailable.

- [ ] **Step 1: failing tests** (no network: fake Fetch serving a tar.gz built in-test with `archive/tar`+`gzip` containing the binary): `TestEnsureDownloadsVerifiesExtracts` (correct sha → path exists, executable; second call does not invoke fetch), `TestEnsureChecksumMismatch` (error contains expected+got, nothing left on disk), `TestAvailable` (def without URL for current platform → false), `TestRegistryMergesUserOverrides`.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS + vet. **Step 5:** verify real checksums used in defs.go match upstream (document the command + output in the report). Commit `feat: distro definitions with verified on-demand download`

---

### Task 5: core rework — state v2, app, CLI, migration, tray/window stopgap

**Files:** Modify `internal/state/state.go` (+test), `internal/app/app.go` (+test), `cmd/compy/main.go`, `internal/tray/tray.go`, `internal/webui/webui.go` (+test), `internal/webui/static/index.html`; Delete `internal/config/` (move last-good snapshot/restore into cfgstore first: `SnapshotActive(root)`, `RestoreActive(root)` covering configs/<active>/ + settings.json).

**Interfaces:**
- Consumes: everything from T1–T4 exactly as declared.
- Produces (the surface P2's REST layer will wrap):

```go
// state.Settings v2 (replaces Enabled/RawMode):
type Settings struct {
    GRPCPort, HTTPPort int
    Distro   string   // global default distro
    ActiveConfig string
    MenuDistroSwap bool
    OSEnv    bool
}

// app:
func (a *App) Configs() ([]cfgstore.Info, error)
func (a *App) ActiveConfig() (string, string, error) // name, active set
func (a *App) Activate(name, set string) error       // set "" keeps meta's active_set; validate → install(plist env = set values + COMPY_*_PORT) → kickstart → probe → snapshot
func (a *App) Apply() error                          // re-activate current
func (a *App) Rollback() error
// CRUD passthroughs to cfgstore (CreateConfig, CreateFromURL, CopyConfig, DeleteConfig(errors if active), WriteConfigYAML(re-activate if active), Sync, Resync, SetVar(re-activate if active+set), UseSet, DeleteSet)
func (a *App) EnsureDistro(name string) (string, error) // resolve config's distro → global default; definition → distro.Ensure (real HTTP fetch); user path → as-is
```

Activation env: the active set's variables + `COMPY_GRPC_PORT`/`COMPY_HTTP_PORT` from settings (so shipped defaults bind the standard ports). Collector args: `--config configs/<name>/config.yaml` — NO feature gate.

CLI v2 (replace backend/raw commands; keep service/status/env/run/ui/window/tray/distro):
```
compy config list|show <name>|create <name> [--from-url URL]|copy <src> <dst>|delete <name>|edit <name>|sync <name>|sync-all|resync <name>
compy use <config> [<set>]          # activate
compy vars <config>                 # table: name, description, default, values per set
compy set <config> <set> KEY=VALUE  # set-var (creates set)
compy sets use <config> <set> | sets delete <config> <set>
compy distro list                   # definitions (with available/downloaded) + user entries
compy distro add <name> <path> | distro use <name> | distro fetch <name>
```

Migration (runs inside app.New once): if legacy `config/backends/` exists → build `migrated` config: if a distro binary is resolvable, run it with the OLD arg list + `--feature-gates=confmap.enableMergeAppendOption,otelcol.printInitialConfig print-initial-config` to render the effective YAML; else fall back to copying old base.yaml. Create config `migrated` (provenance local), set ActiveConfig if there were enabled backends, move `config/` → `legacy-v1/`, log what happened to stderr. MaterializeDefaults runs after.

Tray stopgap (full menu-bar v3 is P4): replace the Backends submenu with a **Configs** submenu (radio-check per config; click = Activate with its active set); Distro submenu only when `settings.MenuDistroSwap` (default false → hidden). Status line unchanged.
Webui stopgap (P3 rebuilds it): replace API struct with `Status`, `Configs`, `Activate(name)`, `LastError` closures; index.html becomes ONE short page: status header + config list with "Use" buttons. Delete obsolete routes AND their tests; keep host/CSRF middleware + its tests intact.

- [ ] **Step 1:** move snapshot/restore into cfgstore with tests (`TestSnapshotRestoreActive`), delete internal/config, fix state.Settings + its tests (settings v2 defaults; migration of old settings JSON fields simply ignores unknown/missing — test that an old settings.json loads).
- [ ] **Step 2: failing app tests** (fake distro script pattern from existing app_test): `TestActivateHappyPath` (plist written with env dict incl. COMPY ports + set values — assert via launchd.Exec stub + reading plist), `TestActivateValidateFailureNoLaunchctl`, `TestActivateUnknownSetErrors`, `TestDeleteActiveConfigErrors`, `TestWriteYAMLReactivatesWhenActive`, `TestMigrationLegacyBackends` (fabricate legacy tree; assert migrated config exists, legacy-v1/ moved, defaults materialized).
- [ ] **Step 3:** implement app + CLI + tray + webui stopgap. **Step 4:** full `go test ./...` + vet + `GOOS=linux CGO_ENABLED=0 go build ./...` green. **Step 5:** Commit `feat!: v2 configurations core (one active config, variable sets, migration)`

---

### Task 6: shipped defaults, e2e, docs

**Files:** Create `internal/cfgstore/defaults/otlp.yaml`, `internal/cfgstore/defaults/bronto.yaml`; Modify `integration/e2e_test.go`, `README.md`, `CLAUDE.md`

- otlp.yaml: standard receivers (as debug.yaml) + `otlp` exporter with `endpoint: ${OTLP_ENDPOINT}  # where to send (host:port)`, `headers: {x-api-key: ${API_KEY:-}}  # optional API key` — hand-verify against `otelcol validate` (guard: only when OTELCOL_BIN set).
- bronto.yaml: otlphttp exporter, `endpoint: ${BRONTO_ENDPOINT:-https://ingestion.eu.bronto.io}  # Bronto ingestion endpoint`, header `x-bronto-api-key: ${BRONTO_API_KEY}  # Bronto API key` (verify names against the bronto skill docs — invoke the Skill tool with skill "bronto"; keep the existing header name if docs confirm).
- e2e (tag integration, OTELCOL_BIN): materialize defaults → activate `debug` (env injection via direct process env since no launchd in test: run collector with `--config` + env vars set on the exec.Cmd) → POST span → assert "e2e-span" in output. Also `TestDefaultsValidate`: every embedded default validates via `otelcol validate` with defaults-only env.
- README/CLAUDE.md: v2 CLI surface, config model paragraph, migration note.
- [ ] Steps: e2e first (RED without code changes? — it exercises T5 surface; write, run with OTELCOL_BIN, make pass), docs, full suite green, Commit `feat: shipped default configs + v2 e2e + docs`

## Post-review notes

- `ebpf-profiler` is deliberately LISTED as unavailable on non-linux (`compy distro list` shows it with "unavailable on this platform") rather than hidden — a deviation from the spec's "hidden on darwin"; visibility was chosen so darwin users can see the distro exists instead of wondering why it's missing.

## Execution notes (for the coordinator)

- Wave A: T1, T2, T3, T4 PARALLEL in isolated worktrees (T2's repo-wide break is contained to its branch; merge T2 together with T5). Merge order: T1, T3, T4 → main-line branch; T2 merges into T5's working branch before T5 starts (T5 fixes all launchd call sites).
- T5 sequential (opus), then T6.
- OTELCOL_BIN for validation/e2e: /private/tmp/claude-501/-Users-severin-Projects-local-collector/05e1307f-c061-4fa0-aa7f-31358b0cef49/scratchpad/otelcol
- The machine's live tray/collector must keep working: after T5+T6 merge, rebuild ./compy, `launchctl kickstart` the tray agent, and re-activate a config live.
- Residual (final re-review): a configs/<name>/ dir lacking config.yaml (e.g. disk-full mid-create from causes other than the fixed CreateFromURL path) is skipped by List AND refused by Delete — no CLI cleanup path; manual rm needed. Candidate P2 fix: let Delete remove such dirs.
