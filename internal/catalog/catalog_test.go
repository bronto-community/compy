package catalog

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/vars"
)

// canonicalKnobs is the knob set the golden render, the tier-invariant
// test, and the sandbox validate all share: 2 backends, mixed temporality,
// all pipeline toggles on.
func canonicalKnobs(t *testing.T) map[string]any {
	t.Helper()
	var knobs map[string]any
	if err := json.Unmarshal([]byte(`{
		"backends": [
			{"name": "honeycomb", "endpoint": "https://api.honeycomb.io", "auth_header": "x-honeycomb-team"},
			{"name": "dynatrace", "endpoint": "https://abc123.live.dynatrace.com/api/v2/otlp",
			 "auth_header": "Authorization", "auth_scheme": "Api-Token", "temporality": "to-delta",
			 "extra_header": "X-Tenant", "extra_value": "tenant-1"}
		],
		"offline_queue": true, "debug_tee": true
	}`), &knobs); err != nil {
		t.Fatal(err)
	}
	return knobs
}

func get(t *testing.T, name string) Template {
	t.Helper()
	tmpl, err := Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func TestLoadTemplates(t *testing.T) {
	ts, err := Templates()
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].Name != "custom-endpoints" {
		t.Fatalf("Templates() = %v, want exactly custom-endpoints", ts)
	}
	if ts[0].Description == "" {
		t.Error("custom-endpoints has no description")
	}
	if _, err := Get("no-such-template"); !state.IsBadRequest(err) {
		t.Errorf("Get(unknown) err = %v, want BadRequest", err)
	}
}

// TestSchemaOrder locks declaration order = form order: the JSON arrays in
// the front matter come through in file order.
func TestSchemaOrder(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	var beNames []string
	for _, f := range tmpl.Backends.Fields {
		beNames = append(beNames, f.Name)
	}
	wantBE := []string{"name", "endpoint", "auth_header", "api_key", "auth_scheme",
		"extra_header", "extra_value", "signals", "temporality"}
	if !reflect.DeepEqual(beNames, wantBE) {
		t.Errorf("backend field order = %v, want %v", beNames, wantBE)
	}
	var names []string
	for _, f := range tmpl.Fields {
		names = append(names, f.Name)
	}
	want := []string{"memory_limiter", "batch", "resource_detection", "offline_queue", "debug_tee"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("field order = %v, want %v", names, want)
	}
	if tmpl.Backends.Min != 1 || tmpl.Backends.Max != 8 {
		t.Errorf("repeat bounds = %d..%d, want 1..8", tmpl.Backends.Min, tmpl.Backends.Max)
	}
	if len(tmpl.Sections) != 2 || !tmpl.Sections[1].Collapsed {
		t.Errorf("sections = %+v, want backends + collapsed pipeline", tmpl.Sections)
	}
}

// TestAdvancedRuleLint is the spec's lintable rule as a test: a field may
// be advanced, or live in a collapsed section, only if it carries a default
// that is safe to ignore. Secrets are exempt (they have no render-time
// value at all). Runs over EVERY template so a future catalog entry is
// linted the day it lands.
func TestAdvancedRuleLint(t *testing.T) {
	ts, err := Templates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range ts {
		collapsed := map[string]bool{}
		for _, s := range tmpl.Sections {
			collapsed[s.ID] = s.Collapsed
		}
		check := func(where string, fields []Field) {
			for _, f := range fields {
				if f.Type == "secret" {
					continue
				}
				if (f.Advanced || collapsed[f.Section]) && f.Default == nil {
					t.Errorf("%s: %s.%s is advanced/collapsed without a default — collapsed must mean safely ignorable", tmpl.Name, where, f.Name)
				}
			}
		}
		check("fields", tmpl.Fields)
		if tmpl.Backends != nil {
			check("backends", tmpl.Backends.Fields)
		}
	}
}

