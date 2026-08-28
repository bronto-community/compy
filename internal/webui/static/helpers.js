"use strict";

/* compy window UI — pure helpers, extracted from app.js so they run under
   `node --test` (helpers.test.js) as well as in the page. Loaded as a
   classic script BEFORE app.js (index.html), so every top-level function
   here is a global binding app.js reaches by bare name — the same style as
   vendor/lucide-icons.js. No DOM, no fetch, no state: anything that needs
   S or document stays in app.js. */

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
/* The activation pre-flight rule, shared with the CLI's warning (cfgstore.
   MissingRequired): required means the yaml has no `:-fallback`
   (has_default false), the name isn't compy-injected (COMPY_*), and the
   preset holds no non-empty value.
   KEEP IN LOCKSTEP with internal/cfgstore's MissingRequired — the test
   table in helpers.test.js mirrors cfgstore_test.go's TestMissingRequired
   verbatim; keep these tables identical. */
function missingRequired(info, preset) {
  // An empty preset resolves to the config's active preset, exactly as the
  // Go rule (and Activate) does.
  if (!preset) preset = (info.meta && info.meta.active_preset) || "";
  const values = ((info.meta && info.meta.presets) || {})[preset] || {};
  return (info.vars || [])
    .filter((v) => !v.has_default && !/^COMPY_/.test(v.name) && !(values[v.name] || "").trim())
    .map((v) => v.name);
}
/* The settings screen's quiet build line: the running compy version, plus —
   when the backend claims one (release builds only) — the newer release and
   how to get it. "" when no version is known yet (first status not in). */
function compyVersionLine(version, update) {
  if (!version) return "";
  let s = "compy " + version;
  if (update) s += " · " + update + " available — brew upgrade compy";
  return s;
}
function nameList(names) {
  return names.length === 1 ? names[0]
    : names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}
/* The generated new-preset name: "default" is every config's invariant
   first preset, so the scheme continues it — preset-2, preset-3, … —
   first free wins. Never random; the field stays editable. */
function freePresetName(list) {
  for (let i = 2; ; i++) if (list.indexOf("preset-" + i) < 0) return "preset-" + i;
}
function portsCompact(ports) {
  if (ports.length > 4) return ports.length + " ports open";
  return ports.map((p) => ":" + p).join(" ");
}
function yamlLineOf(yaml, key) {
  if (!yaml) return "";
  const lines = yaml.split("\n");
  for (let i = 0; i < lines.length; i++) if (lines[i].includes("${env:" + key) || lines[i].includes("${" + key)) return "line " + (i + 1);
  return "";
}
function fmtCount(n) {
  if (n == null) return "—";
  if (n >= 1000000) return (n / 1000000).toFixed(1).replace(/\.0$/, "") + "m";
  if (n >= 1000) return (n / 1000).toFixed(1).replace(/\.0$/, "") + "k";
  return String(n);
}

/* ── collector log parsing ────────────────────────────────────────────
   otelcol stderr is heterogeneous: zap console lines (ts \t level \t
   [caller \t] message [\t {json attrs}]) interleaved with the debug
   exporter's multi-line plain dumps. parseZapLine recognises an
   entry-starting line; anything else is a continuation (app.js's
   logEntries groups them). */
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

/* ── log pane scroll + incremental diff ───────────────────────────────
   atLogBottom: within the threshold of the end counts as pinned (tail
   mode); scrolling further up holds position across refreshes instead. */
function atLogBottom(scrollTop, clientHeight, scrollHeight) {
  return scrollHeight - clientHeight - scrollTop <= 40;
}
/* logDiff aligns the previously rendered (filtered) entry list with the
   new one so the pane can recycle the unchanged rows: the server tail is
   a sliding window over an append-only file, so the common refresh drops
   a few entries off the top and adds a few at the bottom, and everything
   between is identical. Returns {dropped, from} — old entries [0,dropped)
   fell out of the window, old entries [dropped, old.length-1) are
   reusable verbatim, and rendering resumes at new index `from`
   (= old.length-1-dropped: the last old entry always re-renders, because
   a poll can catch it mid-dump and its continuation lines grow). null
   means no clean alignment — rebuild the pane.
   The window almost never slides to an entry boundary: a multi-line debug
   dump straddles the cut, so the new list's first entry is the tail
   fragment of an old entry. headCut reports that case — the pane keeps the
   old entry's rows (a few lines older than the strict window, gone at the
   next boundary) instead of re-rendering the fragment above the reused
   middle.
   ponytail: O(old²) worst case on logs of identical lines; fine at the
   500-line window, revisit if the window grows 10x. */
