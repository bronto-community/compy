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
