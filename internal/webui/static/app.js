"use strict";

/* compy window UI — v3 handoff (docs/design/handoff/README.md for intent,
   compy.dc.html for behaviour, ACCEPTANCE.md for the checklist).

   Four screens over a hash router (#/configs, #/configs/<name>, #/collector,
   #/settings), all data from the P2 REST API. House rules that survive any
   design:
     - every API-derived string reaches the DOM through textContent/el();
       there is no innerHTML in this file, not even for icon markup.
     - dialogs are in-page <dialog>; window.prompt/confirm are dead in the
       WKWebView the real window runs in.
     - the background refresh never clobbers a focused input.
     - unsaved editor work is guarded on navigation and on unload.
     - the body never scrolls horizontally; panes scroll themselves. */

/* ── tiny DOM helper ──────────────────────────────────────────────── */
function el(tag, opts, children) {
  const e = document.createElement(tag);
  opts = opts || {};
  if (opts.class) e.className = opts.class;
  if (opts.text != null) e.textContent = opts.text;
  if (opts.title) e.title = opts.title;
  if (opts.attrs) for (const k in opts.attrs) if (opts.attrs[k] != null) e.setAttribute(k, opts.attrs[k]);
  if (opts.on) for (const k in opts.on) e.addEventListener(k, opts.on[k]);
  if (opts.props) for (const k in opts.props) e[k] = opts.props[k];
  for (const c of children || []) if (c) e.appendChild(c);
  return e;
}
function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }
function span(cls, text) { return el("span", { class: cls, text }); }

// icon builds a Lucide glyph from the vendored path-data map: stroke 1.9,
// currentColor, round caps, per the handoff's iconography rules. SVG needs
// createElementNS, so it can't go through el().
const SVGNS = "http://www.w3.org/2000/svg";
function icon(name, size, filled) {
  const svg = document.createElementNS(SVGNS, "svg");
  svg.setAttribute("width", size || 14);
  svg.setAttribute("height", size || 14);
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", filled ? "currentColor" : "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.9");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  // LUCIDE is a top-level `const` in the vendored script: a global binding
  // reachable by bare name from another classic script, but never a
  // property of window — so it must not be looked up as one.
  const nodes = (typeof LUCIDE !== "undefined" && LUCIDE[name]) || [];
  for (const [tag, attrs] of nodes) {
    const node = document.createElementNS(SVGNS, tag);
    for (const k in attrs) node.setAttribute(k, attrs[k]);
    svg.appendChild(node);
  }
  return svg;
}
function iconWrap(cls, name, size, filled, title) {
  const s = el("span", { class: cls, title }, [icon(name, size, filled)]);
  return s;
}

/* ── API client ───────────────────────────────────────────────────── */
async function api(path, opts) {
  const r = await fetch(path, opts);
  const ct = r.headers.get("content-type") || "";
  const body = ct.includes("json") ? await r.json() : await r.text();
  if (!r.ok) {
    const err = new Error((body && body.error) || r.statusText);
    err.status = r.status;
    if (body && body.still_running) err.stillRunning = body.still_running;
    throw err;
  }
  return body;
}
function apiJSON(path, method, obj) {
  return api(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(obj) });
}
const enc = encodeURIComponent;
const cfgURL = (name) => "/api/configs/" + enc(name);

/* ── in-page dialogs ──────────────────────────────────────────────────
   compy's own window is a WKWebView whose WKUIDelegate implements no
   JavaScript panel methods, so the native prompt returns null and the
   native confirm returns false without showing anything: every action
   gated on one silently did nothing there. <dialog> works. The design puts
   destructive confirmations inline instead — ask() is only for the one
   free-text input the design asks for (a collector's path). */
function ask(message, initial) {
  return new Promise((resolve) => {
    const input = initial == null ? null : el("input", {
      class: "field", attrs: { "aria-label": message }, props: { value: initial },
    });
    const form = el("form", { attrs: { method: "dialog" } }, [
      el("p", { class: "q", text: message }),
      input,
      el("div", { class: "row" }, [
        el("button", { class: "act", attrs: { value: "cancel" }, text: "cancel" }),
        el("button", { class: "btn", attrs: { value: "ok" }, text: "ok" }),
      ]),
    ]);
    const dlg = el("dialog", { class: "ask" }, [form]);
    dlg.addEventListener("close", () => {
      const ok = dlg.returnValue === "ok"; // Escape closes with "" — a cancel
      const typed = input ? input.value.trim() : "";
      dlg.remove();
      resolve(input ? (ok && typed ? typed : null) : ok);
    });
    document.body.appendChild(dlg);
    dlg.showModal();
    if (input) input.select();
  });
}
function askText(message, initial) { return ask(message, initial == null ? "" : initial); }

/* ── state (the README's state model) ─────────────────────────────── */
const S = {
  screen: "configs",
  editId: null,

  // server snapshot
  status: null, configs: [], health: null, log: "", distros: [],
  yaml: "", yamlOf: null,

  // configurations screen
  find: "",
  busyId: null,            // config being activated
  stoppingId: null,        // config whose stop control was pressed
  err: null, errName: null, errKept: null,
  note: null, noteTimer: null,
  flash: null,             // key of the control showing its brief "saved" mark
  newOpen: false, newName: "", newUrl: "", newErr: null, fetching: false,
  confirm: null, confirmVerb: null, confirmId: null, confirmKind: null,
  presetSel: {},           // { configName: presetName }
  presetsOpenId: null,
  inline: null,            // { name, preset, isNew }
  inlineName: "",
  preflight: null,         // { name, preset, missing } — activation held for missing required values
  helpOpen: {},            // { page: true } while its help strip is open (opt-in, not persisted)

  // editor
  unlocked: false, unlockAsk: false, yamlOpen: false,
  preset: null, reveal: {},
  saving: false, valErr: null, valOk: null,
  renameNote: null,

  // collector
  query: "", level: "all", tail: true, restarting: false,

  // settings
  theme: "system",
  dl: {},                  // { distroName: {status, pct, error} }
  up: {},                  // { distroName: true } while its release check runs
  addName: "", addPath: "",
  settings: null,          // { grpc_port, http_port }
  portsSaved: false,       // "applies on the next restart" line showing
  resetArm: false,         // factory-reset inline confirm showing
  resetTyped: "",          // what's in its type-compy-to-confirm field
  resetBusy: false,        // reset request in flight

  // sidebar ports warning (status.conformance)
  adoptAsk: false,         // manual grpc/http assignment showing
  adoptSel: {},            // { grpc: port, http: port } chosen in it
  adopting: false,         // adopt request in flight
};

/* ── theme ────────────────────────────────────────────────────────────
   'system' (default) leaves the root attribute off so prefers-color-scheme
   decides and follows macOS live; an explicit choice stamps data-theme and
   is remembered. */
function loadTheme() {
  try { S.theme = localStorage.getItem("compy.theme") || "system"; } catch (e) { S.theme = "system"; }
}
function applyTheme() {
  if (S.theme === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", S.theme);
}
function setTheme(t) {
  S.theme = t;
  try { localStorage.setItem("compy.theme", t); } catch (e) { /* private mode */ }
  applyTheme();
  render();
}
function osTheme() {
  return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

/* ── derived helpers ──────────────────────────────────────────────── */
function slug(s) { return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""); }
function originOf(info) {
  if (info.provenance === "remote") return "url";
  if (info.provenance === "shipped") return "builtin";
  return "user";
}
function hostOf(info) {
  const u = (info.meta && info.meta.remote_url) || "";
  try { return new URL(u).host; } catch (e) { return u; }
}
function presetsOf(info) {
  // Info.meta.presets is a JSON object, so it carries no ordering; the
  // window sorts alphabetically, as the CLI does.
  return Object.keys((info.meta && info.meta.presets) || {}).sort();
}
function selectedPreset(info) {
  const list = presetsOf(info);
  const want = S.presetSel[info.name] || (info.meta && info.meta.active_preset);
  return list.indexOf(want) > -1 ? want : list[0] || "";
}
function byName(name) { return S.configs.find((c) => c.name === name) || null; }
/* The activation pre-flight rule, shared with the CLI's warning (cfgstore.
   MissingRequired): required means the yaml has no `:-fallback`
   (has_default false), the name isn't compy-injected (COMPY_*), and the
   preset holds no non-empty value. */
function missingRequired(info, preset) {
  const values = ((info.meta && info.meta.presets) || {})[preset] || {};
  return (info.vars || [])
    .filter((v) => !v.has_default && !/^COMPY_/.test(v.name) && !(values[v.name] || "").trim())
    .map((v) => v.name);
}
function isRunningCfg(name) {
  return !!(S.status && S.status.running && S.status.config === name);
}
function nothingActive() { return !(S.status && S.status.running); }
function activeName() {
  if (nothingActive() || !S.status.config) return "nothing active";
  return S.status.config;
}
function isSecret(key) { return /KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH/i.test(key); }
/* Ports honesty: only ports the collector process is ACTUALLY listening on
   (detected OS-side via lsof, /api/status "listening") are ever claimed —
   never a guess from settings or YAML. Nothing detected, nothing shown. */
function detectedPorts() { return (S.status && S.status.listening) || []; }
function portsCompact(ports) {
  if (ports.length > 4) return ports.length + " ports open";
  return ports.map((p) => ":" + p).join(" ");
}
/* What we know about a detected port: the settings grpc/http port, or the
   port the health scrape actually answered on. Anything else is bare. */
function portLabel(p) {
  const st = S.status || {};
  if (p === st.grpc_port) return "grpc";
  if (p === st.http_port) return "http";
  if (S.health && S.health.available && S.health.port === p) return "telemetry";
  return "";
}
/* The drop diagnosis (health.dropping): the backend enforces the honesty
   rule — present only when the running collector reports dropped > 0 AND
   the active config's effective preset is missing required values. Drops
   with values present carry no diagnosis, so the vars are never blamed for
   someone else's failure. */
function droppingVars() {
  return (S.health && S.health.dropping && S.health.dropping.vars) || [];
}
function droppingText(vars) {
  const list = vars.length === 1 ? vars[0]
    : vars.slice(0, -1).join(", ") + " and " + vars[vars.length - 1];
  return "dropping data — " + list + (vars.length === 1 ? " has" : " have") + " no value";
}
/* The pre-flight's "add values" action, reached from runtime evidence: the
   inline preset editor on the active config's running preset (a real one —
   every config keeps at least one). */
function openDroppingEditor() {
  const name = S.status && S.status.config;
  const info = byName(name);
  if (!info) return;
  const preset = (S.status && S.status.preset) || selectedPreset(info);
  openInline(name, preset, false);
  if (S.screen !== "configs") go("#/configs");
}
function fmtCount(n) {
  if (n == null) return "—";
  if (n >= 1000000) return (n / 1000000).toFixed(1).replace(/\.0$/, "") + "m";
  if (n >= 1000) return (n / 1000).toFixed(1).replace(/\.0$/, "") + "k";
  return String(n);
}

/* ── transient notes (the design's ~3s one-liners) ────────────────── */
function noteStrip() {
  if (!S.note) return null;
  return el("div", { class: "strip-wrap" }, [el("div", { class: "note", text: S.note })]);
}
function note(text, ms) {
  S.note = text;
  if (S.noteTimer) clearTimeout(S.noteTimer);
  S.noteTimer = setTimeout(() => { S.note = null; S.noteTimer = null; render(); }, ms || 3200);
  render();
}

/* ── the auto-save residue ────────────────────────────────────────────
   Controls that save on change (settings toggles, ports, a preset's value
   cards in the editor band) leave a brief muted "saved" beside the control
   that just PUT — one shared helper so timing and look are identical
   everywhere. Success paths only: a failed save shows its error and never
   the mark. */
let flashTimer = null;
function flashSaved(key) {
  S.flash = key;
  if (flashTimer) clearTimeout(flashTimer);
  flashTimer = setTimeout(() => { S.flash = null; flashTimer = null; render(); }, 1600);
  render();
}
function savedMark(key) { return S.flash === key ? span("savedmark sans", "saved") : null; }

/* ── failures with nowhere else to go ─────────────────────────────────
   The design gives activation and save failures their own panels, which is
   where the collector's diagnostic belongs. Everything else — a rename that
   collides, a distro that won't remove — lands here: message only for a 4xx
   (the caller's own mistake), message plus a log tail for a 5xx. */
let lastError = null;
async function showError(err) {
  const msg = err && err.message ? err.message : String(err);
  lastError = { msg, tail: "" };
  render();
  if (err && typeof err.status === "number" && err.status >= 500) {
    try {
      const j = await api("/api/log?lines=20");
      if (j.log && lastError && lastError.msg === msg) { lastError.tail = j.log; render(); }
    } catch (e) { /* best effort */ }
  }
}
function clearError() { lastError = null; }
function errorStrip() {
  if (!lastError) return null;
  return el("div", { class: "errbar" }, [
    el("div", { class: "failbar" }, [
      el("span", { class: "dot6", attrs: { style: "background: var(--err)" } }),
      span("msg", lastError.msg),
      el("span", { class: "grow" }),
      el("button", { class: "act", text: "dismiss", on: { click: () => { clearError(); render(); } } }),
    ]),
    lastError.tail ? el("pre", { text: lastError.tail }) : null,
  ]);
}

/* ── data loading ─────────────────────────────────────────────────── */
async function loadCore() {
  const [status, configs] = await Promise.all([api("/api/status"), api("/api/configs")]);
  S.status = status;
  S.configs = configs || [];
}
async function loadCollector() {
  const [health, log] = await Promise.all([
    api("/api/collector/health").catch(() => null),
    api("/api/log?lines=500").catch(() => ({ log: "" })),
  ]);
  S.health = health;
  S.log = (log && log.log) || "";
}
async function loadDistros() { S.distros = (await api("/api/distros")) || []; }
async function loadSettings() { S.settings = await api("/api/settings"); }
async function loadYAML(name) {
  const d = await api(cfgURL(name));
  S.yaml = d.yaml || "";
  S.yamlOf = name;
}

/* ── router ───────────────────────────────────────────────────────── */
function parseHash() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  if (parts[0] === "configs" && parts[1]) return { screen: "editor", name: decodeURIComponent(parts[1]) };
  if (parts[0] === "collector") return { screen: "collector" };
  if (parts[0] === "settings") return { screen: "settings" };
  return { screen: "configs" };
}
function go(hash) { location.hash = hash; }

