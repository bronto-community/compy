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
  source: null,            // tier-3 template source (GET config "source"), null for plain configs

  // configurations screen
  find: "",
  busyId: null,            // config being activated
  stoppingId: null,        // config whose stop control was pressed
  err: null, errName: null, errKept: null,
  note: null, noteTimer: null,
  flash: null,             // key of the control showing its brief "saved" mark
  newOpen: false, newName: "", newUrl: "", newErr: null, fetching: false,
  templates: null,         // catalog schemas (GET /api/templates), fetched once
  confirm: null,           // { text, verb, id, kind } — the destructive inline confirm, under row `id`
  presetSel: {},           // { configName: presetName }
  presetsOpenId: null,
  inline: null,            // { name, preset, isNew }
  inlineName: "",
  inlineDraft: null,       // { KEY: value } — the inline editor's working copy
  inlineErr: null,         // field-adjacent error beside the inline editor's name field
  preflight: null,         // { name, preset, missing } — activation held for missing required values
  helpOpen: {},            // { page: true } while its help strip is open (opt-in, not persisted)

  // editor
  unlocked: false, unlockAsk: false, yamlOpen: false,
  preset: null, reveal: {},
  chipAsk: null,           // preset tab pressed while the tier-3 form is dirty — confirm first
  saving: false, valErr: null, valOk: null,
  valHead: "",             // the failure panel's headline — each save path says who rejected what
  valNote: "",             // the dual-dirty save's honesty line — what the OTHER half's fate was
  valMissing: [],          // unset required vars explaining valErr — offers save-without-validating
  valExcerpt: "",          // ±3 rendered-yaml lines around the line valErr names ("" when it names none)
  valAnyway: false,        // tier-3 rejection: offer the ?validate=false escape
  eform: null,             // the tier-3 editor's form view (buildEditorForm)
  renameNote: null,        // field-adjacent error beside the header's name field
  presetErr: null,         // field-adjacent error under the preset tabs

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

/* ── derived helpers ──────────────────────────────────────────────────
   The pure ones (slug, originOf, hostOf, missingRequired, nameList,
   freePresetName, portsCompact, yamlLineOf, fmtCount, parseZapLine,
   parseAttrs) live in helpers.js, loaded before this file; the ones here
   read S. */
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
function isRunningCfg(name) {
  return !!(S.status && S.status.running && S.status.config === name);
}
function nothingActive() { return !(S.status && S.status.running); }
function activeName() {
  if (nothingActive() || !S.status.config) return "nothing active";
  return S.status.config;
}
function isSecret(key) { return /KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH/i.test(key); }
// The one origin→glyph map — the configs list and the editor header MUST
// show the same icon for the same config (icon = origin, nothing else).
const ORIGIN_ICON = { builtin: "package", user: "user", url: "link" };
/* Ports honesty: only ports the collector process is ACTUALLY listening on
   (detected OS-side via lsof, /api/status "listening") are ever claimed —
   never a guess from settings or YAML. Nothing detected, nothing shown. */
function detectedPorts() { return (S.status && S.status.listening) || []; }
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
  // Tier-3 diagnoses arrive as field paths ("backends[0].api_key") — read
  // them out with the running preset's own row names; tier-2 env var names
  // pass through prettyMissing verbatim. No schema at hand here, so the
  // humanized field name stands in for its label.
  const info = byName(S.status && S.status.config);
  const bag = info ? ((info.meta && info.meta.presets) || {})[(S.status && S.status.preset) || ""] || {} : {};
  const names = prettyMissing(bag, vars, null);
  return "dropping data — " + nameList(names) + (names.length === 1 ? " has" : " have") + " no value";
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
  // A tier-3 config's openInline routed to ITS editor already; the inline
  // card editor lives on the configs screen.
  if (!(info.has_template) && S.screen !== "configs") go("#/configs");
}
/* ── transient notes (the design's ~3s one-liners) ────────────────── */
// The visible strips are rebuilt on every render, so a screen reader never
// hears them; the persistent #live-note / #live-err mirrors (index.html)
// carry the same text through a stable aria-live container.
function announce(id, text) {
  const n = document.getElementById(id);
  if (n) n.textContent = text || "";
}
function noteStrip() {
  if (!S.note) return null;
  return el("div", { class: "strip-wrap" }, [el("div", { class: "note", text: S.note })]);
}
function note(text, ms) {
  S.note = text;
  announce("live-note", text);
  if (S.noteTimer) clearTimeout(S.noteTimer);
  S.noteTimer = setTimeout(() => { S.note = null; S.noteTimer = null; announce("live-note", ""); render(); }, ms || 3200);
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
   (the caller's own mistake) and for a 502 (an upstream service's failure —
   the collector has nothing to do with it); message plus a log tail for a
   500. A 500 whose message is already multi-line carries its own embedded
   tail (the backend builds those as "...: err\n<tail>", still_running ones
   included): show THAT as the tail — fetching another would double it. */
let lastError = null;
async function showError(err) {
  const msg = err && err.message ? err.message : String(err);
  const nl = msg.indexOf("\n");
  lastError = nl < 0 ? { msg, tail: "" } : { msg: msg.slice(0, nl), tail: msg.slice(nl + 1) };
  announce("live-err", lastError.msg);
  render();
  if (err && err.status === 500 && nl < 0) {
    try {
      const j = await api("/api/log?lines=20");
      if (j.log && lastError && lastError.msg === msg) { lastError.tail = j.log; render(); }
    } catch (e) { /* best effort */ }
  }
}
function clearError() { lastError = null; announce("live-err", ""); }
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
  // The manual port assignment only makes sense against a nonconforming
  // verdict; when the fresh status says conforming (or claims nothing),
  // close it. State change on data arrival, never mid-render.
  const v = status && status.running ? status.conformance : null;
  if (!v || v.conforming) S.adoptAsk = false;
}
// The log fetch window. The server tail slides over an append-only file
// (launchd's StandardOutPath appends across restarts, nothing rotates), so
// once the file outgrows this, every poll drops lines off the top; the
// pane's count label says "the last N lines" then instead of implying the
// window is everything.
const LOG_LINES = 500;
async function loadCollector() {
  const [health, log] = await Promise.all([
    api("/api/collector/health").catch(() => null),
    api("/api/log?lines=" + LOG_LINES).catch(() => ({ log: "" })),
  ]);
  S.health = health;
  S.log = (log && log.log) || "";
}
async function loadDistros() { S.distros = (await api("/api/distros")) || []; }
// The catalog is compiled into the backend, so once is enough.
async function loadTemplates() {
  if (!S.templates) S.templates = (await api("/api/templates")) || [];
  return S.templates;
}
async function loadSettings() { S.settings = await api("/api/settings"); }
async function loadYAML(name) {
  const d = await api(cfgURL(name));
  S.yaml = d.yaml || "";
  S.source = d.source != null ? d.source : null; // tier 3 carries its source
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
      // Tier 3 (a config that owns template source): both views, both
      // editable, always — no collapse, no unlock-ask. The capability, not
      // a hazard.
      const t3 = S.source != null;
      S.unlocked = origin === "user" || t3;
      S.yamlOpen = origin === "user" || t3;
      S.unlockAsk = false;
      resetValPanel();
      S.renameNote = null; S.presetErr = null; S.chipAsk = null;
      S.preset = selectedPreset(info);
      buildEditorForm(info);
      destroyEditor();
    } else {
      S.editId = null;
      S.eform = null;
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
    S.inline = null; S.inlineDraft = null; S.eform = null;
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
  if (a && cm && cm.getWrapperElement().contains(a)) return { cm: true };
  if (!a || !a.dataset || !a.dataset.fk) return null;
  // The live value rides along too: fields whose rendered value comes from
  // server state (a half-typed rename, a value card mid-autosave) would
  // otherwise be rebuilt with the older stored text when a timer render
  // lands mid-word — the caret survived, the keystrokes didn't.
  return {
    fk: a.dataset.fk, start: a.selectionStart, end: a.selectionEnd,
    value: a.tagName === "INPUT" || a.tagName === "TEXTAREA" ? a.value : null,
  };
}
function restoreFocus(f) {
  if (!f) return;
  if (f.cm) {
    // The editor reattaches in its own microtask (screenEditor queued it
    // before this one runs); focus after it, on the surviving instance.
    queueMicrotask(() => { if (cm) cm.focus(); });
    return;
  }
  const e = document.querySelector('[data-fk="' + f.fk.replace(/"/g, '\\"') + '"]');
  if (!e) return;
  if (f.value != null && e.value !== f.value) e.value = f.value;
  e.focus();
  if (f.start != null && e.setSelectionRange) {
    try { e.setSelectionRange(f.start, f.end); } catch (err) { /* non-text input */ }
  }
}

/* Screen scroll survives a rebuild the same way focus does: every screen's
   main scroll container (SCREEN_SCROLLER — one shared wrapper per screen;
   the plain editor scrolls nothing at this level, the tier-3 editor
   scrolls .ed-scroll) has
   its offset captured before the DOM is torn down and put back after.
   Keyed to the screen the DOM actually shows (domScreen, not S.screen,
   which enterRoute has already flipped), so switching screens still starts
   at the top. The log pane keeps its own smarter tail machinery below;
   CodeMirror keeps its scroll in its own surviving instance. */
const SCREEN_SCROLLER = "#screen .table-scroll, #screen .settings, #screen .ed-scroll";
let domScreen = null; // which screen the DOM currently shows
function captureScreenScroll() {
  const e = document.querySelector(SCREEN_SCROLLER);
  return e && e.scrollTop ? { screen: domScreen, top: e.scrollTop } : null;
}
function restoreScreenScroll(ss) {
  if (!ss || ss.screen !== S.screen) return;
  const e = document.querySelector(SCREEN_SCROLLER);
  if (!e) return;
  e.scrollTop = ss.top;
  // The tier-3 editor re-attaches its CodeMirror in a microtask queued
  // DURING the rebuild (screenEditor), so at this point .ed-scroll is still
  // missing the pane's height and the assignment above clamps — the view
  // jumped to the top of what little exists. Queued after that microtask,
  // this retry lands once the pane is back at full height.
  if (e.scrollTop !== ss.top) {
    queueMicrotask(() => { if (e.isConnected) e.scrollTop = ss.top; });
  }
}

/* The render rule: a state change that affects LAYOUT — rows appearing,
   panels opening, text that moves its neighbours — goes through render();
   a change that affects exactly one control (a save button's label, a
   confirm verb's disabled bit) flips that control in place, because a full
   render would rebuild the focused input under the user's caret. */
function render() {
  const f = captureFocus();
  const ls = captureLogScroll();
  const ss = captureScreenScroll();
  renderSidebar();
  const root = screenRoot();
  clear(root);
  if (S.screen === "configs") root.appendChild(screenConfigs());
  else if (S.screen === "editor") root.appendChild(screenEditor());
  else if (S.screen === "collector") root.appendChild(screenCollector());
  else root.appendChild(screenSettings());
  domScreen = S.screen;
  restoreFocus(f);
  restoreLogScroll(ls);
  restoreScreenScroll(ss);
}

/* Tail-mode scroll for the log pane, captured/restored around every rebuild:
   pinned to the bottom by default (and whenever the pane first appears), so
   new lines — a restart's banner included, the file only ever appends — show
   up where the eye already is. Scrolling up out of the 40px at-bottom band
   (atLogBottom) holds the view in place across refreshes instead: the
   topmost visible row is the anchor, and since incremental refreshes recycle
   row nodes (logRows), following that node absorbs rows dropping off the top
   above it. A rebuilt pane (filter change, reset) loses the anchor and falls
   back to the raw offset, which the browser clamps. */
function captureLogScroll() {
  const e = document.querySelector(".logs");
  if (!e) return null;
  const s = { top: e.scrollTop, pinned: atLogBottom(e.scrollTop, e.clientHeight, e.scrollHeight), anchor: null, off: 0 };
  if (!s.pinned) {
    for (const r of e.children) {
      if (r.offsetTop + r.offsetHeight > e.scrollTop) { s.anchor = r; s.off = r.offsetTop - e.scrollTop; break; }
    }
  }
  return s;
}
function restoreLogScroll(s) {
  const e = document.querySelector(".logs");
  if (!e) return;
  if (s && logDom && logDom.changed === false) {
    // Zero-op tick: the rows are untouched, so nothing moved — no re-pin,
    // no anchor walk, no rAF dance. Re-adopting the container into the
    // fresh screen DOM resets its scrollTop to 0 (a detached element loses
    // its scroll offset), so put the exact offset back and stop.
    if (e.scrollTop !== s.top) e.scrollTop = s.top;
    return;
  }
  if (!s || s.pinned) {
    e.scrollTop = e.scrollHeight;
    // The rows' content-visibility defers their real sizes to the next
    // rendering pass, which can move the true bottom past the estimate we
    // just scrolled to; re-pin after that pass so the pane sits exactly at
    // the end (and the next capture still reads as pinned).
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (e.isConnected) e.scrollTop = e.scrollHeight;
    }));
    return;
  }
  e.scrollTop = s.anchor && s.anchor.isConnected ? s.anchor.offsetTop - s.off : s.top;
}