// TestNormalizeBag is the validation matrix: every rejection is
// BadRequest-marked and names the offending field.
func TestNormalizeBag(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	be := func(rows ...map[string]any) map[string]any {
		var l []any
		for _, r := range rows {
			l = append(l, r)
		}
		return map[string]any{"backends": l}
	}
	ok := map[string]any{"name": "a", "endpoint": "https://x.example"}

	bad := []struct {
		name, wantIn string
		knobs        map[string]any
	}{
		{"no backends", "backends: need 1 to 8", map[string]any{}},
		{"too many backends", "backends: need 1 to 8", be(ok, ok, ok, ok, ok, ok, ok, ok, ok)},
		{"backends not a list", "backends: not a list", map[string]any{"backends": "x"}},
		{"missing name", "backends[0].name: required", be(map[string]any{"endpoint": "https://x.example"})},
		{"bad slug", "backends[0].name", be(map[string]any{"name": "Bad Name", "endpoint": "https://x.example"})},
		{"missing endpoint", "backends[0].endpoint: required", be(map[string]any{"name": "a"})},
		{"bad url", "backends[0].endpoint", be(map[string]any{"name": "a", "endpoint": "not a url"})},
		{"bad choice", "backends[0].auth_scheme", be(map[string]any{"name": "a", "endpoint": "https://x.example", "auth_scheme": "Digest"})},
		{"bad multi member", "backends[0].signals", be(map[string]any{"name": "a", "endpoint": "https://x.example", "signals": []any{"traces", "profiles"}})},
		{"empty multi", "backends[0].signals", be(map[string]any{"name": "a", "endpoint": "https://x.example", "signals": []any{}})},
		{"unknown field", "backends[0].port: unknown field", be(map[string]any{"name": "a", "endpoint": "https://x.example", "port": 1})},
		{"secret non-string", "backends[0].api_key", be(map[string]any{"name": "a", "endpoint": "https://x.example", "api_key": 5})},
		{"duplicate names", "duplicate name", be(ok, map[string]any{"name": "a", "endpoint": "https://y.example"})},
		{"toggle non-bool", "debug_tee", func() map[string]any { m := be(ok); m["debug_tee"] = "yes"; return m }()},
		{"unknown config field", "sampling: unknown field", func() map[string]any { m := be(ok); m["sampling"] = true; return m }()},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tmpl.NormalizeBag(tc.knobs)
			if err == nil {
				t.Fatal("NormalizeBag = nil error, want rejection")
			}
			if !state.IsBadRequest(err) {
				t.Errorf("err %v not BadRequest-marked", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("err %q does not name the field (want %q)", err, tc.wantIn)
			}
		})
	}

	// The happy path fills every default; an absent secret stays absent
	// (never defaulted, never demanded — the pre-flight's business).
	norm, err := tmpl.NormalizeBag(be(ok))
	if err != nil {
		t.Fatal(err)
	}
	row := norm["backends"].([]any)[0].(map[string]any)
	if row["auth_scheme"] != "none" || row["temporality"] != "as-is" {
		t.Errorf("row defaults not filled: %v", row)
	}
	if !reflect.DeepEqual(row["signals"], []string{"traces", "metrics", "logs"}) {
		t.Errorf("signals default = %v", row["signals"])
	}
	if norm["memory_limiter"] != true || norm["offline_queue"] != false {
		t.Errorf("toggle defaults not filled: %v", norm)
	}
	if _, present := row["api_key"]; present {
		t.Error("absent secret invented by normalization")
	}

	// A set secret rides along in the bag (Amendment 4: presets own
	// everything; secrets are ordinary bag members).
	norm, err = tmpl.NormalizeBag(be(map[string]any{
		"name": "a", "endpoint": "https://x.example", "api_key": "shh",
	}))
	if err != nil {
		t.Fatal(err)
	}
	row = norm["backends"].([]any)[0].(map[string]any)
	if row["api_key"] != "shh" {
		t.Errorf("secret dropped from normalized bag: %v", row)
	}
}

