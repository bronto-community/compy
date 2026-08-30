"use strict";

/* Tests for helpers.js under Node's stdlib runner — no npm, no build:
     node --test internal/webui/static/
   (the same invocation CI runs). */

const test = require("node:test");
const assert = require("node:assert/strict");
const H = require("./helpers.js");

/* The activation pre-flight rule. This table mirrors internal/cfgstore's
   TestMissingRequired (cfgstore_test.go) verbatim — keep these tables
   identical: the JS helper hand-mirrors the Go rule, and this mirror is
   what catches a silent drift between them. */
test("missingRequired mirrors cfgstore.MissingRequired", () => {
  const info = {
    meta: {
      active_preset: "staging",
      presets: {
        staging: { BRONTO_KEY: "bro_live_1" },
        empty: { BRONTO_KEY: "   " }, // whitespace is not a value
        full: { BRONTO_KEY: "k", OTLP_ENDPOINT: "e" },
      },
    },
    vars: [
      { name: "BRONTO_KEY" }, // required
      { name: "OTLP_ENDPOINT" }, // required
      { name: "DATASET", default: "default", has_default: true }, // yaml fallback
      { name: "COMPY_HTTP_PORT" }, // compy-injected
    ],
  };
  const cases = [
    ["explicit preset with one value", "staging", ["OTLP_ENDPOINT"]],
    ["whitespace values count as missing", "empty", ["BRONTO_KEY", "OTLP_ENDPOINT"]],
    ["all values present", "full", []],
    ["empty preset resolves to the active one", "", ["OTLP_ENDPOINT"]],
    ["unknown preset has no values", "nope", ["BRONTO_KEY", "OTLP_ENDPOINT"]],
  ];
  for (const [name, preset, want] of cases) {
    assert.deepEqual(H.missingRequired(info, preset), want, name);
  }

  // No presets at all: nothing has a value, everything required is missing.
  const bare = { meta: { presets: {} }, vars: info.vars };
  assert.deepEqual(H.missingRequired(bare, ""), ["BRONTO_KEY", "OTLP_ENDPOINT"], "no presets");
});

test("slug", () => {
  assert.equal(H.slug("My Collector!"), "my-collector");
  assert.equal(H.slug("--a__b--"), "a-b");
  assert.equal(H.slug("!!!"), "");
});

test("originOf and hostOf", () => {
  assert.equal(H.originOf({ provenance: "remote" }), "url");
  assert.equal(H.originOf({ provenance: "shipped" }), "builtin");
  assert.equal(H.originOf({ provenance: "local" }), "user");
  assert.equal(H.originOf({ provenance: "local", has_template: true }), "tmpl");
  assert.equal(H.originOf({ provenance: "remote", has_template: true }), "url");
  assert.equal(H.hostOf({ meta: { remote_url: "https://otel.acme.dev/c.yaml" } }), "otel.acme.dev");
  // An unparseable URL falls back to the raw string; no meta means "".
  assert.equal(H.hostOf({ meta: { remote_url: "not a url" } }), "not a url");
  assert.equal(H.hostOf({}), "");
});

test("nameList", () => {
  assert.equal(H.nameList(["A"]), "A");
  assert.equal(H.nameList(["A", "B"]), "A and B");
  assert.equal(H.nameList(["A", "B", "C"]), "A, B and C");
});

test("freePresetName takes the first free slot", () => {
  assert.equal(H.freePresetName([]), "preset-2");
  assert.equal(H.freePresetName(["default", "preset-2"]), "preset-3");
  assert.equal(H.freePresetName(["preset-2", "preset-4"]), "preset-3");
});

test("portsCompact", () => {
  assert.equal(H.portsCompact([14317, 14318]), ":14317 :14318");
  assert.equal(H.portsCompact([1, 2, 3, 4, 5]), "5 ports open");
  assert.equal(H.portsCompact([]), "");
});

test("yamlLineOf finds the first referencing line", () => {
  const yaml = "receivers:\n  otlp:\n    endpoint: ${env:HOST}\nkey: ${TOKEN}\n";
  assert.equal(H.yamlLineOf(yaml, "HOST"), "line 3");
  assert.equal(H.yamlLineOf(yaml, "TOKEN"), "line 4");
  assert.equal(H.yamlLineOf(yaml, "NOPE"), "");
  assert.equal(H.yamlLineOf("", "HOST"), "");
});