/* ── sidebar ──────────────────────────────────────────────────────── */
/* otelcol stderr is heterogeneous: zap console lines (ts \t level \t
   [caller \t] message [\t {json attrs}]) interleaved with the debug
   exporter's multi-line plain dumps. Group into entries: a line whose
   first two tab fields are a timestamp and a known level starts an entry;
   anything else is a continuation of the entry above (the dump keeps its
   parent's level for filtering). Total: unknown shapes become a bare
   entry, malformed JSON tails stay in the message text. */
let logCache = { log: null, entries: [] }; // memo: renderSidebar + the log pane both parse per render
function logEntries() {
  if (logCache.log === S.log) return logCache.entries;
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
  logCache = { log: S.log, entries };
  return entries;
}
// parseZapLine/parseAttrs live in helpers.js.
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

  /* the brew-upgrade window (status.stale_binary): the LaunchAgent still
     names a binary the upgrade deleted — the collector runs on the deleted
     inode (or already failed after a reboot). One quiet line; restart (the
     collector screen's button, or activating) re-resolves and heals it. */
  if (S.status && S.status.stale_binary) {
    box.appendChild(span("pw-soft", "compy was upgraded — restart the collector to run the new version"));
  }

  /* the build itself, quietly at the sidebar's very bottom: "compy 0.1.0" /
     "compy dev · 787da79a1b2c" (status.compy_version, rendered server-side
     so every surface agrees). Empty until the first status arrives. */
  const ver = document.getElementById("side-ver");
  ver.textContent = S.status && S.status.compy_version ? "compy " + S.status.compy_version : "";
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
      // Mirrors the app layer's SyncAll rule exactly: only an unmodified
      // config with a remote URL qualifies. Zero qualifying → disabled,
      // like the per-row sync icons.
      (() => {
        const any = S.configs.some((c) => (c.meta && c.meta.remote_url) && !c.modified);
        return el("button", {
          class: "act", text: "sync all",
          title: any ? "re-fetch every unmodified remote config" : "nothing to sync — no unmodified remote configs",
          attrs: any ? null : { disabled: "" },
          on: { click: syncAll },
        });
      })(),
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
  wrap.appendChild(scroll);
  return wrap;
}

/* Help strips: one per screen, OPT-IN — hidden until the header's help
   button opens it; the button (or the ✕) closes it again. Open state is
   in-memory only, so every load starts with no strips (2026-08-27,
   supersedes the shown-by-default rule from the copy round; the old
   compy.helpDismissed localStorage keys are swept at boot). */
const HELP_COPY = {
  configs: "pick a config that ships with compy, add a preset with your endpoint and key (the + button), then press play. activating restarts the collector. new configuration adds your own: paste yaml, fetch it from a url, paste an otelbin.io share link, or copy a template — its options stay editable as a form in the editor.",
  collector: "these numbers are the collector's own, scraped from its telemetry endpoint, and listening shows only ports the process actually has open. the log below is the collector's output, grouped by level and filterable. restart and stop live here; the configurations screen picks what runs.",
  settings: "appearance, and how apps find compy: the advertised endpoint, its protocol, and the system-wide OTEL_* toggle. global variables are values every configuration's yaml can reference; the collector table downloads, updates, or replaces the binary every config runs on. the danger area at the bottom deletes everything compy manages.",
  editor: "a configuration is one whole collector config.yaml plus its presets — a preset holds all of a config's values, and activating a config runs it with the selected preset's. configs built in to compy or fetched from a url guard their yaml; editing makes it yours, and it stops updating from its source. a config whose text opens with a schema block is templated: the form edits the selected preset's values, the source below describes them, and one save carries both. cmd+s saves, and the save button shows amber while anything is unsaved.",
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

/* One shared confirm shape — sentence · grow · cancel · verb — used by the
   destructive confirm row, the yaml unlock-ask, and the activation
   pre-flight. tone is the verb's extra class (danger/accent); opts: cls
   (wrapper class, names the site), cancel (cancel label), mid (an extra
   button before the verb — the pre-flight's "add values"), verbTitle /
   verbDisabled (the pre-flight's in-flight lockout). The typed
   factory-reset confirm and ask() are deliberately not this shape. */
function confirmBar(text, verb, tone, onVerb, onCancel, opts) {
  opts = opts || {};
  return el("div", { class: opts.cls || "confirm" }, [
    el("span", { class: "q sans", text }),
    el("span", { class: "grow" }),
    el("button", { class: "act", text: opts.cancel || "cancel", on: { click: onCancel } }),
    opts.mid,
    el("button", {
      class: "btn " + tone, text: verb,
      title: opts.verbTitle || null,
      attrs: opts.verbDisabled ? { disabled: "" } : null,
      on: { click: onVerb },
    }),
  ]);
}

function confirmRow() {
  const c = S.confirm;
  return confirmBar(c.text, c.verb, "danger", runConfirm,
    () => { S.confirm = null; render(); }, { cancel: "keep it" });
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

  const typeIcon = ORIGIN_ICON[origin];
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

  /* preset cell: the chip IS the preset — clicking it edits (one preset)
     or opens the menu (several). The edit affordance lives INSIDE the
     chip's border so its scope reads as the preset, never the config
     (2026-08-28 ruling: a free-floating pencil inherited the row's scope
     and read as "edit the config", however it was titled). */
  const cell = el("span", { class: "cell-preset" });
  const selBtn = el("button", {
    class: "preset-sel" + (many ? " many" : ""),
    attrs: {
      "aria-haspopup": many ? "true" : null,
      "aria-expanded": many ? (S.presetsOpenId === name ? "true" : "false") : null,
    },
    title: many ? "pick or edit a preset" : "edit the " + sel + " preset",
    on: {
      click: () => {
        if (many) { S.presetsOpenId = S.presetsOpenId === name ? null : name; render(); } else { openInline(name, sel, false); }
      },
    },
  }, [
    // Every config keeps at least one preset, so sel is always real.
    span("nm", sel),
    el("span", { class: "grow" }),
    many ? el("span", { class: "caret" }, [icon("chevron", 12)])
      : el("span", { class: "caret" }, [icon("pencil", 11)]),
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
  // Quick-editing the preset lives in the chip itself; the row keeps only
  // the plus for adding another.
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
          S.confirm = { text: "delete " + name + " and its presets?", verb: "delete", id: name, kind: "delete" };
          render();
        },
      },
    }, [icon("trash", 13)]),
  ]));

  const wrap = el("div", { class: "cfg-row-wrap" }, [row]);
  if (S.confirm && S.confirm.id === name) wrap.appendChild(confirmRow());
  if (S.preflight && S.preflight.name === name) wrap.appendChild(preflightPanel(info));
  if (S.inline && S.inline.name === name) wrap.appendChild(inlinePresetEditor(info));
  return wrap;
}

