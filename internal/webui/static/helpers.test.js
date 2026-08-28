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