function logDiff(oldE, newE) {
  if (!newE.length) return null;
  for (let d = 0; d < oldE.length; d++) {
    const kept = oldE.length - d;
    if (kept < 2 || newE.length < kept) break;
    const head = oldE[d].raw;
    const headCut = head !== newE[0].raw;
    if (headCut && !head.endsWith("\n" + newE[0].raw)) continue;
    let ok = true;
    for (let i = 1; i < kept - 1; i++) {
      if (oldE[d + i].raw !== newE[i].raw) { ok = false; break; }
    }
    if (!ok) continue;
    const oldLast = oldE[oldE.length - 1].raw, newAt = newE[kept - 1].raw;
    if (newAt !== oldLast && !newAt.startsWith(oldLast + "\n")) continue;
    return { dropped: d, headCut, from: kept - 1 };
  }
  return null;
}

/* ── the settings collector-table's per-row decision ladder ───────────
   distroState derives everything the row's look depends on — the state
   line, its class, the leading glyph, whether the update affordance
   applies, and the three action-button titles — from the row (b), its
   download state (d: {status, pct, error}), and whether a release check
   is in flight. Flat early returns instead of the nested ternaries this
   started as; pure (only its arguments), so it runs under node --test.
   app.js's distroRow renders what this returns. */
function distroState(b, d, checking) {
  const busy = d.status === "downloading";
  const failed = d.status === "failed";
  const inUse = !!b.selected;
  const here = !!b.downloaded;
  const blocked = !!b.definition && !b.available;
  const mine = !!b.user_entry && !b.definition;
  const bundled = !!b.bundled;
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

  const state = (() => {
    if (busy) return "downloading… " + (d.pct == null ? "" : d.pct + "%");
    if (failed) return short ? "download failed · " + short : "download failed";
    if (checking) return "checking for a newer release…";
    if (bundled) return here ? "shipped with compy" + ver : "not built — packaging/collector/build.sh";
    if (inUse) return "in use" + ver + (here ? avail : fetches);
    if (blocked) return "not available on macOS";
    if (here) return mine ? "added by you" : "installed" + ver + avail;
    return "available to download" + fetches;
  })();
  const cls = busy || checking || inUse ? " accent"
    : failed ? " bad"
      : blocked || (bundled && !here) ? " off"
        : mine ? " mine" : "";
  const glyph = inUse ? "dot" : blocked || (bundled && !here) ? "ban" : here ? "circle" : "download";

  // The update affordance belongs to INSTALLED pinned definitions only: the
  // bundled collector updates with compy releases, a user-managed path is
  // the user's to update, and an undownloaded row has nothing to update —
  // its download fetches the newest release directly. Each disabled title
  // says so.
  const canUpdate = !!b.definition && !b.user_entry && !blocked && here;
  const updTitle = (() => {
    if (bundled) return "updates with compy releases";
    if (!b.definition || b.user_entry) return "user-managed — update the binary at its path yourself";
    if (blocked) return "not available on macOS";
    if (!here) return "nothing installed — download fetches the newest release";
    if (busy) return "downloading…";
    if (checking) return "checking…";
    if (b.latest_available) return b.latest_available + " is available. update " + b.name;
    return "check for a newer release and update " + b.name;
  })();
  const playTitle = (() => {
    if (inUse) return "already in use";
    if (here) return "run every config on " + b.name;
    if (bundled) return "not built — packaging/collector/build.sh";
    return "download it first";
  })();
  const dlTitle = (() => {
    if (bundled) return "never downloaded — built with compy";
    if (busy) return "downloading…";
    if (failed) return "try again";
    if (blocked) return "not available on macOS";
    if (here) return "already installed";
    return "download and verify " + b.name;
  })();

  return {
    state, cls, glyph, canUpdate, playTitle, dlTitle, updTitle,
    busy, failed, inUse, here, blocked, mine, bundled,
  };
}

/* node --test bridge; a no-op in the browser. */
if (typeof module !== "undefined") {
  module.exports = {
    slug, originOf, hostOf, missingRequired, nameList, freePresetName,
    compyVersionLine,
    portsCompact, yamlLineOf, fmtCount, parseZapLine, parseAttrs,
    atLogBottom, logDiff,
    distroState,
  };
}