function syncAction(info, origin, host) {
  // Templated (tier-3) local configs need nothing here — the config OWNS
  // its source, edited in the editor like any other content. A remote
  // templated config has origin "url" and syncs as any remote does.
  if (origin === "builtin") {
    if (!info.modified) return { on: false, title: "this is the shipped version, nothing to reset", run: () => {} };
    return {
      on: true, title: "reset to the version that ships with compy",
      run: () => {
        S.confirm = { text: "reset " + info.name + " to the version that ships with compy? your changes are lost.", verb: "reset", id: info.name, kind: "reset" };
        render();
      },
    };
  }
  if (origin !== "url") return { on: false, title: "yours from the start, nothing to return to", run: () => {} };
  if (!info.modified) return { on: false, title: "in sync with " + host, run: () => {} };
  return {
    on: true, title: "discard my edits and re-sync from " + host,
    run: () => {
      S.confirm = { text: "re-syncing " + info.name + " throws away your edits.", verb: "discard & re-sync", id: info.name, kind: "resync" };
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
function openInline(name, preset, isNew) {
  // A tier-3 preset is a typed bag the editor's FORM edits — the inline
  // card editor speaks env strings and would misrender it. The pencil (and
  // the plus, and every "add values" path) routes to the editor with that
  // preset selected instead; a new preset is created first, seeded from
  // the selected one, so plus still works with zero typing.
  const t3info = byName(name);
  if (t3info && t3info.has_template) { openT3Preset(t3info, preset, isNew); return; }
  // A new preset opens with a generated available name already in the
  // field, so plus → save works with zero typing; gen is what the name
  // field opened with, which is what dirtiness is measured against.
  const info = byName(name);
  const gen = isNew ? freePresetName(presetsOf(info || { meta: {} })) : "";
  S.inline = { name, preset, isNew, gen };
  S.inlineName = isNew ? gen : preset;
  S.inlineErr = null;
  // The draft starts as a copy of the stored values (a new preset seeds
  // from the currently selected one) — created here, never mid-render.
  const base = info ? (((info.meta && info.meta.presets) || {})[isNew ? selectedPreset(info) : preset]) || {} : {};
  S.inlineDraft = Object.assign({}, base);
  S.presetsOpenId = null;
  render();
}
/* The tier-3 half of openInline: create-if-new (a verbatim copy of the
   selected preset's bag — nothing new to prove, so ?validate=false), then
   land in the editor with the preset selected; its form is the values
   surface. */
async function openT3Preset(info, preset, isNew) {
  S.presetsOpenId = null;
  if (isNew) {
    const n = freePresetName(presetsOf(info));
    const values = (info.meta.presets || {})[selectedPreset(info)] || {};
    try {
      await apiJSON(cfgURL(info.name) + "/presets/" + enc(n) + "?validate=false", "PUT", { values });
    } catch (e) { showError(e); return; }
    preset = n;
  }
  S.presetSel[info.name] = preset;
  go("#/configs/" + enc(info.name)); // enterRoute selects the preset and builds the form
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
  const draft = S.inlineDraft || {}; // created by openInline
  const dirty = inlineDirty();
  return el("div", { class: "inline" }, [
    el("div", { class: "top" }, [
      span("colhead", p.isNew ? "new preset" : "preset"),
      el("input", {
        class: "field sm",
        attrs: { placeholder: "preset name", spellcheck: "false", "data-fk": "inline-name", style: "width:180px", "aria-label": "preset name" },
        props: { value: S.inlineName },
        on: {
          input: (e) => {
            S.inlineName = e.target.value;
            // Typing past a collision clears it (the render restores focus
            // via data-fk); otherwise flip the save button in place only.
            if (S.inlineErr) { S.inlineErr = null; render(); return; }
            inlineSaveSync();
          },
        },
      }),
      S.inlineErr ? span("field-err sans", S.inlineErr) : null,
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
  if (!target) { S.inlineErr = "a preset needs a name"; render(); return; }
  if ((p.isNew || target !== p.preset) && presetsOf(info).indexOf(target) > -1) {
    S.inlineErr = "a preset called " + target + " already exists";
    render();
    return;
  }
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
        // Empty value + yaml default: the default applies at runtime (an
        // empty value is never exported — it would defeat the fallback).
        v.has_default
          ? el("span", { class: "origin", text: "default", title: raw.trim() ? "" : "empty — the yaml default applies" })
          : span("origin", yamlLineOf(yaml, v.name)),
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
async function preflightActivate(name, preset) {
  if (inflight()) return; // every entry button is disabled; belt for stragglers
  const info = byName(name);
  let missing = [];
  if (info && info.has_template) {
    // Tier 3 answers from the config's own schema (the Go rule,
    // cfgstore.MissingRequired root-aware): fetch the source, mirror the
    // check, and name what's missing readably from the bag's own row
    // names. An unfetchable or unparseable source can't say — activate,
    // and the server stays the authority.
    try {
      const d = await api(cfgURL(name));
      const tpl = parseSourceSchema(d.source || "");
      if (tpl) {
        const bag = ((info.meta && info.meta.presets) || {})[preset || (info.meta && info.meta.active_preset)] || {};
        missing = prettyMissing(bag, missingRequiredT3(tpl, bag), tpl);
      }
    } catch (e) { /* the server-side pre-flight still applies */ }
  } else if (info) {
    missing = missingRequired(info, preset);
  }
  if (!missing.length) { activate(name, preset); return; }
  S.preflight = { name, preset, missing };
  S.presetsOpenId = null;
  render();
}
function preflightPanel(info) {
  const p = S.preflight;
  return confirmBar(
    p.name + " needs " + nameList(p.missing) + " before it can send anywhere. add a preset with values, or activate anyway.",
    "activate anyway", "accent",
    () => { S.preflight = null; activate(p.name, p.preset); },
    () => { S.preflight = null; render(); },
    {
      cls: "preflight",
      // The existing inline preset editor, on the real preset this
      // activation would use (every config keeps at least one).
      mid: el("button", {
        class: "btn", text: "add values",
        on: { click: () => { S.preflight = null; openInline(p.name, p.preset, false); } },
      }),
      verbTitle: inflight() ? inflightTitle() : null,
      verbDisabled: inflight(),
    });
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
  await Promise.all([loadCore(), loadCollector()]); // independent GETs, in parallel
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
    await Promise.all([loadCore(), loadCollector()]); // independent GETs, in parallel
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
  const name = S.confirm.id, kind = S.confirm.kind;
  S.confirm = null;
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
  // The template options render from the catalog schemas; fetch once,
  // quietly — the strip works without them until they arrive.
  loadTemplates().then(render, () => {});
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
        text: S.fetching ? "fetching…" : S.newErr || "empty means a blank config · otelbin.io links work",
      }),
    ]),
    // The third way in: one button per catalog template (label and tooltip
    // from its own schema, so a second template appears here for free).
    // Name + create — the editor IS the form now: creating copies the
    // template's source into the new config and lands there.
    S.templates && S.templates.length ? el("div", { class: "fieldset tpls" }, [
      span("lbl", "or from a template"),
      el("div", { class: "tpl-row" }, S.templates.map((t) => el("button", {
        class: "btn quiet", text: t.name,
        title: !s || taken ? "pick a name first" : t.description,
        attrs: !s || taken || S.fetching ? { disabled: "" } : null,
        on: { click: () => createFromCatalog(t) },
      }))),
      el("span", { class: "foot", text: "copies the template into your config — edit its options and source in the editor" }),
    ]) : null,
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

/* ── tier 3: create from the catalog, and the editor's form view ──────
   A catalog entry is a STARTER: creating from one copies its SOURCE into
   the new config (POST /api/configs/from-catalog), which owns it from then
   on — the config lands in the editor, where the form (generated from the
   config's OWN schema) and the source pane are two views of the same file.
   Knobs at create are the schema's defaults plus neutral type-derived
   placeholders (placeholderKnobs) — the editor is the form now. */
async function createFromCatalog(tpl) {
  const name = slug(S.newName);
  if (!name || byName(name) || S.fetching) return;
  clearError();
  S.fetching = true;
  render();
  try {
    await apiJSON("/api/configs/from-catalog", "POST", { name, template: tpl.name, knobs: placeholderKnobs(tpl) });
    S.fetching = false; S.newOpen = false; S.newName = ""; S.newUrl = "";
    await loadCore();
    go("#/configs/" + enc(name)); // the editor: form above, source below
  } catch (e) {
    S.fetching = false;
    showError(e);
    render();
  }
}

/* The editor's form view, rebuilt whenever the stored source OR the
   selected preset changes (route entry, every successful save, a tab
   switch): schema from the CONFIG'S OWN front matter (parsed client-side —
   loosely, the server is the authority), values from the SELECTED preset's
   bag seeded through the schema's defaults — the form IS that preset's
   values surface, secrets included (Amendment 4). parseErr means the form
   quietly steps aside ("the schema doesn't parse") and the source pane is
   the recovery path — saving is never blocked on the client parse. Open
   disclosures survive the rebuild. */
function buildEditorForm(info) {
  const prev = S.eform && !S.eform.parseErr ? S.eform : null;
  S.eform = null;
  if (S.source == null) return;
  const tpl = parseSourceSchema(S.source);
  if (!tpl) { S.eform = { parseErr: true }; return; }
  let knobs;
  try {
    const bag = ((info && info.meta && info.meta.presets) || {})[S.preset] || null;
    knobs = seedKnobs(tpl, bag, true);
    if (tpl.backends && !Array.isArray(knobs.backends)) throw new Error("bad backends");
  } catch (e) { S.eform = { parseErr: true }; return; }
  S.eform = {
    tpl, knobs, base: JSON.stringify(knobs),
    errs: {},
    secOpen: prev ? prev.secOpen : {}, // collapsed sections opened by hand
    rowOpen: prev ? prev.rowOpen : {}, // per-backend-row "more options"
    onChange: edSaveSync,              // a knob keystroke flips the save button in place
  };
}
// Form-side dirtiness: the draft differs from what the store holds. Feeds
// the ONE save button the source pane shares.
function eformDirty() {
  const f = S.eform;
  return !!(f && !f.parseErr && JSON.stringify(f.knobs) !== f.base);
}

// One field card — label, field-adjacent error, control, muted helper line.
// path is the server's error key ("backends[0].endpoint"); row is the draft
// object the control writes into. A text keystroke never re-renders (the
// render would rebuild the focused input); touched() clears the field's
// error, tells the save button, and re-renders only when the error's span
// has to disappear.
function tfField(f, fl, row, path) {
  const err = f.errs[path];
  const touched = () => {
    if (f.onChange) f.onChange();
    if (f.errs[path]) { delete f.errs[path]; render(); }
  };
  let control, revealBtn = null;
  if (fl.type === "secret") {
    // A real input: the form edits the preset's bag, and a secret is an
    // ordinary bag member typed by the schema. Masked, with the value
    // cards' reveal idiom.
    const revealed = !!S.reveal[path];
    control = el("input", {
      class: "field",
      attrs: {
        type: revealed ? "text" : "password",
        spellcheck: "false", autocomplete: "off",
        "data-fk": "tf:" + path, "aria-label": fl.label || fl.name,
        placeholder: fl.optional ? "optional" : null,
      },
      props: { value: row[fl.name] || "" },
      on: { input: (e) => { row[fl.name] = e.target.value; touched(); } },
    });
    revealBtn = el("button", {
      class: "reveal", text: revealed ? "hide" : "reveal",
      attrs: { type: "button" },
      on: { click: () => { S.reveal[path] = !S.reveal[path]; render(); } },
    });
  } else if (fl.type === "choice") {
    control = el("select", {
      class: "field", attrs: { "data-fk": "tf:" + path, "aria-label": fl.label || fl.name },
      on: { change: (e) => { row[fl.name] = e.target.value; touched(); } },
    }, (fl.options || []).map((o) => el("option", { text: o, attrs: { value: o } })));
    control.value = row[fl.name];
  } else if (fl.type === "multi") {
    control = el("div", { class: "tf-checks" }, (fl.options || []).map((o) => {
      const box = el("input", {
        attrs: { type: "checkbox", "data-fk": "tf:" + path + ":" + o },
        props: { checked: (row[fl.name] || []).indexOf(o) > -1 },
        on: {
          change: (e) => {
            const cur = (row[fl.name] || []).filter((x) => x !== o);
            if (e.target.checked) cur.push(o);
            // schema order, not click order
            row[fl.name] = (fl.options || []).filter((x) => cur.indexOf(x) > -1);
            touched();
          },
        },
      });
      return el("label", { class: "tf-check" }, [box, span("", o)]);
    }));
  } else if (fl.type === "toggle") {
    control = el("button", {
      class: "tf-switch",
      attrs: { type: "button", role: "switch", "aria-checked": row[fl.name] ? "true" : "false", "aria-label": fl.label || fl.name, "data-fk": "tf:" + path },
      on: { click: () => { row[fl.name] = !row[fl.name]; touched(); render(); } },
    }, [el("span", { class: "switch" + (row[fl.name] ? " on" : "") }, [el("i")])]);
  } else { // slug | url | string
    control = el("input", {
      class: "field",
      attrs: { spellcheck: "false", "data-fk": "tf:" + path, "aria-label": fl.label || fl.name, placeholder: fl.optional ? "optional" : null },
      props: { value: row[fl.name] || "" },
      on: { input: (e) => { row[fl.name] = e.target.value; touched(); } },
    });
  }
  return el("div", { class: "val tf-field" }, [
    el("div", { class: "k" }, [
      el("span", { class: "name", text: fl.label || fl.name }),
      el("span", { class: "grow" }),
      err ? span("field-err sans", err) : null,
    ]),
    el("div", { class: "v" }, [control, revealBtn]),
    fl.description ? el("div", { class: "d sans", text: fl.description }) : null,
  ]);
}
function tfGrid(f, fields, row, prefix) {
  return el("div", { class: "vals tf-grid" },
    fields.map((fl) => tfField(f, fl, row, prefix + fl.name)));
}

// The repeat group: one card per backend, primary fields up front and the
// advanced ones behind a per-row disclosure; +/✕ respect the schema bounds.
function tfBackends(f) {
  const rep = f.tpl.backends;
  const rows = f.knobs.backends;
  const primary = (rep.fields || []).filter((x) => !x.advanced);
  const adv = (rep.fields || []).filter((x) => x.advanced);
  const wrap = el("div", { class: "tf-rows" });
  rows.forEach((row, i) => {
    const open = !!f.rowOpen[i];
    const canDel = rows.length > rep.min;
    wrap.appendChild(el("div", { class: "tf-brow" }, [
      el("div", { class: "tf-browhead" }, [
        span("colhead", "backend " + (i + 1) + (row.name ? " · " + row.name : "")),
        el("span", { class: "grow" }),
        el("button", {
          class: "act x", text: "✕",
          title: canDel ? "remove this backend" : "a config needs at least " + rep.min + (rep.min === 1 ? " backend" : " backends"),
          attrs: canDel ? null : { disabled: "" },
          on: { click: () => { rows.splice(i, 1); f.errs = {}; f.rowOpen = {}; render(); } },
        }),
      ]),
      tfGrid(f, primary, row, "backends[" + i + "]."),
      adv.length ? el("button", {
        class: "act tf-more", text: open ? "fewer options ▾" : "more options ▸",
        on: { click: () => { f.rowOpen[i] = !open; render(); } },
      }) : null,
      open ? tfGrid(f, adv, row, "backends[" + i + "].") : null,
    ]));
  });
  const canAdd = rows.length < rep.max;
  wrap.appendChild(el("button", {
    class: "act tf-add", text: "+ add backend",
    title: canAdd ? "add another backend" : "at most " + rep.max + " backends",
    attrs: canAdd ? null : { disabled: "" },
    on: { click: () => { rows.push(seedRow(rep.fields, null)); render(); } },
  }));
  return wrap;
}

/* The form view itself — the tier-3 editor's primary view, above the source
   pane. Declaration order is form order, sections group, `collapsed`
   sections and per-row `advanced` fields sit behind disclosures; secrets
   are placeholders (their values live in the preset's cards). Every control
   writes into S.eform.knobs; the shared header save button carries the
   result. */
function editorFormView(info) {
  const f = S.eform;
  if (!f) return null;
  const wrap = el("div", { class: "tform eform pcard" });
  // The preset tabs sit on the card's top edge — the selected tab already
  // says whose values the form shows, so there is no separate header.
  for (const n of presetTabs(info, true)) wrap.appendChild(n);
  if (f.parseErr) {
    wrap.appendChild(el("div", {
      class: "eform-broken sans",
      text: "the schema doesn't parse — fix the front matter in the source below.",
    }));
    return wrap;
  }
  const tpl = f.tpl;
  // The template's own description doubles as the helper line — for the
  // shipped template it names the vendor tables in the docs.
  if (tpl.description) wrap.appendChild(el("div", { class: "tf-desc sans", text: tpl.description }));

  /* body: fields without a section first, then sections in declaration
     order — the backends repeat group renders under its namesake section. */
  const loose = (tpl.fields || []).filter((fl) => !fl.section);
  if (loose.length) wrap.appendChild(tfGrid(f, loose, f.knobs, ""));
  for (const sec of tpl.sections || []) {
    const secFields = (tpl.fields || []).filter((fl) => fl.section === sec.id);
    const isBackends = !!tpl.backends && sec.id === "backends";
    if (!isBackends && !secFields.length) continue;
    const open = !sec.collapsed || !!f.secOpen[sec.id];
    // A backends-level error ("backends: need 1 to 8 entries") belongs to
    // the group, not a field.
    const groupErr = isBackends ? f.errs.backends : null;
    wrap.appendChild(el("button", {
      class: "tf-sechead" + (sec.collapsed ? " toggles" : ""),
      attrs: sec.collapsed ? { type: "button", "aria-expanded": open ? "true" : "false" } : { type: "button", disabled: "" },
      on: sec.collapsed ? { click: () => { f.secOpen[sec.id] = !f.secOpen[sec.id]; render(); } } : null,
    }, [
      sec.collapsed ? el("span", { class: "caret" + (open ? "" : " closed") }, [icon("chevron", 12)]) : null,
      span("colhead", sec.label || sec.id),
      groupErr ? span("field-err sans", groupErr) : null,
      sec.collapsed && !open ? el("span", { class: "why sans", text: "the defaults are right for most setups" }) : null,
    ]));
    if (!open) continue;
    if (isBackends) wrap.appendChild(tfBackends(f));
    if (secFields.length) wrap.appendChild(tfGrid(f, secFields, f.knobs, ""));
  }
  return wrap;
}

/* ── screen 2: configuration editor ───────────────────────────────── */
/* The CodeMirror instance survives re-renders: rebuilding it on every
   render reset scroll and cursor whenever a background refresh or a note
   timer redrew the screen. cmFor/cmRO name what the instance was built for
   (config name, readOnly); a render for the same pair re-adopts the live
   instance. cmBase is the server YAML it last synced to — setValue only
   when the server text changed AND the editor is clean; a clean, unchanged
   editor is not touched at all. */
let cm = null, cmDirty = false, cmFor = null, cmRO = false, cmBase = null, cmT3 = false;
function destroyEditor() { cm = null; cmDirty = false; cmFor = null; cmBase = null; cmT3 = false; }
// One reset for the save-result panel's whole state — every save path and
// the dismiss button clear the same seven fields.
function resetValPanel() {
  S.valErr = null; S.valOk = null; S.valMissing = []; S.valExcerpt = "";
  S.valAnyway = false; S.valHead = ""; S.valNote = "";
}
// Dirty = either view of the one file: the pane (yaml or source) or the
// tier-3 form's knob draft. One save serves both.
function editorDirty() { return S.screen === "editor" && ((!!cm && cmDirty) || eformDirty()); }
// Flip the header save button in place on a cm or form keystroke —
// re-rendering the screen would rebuild the focused control under the caret.
function edSaveSync() {
  const b = document.querySelector(".ed-save");
  if (!b || S.saving) return;
  const dirty = cmDirty || eformDirty();
  b.textContent = dirty ? "save" : "saved";
  b.classList.toggle("accent", dirty);
  if (dirty) b.removeAttribute("disabled"); else b.setAttribute("disabled", "");
}

/* ── preset tabs, ON the values surface ───────────────────────────────
   The presets render as a file-tab row on the top edge of the values card
   itself (tier 3's form card, tier 2's value-card grid) — one editing
   surface, the selected tab connected to the card body. The selected tab
   carries the actions (rename input, duplicate, delete — the last preset
   has no delete at all); the RUNNING preset wears the accent dot whichever
   tab is viewed. The strip renders even with ONE preset (owner ruling,
   2026-08-30: a bare corner + was unreadable — the sole preset shows as
   the selected tab so the + next to it explains itself). */
function presetTabs(info, t3) {
  const list = presetsOf(info);
  const running = isRunningCfg(info.name);
  const out = [];
  const tabs = el("div", { class: "ptabs" });
  for (const p of list) {
    const on = p === S.preset;
    const isRunningPreset = running && p === S.status.preset;
    tabs.appendChild(el("span", { class: "ptab" + (on ? " on" : "") }, [
      isRunningPreset ? el("span", {
        class: "dot5", title: "running now",
        attrs: { style: "background: var(--accent)" },
      }) : null,
      on ? el("input", {
        title: "rename this preset",
        attrs: { spellcheck: "false", size: Math.max(p.length, 4), "data-fk": "chip:" + p, "aria-label": "rename this preset" },
        props: { value: p },
        on: { change: (e) => renamePreset(info, p, e.target.value) },
      }) : el("button", { class: "pick", text: p, on: { click: () => pickPreset(info, p, t3) } }),
      on ? el("button", { class: "mini", title: "duplicate this preset", on: { click: () => dupPreset(info, p) } }, [icon("copy", 12)]) : null,
      on && list.length > 1 ? el("button", {
        class: "mini del",
        title: isRunningPreset ? "this preset is running. activate another one first." : "delete " + p,
        attrs: isRunningPreset ? { disabled: "" } : null,
        on: { click: () => delPreset(info, p) },
      }, [icon("trash", 12)]) : null,
    ]));
  }
  tabs.appendChild(el("button", { class: "ptab-add", title: "add a preset", on: { click: () => addPreset(info) } }, [icon("plus", 13)]));
  out.push(tabs);
  // Field-adjacent: a tab-rename collision answers right under the tabs.
  if (S.presetErr) out.push(el("div", { class: "field-err sans", text: S.presetErr }));
  // A tab pressed while the tier-3 form holds unsaved edits: the switch
  // would swap the form's values out from under them — confirm inline.
  if (S.chipAsk) {
    out.push(confirmBar(
      "switch to " + S.chipAsk + "? unsaved edits to " + S.preset + " are lost.",
      "discard & switch", "accent",
      () => switchPreset(info, S.chipAsk),
      () => { S.chipAsk = null; render(); },
      { cls: "unlock-ask", cancel: "keep editing" }));
  }
  /* The warn appears only when something is wrong, and it is a real "is
     any required value missing" check — tier 3 answers from the config's
     own schema (field paths, made readable), tier 2 from the yaml's vars. */
  const bag = ((info.meta.presets || {})[S.preset]) || {};
  const t3tpl = t3 && S.eform && !S.eform.parseErr ? S.eform.tpl : null;
  const missing = t3
    ? (t3tpl ? prettyMissing(bag, missingRequiredT3(t3tpl, bag), t3tpl) : [])
    : missingRequired(info, S.preset);
  if (S.preset && missing.length) {
    out.push(el("div", { class: "warn sans", text: S.preset + " has no " + nameList(missing) + ". activating with it will fail." }));
  }
  return out;
}

function screenEditor() {
  const info = byName(S.editId);
  if (!info) return el("div", { class: "screen" });
  const origin = originOf(info);
  const host = hostOf(info);
  // Tier 3: the config owns template source — the form and the source pane
  // are two views of one file, both always visible and editable (no
  // collapse, no unlock-ask, whatever the provenance).
  const t3 = S.source != null && S.yamlOf === info.name;
  const locked = !t3 && origin !== "user" && !S.unlocked;
  const yamlShown = t3 || origin === "user" || S.yamlOpen;
  const list = presetsOf(info);
  if (list.indexOf(S.preset) < 0) S.preset = list[0];

  const wrap = el("div", { class: "screen" });

  /* header, one line — the origin lives on the type icon, the URL field and
     the reset button; there is no second line and no origin strip. Exactly
     ONE icon, the same origin glyph the configs list shows for this config
     (owner ruling, 2026-08-30: icon = origin, nothing else — no run dot,
     no template marker). */
  const showReset = origin === "url" || (origin === "builtin" && info.modified);
  const resetLabel = origin === "builtin" ? "reset to shipped" : info.modified ? "discard edits & re-sync" : "re-sync";
  wrap.appendChild(el("div", { class: "ed-head" }, [
    iconWrap("ed-type", ORIGIN_ICON[origin], 14, false,
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
      : S.renameNote ? span("ed-hint field-err", S.renameNote) : null,
    helpButton("editor"),
    showReset ? el("button", {
      class: "btn withicon", title: origin === "url" && !info.modified ? "in sync with " + host : "your version",
      on: { click: () => headerResync(info, origin) },
    }, [icon(origin === "builtin" ? "undo" : "refresh", 12), span("", resetLabel)]) : null,
    (() => {
      // The button carries the WHOLE save state — the pane's text and the
      // tier-3 form's knob draft feed the same save: accent "save" while
      // either view is dirty, muted disabled "saved" while there is nothing
      // to save, "checking…" while the collector is asked. Keystrokes flip
      // it in place via edSaveSync (a cm or form change doesn't re-render).
      const dirty = cmDirty || eformDirty();
      return el("button", {
        class: "btn ed-save" + (dirty && !S.saving ? " accent" : ""),
        text: S.saving ? "checking…" : dirty ? "save" : "saved",
        attrs: S.saving || !dirty ? { disabled: "" } : null,
        on: { click: () => saveConfig(info) },
      });
    })(),
  ]));

  const ehelp = helpStrip("editor");
  if (ehelp) wrap.appendChild(el("div", { class: "strip-wrap" }, [ehelp]));
  const noteEl = noteStrip();
  if (noteEl) wrap.appendChild(noteEl);

  /* Tier 2: the value cards ARE the values surface — one card, the preset
     tabs on its top edge (tier 3's form card gets the same tabs inside
     editorFormView, further down). */
  if (!t3) {
    const card = el("div", { class: "tform pcard ed-vals" });
    for (const n of presetTabs(info, false)) card.appendChild(n);
    const values = ((info.meta.presets || {})[S.preset]) || {};
    card.appendChild(valueCards(info, values, (k, v) => queueValue(info, k, v), "ed"));
    wrap.appendChild(card);
  }

  /* save results, at screen level — visible whether the YAML is open or not */
  if (S.valOk) {
    wrap.appendChild(el("div", { class: "okstrip" }, [el("span", { class: "dot5" }), span("", S.valOk)]));
  }
  if (S.valErr) {
    wrap.appendChild(el("div", { class: "valfail" + (S.valExcerpt ? " tall" : "") }, [
      el("div", { class: "bar2" }, [
        el("span", { class: "dot5", attrs: { style: "background: var(--err)" } }),
        span("headline", S.valHead || "the collector rejected this config. nothing was saved."),
        el("span", { class: "grow" }),
        el("button", { class: "act", text: "copy", on: { click: () => copyText(S.valErr, "diagnostic copied") } }),
        el("button", { class: "act", text: "dismiss", on: { click: () => { resetValPanel(); render(); } } }),
      ]),
      // The dual-dirty save is two requests (source, then preset values) —
      // when one half landed or never got sent, this line says so honestly.
      S.valNote ? el("div", { class: "bar2" }, [span("sans", S.valNote)]) : null,
      // The failure is explained by variables that simply have no values
      // yet — "write the yaml first, fill values second" is a legitimate
      // order of work, so offer the save without the collector's blessing.
      // Activation stays guarded by its own pre-flight either way.
      S.valMissing.length ? el("div", { class: "bar2" }, [
        span("sans", nameList(S.valMissing) + (S.valMissing.length === 1 ? " has no value" : " have no values")
          + " yet — the collector cannot validate without " + (S.valMissing.length === 1 ? "it" : "them") + "."),
        el("span", { class: "grow" }),
        el("button", { class: "act", text: "save without validating", on: { click: () => saveAnyway(info) } }),
      ]) : null,
      // A tier-3 save is validated-or-restored server-side, so the draft is
      // only in this window right now; the ?validate=false escape keeps it
      // so the fixing can continue from what's here.
      S.valAnyway ? el("div", { class: "bar2" }, [
        span("sans", "the draft stays here until it validates — or keep it without the collector's blessing."),
        el("span", { class: "grow" }),
        el("button", { class: "act", text: "save without validating", on: { click: () => saveT3(info, false) } }),
      ]) : null,
      el("pre", { text: S.valErr }),
      // The collector validated the RENDERED yaml; when its diagnostic
      // names a line, show that neighborhood (±3 lines) of the rendered
      // config. Line mapping back to the SOURCE is deferred (design note).
      S.valExcerpt ? el("div", { class: "excerpt" }, [
        el("div", { class: "xl sans", text: "in the rendered config" }),
        el("pre", { text: S.valExcerpt }),
      ]) : null,
    ]));
  }
  if (lastError) wrap.appendChild(el("div", { class: "strip-wrap" }, [errorStrip()]));

  /* YAML: collapsed by default for built-in and linked configs (a tier-3
     config never collapses \u2014 both its views are the point) */
  if (!yamlShown) {
    wrap.appendChild(el("div", { class: "yaml-collapsed" }, [
      span("nm", "config.yaml"),
      el("span", {
        class: "why sans",
        text: origin === "url" ? "kept in sync with " + host
          : "ships with compy. most people never open it.",
      }),
      el("span", { class: "grow" }),
      el("button", { class: "btn quiet", text: "show yaml", on: { click: () => { S.yamlOpen = true; render(); } } }),
    ]));
    return wrap;
  }

  /* Tier 3 stacks the form view over the source pane in one scroller (the
     form can be tall, and the pane grows with its text); a plain config
     keeps the pane as the screen's flex fill. The pane is the same slot
     either way \u2014 it simply shows whichever text the config has. */
  const paneParent = t3 ? el("div", { class: "ed-scroll" }) : wrap;
  if (t3) {
    const form = editorFormView(info);
    if (form) paneParent.appendChild(form);
    wrap.appendChild(paneParent);
  }

  const pane = el("div", { class: "yaml-pane" });
  pane.appendChild(el("div", { class: "yaml-bar" }, [
    span("colhead", t3 ? "config source" : "config.yaml"),
    t3 ? el("span", { class: "why sans", text: "schema, then the template body \u2014 the form above is this same file" }) : null,
    el("span", { class: "grow" }),
    locked ? el("span", { class: "ro" }, [
      span("word", "read-only"),
      el("button", { class: "act unlock", text: "edit anyway", on: { click: () => { S.unlockAsk = true; render(); } } }),
    ]) : null,
  ]));
  if (S.unlockAsk) {
    pane.appendChild(confirmBar(
      origin === "url"
        ? "editing disconnects this from " + host + ". it stops re-syncing."
        : "your version stays through compy updates.",
      "make it mine", "accent",
      () => { S.unlockAsk = false; S.unlocked = true; render(); },
      () => { S.unlockAsk = false; render(); },
      { cls: "unlock-ask" }));
  }
  const host2 = el("div", { class: "cm-host" });
  pane.appendChild(host2);
  paneParent.appendChild(pane);

  // CodeMirror measures itself, so build it once the host is in the
  // document. The instance is keyed on (config, readonly, tier) \u2014 a
  // promotion or demotion swaps which text the pane holds, so it rebuilds.
  const text = t3 ? S.source : S.yaml;
  const editable = !locked;
  queueMicrotask(() => {
    if (!host2.isConnected) return;
    if (cm && cmFor === info.name && cmRO === !editable && cmT3 === t3) {
      // Same config, same readonly-ness: re-adopt the live instance.
      // Scroll and cursor live in its doc, and refresh() restores them
      // after the reattach.
      host2.appendChild(cm.getWrapperElement());
      if (!cmDirty && text !== cmBase) {
        // A save already left the editor holding the new server text;
        // setValue only when the text really differs (it resets the caret).
        if (cm.getValue() !== text) {
          cm.setValue(text); // the change handler fires; stay clean
          cmDirty = false;
          edSaveSync();
        }
        cmBase = text;
      }
      cm.refresh();
      return;
    }
    const keep = cm ? cm.getValue() : null;
    const dirty = cmDirty;
    cm = CodeMirror(host2, {
      value: dirty && keep != null ? keep : text,
      mode: "yaml", // v1: yaml highlighting for source too \u2014 schema is the JSON subset, the body is yaml-shaped
      lineNumbers: true, readOnly: !editable, lineWrapping: false,
      // In the tier-3 scroller the pane is height:auto, so render everything.
      viewportMargin: t3 ? Infinity : 20,
    });
    cmFor = info.name; cmRO = !editable; cmT3 = t3; cmBase = text;
    cmDirty = dirty;
    cm.on("change", () => { cmDirty = true; edSaveSync(); });
  });
  return wrap;
}

/* value edits are instant (everything but activate/restart/save is), and
   land on the preset they belong to. One shared debounce timer serves the
   whole band, so a pending PUT is flushed — fired immediately, never
   dropped — whenever the target (config, preset) changes: by an edit on
   another preset, or by switching presets in the tabs. */
let valueTimer = null, valuePending = null; // valuePending: { name, preset, values, key }
async function putPending(p) {
  try {
    await apiJSON(cfgURL(p.name) + "/presets/" + enc(p.preset), "PUT", { values: p.values });
    await loadCore();
    flashSaved("ed:" + p.key);
  } catch (e) { showError(e); }
}
function flushValue() {
  if (!valueTimer) return;
  clearTimeout(valueTimer);
  valueTimer = null;
  const p = valuePending;
  valuePending = null;
  if (p) putPending(p);
}
function queueValue(info, key, value) {
  const preset = S.preset;
  if (!preset) return;
  if (valuePending && (valuePending.name !== info.name || valuePending.preset !== preset)) flushValue();
  const values = Object.assign({}, (info.meta.presets || {})[preset] || {});
  values[key] = value;
  info.meta.presets[preset] = values; // keep the render in step with the field
  valuePending = { name: info.name, preset, values, key };
  if (valueTimer) clearTimeout(valueTimer);
  valueTimer = setTimeout(() => {
    valueTimer = null;
    const p = valuePending;
    valuePending = null;
    putPending(p);
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
    S.confirm = { text: "reset " + info.name + " to the version that ships with compy? your changes are lost.", verb: "reset", id: info.name, kind: "reset" };
    go("#/configs");
    return;
  }
  try {
    await api(cfgURL(info.name) + (info.modified ? "/resync" : "/sync"), { method: "POST" });
    await loadCore();
    await loadYAML(info.name);
    buildEditorForm(byName(info.name) || info); // a remote source re-syncs like any remote content
    S.unlocked = false; destroyEditor();
    note(info.name + " re-synced from " + hostOf(info));
  } catch (e) { showError(e); }
}

/* Switching the editor's preset tabs: on a tier-3 config the tabs are the
   FORM'S switcher — the form's values swap to the newly selected preset's
   bag — so a dirty form gets the inline confirm first (pickPreset arms it,
   the card's confirmBar runs switchPreset). Tier 2 keeps its instant switch
   (the value cards autosave; there is nothing unsaved to lose). */
function pickPreset(info, p, t3) {
  if (t3 && eformDirty()) { S.chipAsk = p; render(); return; }
  switchPreset(info, p);
}
function switchPreset(info, p) {
  flushValue(); // a tier-2 card edit pending against the old preset
  S.chipAsk = null;
  S.preset = p; S.presetSel[info.name] = p;
  buildEditorForm(info); // tier 3: the form re-seeds from p's bag (no-op for tier 2)
  render();
}

/* A new or duplicated preset copies an existing bag verbatim, so there is
   nothing new to prove: ?validate=false skips re-asking the collector about
   values it already blessed (and lets a bag that was itself saved with the
   escape hatch still be copied). Tier-2 presets never validated either way. */
async function addPreset(info) {
  const n = freePresetName(presetsOf(info)); // same scheme as the configs-row plus
  // Tier 3 seeds from the selected preset's bag — an empty bag would lose
  // the schema's structure (and its required rows); tier 2 starts blank.
  const values = S.source != null ? (info.meta.presets || {})[S.preset] || {} : {};
  await createPreset(info, n, values);
}
async function dupPreset(info, p) {
  const list = presetsOf(info);
  let n = p + "-copy", i = 2;
  while (list.indexOf(n) > -1) { n = p + "-copy-" + i; i++; }
  await createPreset(info, n, (info.meta.presets || {})[p] || {});
}
async function createPreset(info, n, values) {
  // A dirty tier-3 form belongs to the CURRENT preset: creating another
  // must not swap the draft out from under it — the selection stays put.
  const stay = eformDirty();
  try {
    await apiJSON(cfgURL(info.name) + "/presets/" + enc(n) + "?validate=false", "PUT", { values });
    await loadCore();
    if (stay) { note(n + " added — the tabs switch to it", 3600); return; }
    S.preset = n; S.presetSel[info.name] = n;
    buildEditorForm(byName(info.name) || info);
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
    if (S.preset === p) {
      // The selected preset died with any draft it had; re-seed from a
      // survivor. Deleting an unselected one leaves the form untouched.
      S.preset = left[Math.max(i - 1, 0)] || null;
      buildEditorForm(byName(info.name) || info);
    }
    render();
  } catch (e) { showError(e); }
}
async function renamePreset(info, from, raw) {
  const to = slug(raw) || from;
  S.presetErr = null;
  if (to === from) { render(); return; }
  if (presetsOf(info).indexOf(to) > -1) {
    S.presetErr = "a preset called " + to + " already exists. name not changed.";
    render();
    return;
  }
  try {
    await apiJSON(cfgURL(info.name) + "/presets/" + enc(from) + "/rename", "POST", { to });
    await loadCore();
    S.preset = to; S.presetSel[info.name] = to;
    render();
  } catch (e) { showError(e); render(); }
}

/* Save: ONE button, one save, and the pane's CONTENT picks the route —
   exactly the backend's own rule (catalog.IsSource). Plain yaml goes
   through the yaml flow (over a templated config that DEMOTES it: the
   source is dropped, the form disappears); template source and/or a dirty
   form go through the tier-3 flow — the source PUT first, then the
   selected preset's bag, the server's own order. */
async function saveConfig(info) {
  if (S.saving) return;
  const paneDirty = !!(cm && cmDirty);
  const formDirty = eformDirty();
  if (!paneDirty && !formDirty) return; // both views clean: nothing to save
  if (paneDirty && !isSourceText(cm.getValue())) { saveYAML(info); return; }
  saveT3(info, true);
}

/* The yaml flow: the collector's verdict, then the result panel.

   BACKEND NOTE: there is no dry-run — nothing validates YAML text that is
   not on disk yet. So the draft is written, validated, and the previous
   text put back when the collector rejects it, which is what makes the
   panel's "nothing was saved" true. (Rejected plain yaml over a templated
   config restores the source PAIR — plain prev yaml would demote it.) */
async function saveYAML(info) {
  const next = cm.getValue();
  const prev = S.yaml;
  const prevSource = S.source; // non-null: this save demotes a templated config
  S.saving = true;
  resetValPanel();
  clearError();
  render();
  const t0 = Date.now();
  const wasRunning = isRunningCfg(info.name);
  try {
    await api(cfgURL(info.name) + "/yaml", { method: "PUT", headers: { "Content-Type": "text/plain" }, body: next });
    if (!wasRunning) await api(cfgURL(info.name) + "/validate", { method: "POST" });
    cmDirty = false;
    const secs = ((Date.now() - t0) / 1000).toFixed(1);
    S.valOk = wasRunning
      ? "saved and re-applied to the running collector in " + secs + "s"
      : "saved in " + secs + "s. " + info.name + " is not running, so nothing restarted.";
    await loadCore();
    // Reload reflects truth: pasting source text promotes (the form
    // appears), plain yaml over a templated config demotes (it goes away).
    await loadYAML(info.name);
    buildEditorForm(byName(info.name) || info);
  } catch (e) {
    S.valErr = e.message || String(e);
    // Is the rejection explained by variables that simply have no values
    // yet? The draft IS on disk right now (the PUT wrote it before
    // validation failed), so the server's parse of it — the same rule the
    // activation pre-flight uses — is the draft's own variables.
    try {
      const d = await api(cfgURL(info.name));
      S.valMissing = missingRequired(d.info, S.preset);
    } catch (e2) { /* unexplained stays unexplained */ }
    try {
      if (prevSource != null) {
        // The PUT already dropped the source; putting plain prev yaml back
        // would leave the config demoted by a REJECTED save. Restore the
        // source instead (preset bags were never touched) — validate=false
        // skips re-validating known-good text and never touches the
        // running collector.
        await apiJSON(cfgURL(info.name) + "/source?validate=false", "PUT", { source: prevSource });
      } else {
        await api(cfgURL(info.name) + "/yaml", { method: "PUT", headers: { "Content-Type": "text/plain" }, body: prev });
      }
    } catch (e2) { /* nothing better to do */ }
  }
  S.saving = false;
  render();
}

/* The tier-3 flow: the source PUT carries the pane (source only — values
   live in presets now), the preset PUT carries the whole bag the form
   edits; both dirty means two requests, source first — the server's own
   order — and the result panel owes the PAIR honesty, because the write is
   not atomic: the source can land and the values still be refused. Each
   PUT validates server-side with nothing-was-saved semantics for its own
   half. Light client checks (the form's own key space) run first; a 400
   naming a form field lands beside it; everything else is the failure
   panel, with the rendered excerpt when the diagnostic names a line and
   the ?validate=false escape (which re-runs THIS save for whatever is
   still unsaved — only the collector's verdict is skipped; the schema
   still has to parse and the bag to fit). */
async function saveT3(info, validate) {
  if (S.saving) return;
  const paneDirty = !!(cm && cmDirty && isSourceText(cm.getValue()));
  const formDirty = eformDirty();
  if (!paneDirty && !formDirty) return;
  if (formDirty && validate) {
    const errs = knobProblems(S.eform.tpl, S.eform.knobs);
    if (Object.keys(errs).length) { S.eform.errs = errs; render(); return; }
  }
  S.saving = true;
  resetValPanel();
  clearError();
  render();
  const t0 = Date.now();
  const wasRunning = isRunningCfg(info.name);
  const preset = S.preset;
  const draft = formDirty ? S.eform.knobs : null;
  const q = validate ? "" : "?validate=false";
  const what = paneDirty && formDirty ? "the source and the " + preset + " values"
    : paneDirty ? "the source" : "the " + preset + " values";
  let sourceSaved = false, stale = false;
  try {
    if (paneDirty) {
      await apiJSON(cfgURL(info.name) + "/source" + q, "PUT", { source: cm.getValue() });
      cmDirty = false;
      sourceSaved = true;
    }
    if (formDirty) {
      const r = await apiJSON(cfgURL(info.name) + "/presets/" + enc(preset) + q, "PUT", { values: draft });
      stale = !!(r && r.running_stale);
    }
    await loadCore();
    await loadYAML(info.name);
    // Re-derive the form from the (possibly new) schema and the stored bag;
    // values the server pruned or defaulted come back through the reload.
    buildEditorForm(byName(info.name) || info);
    const secs = ((Date.now() - t0) / 1000).toFixed(1);
    if (validate) {
      S.valOk = wasRunning
        ? "saved " + what + " and re-applied to the running collector in " + secs + "s"
        : "saved " + what + " in " + secs + "s. " + info.name + " is not running, so nothing restarted.";
    } else {
      S.valOk = "saved " + what + " without validating. fix it here, then activate to check it."
        + (stale ? " the running collector keeps the previous version until then." : "");
    }
  } catch (e) {
    // Which half failed? The source goes first, so once it landed (or was
    // never dirty) the failure is the preset write's.
    if (sourceSaved || !paneDirty) {
      if (sourceSaved) {
        // The new source IS stored: reload it, re-derive the form from its
        // schema, then put the unsaved draft back so the values survive
        // the fixing.
        try { await loadCore(); await loadYAML(info.name); } catch (e2) { /* keep what we have */ }
        buildEditorForm(byName(info.name) || info);
        if (S.eform && !S.eform.parseErr) S.eform.knobs = seedKnobs(S.eform.tpl, draft, true);
      }
      const fe = e.status === 400 ? parseFieldErr(e.message || "") : null;
      if (fe && S.eform && S.eform.tpl && knownKnobPath(S.eform.tpl, fe.path)) {
        S.eform.errs[fe.path] = fe.msg; // field-adjacent, same key space as the client checks
        if (sourceSaved) note("the source was saved — fix the marked value, then save again", 5200);
      } else {
        S.valErr = e.message || String(e);
        S.valHead = "the rendered config was rejected. the " + preset + " values were not stored.";
        S.valNote = sourceSaved ? "the source half of this save landed — only the values were refused." : "";
        // The server proved the render and stored nothing (of this half),
        // so the stored render (S.yaml) is the closest thing to the
        // rejected one — line numbers can drift by exactly the failed
        // edit; rendered→source mapping is deferred (design note).
        S.valExcerpt = excerptAround(S.yaml, errLineOf(S.valErr), 3);
        S.valAnyway = true;
      }
    } else {
      // The source itself was refused: nothing was saved, and the values
      // were never sent. Honest headline: a source that doesn't PARSE was
      // refused by the template engine, not the collector — and
      // validate=false can't keep it either (the server never stores what
      // it can't render), so the escape hatch is only offered when the
      // source parses.
      S.valErr = e.message || String(e);
      const parseFail = /^template (schema|body):/.test(S.valErr);
      S.valHead = parseFail
        ? "the template source doesn't parse. nothing was saved."
        : "the rendered config was rejected. nothing was saved.";
      S.valNote = formDirty ? "the " + preset + " value changes were not sent — they ride the next save, once the source is fixed." : "";
      S.valExcerpt = parseFail ? "" : excerptAround(S.yaml, errLineOf(S.valErr), 3);
      S.valAnyway = !parseFail;
    }
  }
  S.saving = false;
  render();
}

/* Save-anyway: the escape hatch the panel offers when the rejection is
   explained by unset variables. Writes with ?validate=false — the backend
   never touches the running collector for an unvalidated write, so an
   active running config keeps its previous version (running_stale) until
   the user restarts or activates. */
async function saveAnyway(info) {
  if (S.saving || !cm) return;
  const next = cm.getValue();
  S.saving = true;
  resetValPanel();
  clearError();
  render();
  try {
    const r = await api(cfgURL(info.name) + "/yaml?validate=false", { method: "PUT", headers: { "Content-Type": "text/plain" }, body: next });
    cmDirty = false;
    S.valOk = "saved without validating. fill the values, then activate to check it."
      + (r && r.running_stale ? " the running collector keeps the previous version until then." : "");
    await loadCore();
    await loadYAML(info.name); // reload reflects truth (a demotion included)
    buildEditorForm(byName(info.name) || info);
  } catch (e) {
    S.valErr = e.message || String(e);
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
  // Honesty under flood: the fetch is a sliding LOG_LINES-line window, so a
  // saturated window means the file holds more than we can show.
  const clipped = S.log.split("\n").length >= LOG_LINES;
  bar.appendChild(span("logcount", lineCount(shown) + " of " + (clipped ? "the last " + LOG_LINES : lineCount(all)) + " lines"));
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

  return el("div", { attrs: { style: "flex:1; min-height:0; display:flex; flex-direction:column;" } }, [bar, logRows(shown)]);
}

// The DOM rows for one log entry: the main line plus its continuations.
function entryRows(logs, l) {
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

/* The .logs element persists across renders (logDom) so the 3s refresh can
   recycle rows instead of rebuilding thousands of them: when logDiff aligns
   the old filtered entries with the new (the common tail case — the window
   slid, everything between is identical), drop the rows that slid off the
   top, re-render the last previously-shown entry only when it actually grew
   (a poll can catch it mid-dump), and append what's new. Any other
   change — filter or level flipped, content replaced (factory reset), first
   show — rebuilds the pane exactly as before. And the most common tick of
   all — no visible change (sameShown) — touches nothing: zero DOM
   operations inside the container, no scroll movement, no repaint; the
   whole pane costs one appendChild (render()'s re-adoption of the
   persistent node into the fresh screen). Scroll is render()'s job
   (captureLogScroll/restoreLogScroll), which leans on recycled rows keeping
   their node identity. */
let logDom = null; // { key, shown, node, msg, changed }
function logRows(shown) {
  const key = S.level + " " + S.query.trim().toLowerCase();
  // The empty pane's message is part of the rendered identity: two empty
  // views can differ ("no output yet." vs "no lines match this filter.").
  const msg = shown.length ? "" : (logEntries().length ? "no lines match this filter." : "no output yet.");
  if (logDom && logDom.key === key && logDom.msg === msg && sameShown(logDom.shown, shown)) {
    // The common 3s tick: same filter, same visible lines (new lines the
    // filter hides included). ZERO DOM operations inside the container —
    // render() re-adopts it untouched, nothing repaints, and changed:false
    // tells restoreLogScroll not to move anything either.
    logDom = { key, shown, node: logDom.node, msg, changed: false };
    return logDom.node;
  }
  const d = logDom && logDom.key === key ? logDiff(logDom.shown, shown) : null;
  let logs;
  if (d) {
    logs = logDom.node;
    let drop = 0;
    for (let i = 0; i < d.dropped; i++) drop += 1 + logDom.shown[i].cont.length;
    while (drop--) logs.removeChild(logs.firstChild);
    // logDiff re-offers the last old entry (d.from) because a poll can catch
    // it mid-dump; re-render its rows only when it actually grew — a pure
    // append then touches no existing row at all (kept rows: zero removals).
    let from = d.from;
    if (shown[from].raw === logDom.shown[logDom.shown.length - 1].raw) {
      from++;
    } else {
      let tail = 1 + logDom.shown[logDom.shown.length - 1].cont.length;
      while (tail--) logs.removeChild(logs.lastChild);
    }
    for (let i = from; i < shown.length; i++) entryRows(logs, shown[i]);
    // A window cut mid-entry (headCut) keeps the old head entry's rows —
    // logDom.shown records what is actually rendered, so the next diff and
    // the row-drop arithmetic stay in step with the DOM.
    logDom = { key, shown: d.headCut ? [logDom.shown[d.dropped]].concat(shown.slice(1)) : shown, node: logs, msg, changed: true };
  } else {
    logs = el("div", { class: "logs" });
    for (const l of shown) entryRows(logs, l);
    if (msg) logs.appendChild(el("div", { class: "nologs", text: msg }));
    logDom = { key, shown, node: logs, msg, changed: true };
  }
  return logs;
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
  await Promise.all([loadCore(), loadCollector()]); // independent GETs, in parallel
  render();
}
async function stopCollector() {
  clearError();
  try { await api("/api/service/stop", { method: "POST" }); } catch (e) { showError(e); }
  await Promise.all([loadCore(), loadCollector()]); // independent GETs, in parallel
  render();
}

/* ── screen 4: settings ───────────────────────────────────────────── */

// envGuide: the shell-side alternatives to the OS-level toggle — three
// ways to wire a shell, each with the log toolbar's click-to-copy idiom.
// One entry per way: the label on its own line, the command on its own
// line beneath it (2026-08-27 feedback — the label+command single line was
// hard to process line by line).
function envGuide() {
  const entry = (label, cmd) => el("div", { class: "enventry" }, [
    el("div", { class: "n sans", text: label }),
    el("div", { class: "envcmd" }, [
      el("code", { text: cmd }),
      el("button", {
        class: "ico", title: "copy this command",
        on: { click: () => copyText(cmd, "command copied") },
      }, [icon("copy", 12)]),
    ]),
  ]);
  return el("div", { class: "envguide" }, [
    el("div", { class: "n sans", text: "or wire a shell instead:" }),
    entry("current shell, right now", 'eval "$(compy env)"'),
    entry("every new shell — append to ~/.zshrc (or your shell's rc)", "echo 'eval \"$(compy env)\"' >> ~/.zshrc"),
    entry("one command only", "compy run -- <cmd>"),
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
    seg.appendChild(el("button", {
      class: S.theme === k ? "on" : "", text: k,
      attrs: { "aria-pressed": S.theme === k ? "true" : "false" },
      on: { click: () => setTheme(k) },
    }));
  }
  const proto = (S.settings && S.settings.protocol) || (S.status && S.status.protocol) || "http/protobuf";
  const pseg = el("div", { class: "seg" });
  for (const p of ["grpc", "http/protobuf", "http/json"]) {
    pseg.appendChild(el("button", {
      class: proto === p ? "on" : "", text: p,
      attrs: { "aria-pressed": proto === p ? "true" : "false" },
      on: { click: () => setProtocol(p) },
    }));
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
    // A real <button> so the switch is keyboard-operable (Enter/Space) for
    // free; role/aria-checked keep it announced as a switch.
    el("button", {
      class: "srow clickable", on: { click: () => setOSEnv(!osEnvOn) },
      attrs: { type: "button", role: "switch", "aria-checked": osEnvOn ? "true" : "false" },
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

  /* one quiet line under the app card: the running build, and — release
     builds with a newer release known — how to get it (compyVersionLine,
     helpers.js). dev builds never carry the update half: the backend only
     claims on stamped releases. */
  const verLine = compyVersionLine(S.status && S.status.compy_version, S.status && S.status.compy_update);
  if (verLine) wrap.appendChild(el("div", { class: "ver-line sans", text: verLine }));
  /* the brew-upgrade window, same quiet line as the sidebar. */
  if (S.status && S.status.stale_binary) {
    wrap.appendChild(el("div", { class: "ver-line sans", text: "compy was upgraded — restart the collector to run the new version" }));
  }

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
      yaml: "", yamlOf: null, source: null, eform: null, find: "",
      busyId: null, err: null, errName: null, errKept: null,
      newOpen: false, newName: "", newUrl: "", newErr: null, fetching: false,
      confirm: null,
      presetSel: {}, presetsOpenId: null, inline: null, inlineName: "", inlineDraft: null,
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
  // carried on the row and refreshed with the 3s loadDistros cycle. The
  // whole decision ladder (state line, class, glyph, titles) lives in
  // helpers.js's distroState; this function only renders it.
  const d = S.dl[b.name] || b.download || {};
  const checking = !!S.up[b.name];
  const t = distroState(b, d, checking);

  const row = el("div", { class: "bin-grid bin-row" + (t.inUse ? " on" : "") }, [
    el("span", { class: "ic" + (t.blocked || (t.bundled && !t.here) ? " off" : ""), title: t.state }, [icon(t.glyph, 13)]),
    el("span", { class: "nm", text: b.name, title: b.path || (t.blocked ? "not available on macOS" : t.bundled ? "not built — packaging/collector/build.sh" : "not downloaded yet") }),
    el("span", { class: "bin-state" }, [
      el("span", { class: "s" + t.cls, text: t.state, title: t.failed ? d.error : null }),
      t.busy && d.pct != null ? el("span", { class: "pbar" }, [el("i", { attrs: { style: "width:" + d.pct + "%" } })]) : null,
    ]),
    el("span", { class: "bin-actions" }, [
      el("button", {
        class: "ico" + (!t.inUse && t.here ? " accent" : ""),
        title: t.playTitle,
        attrs: t.inUse || !t.here ? { disabled: "" } : null,
        on: { click: () => useDistro(b.name) },
      }, [icon("play", 11, true)]),
      el("button", {
        class: "ico" + (!t.here && !t.blocked && !t.bundled ? " accent" : ""),
        title: t.dlTitle,
        attrs: t.here || t.blocked || t.busy || t.bundled ? { disabled: "" } : null,
        on: { click: () => fetchDistro(b.name) },
      }, [icon("download", 13)]),
      el("button", {
        // Accent when a newer release is actually known — the existing
        // "this is the actionable thing" treatment, no popups.
        class: "ico" + (b.latest_available && t.canUpdate && !t.busy && !checking ? " accent" : ""),
        title: t.updTitle,
        attrs: t.canUpdate && !t.busy && !checking ? null : { disabled: "" },
        on: { click: () => updateDistro(b.name) },
      }, [icon("refresh", 13)]),
      el("button", {
        class: "ico", title: t.here || t.mine ? "change path" : "nothing installed yet",
        attrs: t.here || t.mine ? null : { disabled: "" },
        on: { click: () => changePath(b) },
      }, [icon("folder", 13)]),
      el("button", {
        class: "ico del", title: t.mine ? "remove " + b.name : "only collectors you added can be removed",
        attrs: t.mine ? null : { disabled: "" },
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
// pollProgress follows a download on the progress route at 300ms until it
// settles: failures land on the row via S.dl, and a finished download runs
// onDone (refresh what the pull changed) before the final render. Shared by
// the explicit fetch and the update's download half.
function pollProgress(name, onDone) {
  const poll = async () => {
    let p;
    try { p = await api("/api/distros/" + enc(name) + "/progress"); } catch (e) { S.dl[name] = null; showError(e); render(); return; }
    S.dl[name] = p;
    render();
    if (p.status === "downloading" || p.status === "idle") { setTimeout(poll, 300); return; }
    if (p.status === "done") { S.dl[name] = null; await onDone(); render(); }
  };
  setTimeout(poll, 300);
}
// The fetch starts a download and returns; progress lives behind its own
// route because the request that started it is long gone.
async function fetchDistro(name) {
  clearError();
  S.dl[name] = { status: "downloading", pct: 0 };
  render();
  try { await api("/api/distros/" + enc(name) + "/fetch", { method: "POST" }); } catch (e) { S.dl[name] = null; showError(e); render(); return; }
  pollProgress(name, loadDistros);
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
  pollProgress(name, async () => {
    await loadDistros(); await loadCore();
    note(name + " updated to " + r.latest, 3200);
  });
}

/* ── background refresh ───────────────────────────────────────────────
   Never touches the DOM while an input in the screen has focus, and never
   while a slow action or an open menu/inline editor would be yanked away. */
function refreshBlocked() {
  const a = document.activeElement;
  // The log filter is exempt: freezing the refresh while it has focus froze
  // the live tail under a lit "live tail" light. Its value and caret survive
  // the re-render (S.query + captureFocus), so typing loses nothing.
  const inField = a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA" || a.tagName === "SELECT")
    && a.dataset.fk !== "logq";
  return inField || S.busyId || S.stoppingId || S.saving || S.restarting || S.presetsOpenId || S.inline
    || S.confirm || S.preflight || S.newOpen || S.unlockAsk || S.chipAsk || S.resetArm || S.resetBusy
    || document.querySelector("dialog[open]")
    || (S.screen === "editor" && (cmDirty || eformDirty()));
}
async function refresh() {
  if (refreshBlocked()) return;
  // The editor screen holds a fully-rendered CodeMirror (viewportMargin
  // Infinity on tier 3): rebuilding it costs a ~140ms main-thread stall on
  // a 2000-line source, every 3 seconds, under a reader who saw nothing
  // change. When the tick brought back an identical snapshot, only the
  // sidebar re-renders (its own container — the screen DOM is untouched).
  const before = S.screen === "editor" ? JSON.stringify([S.status, S.configs]) : null;
  try {
    await loadCore();
    if (S.screen === "collector") { if (S.tail) await loadCollector(); }
    else if (S.screen === "configs") await loadCollector();
    else if (S.screen === "settings") await Promise.all([loadDistros(), loadSettings()]);
  } catch (e) { return; } // a transient failure should not blank the window
  if (refreshBlocked()) return;
  if (before !== null && S.screen === "editor" && JSON.stringify([S.status, S.configs]) === before) {
    renderSidebar();
    return;
  }
  render();
}

/* ── boot ─────────────────────────────────────────────────────────── */
loadTheme();
applyTheme();
// The retired shown-by-default help strips left compy.helpDismissed* keys
// behind; sweep them once so stale state stops accumulating.
try {
  for (const k of Object.keys(localStorage)) {
    if (k.startsWith("compy.helpDismissed")) localStorage.removeItem(k);
  }
} catch (e) { /* private mode */ }
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
  // Escape closes the transient panel that is open — the platform
  // expectation, and the same outcome as each panel's cancel button (the
  // inline editor's draft is as gone as its cancel makes it). One panel per
  // press, in roughly topmost order; an open <dialog> handles its own
  // Escape, so it must not also close a panel underneath.
  if (e.key === "Escape" && !document.querySelector("dialog[open]")) {
    if (S.presetsOpenId) { S.presetsOpenId = null; render(); return; }
    if (S.preflight) { S.preflight = null; render(); return; }
    if (S.confirm) { S.confirm = null; render(); return; }
    if (S.inline) { S.inline = null; S.inlineDraft = null; render(); return; }
    if (S.newOpen) { S.newOpen = false; render(); return; }
    if (S.chipAsk) { S.chipAsk = null; render(); return; }
    if (S.unlockAsk) { S.unlockAsk = false; render(); return; }
    if (S.resetArm) { S.resetArm = false; S.resetTyped = ""; render(); return; }
    if (S.adoptAsk) { S.adoptAsk = false; render(); return; }
  }
  if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "s") {
    e.preventDefault();
    if (!editorDirty()) return;
    const info = byName(S.editId);
    if (info) saveConfig(info);
  }
});
enterRoute();
setInterval(refresh, 3000);