async function enterRoute() {
  const r = parseHash();
  S.screen = r.screen;
  clearError();
  try {
    await loadCore();
    if (r.screen === "editor") {
      S.editId = r.name;
      const info = byName(r.name);
      if (!info) { go("#/configs"); return; }
      await loadYAML(r.name);
      const origin = originOf(info);
      S.unlocked = origin === "user";
      S.yamlOpen = origin === "user";
      S.unlockAsk = false;
      S.valErr = null; S.valOk = null; S.renameNote = null;
      S.preset = selectedPreset(info);
      destroyEditor();
    } else {
      S.editId = null;
      destroyEditor();
    }
    if (r.screen === "collector") await loadCollector();
    if (r.screen === "settings") {
      S.portsSaved = false; S.resetArm = false; S.resetTyped = "";
      await Promise.all([loadDistros(), loadSettings()]);
    }
    if (r.screen === "configs") await loadCollector(); // sidebar's warn badge
  } catch (e) {
    showError(e);
  }
  render();
}

/* Unsaved-changes guard: hashchange fires after the fact, so leaving with
   unsaved YAML (or an unsaved preset draft in the inline editor) either
   gets confirmed inline or the hash goes back (which re-fires hashchange —
   the first line swallows that one). */
let navHash = location.hash;
window.addEventListener("hashchange", async () => {
  if (location.hash === navHash) return;
  if (editorDirty() || inlineDirty()) {
    const target = location.hash;
    location.hash = navHash;
    const q = editorDirty() ? "leave this configuration? unsaved changes are lost."
      : "leave this screen? the unsaved preset draft is lost.";
    if (!(await ask(q, null))) return;
    cm && (cmDirty = false);
    S.inline = null; S.inlineDraft = null;
    location.hash = target;
    return;
  }
  navHash = location.hash;
  enterRoute();
});
window.addEventListener("beforeunload", (e) => {
  if (!editorDirty() && !inlineDirty()) return;
  e.preventDefault();
  e.returnValue = "leave this configuration? unsaved changes are lost.";
});

/* ── render ───────────────────────────────────────────────────────── */
const screenRoot = () => document.getElementById("screen");

