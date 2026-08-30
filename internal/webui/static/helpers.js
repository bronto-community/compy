"use strict";

/* compy window UI — pure helpers, extracted from app.js so they run under
   `node --test` (helpers.test.js) as well as in the page. Loaded as a
   classic script BEFORE app.js (index.html), so every top-level function
   here is a global binding app.js reaches by bare name — the same style as
   vendor/lucide-icons.js. No DOM, no fetch, no state: anything that needs
   S or document stays in app.js. */

function slug(s) { return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""); }
function originOf(info) {
  // Origin is provenance ONLY — builtin (shipped), url (remote), user
  // (local). Templating (has_template) is orthogonal and never earns a
  // glyph of its own: the editor's form/source panes already say it
  // (owner ruling, 2026-08-30).
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
    .filter((v) => {
      if (v.has_default || /^COMPY_/.test(v.name)) return false;
      const val = values[v.name];
      // Non-string values (a demoted tier-3 bag's leftovers) count as set:
      // no claim is better than a wrong one — the Go rule verbatim.
      return val == null || (typeof val === "string" && !val.trim());
    })
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

/* ── template form (config templates, the schema-driven creation form) ─
   The schema (GET /api/templates) is arrays in declaration order; these
   helpers turn it into a knobs draft and back. Secrets never appear in
   knobs — they have no value at render time (they become preset cards). */
function fieldDefault(f) {
  if (f.default != null) return Array.isArray(f.default) ? f.default.slice() : f.default;
  if (f.type === "toggle") return false;
  if (f.type === "multi") return [];
  if (f.type === "choice") return (f.options || [])[0] || "";
  return "";
}
// One row (or the config level) of the draft: stored values win, schema
// defaults fill the rest — so a form seeded from an older bag still carries
// every field the schema has grown since. withSecrets includes secret
// fields (default "") — the editor's form edits a PRESET'S BAG, and secrets
// are ordinary bag members there (Amendment 4); the creation draft leaves
// them out (they have no value yet).
function seedRow(fields, from, withSecrets) {
  const row = {};
  for (const f of fields || []) {
    if (f.type === "secret" && !withSecrets) continue;
    row[f.name] = from && from[f.name] != null ? from[f.name] : fieldDefault(f);
  }
  return row;
}
function seedKnobs(tpl, from, withSecrets) {
  const knobs = seedRow(tpl.fields, from, withSecrets);
  if (tpl.backends) {
    const rows = from && Array.isArray(from.backends) && from.backends.length ? from.backends : [null];
    knobs.backends = rows.map((r) => seedRow(tpl.backends.fields, r, withSecrets));
  }
  return knobs;
}
/* The tier-3 activation pre-flight rule: schema-required fields (secrets and
   non-secrets alike, non-optional with no default) that the preset's bag
   leaves absent or blank, as form-keyed paths ("backends[0].api_key").
   KEEP IN LOCKSTEP with internal/catalog's Template.MissingRequired — the
   test mirrors catalog_test.go's TestMissingRequiredBag. */
function missingRequiredT3(tpl, bag) {
  bag = bag || {};
  const missing = [];
  const check = (prefix, fields, values) => {
    for (const f of fields || []) {
      if (f.optional || f.default != null) continue;
      const v = (values || {})[f.name];
      if (v == null || (typeof v === "string" && !v.trim())) missing.push(prefix + f.name);
    }
  };
  check("", tpl.fields, bag);
  if (tpl.backends && Array.isArray(bag.backends)) {
    bag.backends.forEach((row, i) => check("backends[" + i + "].", tpl.backends.fields, row));
  }
  return missing;
}
/* Missing-value names, made readable for people. A tier-2 var name
   (UPPER_SNAKE) passes verbatim — it IS the name the yaml uses. A tier-3
   field path becomes prose: "backends[0].api_key" reads "backend
   honeycomb's api key" when the bag's row carries a name (row 1 otherwise),
   with the schema's label when one is at hand; the humanized field name is
   the honest fallback, never a guess. */
function prettyMissing(bag, paths, tpl) {
  const label = (fields, name) => {
    const f = (fields || []).find((x) => x.name === name);
    return ((f && f.label) || name.replace(/_/g, " ")).toLowerCase();
  };
  return (paths || []).map((p) => {
    if (/^[A-Z0-9_]+$/.test(p)) return p; // a tier-2 env var name stays itself
    const m = /^backends\[(\d+)\]\.([a-z0-9_]+)$/.exec(p);
    if (!m) return label(tpl && tpl.fields, p);
    const i = parseInt(m[1], 10);
    const row = (bag && Array.isArray(bag.backends) && bag.backends[i]) || {};
    const who = typeof row.name === "string" && row.name ? row.name : "" + (i + 1);
    return "backend " + who + "'s " + label(tpl && tpl.backends && tpl.backends.fields, m[2]);
  });
}
/* Light client-side checks only — the server re-validates everything. */
function fieldProblem(f, v) {
  const s = typeof v === "string" ? v.trim() : v;
  if (f.type === "slug") {
    if (!s) return f.optional ? "" : "required";
    if (!/^[a-z0-9][a-z0-9-]*$/.test(s)) return "lowercase letters, digits, dashes";
  }
  if (f.type === "url") {
    if (!s) return f.optional ? "" : "required";
    if (!/^https?:\/\/./.test(s)) return "must start with http:// or https://";
  }
  if (f.type === "string" && !f.optional && f.default == null && !s) return "required";
  if (f.type === "multi" && !f.optional && (!v || !v.length)) return "pick at least one";
  return "";
}
// Every problem in the draft, keyed the way the server keys its 400s
// ("backends[0].endpoint") so both land on the same field.
function knobProblems(tpl, knobs) {
  const errs = {};
  for (const f of tpl.fields || []) {
    if (f.type === "secret") continue;
    const p = fieldProblem(f, knobs[f.name]);
    if (p) errs[f.name] = p;
  }
  if (tpl.backends) {
    (knobs.backends || []).forEach((row, i) => {
      for (const f of tpl.backends.fields || []) {
        if (f.type === "secret") continue;
        const p = fieldProblem(f, row[f.name]);
        if (p) errs["backends[" + i + "]." + f.name] = p;
      }
    });
  }
  return errs;
}
// A validation 400 that names a field ("backends[0].endpoint: required")
// parses into {path, msg} for field-adjacent display; anything else (a
// name collision, a render failure) returns null and goes to the errbar.
function parseFieldErr(msg) {
  const m = /^([a-z0-9_]+(?:\[\d+\])?(?:\.[a-z0-9_]+)?): ([^]+)$/.exec(msg || "");
  return m ? { path: m[1], msg: m[2] } : null;
}
// A parsed 400 path is field-adjacent only when the form actually has that
// control; a collector diagnostic that happens to look like "exporters: …"
// belongs to the failure panel, never to a field that doesn't exist.
function knownKnobPath(tpl, path) {
  if (!tpl || !path) return false;
  if (tpl.backends) {
    if (path === "backends") return true;
    const m = /^backends\[\d+\]\.([a-z0-9_]+)$/.exec(path);
    if (m) return (tpl.backends.fields || []).some((f) => f.name === m[1]);
  }
  return (tpl.fields || []).some((f) => f.name === path);
}

/* ── tier-3 config sources (schema front matter + "---" + template body) ─
   isSourceText mirrors internal/catalog's IsSource rule, two forms:
   - JSON (exact mirror): first non-blank byte '{' plus a "\n---\n"
     separator. Plain collector yaml never starts with '{'.
   - YAML front matter (approximate mirror): first non-blank line is a
     "---" marker, a closing "---" line follows, and a top-level "name:"
     line sits between them. Go PARSES the between-text (strict decode +
     name) — JS has no yaml parser, so this cheap sniff only has to be
     consistent enough for UI routing: a false positive routes to the
     source save, where the server answers with the real schema error; a
     false negative routes to the yaml save, where server-side tier
     detection re-judges the text anyway. The parsed schema itself always
     comes from the server (config detail's "template").
   KEEP IN LOCKSTEP with catalog.IsSource / catalog.LooksLikeSource. */
function isSourceText(s) {
  const t = (s || "").replace(/^[ \t\r\n]+/, "");
  if (t.charAt(0) === "{") return (s || "").indexOf("\n---\n") > -1;
  const lines = t.split("\n");
  if (lines[0].replace(/[ \t\r]+$/, "") !== "---") return false;
  let close = -1;
  for (let i = 1; i < lines.length; i++) {
    if (lines[i].replace(/[ \t\r]+$/, "") === "---") { close = i; break; }
  }
  return close > 1 && lines.slice(1, close).some((l) => /^name\s*:/.test(l));
}
/* Creation from the catalog is name + create — the editor is the form now —
   but the backend refuses knobs that leave required fields empty. So the
   create sends the schema's defaults plus neutral TYPE-derived placeholders
   for required slug/url/multi fields (no template field name appears here;
   a second catalog entry creates for free), and the user reshapes
   everything in the editor's form. */
function placeholderKnobs(tpl) {
  const fill = (fields, row) => {
    for (const f of fields || []) {
      if (f.type === "secret" || f.optional || f.default != null) continue;
      if (f.type === "slug" && !row[f.name]) row[f.name] = "backend";
      else if (f.type === "url" && !row[f.name]) row[f.name] = "https://api.example.com";
      else if (f.type === "multi" && !(row[f.name] || []).length) row[f.name] = (f.options || []).slice();
    }
  };
  const knobs = seedKnobs(tpl, null);
  fill(tpl.fields, knobs);
  if (tpl.backends) for (const r of knobs.backends || []) fill(tpl.backends.fields, r);
  return knobs;
}
/* The failure panel's rendered excerpt: the collector validates the
   RENDERED yaml, so a diagnostic naming a line means that file, not the
   source. errLineOf finds the named line; excerptAround cuts ±ctx lines
   with the named one marked ("" when the diagnostic names no line the
   yaml has). Rendered→source line mapping is deferred (design note,
   recorded friction). */
function errLineOf(msg) {
  const m = /\bline (\d+)/.exec(msg || "");
  return m ? parseInt(m[1], 10) : 0;
}
function excerptAround(yaml, line, ctx) {
  const all = (yaml || "").split("\n");
  if (!line || line > all.length) return "";
  const c = ctx || 3;
  const from = Math.max(1, line - c), to = Math.min(all.length, line + c);
  const w = String(to).length;
  const out = [];
  for (let n = from; n <= to; n++) {
    out.push(String(n).padStart(w) + (n === line ? " > " : "   ") + all[n - 1]);
  }
  return out.join("\n");
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

/* splitAttrs: the collector staples its OWN telemetry identity onto every
   zap line — a "resource" object ({service.instance.id, service.name,
   service.version}, identical for the whole run) plus per-component
   "otelcol.*" keys on component lines — and that boilerplate drowns the
   actual fields ("resource logs": 2, "endpoint": …). Split an attrs object
   into {main, noise}: noise is that self-telemetry (a "resource" key whose
   value is an object, and any "otelcol."-prefixed key), main is everything
   else. Stateless per line by design, so row recycling and the sliding
   window never change how a given line renders; a line carrying neither
   shape renders unchanged (noise empty). */
function splitAttrs(attrs) {
  const main = {}, noise = {};
  for (const k in attrs) {
    const v = attrs[k];
    const selfRes = k === "resource" && v && typeof v === "object" && !Array.isArray(v);
    if (selfRes || k.startsWith("otelcol.")) noise[k] = v;
    else main[k] = v;
  }
  return { main, noise };
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
/* sameShown: the zero-op tick's test — same visible entries, line for line.
   O(n) string compares over the ~500-line window, cheaper than one DOM
   mutation; true means the pane needs no work at all this refresh. */
function sameShown(a, b) {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i].raw !== b[i].raw) return false;
  return true;
}
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
    fieldDefault, seedRow, seedKnobs, missingRequiredT3, prettyMissing,
    fieldProblem, knobProblems, parseFieldErr,
    knownKnobPath, isSourceText, placeholderKnobs,
    errLineOf, excerptAround,
    portsCompact, yamlLineOf, fmtCount, parseZapLine, parseAttrs, splitAttrs,
    atLogBottom, logDiff, sameShown,
    distroState,
  };
}