// TestRenderGolden locks the exact rendered YAML — comments included — for
// the canonical knob set. If this changes deliberately, update testdata.
func TestRenderGolden(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	got, err := tmpl.Render(canonicalKnobs(t), "/home/u/compy/storage")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile("testdata/custom-endpoints-golden.yaml", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/custom-endpoints-golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("render drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestMetricsGroupsSplit: three temporalities and a metrics-less backend
// give exactly the three split pipelines, each with the right converter and
// exporters.
func TestMetricsGroupsSplit(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	knobs := map[string]any{
		"backends": []any{
			map[string]any{"name": "keep", "endpoint": "https://a.example"},
			map[string]any{"name": "dd", "endpoint": "https://b.example", "temporality": "to-delta"},
			map[string]any{"name": "vm", "endpoint": "https://c.example", "temporality": "to-cumulative"},
			map[string]any{"name": "tr", "endpoint": "https://d.example", "signals": []any{"traces"}},
		},
	}
	out, err := tmpl.Render(knobs, "/s")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"    metrics:\n      receivers: [otlp]\n      processors: [memory_limiter, resource_detection, batch]\n      exporters: [otlphttp/keep]\n",
		"    metrics/delta:\n      receivers: [otlp]\n      processors: [memory_limiter, resource_detection, cumulative_to_delta, batch]\n      exporters: [otlphttp/dd]\n",
		"    metrics/cumulative:\n      receivers: [otlp]\n      processors: [memory_limiter, resource_detection, delta_to_cumulative, batch]\n      exporters: [otlphttp/vm]\n",
		"      exporters: [otlphttp/keep, otlphttp/dd, otlphttp/vm, otlphttp/tr]\n", // traces: all four
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing:\n%s\nin:\n%s", want, out)
		}
	}
	if strings.Contains(out, "otlphttp/tr,") || strings.Contains(out, "[otlphttp/tr]") {
		// tr is traces-only: it must appear in no metrics/logs pipeline.
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "otlphttp/tr") && !strings.Contains(line, "otlphttp/keep") {
				t.Errorf("traces-only backend leaked into %q", line)
			}
		}
	}
}

// TestRenderMinimal: one backend, everything off, renders no processors
// block, no extensions, no debug.
func TestRenderMinimal(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	knobs := map[string]any{
		"backends":           []any{map[string]any{"name": "b", "endpoint": "https://x.example", "auth_header": "Authorization", "auth_scheme": "Bearer"}},
		"memory_limiter":     false,
		"batch":              false,
		"resource_detection": false,
	}
	out, err := tmpl.Render(knobs, "/s")
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"processors", "extensions", "debug", "sending_queue"} {
		if strings.Contains(out, absent) {
			t.Errorf("minimal render contains %q:\n%s", absent, out)
		}
	}
	if !strings.Contains(out, "Authorization: Bearer ${env:B_API_KEY}  # b api key") {
		t.Errorf("auth scheme prefix missing:\n%s", out)
	}
}

// TestTierInvariant: the rendered config, fed back through the tier-2 vars
// parser, yields exactly the secret cards — named, described, and required
// (COMPY ports carry defaults and are compy-injected, so the pre-flight
// asks for the API keys and nothing else).
func TestTierInvariant(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	out, err := tmpl.Render(canonicalKnobs(t), "/home/u/compy/storage")
	if err != nil {
		t.Fatal(err)
	}
	parsed := vars.Parse(out)
	byName := map[string]vars.Var{}
	var required []string
	for _, v := range parsed {
		byName[v.Name] = v
		if !v.HasDefault && !strings.HasPrefix(v.Name, "COMPY_") {
			required = append(required, v.Name)
		}
	}
	if want := []string{"DYNATRACE_API_KEY", "HONEYCOMB_API_KEY"}; !reflect.DeepEqual(required, want) {
		t.Fatalf("required vars = %v, want %v", required, want)
	}
	if d := byName["HONEYCOMB_API_KEY"].Description; d != "honeycomb api key" {
		t.Errorf("HONEYCOMB_API_KEY description = %q", d)
	}
	if d := byName["DYNATRACE_API_KEY"].Description; d != "dynatrace api key" {
		t.Errorf("DYNATRACE_API_KEY description = %q", d)
	}
	// The env-with-default port refs survive as the shipped configs' idiom.
	if !byName["COMPY_GRPC_PORT"].HasDefault || !byName["COMPY_HTTP_PORT"].HasDefault {
		t.Error("COMPY port refs lost their defaults")
	}
}