test("fmtCount", () => {
  assert.equal(H.fmtCount(null), "—");
  assert.equal(H.fmtCount(0), "0");
  assert.equal(H.fmtCount(999), "999");
  assert.equal(H.fmtCount(1000), "1k");
  assert.equal(H.fmtCount(1500), "1.5k");
  assert.equal(H.fmtCount(2000000), "2m");
});

test("parseZapLine recognises zap console lines", () => {
  const line = "2026-08-28T09:15:01.123Z\terror\texporterhelper/queue_sender.go:101\tExporting failed.\t{\"kind\": \"exporter\"}";
  const e = H.parseZapLine(line);
  assert.equal(e.time, "09:15:01");
  assert.equal(e.level, "error");
  assert.equal(e.caller, "exporterhelper/queue_sender.go:101");
  assert.equal(e.text, "Exporting failed.");
  assert.deepEqual(e.attrs, { kind: "exporter" });
  assert.equal(e.raw, line);

  // No caller field: the message starts at field 2.
  const bare = H.parseZapLine("2026-08-28T09:15:02.000Z\tinfo\tEverything is ready.");
  assert.equal(bare.caller, "");
  assert.equal(bare.text, "Everything is ready.");

  // A malformed JSON tail stays in the message text.
  const mal = H.parseZapLine("2026-08-28T09:15:03.000Z\twarn\tmsg\t{not json");
  assert.equal(mal.attrs, null);
  assert.equal(mal.text, "msg {not json");

  // Not an entry-starting line: continuations and dumps return null.
  assert.equal(H.parseZapLine("Span #0"), null);
  assert.equal(H.parseZapLine("2026-08-28T09:15:04.000Z\tnotalevel\tmsg"), null);
});

test("parseAttrs accepts only JSON objects", () => {
  assert.deepEqual(H.parseAttrs('{"a": 1}'), { a: 1 });
  assert.equal(H.parseAttrs("[1,2]"), null);
  assert.equal(H.parseAttrs("junk"), null);
  assert.equal(H.parseAttrs("null"), null);
});

/* The settings collector-table's decision ladder: one case per rung of the
   state line (priority order matters — busy beats failed beats checking…),
   plus the update affordance's ownership rules. */
test("distroState decision ladder", () => {
  const idle = {};
  const cases = [
    // [label, b, d, checking, want]
    ["downloading wins over everything", { name: "contrib", selected: true, downloaded: true, definition: true, available: true },
      { status: "downloading", pct: 40 }, false,
      { state: "downloading… 40%", cls: " accent", glyph: "dot", updTitle: "downloading…" }],
    ["download with no declared length shows no made-up percent", { name: "contrib", definition: true, available: true },
      { status: "downloading" }, false, { state: "downloading… " }],
    ["failure keeps a short reason; the ladder stops there", { name: "core", definition: true, available: true },
      { status: "failed", error: "distro core: fetch https://example.com/very/long/url/that/never/ends: HTTP 503\nmore" }, false,
      { state: "download failed · fetch https://example.com/very/long/url/that/…", cls: " bad" }],
    ["release check in flight", { name: "contrib", downloaded: true, definition: true, available: true }, idle, true,
      { state: "checking for a newer release…", cls: " accent", updTitle: "checking…" }],
    ["bundled, built", { name: "otelcol-compy", bundled: true, downloaded: true, version: "0.104.0" }, idle, false,
      { state: "shipped with compy · 0.104.0", updTitle: "updates with compy releases", canUpdate: false }],
    ["bundled, not built", { name: "otelcol-compy", bundled: true }, idle, false,
      { state: "not built — packaging/collector/build.sh", cls: " off", glyph: "ban", playTitle: "not built — packaging/collector/build.sh", dlTitle: "never downloaded — built with compy" }],
    ["in use, installed, update known", { name: "contrib", selected: true, downloaded: true, definition: true, available: true, version: "0.104.0", latest_available: "0.105.0" }, idle, false,
      { state: "in use · 0.104.0 · 0.105.0 available", cls: " accent", glyph: "dot", canUpdate: true, updTitle: "0.105.0 is available. update contrib", playTitle: "already in use" }],
    ["in use but not downloaded advertises the fetch", { name: "contrib", selected: true, definition: true, available: true, fetch_version: "0.105.0" }, idle, false,
      { state: "in use · downloads 0.105.0", glyph: "dot" }],
    ["no build for this platform", { name: "windows-only", definition: true }, idle, false,
      { state: "not available on macOS", cls: " off", glyph: "ban", canUpdate: false, updTitle: "not available on macOS", dlTitle: "not available on macOS" }],
    ["installed definition", { name: "core", downloaded: true, definition: true, available: true, version: "0.104.0" }, idle, false,
      { state: "installed · 0.104.0", cls: "", glyph: "circle", canUpdate: true, updTitle: "check for a newer release and update core", playTitle: "run every config on core", dlTitle: "already installed" }],
    ["user entry", { name: "mine", downloaded: true, user_entry: true, path: "/opt/otelcol" }, idle, false,
      { state: "added by you", cls: " mine", canUpdate: false, updTitle: "user-managed — update the binary at its path yourself" }],
    ["available to download", { name: "core", definition: true, available: true, fetch_version: "0.105.0" }, idle, false,
      { state: "available to download · downloads 0.105.0", cls: "", glyph: "download", canUpdate: false, updTitle: "nothing installed — download fetches the newest release", playTitle: "download it first", dlTitle: "download and verify core" }],
  ];
  for (const [label, b, d, checking, want] of cases) {
    const got = H.distroState(b, d, checking);
    for (const k in want) assert.deepEqual(got[k], want[k], label + ": " + k);
  }
});

