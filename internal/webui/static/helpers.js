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

/* node --test bridge; a no-op in the browser. */
if (typeof module !== "undefined") {
  module.exports = {
    slug, originOf, hostOf, missingRequired, nameList, freePresetName,
    portsCompact, yamlLineOf, fmtCount, parseZapLine, parseAttrs,
  };
}