// TestEnvVarFor: dashes cannot appear in env var names.
func TestEnvVarFor(t *testing.T) {
	if got := envVarFor("my-backend"); got != "MY_BACKEND_API_KEY" {
		t.Errorf("envVarFor = %q", got)
	}
}

// TestIsSource is the tier-detection rule. JSON front matter + separator is
// source (textual commit); YAML front matter between "---" marker lines
// commits only when the between-text strictly parses as a schema with a
// name — plain collector YAML in its usual shapes, the "---" document
// marker included, is not.
func TestIsSource(t *testing.T) {
	src := `{"name": "t", "fields": []}
---
a: 1
`
	ysrc := "---\nname: t\nfields: []\n---\na: 1\n"
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{"source", src, true},
		{"source with leading blank lines", "\n\n" + src, true},
		{"plain yaml", "receivers: {}\n", false},
		{"yaml doc marker", "---\nreceivers: {}\n", false},
		{"yaml containing a --- line", "a: |\n  x\n---\nb: 1\n", false},
		{"front matter without separator", `{"name": "t"}` + "\nbody\n", false},
		{"empty", "", false},
		{"yaml front matter", ysrc, true},
		{"yaml front matter with leading blank lines", "\n\n" + ysrc, true},
		{"yaml front matter, minimal (name only)", "---\nname: t\n---\nbody\n", true},
		{"yaml front matter, marker trailing whitespace", "--- \nname: t\n--- \nbody\n", true},
		{"doc marker plus a later --- but no schema", "---\nreceivers: {}\n---\nexporters: {}\n", false},
		{"yaml front matter, broken yaml between markers", "---\nname: t\nbroken: [\n---\nbody\n", false},
		{"yaml front matter, unknown schema key", "---\nname: t\nwat: 1\n---\nbody\n", false},
		{"yaml front matter, no name", "---\ndescription: d\n---\nbody\n", false},
		{"yaml front matter, comments only", "---\n# nothing\n---\nbody\n", false},
		{"opening marker without a closing one", "---\nname: t\nbody\n", false},
	} {
		if got := IsSource(tc.content); got != tc.want {
			t.Errorf("%s: IsSource = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestLooksLikeSource: the textual-shape test the source-save route uses to
// pick between "never meant as a source" (generic 400) and "a source
// attempt whose schema deserves the loud parse error".
func TestLooksLikeSource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{"json form", `{"name": "t"}` + "\n---\na: 1\n", true},
		{"yaml form, valid schema", "---\nname: t\n---\nbody\n", true},
		{"yaml form, broken schema still looks like one", "---\nname: t\nwat: 1\n---\nbody\n", true},
		{"doc marker with a closing --- looks like one", "---\nreceivers: {}\n---\nmore: {}\n", true},
		{"plain yaml", "receivers: {}\n", false},
		{"doc marker without a closing ---", "---\nreceivers: {}\n", false},
		{"empty", "", false},
	} {
		if got := LooksLikeSource(tc.content); got != tc.want {
			t.Errorf("%s: LooksLikeSource = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestYAMLFrontMatterTwin: the same schema written as YAML front matter and
// as JSON front matter parses to the identical schema struct — order,
// defaults, options, repeat bounds — and renders the identical output.
func TestYAMLFrontMatterTwin(t *testing.T) {
	body := "a: {{.g}}\n{{range .backends}}b: {{.name}}\n{{end}}"
	jsonSrc := `{"name": "twin", "description": "d",
 "sections": [{"id": "s", "label": "S", "collapsed": true}],
 "fields": [
   {"name": "g", "type": "string", "label": "G", "default": "x", "section": "s"},
   {"name": "on", "type": "toggle", "label": "O", "default": true},
   {"name": "k", "type": "secret", "label": "K", "description": "a key"}],
 "backends": {"min": 1, "max": 3, "fields": [
   {"name": "name", "type": "slug", "label": "N"},
   {"name": "signals", "type": "multi", "label": "Sig", "options": ["a", "b"], "default": ["a"], "advanced": true}]}}
---
` + body
	yamlSrc := `---
name: twin
description: d
sections:
  - id: s
    label: S
    collapsed: true
fields:
  - name: g
    type: string
    label: G
    default: x
    section: s
  - name: on
    type: toggle
    label: O
    default: true
  - name: k
    type: secret
    label: K
    description: a key
backends:
  min: 1
  max: 3
  fields:
    - name: name
      type: slug
      label: N
    - name: signals
      type: multi
      label: Sig
      options: [a, b]
      default: [a]
      advanced: true
---
` + body
	jt, err := ParseSource(jsonSrc)
	if err != nil {
		t.Fatal(err)
	}
	yt, err := ParseSource(yamlSrc)
	if err != nil {
		t.Fatal(err)
	}
	strip := func(t Template) Template { t.body, t.raw = nil, ""; return t }
	if !reflect.DeepEqual(strip(jt), strip(yt)) {
		t.Errorf("schemas differ:\njson: %+v\nyaml: %+v", strip(jt), strip(yt))
	}
	if yt.Source() != yamlSrc {
		t.Error("Source() does not round-trip the raw YAML-fronted text")
	}
	bag := map[string]any{"backends": []any{map[string]any{"name": "one", "signals": []string{"b"}}}}
	jout, err := jt.Render(bag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	yout, err := yt.Render(bag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if jout != yout {
		t.Errorf("renders differ:\njson: %q\nyaml: %q", jout, yout)
	}
}

// TestParseSourceYAMLErrors: schema trouble in YAML front matter errors
// loudly (BadRequest), names the trouble, and carries line numbers relative
// to the FULL source (opening marker and leading blanks counted).
func TestParseSourceYAMLErrors(t *testing.T) {
	for _, tc := range []struct{ name, content, wantIn string }{
		{"unknown schema key (KnownFields)", "---\nname: t\nwat: 1\n---\nbody\n", "wat"},
		{"unknown key line is full-source-relative", "---\nname: t\nwat: 1\n---\nbody\n", "line 3"},
		{"leading blanks count too", "\n\n---\nname: t\nwat: 1\n---\nbody\n", "line 5"},
		{"broken yaml", "---\nname: t\nbroken: [\n---\nbody\n", "schema"},
		{"name required", "---\ndescription: d\n---\nbody\n", "name is required"},
		{"bad field type", "---\nname: t\nfields:\n  - name: x\n    type: wat\n---\nbody\n", "unknown type"},
		{"bad body", "---\nname: t\n---\n{{end}}\n", "body"},
	} {
		_, err := ParseSource(tc.content)
		if err == nil {
			t.Errorf("%s: parsed, want error", tc.name)
			continue
		}
		if !state.IsBadRequest(err) {
			t.Errorf("%s: err %v not BadRequest-marked", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.wantIn) {
			t.Errorf("%s: err %q missing %q", tc.name, err, tc.wantIn)
		}
	}
}

// TestParseSource: user sources need no filename to match, keep their raw
// text (what a catalog create copies), and every parse failure is a
// BadRequest naming the trouble.
func TestParseSource(t *testing.T) {
	src := `{"name": "whatever", "description": "d",
 "fields": [{"name": "g", "type": "string", "label": "G", "default": "x"}]}
---
a: {{.g}}
`
	tmpl, err := ParseSource(src)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Source() != src {
		t.Error("Source() does not round-trip the raw text")
	}
	out, err := tmpl.Render(nil, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if out != "a: x\n" {
		t.Errorf("render = %q", out)
	}

	for _, tc := range []struct{ name, content, wantIn string }{
		{"no separator", `{"name": "t"}` + "\nbody", "---"},
		{"bad json", "{nope\n---\nbody\n", "schema"},
		{"unknown schema key", `{"name": "t", "wat": 1}` + "\n---\nbody\n", "wat"},
		{"bad field type", `{"name": "t", "fields": [{"name": "x", "type": "wat"}]}` + "\n---\nbody\n", "unknown type"},
		{"bad body", `{"name": "t"}` + "\n---\n{{end}}\n", "body"},
	} {
		_, err := ParseSource(tc.content)
		if err == nil {
			t.Errorf("%s: parsed, want error", tc.name)
			continue
		}
		if !state.IsBadRequest(err) {
			t.Errorf("%s: err %v not BadRequest-marked", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.wantIn) {
			t.Errorf("%s: err %q missing %q", tc.name, err, tc.wantIn)
		}
	}
}

// TestPruneUnknown: fields the schema no longer declares vanish, backend
// rows included — how stored bags survive a schema edit — while declared
// values, secrets among them (they are declared fields, and pruning a
// secret would delete a key), pass through untouched.
func TestPruneUnknown(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	knobs := map[string]any{
		"debug_tee": true,
		"gone":      "x",
		"backends": []any{map[string]any{
			"name": "hc", "endpoint": "https://x.example",
			"api_key": "shh", "old_field": 1,
		}},
	}
	out := tmpl.PruneUnknown(knobs)
	if _, has := out["gone"]; has {
		t.Errorf("unknown field survived: %v", out)
	}
	if out["debug_tee"] != true {
		t.Errorf("declared field lost: %v", out)
	}
	row := out["backends"].([]any)[0].(map[string]any)
	if row["api_key"] != "shh" {
		t.Errorf("secret did not survive the prune: %v", row)
	}
	if _, has := row["old_field"]; has {
		t.Errorf("unknown row field survived: %v", row)
	}
	if row["name"] != "hc" || row["endpoint"] != "https://x.example" {
		t.Errorf("declared row values lost: %v", row)
	}
	if got := tmpl.PruneUnknown(nil); len(got) != 0 {
		t.Errorf("PruneUnknown(nil) = %v, want empty", got)
	}
}

// TestRenderNeverBakesSecrets: a bag carrying secret values renders to the
// same yaml as one without them — the ${env:} references stay, the value
// appears nowhere. THE invisible rule.
func TestRenderNeverBakesSecrets(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	bag := map[string]any{
		"backends": []any{map[string]any{
			"name": "hc", "endpoint": "https://x.example",
			"auth_header": "x-team", "api_key": "sup3rs3cret",
		}},
	}
	out, err := tmpl.Render(bag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "sup3rs3cret") {
		t.Fatalf("secret value baked into the render:\n%s", out)
	}
	if !strings.Contains(out, "${env:HC_API_KEY}") {
		t.Errorf("env reference missing:\n%s", out)
	}
	delete(bag["backends"].([]any)[0].(map[string]any), "api_key")
	without, err := tmpl.Render(bag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if out != without {
		t.Error("secret presence changed the render")
	}
}

// TestSecretEnv: the env split's mapping — a row secret becomes
// UPPER(row_name)_UPPER(field), agreeing exactly with the rendered
// ${env:} references; blanks are omitted.
func TestSecretEnv(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	bag := map[string]any{
		"backends": []any{
			map[string]any{"name": "my-backend", "endpoint": "https://x.example",
				"auth_header": "Authorization", "api_key": "k1"},
			map[string]any{"name": "other", "endpoint": "https://y.example",
				"auth_header": "api-key", "api_key": "   "},
		},
	}
	env := tmpl.SecretEnv(bag)
	if !reflect.DeepEqual(env, map[string]string{"MY_BACKEND_API_KEY": "k1"}) {
		t.Errorf("SecretEnv = %v, want just MY_BACKEND_API_KEY (blank omitted)", env)
	}
	// Agreement with the render: every required ${env:} ref the render
	// emits is a name SecretEnv would fill when the bag holds a value.
	norm, err := tmpl.NormalizeBag(bag)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tmpl.Render(norm, "/s")
	if err != nil {
		t.Fatal(err)
	}
	filled := tmpl.SecretEnv(map[string]any{
		"backends": []any{
			map[string]any{"name": "my-backend", "api_key": "a"},
			map[string]any{"name": "other", "api_key": "b"},
		},
	})
	for _, v := range vars.Parse(out) {
		if v.HasDefault || strings.HasPrefix(v.Name, "COMPY_") {
			continue
		}
		if _, ok := filled[v.Name]; !ok {
			t.Errorf("rendered ref %s has no SecretEnv counterpart (%v)", v.Name, filled)
		}
	}
}

// TestReconcile is the per-preset schema-edit rule: unknown pruned, newly
// defaulted filled, required-without-default left absent (lenient — the
// strict answer belongs to that preset's next write or activation).
func TestReconcile(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	bag := tmpl.Reconcile(map[string]any{
		"gone": 1,
		"backends": []any{map[string]any{
			"name": "hc", "api_key": "shh", "old": true,
		}},
	})
	if _, has := bag["gone"]; has {
		t.Errorf("unknown survived: %v", bag)
	}
	if bag["memory_limiter"] != true || bag["offline_queue"] != false {
		t.Errorf("defaults not filled: %v", bag)
	}
	row := bag["backends"].([]any)[0].(map[string]any)
	if row["api_key"] != "shh" || row["auth_scheme"] != "none" {
		t.Errorf("row not reconciled: %v", row)
	}
	if _, has := row["endpoint"]; has {
		t.Errorf("reconcile invented a required value: %v", row)
	}
}

// TestMissingRequiredBag: the generalized pre-flight — schema-required
// fields (secrets and non-secrets alike) absent or blank in the bag, named
// as field paths.
func TestMissingRequiredBag(t *testing.T) {
	tmpl := get(t, "custom-endpoints")
	missing := tmpl.MissingRequired(map[string]any{
		"backends": []any{
			map[string]any{"name": "hc", "endpoint": "https://x.example", "api_key": "k"},
			map[string]any{"name": "dd", "endpoint": "  ", "api_key": ""},
		},
	})
	want := []string{"backends[1].endpoint", "backends[1].api_key"}
	if !reflect.DeepEqual(missing, want) {
		t.Errorf("MissingRequired = %v, want %v", missing, want)
	}
	if got := tmpl.MissingRequired(map[string]any{"backends": []any{
		map[string]any{"name": "hc", "endpoint": "https://x.example", "api_key": "k"},
	}}); got != nil {
		t.Errorf("complete bag reported missing: %v", got)
	}
}

// TestVocabularyForUserTemplates: a hand-written template that declares
// only SOME of the recognized knobs still renders — the vocabulary fills
// zero values (and shipped row defaults) for the rest instead of panicking.
func TestVocabularyForUserTemplates(t *testing.T) {
	src := `{"name": "mini", "description": "d",
 "backends": {"min": 1, "max": 2, "fields": [
   {"name": "name", "type": "slug", "label": "N"},
   {"name": "endpoint", "type": "url", "label": "E"}]}}
---
exporters:
{{range .Backends}}  otlphttp/{{.Name}}:
    endpoint: {{.Endpoint}}
{{end}}service:
  pipelines:
{{if .HasTraces}}    traces: {exporters: [{{.TracesExps}}]}
{{end}}`
	tmpl, err := ParseSource(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tmpl.Render(map[string]any{
		"backends": []any{map[string]any{"name": "b", "endpoint": "https://x.example"}},
	}, "/s")
	if err != nil {
		t.Fatal(err)
	}
	// No signals field declared → the row defaults to all signals; no
	// toggles declared → no processors in the lists.
	if !strings.Contains(out, "traces: {exporters: [otlphttp/b]}") {
		t.Errorf("vocabulary defaults missing:\n%s", out)
	}
}