/* The settings screen's quiet build line. The update half only appears when
   the backend claims one — which it only does on release builds. */
test("compyVersionLine", () => {
  const cases = [
    ["release, no update", "0.1.0", "", "compy 0.1.0"],
    ["release with update", "0.1.0", "0.2.0", "compy 0.1.0 · 0.2.0 available — brew upgrade compy"],
    ["dev build (backend never sends an update)", "dev · 787da79a1b2c", "", "compy dev · 787da79a1b2c"],
    ["no status yet", "", "", ""],
    ["no status yet ignores a stray update", undefined, "0.2.0", ""],
  ];
  for (const [name, version, update, want] of cases) {
    assert.equal(H.compyVersionLine(version, update), want, name);
  }
});

/* The template form's schema helpers: draft seeding, light validation, and
   the parse of a field-naming 400 into a field-adjacent error. */
const TPL = {
  name: "custom-endpoints",
  sections: [{ id: "backends", label: "Backends" }, { id: "pipeline", label: "Pipeline options", collapsed: true }],
  backends: {
    min: 1, max: 8,
    fields: [
      { name: "name", type: "slug", label: "Name" },
      { name: "endpoint", type: "url", label: "Endpoint" },
      { name: "auth_header", type: "string", label: "Auth header", optional: true },
      { name: "api_key", type: "secret", label: "API key" },
      { name: "auth_scheme", type: "choice", options: ["none", "Bearer"], default: "none", advanced: true },
      { name: "signals", type: "multi", options: ["traces", "metrics", "logs"], default: ["traces", "metrics", "logs"], advanced: true },
    ],
  },
  fields: [
    { name: "batch", type: "toggle", default: true, section: "pipeline" },
    { name: "debug_tee", type: "toggle", default: false, section: "pipeline" },
  ],
};

test("seedKnobs fills defaults and keeps stored knobs", () => {
  // Fresh draft: schema defaults, one empty backend row, no secrets.
  const fresh = H.seedKnobs(TPL, null);
  assert.deepEqual(fresh, {
    batch: true, debug_tee: false,
    backends: [{ name: "", endpoint: "", auth_header: "", auth_scheme: "none", signals: ["traces", "metrics", "logs"] }],
  });
  assert.equal("api_key" in fresh.backends[0], false, "secrets never enter the draft");
  // Defaults are copies: editing the draft must not mutate the schema.
  fresh.backends[0].signals.push("junk");
  assert.deepEqual(TPL.backends.fields[5].default, ["traces", "metrics", "logs"]);

  // Change-options: stored meta.knobs win, schema growth backfills.
  const seeded = H.seedKnobs(TPL, {
    debug_tee: true,
    backends: [{ name: "hc", endpoint: "https://api.honeycomb.io", auth_scheme: "Bearer" }],
  });
  assert.equal(seeded.debug_tee, true);
  assert.equal(seeded.batch, true, "missing knob falls back to the default");
  assert.equal(seeded.backends[0].name, "hc");
  assert.deepEqual(seeded.backends[0].signals, ["traces", "metrics", "logs"], "field the schema grew");
});