// Focus survives a re-render: every live input carries data-fk, and the key
// plus caret of the focused one is restored afterwards. That is what lets a
// keystroke re-render the whole screen without eating the next one.
function captureFocus() {
  const a = document.activeElement;
  if (!a || !a.dataset || !a.dataset.fk) return null;
  return { fk: a.dataset.fk, start: a.selectionStart, end: a.selectionEnd };
}
function restoreFocus(f) {
  if (!f) return;
  const e = document.querySelector('[data-fk="' + f.fk.replace(/"/g, '\\"') + '"]');
  if (!e) return;
  e.focus();
  if (f.start != null && e.setSelectionRange) {
    try { e.setSelectionRange(f.start, f.end); } catch (err) { /* non-text input */ }
  }
}

function render() {
  const f = captureFocus();
  renderSidebar();
  const root = screenRoot();
  clear(root);
  if (S.screen === "configs") root.appendChild(screenConfigs());
  else if (S.screen === "editor") root.appendChild(screenEditor());
  else if (S.screen === "collector") root.appendChild(screenCollector());
  else root.appendChild(screenSettings());
  restoreFocus(f);
}

/* ── sidebar ──────────────────────────────────────────────────────── */
/* otelcol stderr is heterogeneous: zap console lines (ts \t level \t
   [caller \t] message [\t {json attrs}]) interleaved with the debug
   exporter's multi-line plain dumps. Group into entries: a line whose
   first two tab fields are a timestamp and a known level starts an entry;
   anything else is a continuation of the entry above (the dump keeps its
   parent's level for filtering). Total: unknown shapes become a bare
   entry, malformed JSON tails stay in the message text. */
function logEntries() {
  const entries = [];
  for (const line of S.log.split("\n")) {
    if (!line.trim()) continue;
    const e = parseZapLine(line);
    if (e) entries.push(e);
    else if (entries.length) {
      const p = entries[entries.length - 1];
      p.cont.push(line);
      p.raw += "\n" + line;
    } else entries.push({ time: "", level: "", text: line, caller: "", attrs: null, cont: [], raw: line });
  }
  return entries;
}
function parseZapLine(line) {
  const f = line.split("\t");
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d/.test(f[0] || "")) return null;
  const lvl = (f[1] || "").trim().toLowerCase();
  if (["error", "warn", "info", "debug"].indexOf(lvl) < 0) return null;
  let i = 2, caller = "";
  if (/\.go:\d+$/.test(f[2] || "")) { caller = f[2]; i = 3; }
  // A trailing {…} field is structured attrs — but only when a message
  // field precedes it, and only if it actually parses to an object.
  let end = f.length, attrs = null;
  if (f.length - i >= 2 && /^\{/.test(f[f.length - 1])) {
    attrs = parseAttrs(f[f.length - 1]);
    if (attrs) end = f.length - 1;
  }
  return {
    time: f[0].slice(11, 19), level: lvl, text: f.slice(i, end).join(" "),
    caller, attrs, cont: [], raw: line,
  };
}
function parseAttrs(s) {
  try {
    const o = JSON.parse(s);
    if (o && typeof o === "object" && !Array.isArray(o)) return o;
  } catch (_) { /* malformed tail stays in the message text */ }
  return null;
}
// D2, per-surface literal: the sidebar badge sums warn and error entries and
// labels the total "warn" (the menu bar's own count is warn-only; that
// surface is the tray's).
function issueCount() {
  return logEntries().filter((l) => l.level === "warn" || l.level === "error").length;
}

function renderSidebar() {
  const nav = document.getElementById("nav");
  clear(nav);
  const issues = issueCount();
  const items = [
    ["configs", "configurations", "list", String(S.configs.length), "#/configs"],
    ["collector", "collector", "activity", issues ? issues + " warn" : "", "#/collector"],
    ["settings", "settings", "sliders", "", "#/settings"],
  ];
  for (const [key, label, glyph, badge, hash] of items) {
    const on = S.screen === key || (key === "configs" && S.screen === "editor");
    nav.appendChild(el("button", {
      class: "nav-item" + (on ? " on" : ""), on: { click: () => go(hash) },
    }, [
      el("span", { class: "glyph" }, [icon(glyph, 14)]),
      span("", label),
      el("span", { class: "grow" }),
      span("badge", badge),
    ]));
  }

  const box = document.getElementById("side-status");
  clear(box);
  const stopped = nothingActive();
  const busy = !!S.busyId || S.restarting;
  const word = stopped && !busy ? "stopped" : busy ? "restarting…" : "running";
  const dotColor = busy ? "var(--accent)" : stopped ? "var(--dim2)" : "var(--ok)";
  box.appendChild(el("div", { class: "line1" }, [
    el("span", { class: "dot5", attrs: { style: "background: " + dotColor } }),
    span("", word),
  ]));
  box.appendChild(span("name", activeName()));
  // Stopped, the card already says "nothing active"; naming the preset that
  // is not running next to it just contradicts the line above. Running, the
  // preset is always a real one (every config keeps at least one).
  box.appendChild(span("preset", "preset · " + (stopped ? "—" : (S.status && S.status.preset) || "—")));
  // Only detected ports are claimed; stopped or nothing detected shows no
  // ports line at all. (The collector in use lives in settings, not here.)
  const ports = detectedPorts();
  if (!stopped && ports.length) box.appendChild(span("ports", portsCompact(ports)));

  /* The conformance verdict (status.conformance, present only while running
     with detection): nonconforming means an app following compy's advertised
     env would miss this collector — warn, and offer adopt. A conforming
     config whose grpc port just isn't bound gets only a soft addendum. */
  const v = !stopped && S.status ? S.status.conformance : null;
  if (v && !v.conforming) {
    box.appendChild(portsWarning(v));
  } else {
    S.adoptAsk = false;
    /* The secondary port — whichever one the advertised protocol's
       endpoint does NOT use — missing is only a soft addendum. */
    const grpcPrimary = S.status && S.status.protocol === "grpc";
    if (v && grpcPrimary && v.missing_http) {
      box.appendChild(span("pw-soft", "http :" + S.status.http_port + " not among this config's listeners"));
    } else if (v && !grpcPrimary && v.missing_grpc) {
      box.appendChild(span("pw-soft", "grpc :" + S.status.grpc_port + " not among this config's listeners"));
    }
  }

  // "runs but drops": health.dropping (both legs backend-verified). Shown
  // only where health is actually being refreshed, so the claim is current.
  const dvars = !stopped && (S.screen === "configs" || S.screen === "collector") ? droppingVars() : [];
  if (dvars.length) {
    box.appendChild(el("div", { class: "portwarn" }, [
      el("div", { class: "pw-line" }, [
        el("span", { class: "dot5", attrs: { style: "background: var(--err)" } }),
        span("", droppingText(dvars)),
      ]),
      el("button", { class: "act adopt", text: "add values", on: { click: openDroppingEditor } }),
    ]));
  }
}

function portsWarning(v) {
  const st = S.status;
  /* The advertised port follows the protocol: grpc advertises the grpc
     port, both http flavors the http one. */
  const grpcPrimary = st.protocol === "grpc";
  const advPort = grpcPrimary ? st.grpc_port : st.http_port;
  const advVar = grpcPrimary ? "COMPY_GRPC_PORT" : "COMPY_HTTP_PORT";
  const listens = v.actual && v.actual.length
    ? "this config listens on " + portsCompact(v.actual)
    : "this config opens no other ports";
  const wrap = el("div", { class: "portwarn" }, [
    el("div", { class: "pw-line" }, [
      el("span", { class: "dot5", attrs: { style: "background: var(--err)" } }),
      span("", "apps point at :" + advPort + " — " + listens),
    ]),
    el("span", { class: "pw-hint sans", text: "bind ${env:" + advVar + "} in the config, or:" }),
  ]);
  if (!S.adoptAsk) {
    wrap.appendChild(el("button", {
      class: "act adopt", text: S.adopting ? "adopting…" : "adopt this config's ports",
      attrs: S.adopting ? { disabled: "" } : null,
      on: { click: () => adoptPorts(null) },
    }));
    return wrap;
  }
  /* Ambiguous classification: never guess silently — the user says which
     detected port is which. */
  const opts = v.actual || [];
  const sel = (key) => {
    const s = el("select", {
      class: "field sm", attrs: { "data-fk": "adopt-" + key, "aria-label": "otlp/" + key + " port" },
      on: { change: (e) => { S.adoptSel[key] = parseInt(e.target.value, 10) || 0; render(); } },
    }, [el("option", { text: key + " —", attrs: { value: "" } })].concat(
      opts.map((p) => el("option", { text: key + " :" + p, attrs: { value: String(p) } }))));
    if (S.adoptSel[key]) s.value = String(S.adoptSel[key]);
    return s;
  };
  const ready = S.adoptSel.grpc && S.adoptSel.http && S.adoptSel.grpc !== S.adoptSel.http && !S.adopting;
  wrap.appendChild(el("span", { class: "pw-hint sans", text: "can't tell which port is which. assign them:" }));
  wrap.appendChild(el("div", { class: "pw-row" }, [sel("grpc"), sel("http")]));
  wrap.appendChild(el("div", { class: "pw-row" }, [
    el("button", { class: "act", text: "cancel", on: { click: () => { S.adoptAsk = false; render(); } } }),
    el("button", {
      class: "act adopt", text: S.adopting ? "adopting…" : "adopt",
      attrs: ready ? null : { disabled: "" },
      on: { click: () => adoptPorts({ grpc_port: S.adoptSel.grpc, http_port: S.adoptSel.http }) },
    }),
  ]));
  return wrap;
}

/* Adopt: no body lets the backend classify the detected ports (otlp/http
   speaks HTTP/1.1, grpc doesn't); a 400 on that path is the backend
   refusing to guess — open the manual assignment instead of an error. An
   explicit body is the user's own assignment and fails loudly. */
async function adoptPorts(body) {
  if (S.adopting) return;
  clearError();
  S.adopting = true;
  render();
  try {
    await apiJSON("/api/service/adopt-ports", "POST", body || {});
    S.adopting = false; S.adoptAsk = false; S.adoptSel = {};
    await loadCore();
    note("advertised ports now follow " + ((S.status && S.status.config) || "this config"), 4200);
  } catch (e) {
    S.adopting = false;
    const v = S.status && S.status.conformance;
    if (!body && e.status === 400 && v && v.actual && v.actual.length) {
      S.adoptAsk = true;
      render();
      return;
    }
    showError(e);
  }
  render();
}

/* ── screen 1: configurations ─────────────────────────────────────── */
function screenConfigs() {
  const wrap = el("div", { class: "screen" });

  // No subtitle: "activating restarts the collector" lives in the help
  // strip's copy now (2026-08-26 round 2).
  wrap.appendChild(el("div", { class: "head" }, [
    span("title", "configurations"),
    el("span", { class: "grow" }),
    el("span", { class: "find" }, [
      el("span", { class: "glyph" }, [icon("search", 12)]),
      el("input", {
        attrs: { placeholder: "find", spellcheck: "false", "data-fk": "find", "aria-label": "find a configuration" },
        props: { value: S.find },
        on: { input: (e) => { S.find = e.target.value; render(); } },
      }),
    ]),
    el("div", { attrs: { style: "display:flex; align-items:center; gap:18px; font-size:12px;" } }, [
      helpButton("configs"),
      el("button", { class: "act", text: "sync all", on: { click: syncAll } }),
      el("button", { class: "act primary", text: "new configuration", on: { click: openNew } }),
    ]),
  ]));

  const strips = el("div", { class: "strip-wrap" });
  const help = helpStrip("configs");
  if (help) strips.appendChild(help);
  if (S.newOpen) strips.appendChild(newConfigStrip());
  if (S.note) strips.appendChild(el("div", { class: "note", text: S.note }));
  if (nothingActive() && !S.busyId) strips.appendChild(nothingActiveStrip());
  if (S.err) strips.appendChild(failurePanel());
  if (lastError) strips.appendChild(errorStrip());
  wrap.appendChild(strips);

  const scroll = el("div", { class: "table-scroll" });
  scroll.appendChild(el("div", { class: "cfg-grid cfg-head colhead" }, [
    el("span"), span("", "name"), span("", "preset"), el("span"),
  ]));

  const find = S.find.trim().toLowerCase();
  const rows = S.configs.filter((c) => !find || c.name.toLowerCase().includes(find));
  for (const info of rows) scroll.appendChild(configRow(info));

  if (find && rows.length === 0) {
    scroll.appendChild(el("div", { class: "nomatch", text: "no configuration matches “" + S.find.trim() + "”" }));
  }
  if (S.confirm) scroll.appendChild(confirmRow());
  wrap.appendChild(scroll);
  return wrap;
}

/* Help strips: one per screen, OPT-IN — hidden until the header's help
   button opens it; the button (or the ✕) closes it again. Open state is
   in-memory only, so every load starts with no strips (2026-08-27,
   supersedes the shown-by-default rule from the copy round; the old
   compy.helpDismissed localStorage keys are simply ignored). */
const HELP_COPY = {
  configs: "pick a config that ships with compy, add a preset with your endpoint and key (the + button), then press play. activating restarts the collector. new configuration adds your own: paste yaml or fetch it from a url.",
  collector: "these numbers are the collector's own, scraped from its telemetry endpoint, and listening shows only ports the process actually has open. the log below is the collector's output, grouped by level and filterable. restart and stop live here; the configurations screen picks what runs.",
  settings: "appearance, and how apps find compy: the advertised endpoint, its protocol, and the system-wide OTEL_* toggle. global variables are values every configuration's yaml can reference; the collector table downloads, updates, or replaces the binary every config runs on. the danger area at the bottom deletes everything compy manages.",
  editor: "a configuration is one whole collector config.yaml plus its presets: named sets of values for the ${VAR} references in the yaml. configs built in to compy or fetched from a url guard their yaml; editing makes it yours, and it stops updating from its source. cmd+s saves, and the save button shows amber while anything is unsaved.",
};
function helpButton(page) {
  return el("button", {
    class: "ico help", title: "help",
    on: { click: () => { S.helpOpen[page] = !S.helpOpen[page]; render(); } },
  }, [icon("help", 14)]);
}
function helpStrip(page) {
  if (!S.helpOpen[page]) return null;
  return el("div", { class: "gethelp" }, [
    el("span", { class: "hicon" }, [icon("help", 14)]),
    el("span", { class: "b sans", text: HELP_COPY[page] }),
    el("span", { class: "grow" }),
    el("button", {
      class: "act x", text: "✕", title: "hide this",
      on: { click: () => { S.helpOpen[page] = false; render(); } },
    }),
  ]);
}

function nothingActiveStrip() {
  // One quiet line, no box, no suggestion button: a deliberately stopped
  // collector is a state, not a problem to nag about — the sidebar's
  // stopped card already carries it (2026-08-27, supersedes D1).
  return el("div", {
    class: "quietline sans",
    text: "nothing active. press play on a config to start the collector.",
  });
}

function failurePanel() {
  const name = S.errName;
  return el("div", { class: "fail" }, [
    el("div", { class: "failbar" }, [
      el("span", { class: "dot6", attrs: { style: "background: var(--err)" } }),
      span("headline", "couldn't activate " + name),
      el("span", { class: "grow" }),
      span("kept", S.errKept || ""),
      el("button", {
        class: "act", text: "dismiss", attrs: { style: "padding-left:10px" },
        on: { click: () => { S.err = null; render(); } },
      }),
    ]),
    el("pre", { text: S.err }),
    el("div", { class: "foot" }, [
      el("button", {
        class: "act open", text: "open in editor",
        on: { click: () => { const n = (S.errName || "").split(" · ")[0]; S.err = null; go("#/configs/" + enc(n)); } },
      }),
      el("button", { class: "act", text: "copy diagnostic", on: { click: () => copyText(S.err, "diagnostic copied") } }),
    ]),
  ]);
}

function confirmRow() {
  return el("div", { class: "confirm" }, [
    el("span", { class: "q sans", text: S.confirm }),
    el("span", { class: "grow" }),
    el("button", { class: "act", text: "keep it", on: { click: () => { S.confirm = null; render(); } } }),
    el("button", { class: "btn danger", text: S.confirmVerb, on: { click: runConfirm } }),
  ]);
}

function configRow(info) {
  const name = info.name;
  const origin = originOf(info);
  const running = isRunningCfg(name);
  const isActiveCfg = !!(S.status && S.status.config === name);
  const busy = S.busyId === name;
  const stopping = S.stoppingId === name;
  const list = presetsOf(info);
  const sel = selectedPreset(info);
  const many = list.length > 1;
  const host = hostOf(info);

  const typeIcon = { builtin: "package", user: "user", url: "link" }[origin];
  const typeTitle = origin === "url" ? "fetched from " + host
    : origin === "builtin" ? "built in to compy" : "yours";

  // The whole row opens the config editor (same action as the name) so the
  // dead space between columns is clickable; interactive children are
  // excluded by the closest() guard rather than per-child stopPropagation.
  const row = el("div", {
    class: "cfg-grid cfg-row" + (running ? " on" : ""),
    on: {
      click: (e) => {
        if (e.target.closest("button, input, .menu")) return;
        go("#/configs/" + enc(name));
      },
    },
  });

  row.appendChild(el("span", { class: "cell-icons" }, [
    iconWrap("run", running ? "dot" : "circle", 13, false, running ? "running now" : "not running"),
    iconWrap("type", typeIcon, 13, false, typeTitle),
  ]));

  row.appendChild(el("button", {
    class: "cfg-name", on: { click: () => go("#/configs/" + enc(name)) },
  }, [
    span("", name),
    // The row's ONE status slot (owner ruling, 2026-08-27): steady running
    // is the static word; an in-flight activation or stop is the busy word
    // + bar in the same place. The preset cell never carries status.
    busy || stopping
      ? el("span", { class: "busy" }, [
        span("word", busy ? "restarting…" : "stopping…"),
        el("span", { class: "bar" }, [el("i")]),
      ])
      : running ? span("runword", "running") : null,
  ]));

  /* preset cell: selector (chevron only when there is more than one),
     play, pencil, and the in-flight indicator. */
  const cell = el("span", { class: "cell-preset" });
  const selBtn = el("button", {
    class: "preset-sel" + (many ? " many" : ""),
    attrs: many ? { "aria-haspopup": "true" } : { tabindex: "-1" },
    on: { click: () => { if (many) { S.presetsOpenId = S.presetsOpenId === name ? null : name; render(); } } },
  }, [
    // Every config keeps at least one preset, so sel is always real.
    span("nm", sel),
    el("span", { class: "grow" }),
    many ? el("span", { class: "caret" }, [icon("chevron", 12)]) : null,
  ]);
  cell.appendChild(selBtn);

  const alreadyRunning = running && sel === (S.status && S.status.preset);
  // While an activation or stop is in flight every OTHER play is locked
  // out — install/probe can't be safely cancelled, so the ruling is
  // greying, not aborting; the in-flight row keeps its busy indicator.
  const locked = inflight() && !busy && !stopping;
  if (alreadyRunning) {
    // The running row's play slot is a stop control — the collector-screen
    // stop flow (no confirm there, none here), err-toned like delete.
    cell.appendChild(el("button", {
      class: "play stop",
      title: stopping ? "stopping…" : locked ? inflightTitle() : "stop the collector",
      attrs: stopping || locked ? { disabled: "" } : null,
      on: { click: () => stopFromRow(name) },
    }, [icon("square", 11, true)]));
  } else {
    cell.appendChild(el("button", {
      class: "play",
      title: locked ? inflightTitle() : "activate " + name + " · " + sel,
      attrs: busy || locked ? { disabled: "" } : null,
      on: { click: () => preflightActivate(name, sel) },
    }, [icon("play", 11, true)]));
  }
  // A pencil here read as "edit the config"; the row-level icon is now a
  // plus that adds a preset (the per-preset pencil lives in the dropdown).
  cell.appendChild(el("button", {
    class: "addp", title: "add a preset",
    on: { click: () => openInline(name, "", true) },
  }, [icon("plus", 12)]));

  if (S.presetsOpenId === name) cell.appendChild(presetMenu(info, list));
  row.appendChild(cell);

  /* actions: always all three, greyed with an explaining title when they
     don't apply — never hidden. */
  const sync = syncAction(info, origin, host);
  row.appendChild(el("span", { class: "cell-actions" }, [
    el("button", {
      class: "ico", title: "duplicate, including presets",
      on: { click: () => duplicate(name) },
    }, [icon("copy", 13)]),
    el("button", {
      class: "ico" + (sync.on ? " accent" : ""), title: sync.title,
      attrs: sync.on ? null : { disabled: "" },
      on: { click: sync.run },
    }, [icon(origin === "builtin" ? "undo" : "refresh", 13)]),
    el("button", {
      class: "ico del", title: isActiveCfg ? "can't delete the running config" : "delete " + name,
      attrs: isActiveCfg ? { disabled: "" } : null,
      on: {
        click: () => {
          S.confirm = "delete " + name + " and its presets?";
          S.confirmVerb = "delete"; S.confirmId = name; S.confirmKind = "delete";
          render();
        },
      },
    }, [icon("trash", 13)]),
  ]));

  const wrap = el("div", { class: "cfg-row-wrap" }, [row]);
  if (S.preflight && S.preflight.name === name) wrap.appendChild(preflightPanel(info));
  if (S.inline && S.inline.name === name) wrap.appendChild(inlinePresetEditor(info));
  return wrap;
}

function syncAction(info, origin, host) {
  if (origin === "builtin") {
    if (!info.modified) return { on: false, title: "this is the shipped version, nothing to reset", run: () => {} };
    return {
      on: true, title: "reset to the version that ships with compy",
      run: () => {
        S.confirm = "reset " + info.name + " to the version that ships with compy? your changes are lost.";
        S.confirmVerb = "reset"; S.confirmId = info.name; S.confirmKind = "reset";
        render();
      },
    };
  }
  if (origin !== "url") return { on: false, title: "yours from the start, nothing to return to", run: () => {} };
  if (!info.modified) return { on: false, title: "in sync with " + host, run: () => {} };
  return {
    on: true, title: "discard my edits and re-sync from " + host,
    run: () => {
      S.confirm = "re-syncing " + info.name + " throws away your edits.";
      S.confirmVerb = "discard & re-sync"; S.confirmId = info.name; S.confirmKind = "resync";
      render();
    },
  };
}

function presetMenu(info, list) {
  const menu = el("div", { class: "menu" });
  for (const p of list) {
    const on = isRunningCfg(info.name) && p === S.status.preset;
    menu.appendChild(el("div", { class: "menu-row" }, [
      el("button", {
        class: "pick" + (on ? " on" : ""), text: (on ? "● " : "") + p,
        on: { click: () => { S.presetSel[info.name] = p; S.presetsOpenId = null; render(); } },
      }),
      el("button", {
        class: "mini accent", title: inflight() ? inflightTitle() : "activate this preset",
        attrs: inflight() ? { disabled: "" } : null,
        on: { click: () => { S.presetsOpenId = null; preflightActivate(info.name, p); } },
      }, [icon("play", 11, true)]),
      el("button", {
        class: "mini", title: "edit this preset",
        on: { click: () => openInline(info.name, p, false) },
      }, [icon("pencil", 12)]),
    ]));
  }
  menu.appendChild(el("button", {
    class: "menu-add", text: "+ add preset", title: "add a preset",
    on: { click: () => openInline(info.name, "", true) },
  }));
  return menu;
}

/* ── inline preset editor (the pencil, under its row) ─────────────── */
/* The generated new-preset name: "default" is every config's invariant
   first preset, so the scheme continues it — preset-2, preset-3, … —
   first free wins. Never random; the field stays editable. */
function freePresetName(list) {
  for (let i = 2; ; i++) if (list.indexOf("preset-" + i) < 0) return "preset-" + i;
}
function openInline(name, preset, isNew) {
  // A new preset opens with a generated available name already in the
  // field, so plus → save works with zero typing; gen is what the name
  // field opened with, which is what dirtiness is measured against.
  const gen = isNew ? freePresetName(presetsOf(byName(name) || { meta: {} })) : "";
  S.inline = { name, preset, isNew, gen };
  S.inlineName = isNew ? gen : preset;
  S.inlineDraft = null; // never inherit another preset's half-edited draft
  S.presetsOpenId = null;
  render();
}
/* Dirty = the draft differs from what the store holds: a value changed, or
   the name field left its opening state. Drives the save button's accent
   and the navigation guard. */
function inlineDirty() {
  const p = S.inline;
  if (!p || !S.inlineDraft) return false;
  const info = byName(p.name);
  if (!info) return false;
  if (S.inlineName !== (p.isNew ? p.gen : p.preset)) return true;
  const base = ((info.meta.presets || {})[p.isNew ? selectedPreset(info) : p.preset]) || {};
  const keys = Object.keys(Object.assign({}, base, S.inlineDraft));
  return keys.some((k) => (base[k] || "") !== (S.inlineDraft[k] || ""));
}
/* Typing in the draft doesn't re-render (the render would rebuild the
   focused field), so the save button is flipped in place — the same idiom
   as the factory-reset confirm field. */
function inlineSaveSync() {
  const b = document.querySelector(".inline .inline-save");
  if (!b) return;
  const dirty = inlineDirty();
  const isNew = !!(S.inline && S.inline.isNew);
  b.textContent = dirty || isNew ? "save preset" : "saved";
  b.classList.toggle("accent", dirty);
  // A new preset is saveable as opened (the generated name is real);
  // amber only once something is actually changed, per the save model.
  if (dirty || isNew) b.removeAttribute("disabled"); else b.setAttribute("disabled", "");
}
function inlinePresetEditor(info) {
  const p = S.inline;
  const values = ((info.meta.presets || {})[p.isNew ? selectedPreset(info) : p.preset]) || {};
  const draft = S.inlineDraft || (S.inlineDraft = Object.assign({}, values));
  const dirty = inlineDirty();
  return el("div", { class: "inline" }, [
    el("div", { class: "top" }, [
      span("colhead", p.isNew ? "new preset" : "preset"),
      el("input", {
        class: "field sm",
        attrs: { placeholder: "preset name", spellcheck: "false", "data-fk": "inline-name", style: "width:180px", "aria-label": "preset name" },
        props: { value: S.inlineName },
        on: { input: (e) => { S.inlineName = e.target.value; inlineSaveSync(); } },
      }),
      el("span", { class: "grow" }),
      el("button", { class: "act", text: "cancel", on: { click: () => { S.inline = null; S.inlineDraft = null; render(); } } }),
      el("button", {
        // Accent "save preset" while the draft differs from the stored
        // values; muted, disabled "saved" once they match. A new preset is
        // saveable from the start — the generated name is a real one — but
        // only goes amber once something is edited (the save model).
        class: "btn inline-save" + (dirty ? " accent" : ""),
        text: dirty || p.isNew ? "save preset" : "saved",
        attrs: dirty || p.isNew ? null : { disabled: "" },
        on: { click: () => saveInline(info) },
      }),
    ]),
    valueCards(info, draft, (k, v) => { draft[k] = v; inlineSaveSync(); }, "inline"),
    el("div", { class: "hint sans", text: p.isNew
      ? "starts from the current values. saving does not activate it."
      : "saving does not restart the collector unless this preset is running." }),
  ]);
}
async function saveInline(info) {
  const p = S.inline;
  const target = slug(S.inlineName) || p.preset;
  if (!target) { showError(new Error("a preset needs a name")); return; }
  if ((p.isNew || target !== p.preset) && presetsOf(info).indexOf(target) > -1) { note("a preset called " + target + " already exists", 3000); return; }
  const values = S.inlineDraft || {};
  try {
    // Rename before writing values: PUT-to-target-first would create the
    // target as a duplicate (or clobber an existing preset's values) and
    // then fail the rename against it.
    if (!p.isNew && target !== p.preset) await apiJSON(cfgURL(info.name) + "/presets/" + enc(p.preset) + "/rename", "POST", { to: target });
    await apiJSON(cfgURL(info.name) + "/presets/" + enc(target), "PUT", { values });
    S.inline = null; S.inlineDraft = null;
    S.presetSel[info.name] = target;
    await loadCore();
    note(target + " saved", 3000);
  } catch (e) { showError(e); }
}

/* Value cards, fixed 3 per row: bare key + its description as tooltip,
   origin hint right, then the value with a reveal/hide toggle for secrets.
   The origin hint reads "default" when the YAML supplies a fallback and
   "line N" when it does not — that is what the reference render shows. */
function valueCards(info, values, onEdit, scope) {
  const yaml = S.yamlOf === info.name ? S.yaml : "";
  const grid = el("div", { class: "vals" });
  for (const v of info.vars || []) {
    if (/^COMPY_/.test(v.name)) continue; // compy's own ports, not the user's to set
    const raw = values[v.name] || "";
    const secret = isSecret(v.name);
    const hidden = secret && !S.reveal[v.name] && raw;
    grid.appendChild(el("div", { class: "val" }, [
      el("div", { class: "k" }, [
        el("span", { class: "name", text: v.name, title: v.description || v.name }),
        el("span", { class: "grow" }),
        // The editor band auto-saves (queueValue); its card carries the
        // brief "saved" mark. The inline editor saves via its button.
        scope === "ed" ? savedMark("ed:" + v.name) : null,
        span("origin", v.has_default ? "default" : yamlLineOf(yaml, v.name)),
      ]),
      el("div", { class: "v" }, [
        el("input", {
          class: "field",
          attrs: {
            spellcheck: "false", "aria-label": v.name,
            placeholder: v.has_default ? v.default : "required, no default",
            "data-fk": scope + ":" + v.name,
          },
          props: { value: hidden ? "•".repeat(Math.min(raw.length, 18)) : raw },
          on: {
            focus: (e) => { if (hidden) { S.reveal[v.name] = true; render(); } },
            input: (e) => onEdit(v.name, e.target.value),
          },
        }),
        secret ? el("button", {
          class: "reveal", text: S.reveal[v.name] ? "hide" : "reveal",
          on: { click: () => { S.reveal[v.name] = !S.reveal[v.name]; render(); } },
        }) : null,
      ]),
    ]));
  }
  return grid;
}
function yamlLineOf(yaml, key) {
  if (!yaml) return "";
  const lines = yaml.split("\n");
  for (let i = 0; i < lines.length; i++) if (lines[i].includes("${env:" + key) || lines[i].includes("${" + key)) return "line " + (i + 1);
  return "";
}

/* ── configurations: actions ──────────────────────────────────────── */
/* Every activation on this screen goes through the pre-flight: a config
   whose required values are missing would start a collector that silently
   drops everything (its exporter has nowhere to send), so ask first — an
   inline panel under the row, never a dialog. Nothing missing → activate
   immediately, zero friction. */
// A stop is an in-flight action exactly like an activation: while either
// runs, every other play greys out and further requests are ignored.
function inflight() { return !!(S.busyId || S.stoppingId); }
function inflightTitle() {
  return S.busyId ? "activating " + S.busyId + "…, wait for it to finish"
    : "stopping " + S.stoppingId + "…, wait for it to finish";
}
function preflightActivate(name, preset) {
  if (inflight()) return; // every entry button is disabled; belt for stragglers
  const info = byName(name);
  const missing = info ? missingRequired(info, preset) : [];
  if (!missing.length) { activate(name, preset); return; }
  S.preflight = { name, preset, missing };
  S.presetsOpenId = null;
  render();
}
function preflightPanel(info) {
  const p = S.preflight;
  const names = p.missing;
  const list = names.length === 1 ? names[0]
    : names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
  return el("div", { class: "preflight" }, [
    el("span", { class: "q sans", text: p.name + " needs " + list + " before it can send anywhere. add a preset with values, or activate anyway." }),
    el("span", { class: "grow" }),
    el("button", { class: "act", text: "cancel", on: { click: () => { S.preflight = null; render(); } } }),
    el("button", {
      class: "btn", text: "add values",
      on: {
        click: () => {
          // The existing inline preset editor, on the real preset this
          // activation would use (every config keeps at least one).
          S.preflight = null;
          openInline(p.name, p.preset, false);
        },
      },
    }),
    el("button", {
      class: "btn accent", text: "activate anyway",
      title: inflight() ? inflightTitle() : null,
      attrs: inflight() ? { disabled: "" } : null,
      on: { click: () => { S.preflight = null; activate(p.name, p.preset); } },
    }),
  ]);
}
/* The row's stop control: the collector screen's stop flow (POST
   /api/service/stop, no confirmation there either), plus the row's
   in-flight state so the status slot says "stopping…" and other plays
   grey out for the duration. */
async function stopFromRow(name) {
  if (inflight()) return;
  S.stoppingId = name;
  clearError();
  render();
  try { await api("/api/service/stop", { method: "POST" }); } catch (e) { showError(e); }
  S.stoppingId = null;
  await loadCore(); await loadCollector();
  render();
}

async function activate(name, preset) {
  if (inflight()) return; // further activations are ignored until this settles
  S.busyId = name;
  S.err = null; S.presetsOpenId = null; S.preflight = null;
  clearError();
  render();
  const info = byName(name);
  const label = info && presetsOf(info).length > 1 && preset ? name + " · " + preset : name;
  try {
    await apiJSON(cfgURL(name) + "/activate", "POST", { preset: preset || "" });
    S.busyId = null;
    S.presetSel[name] = preset;
    await loadCore();
    await loadCollector();
    // The success path stays honest about reachability: activated, yes —
    // but a config that ignores compy's advertised ports strands every app
    // that trusts them, and that is worth a sentence right now.
    const v = S.status && S.status.conformance;
    if (v && !v.conforming) {
      note("activated, but this config doesn't listen on compy's ports, so apps using compy env won't reach it", 7000);
    }
  } catch (e) {
    S.busyId = null;
    // C1.14: the panel shows the collector's real diagnostic, never a
    // canned string. still_running names what survived when the backend
    // knows; it is absent when a restore itself failed.
    S.err = e.message || String(e);
    S.errName = label;
    await loadCore();
    S.errKept = e.stillRunning ? e.stillRunning + " still running"
      : nothingActive() ? "collector still stopped"
        : activeName() + " still running";
  }
  render();
}

async function duplicate(name) {
  let dst = name + "-copy", i = 2;
  while (byName(dst)) { dst = name + "-copy-" + i; i++; }
  try {
    await apiJSON(cfgURL(name) + "/copy", "POST", { dst });
    await loadCore();
    render();
  } catch (e) { showError(e); }
}

async function runConfirm() {
  const name = S.confirmId, kind = S.confirmKind;
  S.confirm = null; S.confirmId = null; S.confirmKind = null;
  render();
  try {
    if (kind === "delete") {
      await api(cfgURL(name), { method: "DELETE" });
      await loadCore();
    } else if (kind === "resync") {
      await api(cfgURL(name) + "/resync", { method: "POST" });
      await loadCore();
      note(name + " re-synced from " + hostOf(byName(name) || { meta: {} }));
    } else if (kind === "reset") {
      await api(cfgURL(name) + "/reset", { method: "POST" });
      await loadCore();
      note(name + " reset to the version that ships with compy");
    }
  } catch (e) { showError(e); }
  render();
}

async function syncAll() {
  clearError();
  try {
    const r = await api("/api/configs/sync-all", { method: "POST" });
    const n = (r.synced || []).length;
    await loadCore();
    note(n ? n + " configuration" + (n === 1 ? "" : "s") + " re-synced" : "nothing to sync");
  } catch (e) { showError(e); }
}

/* "empty means a blank config" has to mean the same thing here as it does
   in `compy config create` (cmd/compy's blankConfig): enough shape to edit,
   on compy's own ports. An actually-empty file is not a config the
   collector will ever start. */
const BLANK_CONFIG = [
  "receivers:",
  "  otlp:",
  "    protocols:",
  "      grpc:",
  "        endpoint: 127.0.0.1:${env:COMPY_GRPC_PORT:-14317}",
  "      http:",
  "        endpoint: 127.0.0.1:${env:COMPY_HTTP_PORT:-14318}",
  "exporters:",
  "  debug:",
  "service:",
  "  pipelines:",
  "    traces: {receivers: [otlp], exporters: [debug]}",
  "    metrics: {receivers: [otlp], exporters: [debug]}",
  "    logs: {receivers: [otlp], exporters: [debug]}",
  "",
].join("\n");

function openNew() {
  S.newOpen = true; S.newName = ""; S.newUrl = ""; S.newErr = null; S.fetching = false;
  render();
}
function newConfigStrip() {
  const s = slug(S.newName);
  const taken = !!(s && byName(s));
  const slugNote = !S.newName ? "lowercase, digits, dashes" : taken ? s + " already exists" : "saved as " + s;
  return el("div", { class: "newcfg" }, [
    el("div", { class: "fieldset name" }, [
      span("lbl", "name"),
      el("input", {
        class: "field",
        attrs: { placeholder: "my collector", spellcheck: "false", "data-fk": "new-name", "aria-label": "name" },
        props: { value: S.newName },
        on: { input: (e) => { S.newName = e.target.value; S.newErr = null; render(); } },
      }),
      el("span", { class: "foot" + (taken ? " bad" : ""), text: slugNote }),
    ]),
    el("div", { class: "fieldset url" }, [
      span("lbl", "from url (optional)"),
      el("input", {
        class: "field",
        attrs: { placeholder: "https://otel.acme.dev/configs/standard.yaml", spellcheck: "false", "data-fk": "new-url", "aria-label": "from url" },
        props: { value: S.newUrl },
        on: { input: (e) => { S.newUrl = e.target.value; S.newErr = null; render(); } },
      }),
      el("span", {
        class: "foot" + (S.newErr ? " bad" : ""),
        text: S.fetching ? "fetching…" : S.newErr || "empty means a blank config",
      }),
    ]),
    el("button", { class: "act cancel", text: "cancel", on: { click: () => { S.newOpen = false; render(); } } }),
    el("button", {
      class: "btn accent create", text: S.fetching ? "fetching…" : "create",
      attrs: S.fetching ? { disabled: "" } : null,
      on: { click: createNew },
    }),
  ]);
}
async function createNew() {
  const name = slug(S.newName);
  if (!name || byName(name)) return;
  const url = S.newUrl.trim();
  if (!url) {
    try {
      await apiJSON("/api/configs", "POST", { name, yaml: BLANK_CONFIG });
      S.newOpen = false; S.newName = ""; S.newUrl = "";
      await loadCore();
      render();
    } catch (e) { showError(e); }
    return;
  }
  S.fetching = true; S.newErr = null; render();
  try {
    await apiJSON("/api/configs/from-url", "POST", { name, url });
    S.fetching = false; S.newOpen = false; S.newName = ""; S.newUrl = "";
    await loadCore();
    let host = url;
    try { host = new URL(url).host; } catch (e) { /* keep the raw string */ }
    note(name + " fetched from " + host);
  } catch (e) {
    // The status code is the collector's/server's, not ours to invent; the
    // sentence around it is the design's.
    S.fetching = false;
    const code = (e.message || "").match(/HTTP (\d{3})/);
    S.newErr = code ? code[1] + " · nothing at that URL. compy kept nothing." : e.message;
    render();
  }
}

async function copyText(text, confirmation) {
  try {
    if (navigator.clipboard) await navigator.clipboard.writeText(text);
    note(confirmation, 2600);
  } catch (e) { showError(new Error("could not reach the clipboard")); }
}

/* ── screen 2: configuration editor ───────────────────────────────── */
let cm = null, cmDirty = false;
function destroyEditor() { cm = null; cmDirty = false; }
function editorDirty() { return S.screen === "editor" && !!cm && cmDirty; }
// Flip the header save button in place on a cm keystroke — re-rendering the
// screen would rebuild CodeMirror under the caret.
function edSaveSync() {
  const b = document.querySelector(".ed-save");
  if (!b || S.saving) return;
  b.textContent = cmDirty ? "save" : "saved";
  b.classList.toggle("accent", cmDirty);
  if (cmDirty) b.removeAttribute("disabled"); else b.setAttribute("disabled", "");
}

function screenEditor() {
  const info = byName(S.editId);
  if (!info) return el("div", { class: "screen" });
  const origin = originOf(info);
  const host = hostOf(info);
  const running = isRunningCfg(info.name);
  const locked = origin !== "user" && !S.unlocked;
  const yamlShown = origin === "user" || S.yamlOpen;
  const list = presetsOf(info);
  if (list.indexOf(S.preset) < 0) S.preset = list[0];

  const wrap = el("div", { class: "screen" });

  /* header, one line — the origin lives on the type icon, the URL field and
     the reset button; there is no second line and no origin strip. */
  const showReset = origin === "url" || (origin === "builtin" && info.modified);
  const resetLabel = origin === "builtin" ? "reset to shipped" : info.modified ? "discard edits & re-sync" : "re-sync";
  wrap.appendChild(el("div", { class: "ed-head" }, [
    running ? iconWrap("ed-run", "dot", 13, false, "running now") : null,
    iconWrap("ed-type", { builtin: "package", user: "user", url: "link" }[origin], 14, false,
      origin === "url" ? "fetched from " + host
        : origin === "builtin" ? "built in to compy. updates with it until you edit."
          : "yours"),
    el("input", {
      class: "ed-name",
      attrs: { spellcheck: "false", "data-fk": "ed-name", "aria-label": "configuration name" },
      props: { value: info.name },
      on: {
        // Pending rename is visible: the field goes accent while it differs
        // from the current name; enter or focus-out applies it (change).
        input: (e) => e.target.classList.toggle("pending", slug(e.target.value) !== info.name),
        change: (e) => renameConfig(info, e.target.value),
      },
    }),
    origin === "url" ? el("input", {
      class: "ed-url", title: "source URL",
      attrs: { spellcheck: "false", "data-fk": "ed-url", "aria-label": "source URL" },
      props: { value: (info.meta && info.meta.remote_url) || "" },
      on: { change: (e) => setRemoteURL(info, e.target.value) },
    }) : null,
    el("span", { class: "grow" }),
    S.saving ? el("span", { class: "ed-hint busy-word", text: "asking the collector…" })
      : S.renameNote ? span("ed-hint", S.renameNote) : null,
    helpButton("editor"),
    showReset ? el("button", {
      class: "btn withicon", title: origin === "url" && !info.modified ? "in sync with " + host : "your version",
      on: { click: () => headerResync(info, origin) },
    }, [icon(origin === "builtin" ? "undo" : "refresh", 12), span("", resetLabel)]) : null,
    el("button", {
      // The button carries the yaml's save state: accent "save" while the
      // editor is dirty, muted disabled "saved" while there is nothing to
      // save, "checking…" while the collector is asked. Keystrokes flip it
      // in place via edSaveSync (a cm change doesn't re-render).
      class: "btn ed-save" + (cmDirty && !S.saving ? " accent" : ""),
      text: S.saving ? "checking…" : cmDirty ? "save" : "saved",
      attrs: S.saving || !cmDirty ? { disabled: "" } : null,
      on: { click: () => saveConfig(info) },
    }),
  ]));

  const ehelp = helpStrip("editor");
  if (ehelp) wrap.appendChild(el("div", { class: "strip-wrap" }, [ehelp]));
  const noteEl = noteStrip();
  if (noteEl) wrap.appendChild(noteEl);

  /* presets band */
  const band = el("div", { class: "band" });
  const chips = el("div", { class: "chips" }, [span("colhead", "presets")]);
  for (const p of list) {
    const on = p === S.preset;
    const isRunningPreset = running && p === S.status.preset;
    const last = list.length < 2;
    const delTitle = last ? "a config always keeps one preset"
      : isRunningPreset ? "this preset is running. activate another one first."
        : "delete " + p;
    chips.appendChild(el("span", { class: "chip" }, [
      on ? el("span", { class: "dot5", attrs: { style: "background: var(--accent)" } }) : null,
      on ? el("input", {
        title: "rename this preset",
        attrs: { spellcheck: "false", size: Math.max(p.length, 4), "data-fk": "chip:" + p, "aria-label": "rename this preset" },
        props: { value: p },
        on: { change: (e) => renamePreset(info, p, e.target.value) },
      }) : el("button", { class: "pick", text: p, on: { click: () => { S.preset = p; S.presetSel[info.name] = p; render(); } } }),
      el("button", { class: "mini", title: "duplicate this preset", on: { click: () => dupPreset(info, p) } }, [icon("copy", 12)]),
      el("button", {
        class: "mini del", title: delTitle,
        attrs: last || isRunningPreset ? { disabled: "" } : null,
        on: { click: () => delPreset(info, p) },
      }, [icon("trash", 12)]),
    ]));
  }
  chips.appendChild(el("button", { class: "chip-add", title: "add a preset", on: { click: () => addPreset(info) } }, [icon("plus", 13)]));
  band.appendChild(chips);

  const values = ((info.meta.presets || {})[S.preset]) || {};
  band.appendChild(valueCards(info, values, (k, v) => queueValue(info, k, v), "ed"));

  /* Row three appears only when something is wrong, and it is a real "is
     any required value missing" check — the key's own name, not a
     name-specific special case. */
  const missing = missingRequired(info, S.preset);
  if (S.preset && missing.length) {
    band.appendChild(el("div", { class: "warn sans", text: S.preset + " has no " + missing.join(", ") + ". activating with it will fail." }));
  }
  wrap.appendChild(band);

  /* save results, at screen level — visible whether the YAML is open or not */
  if (S.valOk) {
    wrap.appendChild(el("div", { class: "okstrip" }, [el("span", { class: "dot5" }), span("", S.valOk)]));
  }
  if (S.valErr) {
    wrap.appendChild(el("div", { class: "valfail" }, [
      el("div", { class: "bar2" }, [
        el("span", { class: "dot5", attrs: { style: "background: var(--err)" } }),
        span("headline", "the collector rejected this config. nothing was saved."),
        el("span", { class: "grow" }),
        el("button", { class: "act", text: "copy", on: { click: () => copyText(S.valErr, "diagnostic copied") } }),
        el("button", { class: "act", text: "dismiss", on: { click: () => { S.valErr = null; render(); } } }),
      ]),
      el("pre", { text: S.valErr }),
    ]));
  }
  if (lastError) wrap.appendChild(el("div", { class: "strip-wrap" }, [errorStrip()]));

  /* YAML: collapsed by default for built-in and linked configs */
  if (!yamlShown) {
    wrap.appendChild(el("div", { class: "yaml-collapsed" }, [
      span("nm", "config.yaml"),
      el("span", {
        class: "why sans",
        text: origin === "url" ? "kept in sync with " + host : "ships with compy. most people never open it.",
      }),
      el("span", { class: "grow" }),
      el("button", { class: "btn quiet", text: "show yaml", on: { click: () => { S.yamlOpen = true; render(); } } }),
    ]));
    return wrap;
  }

  const pane = el("div", { class: "yaml-pane" });
  pane.appendChild(el("div", { class: "yaml-bar" }, [
    span("colhead", "config.yaml"),
    el("span", { class: "grow" }),
    locked ? el("span", { class: "ro" }, [
      span("word", "read-only"),
      el("button", { class: "act unlock", text: "edit anyway", on: { click: () => { S.unlockAsk = true; render(); } } }),
    ]) : null,
  ]));
  if (S.unlockAsk) {
    pane.appendChild(el("div", { class: "unlock-ask" }, [
      el("span", {
        class: "q sans",
        text: origin === "url"
          ? "editing disconnects this from " + host + ". it stops re-syncing."
          : "your version stays through compy updates.",
      }),
      el("span", { class: "grow" }),
      el("button", { class: "act", text: "cancel", on: { click: () => { S.unlockAsk = false; render(); } } }),
      el("button", { class: "btn accent", text: "make it mine", on: { click: () => { S.unlockAsk = false; S.unlocked = true; render(); } } }),
    ]));
  }
  const host2 = el("div", { class: "cm-host" });
  pane.appendChild(host2);
  wrap.appendChild(pane);

  // CodeMirror measures itself, so build it once the host is in the document.
  const yaml = S.yaml;
  const editable = !locked;
  queueMicrotask(() => {
    if (!host2.isConnected) return;
    const keep = cm ? cm.getValue() : null;
    const dirty = cmDirty;
    cm = CodeMirror(host2, {
      value: dirty && keep != null ? keep : yaml,
      mode: "yaml", lineNumbers: true, readOnly: !editable, lineWrapping: false, viewportMargin: 20,
    });
    cmDirty = dirty;
    cm.on("change", () => { cmDirty = true; edSaveSync(); });
  });
  return wrap;
}

/* value edits are instant (everything but activate/restart/save is), and
   land on the preset they belong to. */
let valueTimer = null;
function queueValue(info, key, value) {
  const preset = S.preset;
  if (!preset) return;
  const values = Object.assign({}, (info.meta.presets || {})[preset] || {});
  values[key] = value;
  info.meta.presets[preset] = values; // keep the render in step with the field
  if (valueTimer) clearTimeout(valueTimer);
  valueTimer = setTimeout(async () => {
    try {
      await apiJSON(cfgURL(info.name) + "/presets/" + enc(preset), "PUT", { values });
      await loadCore();
      flashSaved("ed:" + key);
    } catch (e) { showError(e); }
  }, 500);
}

async function renameConfig(info, raw) {
  const next = slug(raw);
  if (!next || next === info.name) { render(); return; }
  if (byName(next)) { S.renameNote = next + " already exists. name not changed."; render(); return; }
  S.renameNote = null;
  try {
    await apiJSON(cfgURL(info.name) + "/rename", "POST", { to: next });
    await loadCore();
    go("#/configs/" + enc(next)); // enterRoute reloads the editor under the new name
  } catch (e) { showError(e); }
  render();
}
async function setRemoteURL(info, url) {
  try {
    await apiJSON(cfgURL(info.name) + "/meta", "PUT", { remote_url: url });
    await loadCore();
    note("source url saved", 3000);
  } catch (e) { showError(e); }
}
async function headerResync(info, origin) {
  if (origin === "builtin") {
    S.confirm = "reset " + info.name + " to the version that ships with compy? your changes are lost.";
    S.confirmVerb = "reset"; S.confirmId = info.name; S.confirmKind = "reset";
    go("#/configs");
    return;
  }
  try {
    await api(cfgURL(info.name) + (info.modified ? "/resync" : "/sync"), { method: "POST" });
    await loadCore();
    await loadYAML(info.name);
    S.unlocked = false; destroyEditor();
    note(info.name + " re-synced from " + hostOf(info));
  } catch (e) { showError(e); }
}

async function addPreset(info) {
  const n = freePresetName(presetsOf(info)); // same scheme as the configs-row plus
  try {
    await apiJSON(cfgURL(info.name) + "/presets/" + enc(n), "PUT", { values: {} });
    await loadCore();
    S.preset = n; S.presetSel[info.name] = n;
    render();
  } catch (e) { showError(e); }
}
async function dupPreset(info, p) {
  const list = presetsOf(info);
  let n = p + "-copy", i = 2;
  while (list.indexOf(n) > -1) { n = p + "-copy-" + i; i++; }
  try {
    await apiJSON(cfgURL(info.name) + "/presets/" + enc(n), "PUT", { values: (info.meta.presets || {})[p] || {} });
    await loadCore();
    S.preset = n; S.presetSel[info.name] = n;
    render();
  } catch (e) { showError(e); }
}
async function delPreset(info, p) {
  const list = presetsOf(info);
  const i = list.indexOf(p);
  try {
    await api(cfgURL(info.name) + "/presets/" + enc(p), { method: "DELETE" });
    await loadCore();
    const left = presetsOf(byName(info.name) || info);
    if (S.preset === p) S.preset = left[Math.max(i - 1, 0)] || null;
    render();
  } catch (e) { showError(e); }
}
async function renamePreset(info, from, raw) {
  const to = slug(raw) || from;
  if (to === from) { render(); return; }
  if (presetsOf(info).indexOf(to) > -1) { note("a preset called " + to + " already exists", 3000); return; }
  try {
    await apiJSON(cfgURL(info.name) + "/presets/" + enc(from) + "/rename", "POST", { to });
    await loadCore();
    S.preset = to; S.presetSel[info.name] = to;
    render();
  } catch (e) { showError(e); render(); }
}

/* Save: the collector's verdict, then the result panel.

   BACKEND NOTE: there is no dry-run — nothing validates YAML text that is
   not on disk yet. So the draft is written, validated, and the previous
   text put back when the collector rejects it, which is what makes the
   panel's "nothing was saved" true. */
async function saveConfig(info) {
  if (S.saving || !cm || !cmDirty) return; // clean editor: nothing to save
  const next = cm.getValue();
  const prev = S.yaml;
  S.saving = true; S.valErr = null; S.valOk = null; clearError();
  render();
  const t0 = Date.now();
  const wasRunning = isRunningCfg(info.name);
  try {
    await api(cfgURL(info.name) + "/yaml", { method: "PUT", headers: { "Content-Type": "text/plain" }, body: next });
    if (!wasRunning) await api(cfgURL(info.name) + "/validate", { method: "POST" });
    S.yaml = next; cmDirty = false;
    const secs = ((Date.now() - t0) / 1000).toFixed(1);
    S.valOk = wasRunning
      ? "saved and re-applied to the running collector in " + secs + "s"
      : "saved in " + secs + "s. " + info.name + " is not running, so nothing restarted.";
    await loadCore();
  } catch (e) {
    S.valErr = e.message || String(e);
    try { await api(cfgURL(info.name) + "/yaml", { method: "PUT", headers: { "Content-Type": "text/plain" }, body: prev }); } catch (e2) { /* nothing better to do */ }
  }
  S.saving = false;
  render();
}

/* ── screen 3: collector ──────────────────────────────────────────── */
function screenCollector() {
  const stopped = nothingActive();
  const busy = S.restarting;
  const wrap = el("div", { class: "screen" });

  const dot = busy ? "var(--accent)" : stopped ? "var(--dim2)" : "var(--ok)";
  const stateWord = busy ? "restarting…" : stopped ? "stopped" : "running";
  wrap.appendChild(el("div", { class: "col-head" }, [
    el("div", { class: "col-line" }, [
      el("span", { class: "dot8", attrs: { style: "background: " + dot } }),
      el("span", { class: "col-state" + (busy ? " " : ""), text: stateWord }),
      // pid and uptime are not in /api/status; "no process" is, and is the
      // half that carries meaning.
      span("col-meta", stopped ? "no process" : ""),
      el("span", { class: "grow" }),
      helpButton("collector"),
      el("button", {
        class: "btn", text: busy ? "restarting…" : stopped ? "start" : "restart",
        attrs: busy ? { disabled: "" } : null,
        on: { click: restartCollector },
      }),
      !stopped && !busy ? el("button", {
        class: "btn quiet", text: "stop",
        title: "stop the collector. it receives nothing until you activate a config again.",
        on: { click: stopCollector },
      }) : null,
    ]),
  ]));

  // The help strip sits in the same slot on every screen: directly under
  // the header line, above the content — so the tiles moved out of the
  // header block (2026-08-27 feedback).
  const chelp = helpStrip("collector");
  if (chelp) wrap.appendChild(el("div", { class: "strip-wrap" }, [chelp]));
  const cn = noteStrip();
  if (cn) wrap.appendChild(cn);
  wrap.appendChild(el("div", { class: "tiles-wrap" }, [tiles(stopped)]));
  wrap.appendChild(healthStrip(stopped));
  const drop = droppingStrip();
  if (drop) wrap.appendChild(drop);
  wrap.appendChild(logPane(stopped));
  if (lastError) wrap.appendChild(el("div", { class: "strip-wrap" }, [errorStrip()]));
  return wrap;
}

function tiles(stopped) {
  const st = S.status || {};
  const tile = (label, node) => el("div", { class: "tile" }, [span("colhead", label), node]);
  // "listening" shows the FULL detected list, labeling what we know: the
  // settings grpc/http ports and the port health actually scraped. Bare
  // otherwise. Nothing detected while running is said plainly, not guessed.
  const ports = detectedPorts();
  const listenText = ports.map((p) => (":" + p + (portLabel(p) ? " " + portLabel(p) : ""))).join(" · ");
  return el("div", { class: "tiles" }, [
    tile("configuration", el("span", { class: "v accent", text: activeName() })),
    tile("preset", el("span", { class: "v" + (stopped || !st.preset ? " off" : ""), text: stopped ? "—" : st.preset || "—" })),
    tile("collector", el("button", {
      class: "link", text: st.distro || "none selected",
      title: "every config runs on this one. change it in settings.",
      on: { click: () => go("#/settings") },
    })),
    tile("listening", el("span", {
      class: "v" + (stopped || !ports.length ? " off" : ""),
      attrs: { style: "white-space: normal; overflow-wrap: anywhere;" },
      text: stopped ? "not listening" : ports.length ? listenText : "nothing detected",
    })),
  ]);
}

function healthStrip(stopped) {
  const h = S.health || {};
  const has = !!h.available;
  const m = (label, value, warn) => el("span", { class: "m" }, [
    span("l", label),
    el("span", { class: "v" + (has ? (warn ? " warn" : "") : " off"), text: has ? value : "—" }),
  ]);
  return el("div", { class: "health" }, [
    m("received", fmtCount(h.received)),
    m("exported", fmtCount(h.exported)),
    m("queue", fmtCount(h.queue)),
    m("dropped", fmtCount(h.dropped), h.dropped > 0),
    el("span", { class: "grow" }),
    el("span", {
      class: "src", title: "the collector's own prometheus endpoint",
      text: stopped ? "no metrics while stopped" : has ? "localhost:" + (h.port || 8888) + "/metrics" : "localhost:8888/metrics · no answer",
    }),
  ]);
}

// The drop diagnosis under the health numbers it derives from: the dropped
// counter is climbing AND the active preset is missing required values —
// the state "activate anyway" accepted, now with runtime evidence. Absent
// whenever either leg is (drops with values present get no vars blamed).
function droppingStrip() {
  const dvars = nothingActive() ? [] : droppingVars();
  if (!dvars.length) return null;
  return el("div", { class: "strip-wrap" }, [el("div", { class: "errbar" }, [
    el("div", { class: "failbar" }, [
      el("span", { class: "dot6", attrs: { style: "background: var(--err)" } }),
      span("msg", droppingText(dvars) + ", so the exporter has nowhere to send. validation can't catch this."),
      el("span", { class: "grow" }),
      el("button", { class: "act", text: "add values", on: { click: openDroppingEditor } }),
    ]),
  ])]);
}

function logPane(stopped) {
  const all = logEntries();
  const q = S.query.trim().toLowerCase();
  // Level and text filters work on whole entries, so a debug dump stays
  // with its parent line; text matches the full raw text, continuations
  // included.
  const shown = all.filter((l) => (S.level === "all" || l.level === S.level) && (!q || l.raw.toLowerCase().includes(q)));
  const lineCount = (list) => list.reduce((n, l) => n + 1 + l.cont.length, 0);

  const bar = el("div", { class: "logbar" }, [
    el("input", {
      class: "field filter",
      attrs: { placeholder: "filter log…", spellcheck: "false", "data-fk": "logq", "aria-label": "filter log" },
      props: { value: S.query },
      on: { input: (e) => { S.query = e.target.value; render(); } },
    }),
  ]);
  for (const k of ["all", "error", "warn", "info", "debug"]) {
    bar.appendChild(el("button", { class: "lvl", on: { click: () => { S.level = k; render(); } } }, [
      S.level === k ? el("span", { class: "dot5" }) : null,
      span("", k),
    ]));
  }
  bar.appendChild(el("span", { class: "grow" }));
  bar.appendChild(span("logcount", lineCount(shown) + " of " + lineCount(all) + " lines"));
  bar.appendChild(el("button", {
    class: "ico", title: "copy these " + lineCount(shown) + " lines",
    on: { click: () => copyText(shown.map((l) => l.raw).join("\n"), lineCount(shown) + " log lines copied") },
  }, [icon("copy", 13)]));
  bar.appendChild(el("button", {
    class: "tail", attrs: stopped ? { disabled: "" } : null,
    on: { click: () => { if (!stopped) { S.tail = !S.tail; render(); } } },
  }, [
    el("span", { class: "dot5", attrs: { style: "background: " + (stopped || !S.tail ? "var(--dim2)" : "var(--ok)") } }),
    span("", stopped ? "no output" : S.tail ? "live tail" : "paused"),
  ]));

  const logs = el("div", { class: "logs" });
  for (const l of shown) {
    const m = el("span", { class: "m" });
    if (l.text) m.appendChild(span("", l.text));
    if (l.attrs) for (const k in l.attrs) m.appendChild(kvPair(k, l.attrs[k]));
    // The caller (service@…/file.go:123) is deliberately a row tooltip,
    // not an inline cell — it earns no space at this density.
    logs.appendChild(el("div", { class: "logline", title: l.caller || null }, [
      span("t", l.time),
      span(l.level ? "lv-" + l.level : "", l.level),
      m,
    ]));
    for (const c of l.cont) {
      // A continuation line that is itself a {…} object (the debug dump's
      // trailing attrs) renders as pairs; everything else keeps its own
      // whitespace verbatim.
      const attrs = c.trimStart().startsWith("{") ? parseAttrs(c.trim()) : null;
      const cm = el("span", { class: "m" });
      if (attrs) for (const k in attrs) cm.appendChild(kvPair(k, attrs[k]));
      else cm.textContent = c;
      logs.appendChild(el("div", { class: "logline cont" }, [span("t", ""), span("", ""), cm]));
    }
  }
  if (!shown.length) logs.appendChild(el("div", { class: "nologs", text: all.length ? "no lines match this filter." : "no output yet." }));

  return el("div", { attrs: { style: "flex:1; min-height:0; display:flex; flex-direction:column;" } }, [bar, logs]);
}

// One structured-attribute pair, `key=value`; nested values render as
// compact JSON. Dimmed, and wraps as a unit (inline-block).
function kvPair(k, v) {
  return el("span", { class: "kv" }, [
    span("k", k + "="),
    span("", typeof v === "string" ? v : JSON.stringify(v)),
  ]);
}

async function restartCollector() {
  if (S.restarting) return;
  S.restarting = true; clearError(); render();
  try {
    await api("/api/service/start", { method: "POST" });
  } catch (e) { showError(e); }
  S.restarting = false;
  await loadCore(); await loadCollector();
  render();
}
async function stopCollector() {
  clearError();
  try { await api("/api/service/stop", { method: "POST" }); } catch (e) { showError(e); }
  await loadCore(); await loadCollector();
  render();
}

/* ── screen 4: settings ───────────────────────────────────────────── */

// envGuide: the shell-side alternatives to the OS-level toggle — three
// ways to wire a shell, each with the log toolbar's click-to-copy idiom.
function envGuide() {
  const row = (desc, cmd) => el("div", { class: "envrow" }, [
    el("span", { class: "n sans", text: desc }),
    el("span", { class: "grow" }),
    el("code", { text: cmd }),
    el("button", {
      class: "ico", title: "copy this command",
      on: { click: () => copyText(cmd, "command copied") },
    }, [icon("copy", 12)]),
  ]);
  return el("div", { class: "envguide" }, [
    el("div", { class: "n sans", text: "or wire a shell instead:" }),
    row("current shell, right now", 'eval "$(compy env)"'),
    row("every new shell: append to ~/.zshrc (or your shell's rc)", "echo 'eval \"$(compy env)\"' >> ~/.zshrc"),
    row("one command only", "compy run -- <cmd>"),
    el("div", { class: "n sans", text: "an app's own environment always wins over the system-wide toggle." }),
  ]);
}

function screenSettings() {
  const wrap = el("div", { class: "settings" });
  wrap.appendChild(el("div", { class: "sec" }, [
    el("div", { attrs: { style: "display:flex; align-items:center; gap:12px;" } }, [
      span("title", "app"),
      el("span", { class: "grow" }),
      helpButton("settings"),
    ]),
    el("div", { class: "subtitle sans", text: "appearance, and how apps find compy." }),
  ]));

  // Help under the header, above the content — the same slot as every
  // other screen (it used to sit above the title; 2026-08-27 feedback).
  const shelp = helpStrip("settings");
  if (shelp) wrap.appendChild(el("div", { class: "strip-wrap" }, [shelp]));
  const sn = noteStrip();
  if (sn) wrap.appendChild(sn);

  const themeNote = S.theme === "system" ? "following macOS — currently " + osTheme() : "always " + S.theme;
  const seg = el("div", { class: "seg" });
  for (const k of ["system", "dark", "light"]) {
    seg.appendChild(el("button", { class: S.theme === k ? "on" : "", text: k, on: { click: () => setTheme(k) } }));
  }
  const proto = (S.settings && S.settings.protocol) || (S.status && S.status.protocol) || "http/protobuf";
  const pseg = el("div", { class: "seg" });
  for (const p of ["grpc", "http/protobuf", "http/json"]) {
    pseg.appendChild(el("button", { class: proto === p ? "on" : "", text: p, on: { click: () => setProtocol(p) } }));
  }
  const osEnvOn = !!(S.status && S.status.os_env);
  wrap.appendChild(el("div", { class: "card" }, [
    el("div", { class: "srow" }, [
      el("span", { class: "lbl" }, [span("t", "appearance"), el("span", { class: "n sans", text: themeNote })]),
      el("span", { class: "grow" }), seg,
    ]),
    el("div", { class: "srow" }, [
      el("span", { class: "lbl" }, [
        span("t", "protocol"),
        el("span", { class: "n sans", text: "what the advertised endpoint speaks. http/protobuf unless your sdk needs otherwise" }),
      ]),
      el("span", { class: "grow" }), savedMark("protocol"), pseg,
    ]),
    el("div", {
      class: "srow clickable", on: { click: () => setOSEnv(!osEnvOn) },
      attrs: { role: "switch", "aria-checked": osEnvOn ? "true" : "false", tabindex: "0" },
    }, [
      el("span", { class: "lbl" }, [
        span("t", "set OTEL_* variables system-wide"),
        el("span", { class: "n sans", text: "apps launched from now on point at compy. already-running ones (your terminal included) pick it up after a relaunch" }),
      ]),
      el("span", { class: "grow" }),
      savedMark("osenv"),
      el("span", { class: "switch" + (osEnvOn ? " on" : "") }, [el("i")]),
    ]),
    envGuide(),
  ]));

  wrap.appendChild(el("div", { class: "sec", attrs: { style: "margin-top:4px" } }, [
    span("title", "global variables"),
    el("div", { class: "subtitle sans", text: "available in every configuration's yaml, which is why they're not part of presets." }),
  ]));
  wrap.appendChild(globalVars());

  wrap.appendChild(el("div", { class: "sec", attrs: { style: "margin-top:4px" } }, [
    span("title", "collector"),
    el("div", { class: "subtitle sans", text: "one collector runs every configuration. compy ships with its own (otelcol-compy); download core, contrib, or otlp, or add your own." }),
  ]));

  const table = el("div", { class: "card" }, [
    el("div", { class: "bin-grid bin-head colhead" }, [el("span"), span("", "collector"), span("", "state"), el("span")]),
  ]);
  for (const b of S.distros) table.appendChild(distroRow(b));
  table.appendChild(el("div", { class: "bin-grid bin-add" }, [
    el("span", { attrs: { style: "display:flex; color: var(--faint)" } }, [icon("plus", 13)]),
    el("input", {
      class: "field",
      attrs: { placeholder: "name", spellcheck: "false", "data-fk": "add-name", "aria-label": "collector name" },
      props: { value: S.addName },
      on: { input: (e) => { S.addName = e.target.value; } },
    }),
    el("input", {
      class: "field",
      attrs: { placeholder: "/usr/local/bin/otelcol-mine", spellcheck: "false", "data-fk": "add-path", "aria-label": "collector path" },
      props: { value: S.addPath },
      on: { input: (e) => { S.addPath = e.target.value; } },
    }),
    el("span", { attrs: { style: "display:flex; justify-content:flex-end" } }, [
      el("button", { class: "act", text: "add", on: { click: addDistro } }),
    ]),
  ]));
  wrap.appendChild(table);

  // Factory reset lives at the very bottom, quietly set apart as the one
  // danger area: muted err styling, and a stronger confirm than delete —
  // this deletes user data wholesale, so the verb stays disabled until
  // "compy" is typed.
  wrap.appendChild(el("div", { class: "sec", attrs: { style: "margin-top:4px" } }, [
    span("title", "danger"),
  ]));
  wrap.appendChild(factoryResetCard());

  if (lastError) wrap.appendChild(errorStrip());
  return wrap;
}

function factoryResetCard() {
  const card = el("div", { class: "card danger-card" });
  card.appendChild(el("div", { class: "srow" }, [
    el("span", { class: "lbl" }, [
      span("t", "reset compy to factory settings"),
      el("span", { class: "n sans", text: "deletes all configurations, presets, downloaded collectors, logs, and settings. the shipped configs come back fresh." }),
    ]),
    el("span", { class: "grow" }),
    S.resetArm ? null : el("button", {
      class: "btn quiet", text: "reset…",
      on: {
        click: () => {
          S.resetArm = true; S.resetTyped = "";
          render();
          const f = document.querySelector('[data-fk="reset-confirm"]');
          if (f) f.focus();
        },
      },
    }),
  ]));
  if (S.resetArm) {
    const ready = S.resetTyped.trim() === "compy" && !S.resetBusy;
    card.appendChild(el("div", { class: "confirm reset-confirm" }, [
      el("span", { class: "q sans", text: "this deletes everything compy manages. type compy to confirm." }),
      el("input", {
        class: "field sm reset-field",
        attrs: { placeholder: "compy", spellcheck: "false", autocomplete: "off", "data-fk": "reset-confirm", "aria-label": "type compy to confirm the reset" },
        props: { value: S.resetTyped },
        on: {
          // No render() here: it would rebuild the focused input mid-word.
          // The verb's disabled state is flipped in place instead.
          input: (e) => {
            S.resetTyped = e.target.value;
            const verb = e.target.closest(".reset-confirm").querySelector(".btn.danger");
            if (S.resetTyped.trim() === "compy" && !S.resetBusy) verb.removeAttribute("disabled");
            else verb.setAttribute("disabled", "");
          },
        },
      }),
      el("span", { class: "grow" }),
      el("button", { class: "act", text: "keep everything", on: { click: () => { S.resetArm = false; S.resetTyped = ""; render(); } } }),
      el("button", {
        class: "btn danger", text: S.resetBusy ? "resetting…" : "reset compy",
        attrs: ready ? null : { disabled: "" },
        on: { click: doFactoryReset },
      }),
    ]));
  }
  return card;
}

async function doFactoryReset() {
  if (S.resetBusy || S.resetTyped.trim() !== "compy") return;
  clearError();
  S.resetBusy = true;
  render();
  try {
    await api("/api/factory-reset", { method: "POST" });
    // Nothing in memory survives a factory reset: drop every server-derived
    // and per-config bit of client state, then load it all fresh.
    Object.assign(S, {
      status: null, configs: [], health: null, log: "", distros: [],
      yaml: "", yamlOf: null, find: "",
      busyId: null, err: null, errName: null, errKept: null,
      newOpen: false, newName: "", newUrl: "", newErr: null, fetching: false,
      confirm: null, confirmVerb: null, confirmId: null, confirmKind: null,
      presetSel: {}, presetsOpenId: null, inline: null, inlineName: "",
      dl: {}, up: {}, addName: "", addPath: "", settings: null, portsSaved: false,
      resetArm: false, resetTyped: "",
    });
    await Promise.all([loadCore(), loadDistros(), loadSettings()]);
    note("compy was reset", 4000);
  } catch (e) { showError(e); }
  S.resetBusy = false;
  render();
}

function distroRow(b) {
  // S.dl is a fetch this screen started (polled at 300ms); b.download is
  // one it didn't — an activation auto-fetching the default collector —
  // carried on the row and refreshed with the 3s loadDistros cycle.
  const d = S.dl[b.name] || b.download || {};
  const busy = d.status === "downloading";
  const failed = d.status === "failed";
  const inUse = !!b.selected;
  const here = !!b.downloaded;
  const blocked = b.definition && !b.available;
  const mine = b.user_entry && !b.definition;
  const bundled = !!b.bundled;
  const checking = !!S.up[b.name];
  const ver = b.version ? " · " + b.version : "";
  // The persisted release check (background or on-demand) claims a newer
  // version; only installed updatable rows carry it, and it clears once the
  // update pulls (the version in effect catches up). An undownloaded row
  // instead says what a download would fetch: the persisted latest, or the
  // compiled-in pin when no check has run yet.
  const avail = b.latest_available ? " · " + b.latest_available + " available" : "";
  const fetches = b.fetch_version ? " · downloads " + b.fetch_version : "";

  // A real fetch failure is a Go error with a URL in it — too long for a
  // 1fr cell. The row shows one short line; the whole thing is the tooltip.
  const reason = (d.error || "").split("\n")[0].replace(/^distro [^:]+: /, "");
  const short = reason.length > 46 ? reason.slice(0, 45) + "…" : reason;
  const state = busy ? "downloading… " + (d.pct == null ? "" : d.pct + "%")
    : failed ? (short ? "download failed · " + short : "download failed")
      : checking ? "checking for a newer release…"
        : bundled ? (here ? "shipped with compy" + ver : "not built — packaging/collector/build.sh")
          : inUse ? "in use" + ver + (here ? avail : fetches)
            : blocked ? "not available on macOS"
              : here ? (mine ? "added by you" : "installed" + ver + avail)
                : "available to download" + fetches;
  const stateCls = busy || checking || inUse ? " accent" : failed ? " bad" : blocked || (bundled && !here) ? " off" : mine ? " mine" : "";
  const glyph = inUse ? "dot" : blocked || (bundled && !here) ? "ban" : here ? "circle" : "download";

  // The update affordance belongs to INSTALLED pinned definitions only: the
  // bundled collector updates with compy releases, a user-managed path is
  // the user's to update, and an undownloaded row has nothing to update —
  // its download fetches the newest release directly. Each disabled title
  // says so.
  const canUpdate = b.definition && !b.user_entry && !blocked && here;
  const updateTitle = bundled ? "updates with compy releases"
    : !b.definition || b.user_entry ? "user-managed — update the binary at its path yourself"
      : blocked ? "not available on macOS"
        : !here ? "nothing installed — download fetches the newest release"
          : busy ? "downloading…"
            : checking ? "checking…"
              : b.latest_available ? b.latest_available + " is available. update " + b.name
                : "check for a newer release and update " + b.name;

  const row = el("div", { class: "bin-grid bin-row" + (inUse ? " on" : "") }, [
    el("span", { class: "ic" + (blocked || (bundled && !here) ? " off" : ""), title: state }, [icon(glyph, 13)]),
    el("span", { class: "nm", text: b.name, title: b.path || (blocked ? "not available on macOS" : bundled ? "not built — packaging/collector/build.sh" : "not downloaded yet") }),
    el("span", { class: "bin-state" }, [
      el("span", { class: "s" + stateCls, text: state, title: failed ? d.error : null }),
      busy && d.pct != null ? el("span", { class: "pbar" }, [el("i", { attrs: { style: "width:" + d.pct + "%" } })]) : null,
    ]),
    el("span", { class: "bin-actions" }, [
      el("button", {
        class: "ico" + (!inUse && here ? " accent" : ""),
        title: inUse ? "already in use" : here ? "run every config on " + b.name : bundled ? "not built — packaging/collector/build.sh" : "download it first",
        attrs: inUse || !here ? { disabled: "" } : null,
        on: { click: () => useDistro(b.name) },
      }, [icon("play", 11, true)]),
      el("button", {
        class: "ico" + (!here && !blocked && !bundled ? " accent" : ""),
        title: bundled ? "never downloaded — built with compy" : busy ? "downloading…" : failed ? "try again" : blocked ? "not available on macOS" : here ? "already installed" : "download and verify " + b.name,
        attrs: here || blocked || busy || bundled ? { disabled: "" } : null,
        on: { click: () => fetchDistro(b.name) },
      }, [icon("download", 13)]),
      el("button", {
        // Accent when a newer release is actually known — the existing
        // "this is the actionable thing" treatment, no popups.
        class: "ico" + (b.latest_available && canUpdate && !busy && !checking ? " accent" : ""),
        title: updateTitle,
        attrs: canUpdate && !busy && !checking ? null : { disabled: "" },
        on: { click: () => updateDistro(b.name) },
      }, [icon("refresh", 13)]),
      el("button", {
        class: "ico", title: here || mine ? "change path" : "nothing installed yet",
        attrs: here || mine ? null : { disabled: "" },
        on: { click: () => changePath(b) },
      }, [icon("folder", 13)]),
      el("button", {
        class: "ico del", title: mine ? "remove " + b.name : "only collectors you added can be removed",
        attrs: mine ? null : { disabled: "" },
        on: { click: () => removeDistro(b.name) },
      }, [icon("trash", 13)]),
    ]),
  ]);
  return row;
}

/* Global variables: the COMPY_GRPC_PORT / COMPY_HTTP_PORT values (excluded
   from preset value cards — they're global; this section is their home,
   2026-08-26 round 2). Same card grid as a preset's values, sized to take
   more cards later. The backend only stores them: nothing re-applies a
   running collector, so a save is answered honestly with "applies when the
   collector next restarts". */
const GLOBAL_VARS = [
  { key: "grpc_port", name: "COMPY_GRPC_PORT", desc: "otlp/grpc port — reference it as ${env:COMPY_GRPC_PORT}" },
  { key: "http_port", name: "COMPY_HTTP_PORT", desc: "otlp/http port — reference it as ${env:COMPY_HTTP_PORT}" },
];
function globalVars() {
  const st = S.settings;
  const frag = el("div", { class: "gvwrap" });
  const grid = el("div", { class: "vals gvars" });
  for (const g of GLOBAL_VARS) {
    grid.appendChild(el("div", { class: "val" }, [
      el("div", { class: "k" }, [
        el("span", { class: "name", text: g.name, title: g.desc }),
        el("span", { class: "grow" }),
        savedMark("gvar-" + g.key),
        span("origin", "settings"),
      ]),
      el("div", { class: "v" }, [
        el("input", {
          class: "field",
          attrs: {
            type: "number", min: "1", max: "65535", spellcheck: "false",
            "data-fk": "gvar-" + g.key, "aria-label": g.name,
          },
          props: { value: st && st[g.key] != null ? String(st[g.key]) : "" },
          on: { change: (e) => savePort(g.key, e.target.value) },
        }),
      ]),
    ]));
  }
  frag.appendChild(grid);
  if (S.portsSaved) {
    const running = !nothingActive();
    frag.appendChild(el("div", { class: "gvnote" }, [
      el("span", { class: "n sans", text: running
        ? "saved. the new value applies when the collector next restarts."
        : "saved. the collector is stopped — the new value applies when it starts." }),
      el("span", { class: "grow" }),
      running ? el("button", {
        class: "act", text: "restart now",
        on: { click: async () => { S.portsSaved = false; await restartCollector(); note("collector restarted on the new values", 3200); } },
      }) : null,
    ]));
  }
  return frag;
}
async function savePort(key, raw) {
  const n = parseInt(raw, 10);
  if (!n) { render(); return; } // empty or junk: put the saved value back
  clearError();
  try {
    const body = {};
    body[key] = n;
    S.settings = await apiJSON("/api/settings", "PUT", body); // backend 400s out-of-range
    S.portsSaved = true;
    await loadCore(); // status carries the (still-running) old ports; refresh anyway
    flashSaved("gvar-" + key);
  } catch (e) { showError(e); try { await loadSettings(); } catch (e2) { /* keep stale */ } }
  render();
}

async function setOSEnv(on) {
  clearError();
  try {
    await apiJSON("/api/os-env", "POST", { on });
    await loadCore();
    flashSaved("osenv");
    note(on ? "saved. OTEL_* variables set system-wide" : "saved. OTEL_* variables cleared", 3000);
  } catch (e) { showError(e); }
}
/* Advertisement only — the collector serves every protocol regardless, so
   nothing restarts. With the OS-level toggle on, the backend refreshes the
   injected values; newly launched apps pick them up (the os-env row already
   says so). */
async function setProtocol(p) {
  clearError();
  try {
    S.settings = await apiJSON("/api/settings", "PUT", { protocol: p });
    await loadCore(); // endpoint + verdict follow the advertisement
    flashSaved("protocol");
    note("saved. advertised endpoint now speaks " + p, 3000);
  } catch (e) { showError(e); }
  render();
}
async function useDistro(name) {
  clearError();
  try {
    await api("/api/distros/" + enc(name) + "/use", { method: "POST" });
    await loadDistros(); await loadCore();
    note("every config now runs on " + name, 3000);
  } catch (e) { showError(e); }
  render();
}
async function changePath(b) {
  const p = await askText("path to " + b.name, b.path || "");
  if (!p) return;
  try {
    const r = await apiJSON("/api/distros/" + enc(b.name), "PUT", { path: p });
    await loadDistros();
    if (r && r.warning) note(r.warning, 4000); else render();
  } catch (e) { showError(e); render(); }
}
async function removeDistro(name) {
  clearError();
  try {
    const r = await api("/api/distros/" + enc(name), { method: "DELETE" });
    await loadDistros();
    if (r && r.reverted) note(name + " reverted to the version that ships with compy", 3200); else render();
  } catch (e) { showError(e); render(); }
}
async function addDistro() {
  const name = slug(S.addName), path = S.addPath.trim();
  if (!name || !path) return;
  clearError();
  try {
    const r = await apiJSON("/api/distros", "POST", { name, path });
    S.addName = ""; S.addPath = "";
    await loadDistros();
    if (r && r.warning) note(r.warning, 4000); else render();
  } catch (e) { showError(e); render(); }
}
// The fetch starts a download and returns; progress lives behind its own
// route because the request that started it is long gone.
async function fetchDistro(name) {
  clearError();
  S.dl[name] = { status: "downloading", pct: 0 };
  render();
  try { await api("/api/distros/" + enc(name) + "/fetch", { method: "POST" }); } catch (e) { S.dl[name] = null; showError(e); render(); return; }
  const poll = async () => {
    let p;
    try { p = await api("/api/distros/" + enc(name) + "/progress"); } catch (e) { S.dl[name] = null; showError(e); render(); return; }
    S.dl[name] = p;
    render();
    if (p.status === "downloading" || p.status === "idle") { setTimeout(poll, 300); return; }
    if (p.status === "done") { S.dl[name] = null; await loadDistros(); render(); }
  };
  setTimeout(poll, 300);
}
// The update checks upstream synchronously (started false with current ==
// latest means already newest), then downloads like a fetch: same progress
// route, same polling.
async function updateDistro(name) {
  clearError();
  S.up[name] = true;
  render();
  let r;
  try { r = await api("/api/distros/" + enc(name) + "/update", { method: "POST" }); }
  catch (e) { delete S.up[name]; showError(e); render(); return; }
  delete S.up[name];
  if (!r.started) {
    note(name + " is already the newest release (" + r.current + ")", 3200);
    render();
    return;
  }
  S.dl[name] = { status: "downloading", pct: 0 };
  render();
  const poll = async () => {
    let p;
    try { p = await api("/api/distros/" + enc(name) + "/progress"); } catch (e) { S.dl[name] = null; showError(e); render(); return; }
    S.dl[name] = p;
    render();
    if (p.status === "downloading" || p.status === "idle") { setTimeout(poll, 300); return; }
    if (p.status === "done") {
      S.dl[name] = null;
      await loadDistros(); await loadCore();
      note(name + " updated to " + r.latest, 3200);
      render();
    }
  };
  setTimeout(poll, 300);
}

/* ── background refresh ───────────────────────────────────────────────
   Never touches the DOM while an input in the screen has focus, and never
   while a slow action or an open menu/inline editor would be yanked away. */
function refreshBlocked() {
  const a = document.activeElement;
  const inField = a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA" || a.tagName === "SELECT");
  return inField || S.busyId || S.stoppingId || S.saving || S.restarting || S.presetsOpenId || S.inline
    || S.confirm || S.preflight || S.newOpen || S.unlockAsk || S.resetArm || S.resetBusy
    || document.querySelector("dialog[open]")
    || (S.screen === "editor" && cmDirty);
}
async function refresh() {
  if (refreshBlocked()) return;
  try {
    await loadCore();
    if (S.screen === "collector") { if (S.tail) await loadCollector(); }
    else if (S.screen === "configs") await loadCollector();
    else if (S.screen === "settings") await Promise.all([loadDistros(), loadSettings()]);
  } catch (e) { return; } // a transient failure should not blank the window
  if (refreshBlocked()) return;
  render();
}

/* ── boot ─────────────────────────────────────────────────────────── */
loadTheme();
applyTheme();
if (window.matchMedia) {
  // 'system' follows macOS live: the tokens swap via the media query, and
  // the note beside the control needs a re-render to name the new value.
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => { if (S.theme === "system") render(); });
}
document.addEventListener("click", (e) => {
  if (S.presetsOpenId && !e.target.closest(".cell-preset")) { S.presetsOpenId = null; render(); }
}, true);
// cmd/ctrl+S saves in the editor — the same save-and-validate as the button,
// and the same states: a clean editor makes it a no-op. preventDefault
// always, so the browser's save dialog never opens; outside the editor the
// shortcut otherwise does nothing. No other shortcuts.
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && S.preflight) { S.preflight = null; render(); return; }
  if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "s") {
    e.preventDefault();
    if (!editorDirty()) return;
    const info = byName(S.editId);
    if (info) saveConfig(info);
  }
});
enterRoute();
setInterval(refresh, 3000);