test("knobProblems keys errors the way the server does", () => {
  const knobs = H.seedKnobs(TPL, null);
  knobs.backends[0].name = "Bad Name";
  knobs.backends[0].endpoint = "api.honeycomb.io";
  knobs.backends[0].signals = [];
  const errs = H.knobProblems(TPL, knobs);
  assert.deepEqual(errs, {
    "backends[0].name": "lowercase letters, digits, dashes",
    "backends[0].endpoint": "must start with http:// or https://",
    "backends[0].signals": "pick at least one",
  });
  // The thirty-second path is clean.
  knobs.backends[0].name = "honeycomb";
  knobs.backends[0].endpoint = "https://api.honeycomb.io";
  knobs.backends[0].signals = ["traces"];
  assert.deepEqual(H.knobProblems(TPL, knobs), {});
  // Empty required vs empty optional.
  knobs.backends[0].name = "";
  knobs.backends[0].auth_header = "";
  assert.deepEqual(H.knobProblems(TPL, knobs), { "backends[0].name": "required" });
});

test("parseFieldErr", () => {
  assert.deepEqual(H.parseFieldErr("backends[0].endpoint: required"),
    { path: "backends[0].endpoint", msg: "required" });
  assert.deepEqual(H.parseFieldErr("memory_limiter: true is not true/false"),
    { path: "memory_limiter", msg: "true is not true/false" });
  assert.deepEqual(H.parseFieldErr("backends: need 1 to 8 entries, got 0"),
    { path: "backends", msg: "need 1 to 8 entries, got 0" });
  // Errors that name no field go to the errbar instead.
  assert.equal(H.parseFieldErr('config "x" already exists'), null);
  assert.equal(H.parseFieldErr(""), null);
});

test("knownKnobPath admits only paths the form can show", () => {
  assert.equal(H.knownKnobPath(TPL, "backends"), true, "group-level error");
  assert.equal(H.knownKnobPath(TPL, "backends[0].endpoint"), true);
  assert.equal(H.knownKnobPath(TPL, "batch"), true);
  // A collector diagnostic that merely LOOKS field-shaped stays a panel error.
  assert.equal(H.knownKnobPath(TPL, "exporters"), false);
  assert.equal(H.knownKnobPath(TPL, "backends[0].nope"), false);
  assert.equal(H.knownKnobPath(TPL, ""), false);
  assert.equal(H.knownKnobPath(null, "batch"), false);
  const noRepeat = { fields: [{ name: "batch", type: "toggle" }] };
  assert.equal(H.knownKnobPath(noRepeat, "backends"), false, "no repeat group, no group error");
});

/* Tier-3 sources: detection mirrors catalog.IsSource, the client schema
   parse is deliberately loose (the server is the authority), and creation
   placeholders are type-derived only. */
const SRC = JSON.stringify(TPL) + "\n---\nreceivers: {}\n";

test("isSourceText mirrors catalog.IsSource", () => {
  assert.equal(H.isSourceText(SRC), true);
  assert.equal(H.isSourceText("  \n\t{\"name\":\"x\"}\n---\nbody"), true, "leading blanks ignored");
  assert.equal(H.isSourceText("receivers:\n  otlp:\n"), false, "plain yaml");
  assert.equal(H.isSourceText("# comment\n{}\n---\n"), false, "yaml never starts with '{' — a comment doesn't count");
  assert.equal(H.isSourceText('{"name":"x"} no separator'), false);
  assert.equal(H.isSourceText(""), false);
});

test("parseSourceSchema parses the front matter, loosely", () => {
  const t = H.parseSourceSchema(SRC);
  assert.equal(t.name, "custom-endpoints");
  assert.equal(t.backends.min, 1);
  // Broken front matter (mid-edit) is null — the form steps aside.
  assert.equal(H.parseSourceSchema('{"name": broken\n---\nbody'), null);
  assert.equal(H.parseSourceSchema("plain: yaml"), null);
  assert.equal(H.parseSourceSchema('[1,2]\n---\nbody'), null, "front matter must be an object");
  assert.equal(H.parseSourceSchema('{"fields": "nope"}\n---\nbody'), null, "fields must be objects in an array");
  assert.equal(H.parseSourceSchema('{"backends": [1]}\n---\nbody'), null, "backends must be an object");
});

test("placeholderKnobs makes a creatable draft with no field names of its own", () => {
  const knobs = H.placeholderKnobs(TPL);
  // Required slug/url fields get neutral type-derived placeholders…
  assert.equal(knobs.backends[0].name, "backend");
  assert.equal(knobs.backends[0].endpoint, "https://api.example.com");
  // …defaults and optionals stay what seedKnobs made them.
  assert.equal(knobs.backends[0].auth_scheme, "none");
  assert.equal(knobs.backends[0].auth_header, "");
  assert.equal(knobs.batch, true);
  // A required multi without a default picks everything rather than nothing.
  const t2 = { fields: [{ name: "sig", type: "multi", options: ["a", "b"] }] };
  assert.deepEqual(H.placeholderKnobs(t2).sig, ["a", "b"]);
  // The draft passes the same light checks the form runs before a save.
  assert.deepEqual(H.knobProblems(TPL, knobs), {});
});

/* The failure panel's rendered excerpt: ±3 lines around the line the
   collector's diagnostic names, the named line marked. */
test("errLineOf and excerptAround", () => {
  assert.equal(H.errLineOf("yaml: line 12: could not find expected ':'"), 12);
  assert.equal(H.errLineOf('collector "x" rejected the config'), 0);
  const yaml = ["l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8"].join("\n");
  assert.equal(H.excerptAround(yaml, 5, 3), [
    "2   l2", "3   l3", "4   l4", "5 > l5", "6   l6", "7   l7", "8   l8",
  ].join("\n"));
  assert.equal(H.excerptAround(yaml, 1, 3), ["1 > l1", "2   l2", "3   l3", "4   l4"].join("\n"), "clamped at the top");
  assert.equal(H.excerptAround(yaml, 99, 3), "", "a line the yaml doesn't have");
  assert.equal(H.excerptAround(yaml, 0, 3), "", "no line named");
  assert.equal(H.excerptAround("", 1, 3), "1 > ", "degenerate but stable");
});

/* The log pane's tail-mode scroll rule and its incremental-render diff. */
test("atLogBottom", () => {
  const cases = [
    ["exactly at the end", 900, 100, 1000, true],
    ["within the 40px threshold", 865, 100, 1000, true],
    ["just past the threshold", 859, 100, 1000, false],
    ["scrolled to the top", 0, 100, 1000, false],
    ["content shorter than the pane", 0, 100, 80, true],
  ];
  for (const [name, top, client, scroll, want] of cases) {
    assert.equal(H.atLogBottom(top, client, scroll), want, name);
  }
});

test("sameShown", () => {
  const e = (raw) => ({ raw });
  assert.equal(H.sameShown([e("a"), e("b")], [e("a"), e("b")]), true, "identical");
  assert.equal(H.sameShown([], []), true, "both empty");
  assert.equal(H.sameShown([e("a")], [e("a"), e("b")]), false, "appended");
  assert.equal(H.sameShown([e("a"), e("b")], [e("a"), e("b\ncont")]), false, "last entry grew");
});

test("logDiff", () => {
  const e = (raw) => ({ raw });
  const [a, b, c, d, x] = ["a", "b", "c", "d", "x"].map(e);
  const dump = e("a\ndump line 1\ndump line 2");
  const cases = [
    ["pure append", [a, b], [a, b, c], { dropped: 0, headCut: false, from: 1 }],
    ["no change re-renders only the last entry", [a, b], [a, b], { dropped: 0, headCut: false, from: 1 }],
    ["window slid: dropped off the top, appended below", [a, b, c], [b, c, d], { dropped: 1, headCut: false, from: 1 }],
    ["window slid mid-dump: head is a fragment of the old entry", [dump, b, c], [e("dump line 2"), b, c, d], { dropped: 0, headCut: true, from: 2 }],
    ["slid past one entry AND cut the next", [a, dump, c], [e("dump line 2"), c, d], { dropped: 1, headCut: true, from: 1 }],
    ["head fragment not at a line boundary", [dump, b], [e("line 2"), b], null],
    ["last entry grew a continuation", [a, b], [a, e("b\ncont"), c], { dropped: 0, headCut: false, from: 1 }],
    ["last entry grew mid-line (not at a boundary)", [a, b], [a, e("bcd")], null],
    ["different content", [a, b], [x, c], null],
    ["shrunk (reset)", [a, b, c], [a], null],
    ["was empty", [], [a], null],
    ["single old entry, window slid past it", [a], [b, c], null],
  ];
  for (const [name, oldE, newE, want] of cases) {
    assert.deepEqual(H.logDiff(oldE, newE), want, name);
  }
});
