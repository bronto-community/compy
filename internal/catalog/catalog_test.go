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

// canonicalKnobs is the knob set the golden render and the tier-invariant
// test share: two labelled backends, one with auth and one without, every
// pipeline toggle on.
func canonicalKnobs(t *testing.T) map[string]any {
	t.Helper()
	var knobs map[string]any
	if err := json.Unmarshal([]byte(`{
		"backends": [
			{"_label": "EU prod", "endpoint": "https://api.example.com"},
			{"_label": "vendor two", "endpoint": "https://otlp.example.net",
			 "auth_header": "x-example-key"}
		],
		"offline_queue": true
	}`), &knobs); err != nil {
		t.Fatal(err)
	}
	return knobs
}

// get returns a template by name. "rich" is not shipped: it is the engine's
// fixture — TWO author-defined groups, every field shape, sections, toggles
// — so the engine tests keep their coverage without a shipped template
// having to carry it.
func get(t *testing.T, name string) Template {
	t.Helper()
	if name == "rich" {
		tmpl, err := ParseSource(richSrc)
		if err != nil {
			t.Fatal(err)
		}
		return tmpl
	}
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
	var names []string
	for _, tm := range ts {
		names = append(names, tm.Name)
		if tm.Description == "" {
			t.Errorf("%s has no description", tm.Name)
		}
	}
	if want := []string{"debug", "otlp-forward"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("Templates() = %v, want %v", names, want)
	}
	if _, err := Get("no-such-template"); !state.IsBadRequest(err) {
		t.Errorf("Get(unknown) err = %v, want BadRequest", err)
	}
}

// TestShippedTemplatesSeedable: every shipped template must materialize
// with no user in the loop — Reconcile(nil) (the schema's normalized
// default bag, groups seeded) has to render. A template that fails this
// cannot ship as a default.
func TestShippedTemplatesSeedable(t *testing.T) {
	ts, err := Templates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range ts {
		seed := tmpl.Reconcile(nil, "/s")
		out, err := tmpl.Render(seed, "/s")
		if err != nil {
			t.Errorf("%s: default seed does not render: %v", tmpl.Name, err)
			continue
		}
		if strings.Contains(out, "{{") {
			t.Errorf("%s: unrendered template syntax in default render:\n%s", tmpl.Name, out)
		}
		// A shipped default may not open with a warning: nothing required is
		// allowed to be missing from the seed.
		if m := tmpl.MissingRequired(seed); len(m) > 0 {
			t.Errorf("%s: default seed is missing required values %v", tmpl.Name, m)
		}
	}
}

// TestRenderShippedDebug: the one knob bakes.
func TestRenderShippedDebug(t *testing.T) {
	tmpl := get(t, "debug")
	out, err := tmpl.Render(map[string]any{"verbosity": "detailed"}, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "verbosity: detailed") {
		t.Errorf("verbosity not baked:\n%s", out)
	}
	if !strings.Contains(out, "127.0.0.1:${env:COMPY_GRPC_PORT:-14317}") {
		t.Errorf("receiver block missing:\n%s", out)
	}
}

// TestRenderShippedOTLPForward is the auth rule in one place: the default
// Authorization header sends a bearer token, ANY other header name sends the
// value bare, and an emptied header name sends no auth at all. Exporter ids
// and env var names come from the row LABEL — there is no name field.
func TestRenderShippedOTLPForward(t *testing.T) {
	tmpl := get(t, "otlp-forward")
	bag := map[string]any{"backends": []any{
		map[string]any{"_label": "EU prod", "endpoint": "https://api.example.com"},
		map[string]any{"_label": "vendor two", "endpoint": "https://otlp.example.net", "auth_header": "x-example-key"},
		map[string]any{"_label": "local tap", "endpoint": "http://10.0.0.5:4318", "auth_header": ""},
	}}
	out, err := tmpl.Render(bag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  otlp_http/eu-prod:\n    endpoint: https://api.example.com\n    headers:\n      Authorization: Bearer ${env:EU_PROD_API_KEY:-}  # EU prod auth value\n",
		"  otlp_http/vendor-two:\n    endpoint: https://otlp.example.net\n    headers:\n      x-example-key: ${env:VENDOR_TWO_API_KEY:-}  # vendor two auth value\n",
		"  otlp_http/local-tap:\n    endpoint: http://10.0.0.5:4318\n",
		"  memory_limiter:\n    check_interval: 1s",
		"  batch:\n    send_batch_size: 1024",
		"    traces:\n      receivers: [otlp]\n      processors: [memory_limiter, batch]\n      exporters: [otlp_http/eu-prod, otlp_http/vendor-two, otlp_http/local-tap]\n",
		"    metrics:\n      receivers: [otlp]\n      processors: [memory_limiter, batch]\n      exporters: [otlp_http/eu-prod, otlp_http/vendor-two, otlp_http/local-tap]\n",
		"    logs:\n      receivers: [otlp]\n      processors: [memory_limiter, batch]\n      exporters: [otlp_http/eu-prod, otlp_http/vendor-two, otlp_http/local-tap]\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing:\n%s\nin:\n%s", want, out)
		}
	}
	if strings.Contains(out, "otlp_http/local-tap:\n    endpoint: http://10.0.0.5:4318\n    headers") {
		t.Errorf("emptied auth header still rendered headers:\n%s", out)
	}
	if strings.Contains(out, "x-example-key: Bearer") {
		t.Errorf("Bearer prefix leaked onto a custom header:\n%s", out)
	}
	if strings.Contains(out, "file_storage") {
		t.Errorf("offline queue rendered while off by default:\n%s", out)
	}
	// Toggles off: the pipeline drops back to bare receive→export.
	bag["memory_limiter"] = false
	bag["batch"] = false
	out, err = tmpl.Render(bag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "processors") {
		t.Errorf("processors rendered with every toggle off:\n%s", out)
	}
	if !strings.Contains(out, "    traces:\n      receivers: [otlp]\n      exporters: [otlp_http/eu-prod") {
		t.Errorf("bare pipeline missing with toggles off:\n%s", out)
	}
	// The offline queue brings the extension and the per-exporter queue.
	bag["offline_queue"] = true
	out, err = tmpl.Render(bag, "/home/u/compy/storage")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"    sending_queue:\n      storage: file_storage",
		"extensions:\n  file_storage:\n    directory: \"/home/u/compy/storage\"\n    create_directory: true",
		"  extensions: [file_storage]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("offline queue render missing:\n%s\nin:\n%s", want, out)
		}
	}
}

// TestSchemaOrder locks declaration order = form order: the arrays in the
// front matter come through in file order, groups included.
func TestSchemaOrder(t *testing.T) {
	tmpl := get(t, "rich")
	var ids []string
	for _, g := range tmpl.Groups {
		ids = append(ids, g.ID)
	}
	if want := []string{"backends", "receivers"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("group order = %v, want %v", ids, want)
	}
	var beNames []string
	for _, f := range tmpl.Groups[0].Fields {
		beNames = append(beNames, f.Name)
	}
	wantBE := []string{"endpoint", "auth_header", "api_key", "auth_scheme", "signals"}
	if !reflect.DeepEqual(beNames, wantBE) {
		t.Errorf("backend field order = %v, want %v", beNames, wantBE)
	}
	var names []string
	for _, f := range tmpl.Fields {
		names = append(names, f.Name)
	}
	want := []string{"memory_limiter", "batch", "debug_tee"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("field order = %v, want %v", names, want)
	}
	if tmpl.Groups[0].Min != 1 || tmpl.Groups[0].Max != 8 {
		t.Errorf("row bounds = %d..%d, want 1..8", tmpl.Groups[0].Min, tmpl.Groups[0].Max)
	}
	// label/item derive from the id when the author omits them; an omitted
	// max is the engine's cap.
	if tmpl.Groups[0].Label != "Backends" || tmpl.Groups[0].Item != "backend" {
		t.Errorf("derived group naming = %q/%q", tmpl.Groups[0].Label, tmpl.Groups[0].Item)
	}
	if tmpl.Groups[1].Item != "receiver" || tmpl.Groups[1].Max != maxGroupRows {
		t.Errorf("second group = %+v", tmpl.Groups[1])
	}
	if len(tmpl.Sections) != 1 || !tmpl.Sections[0].Collapsed {
		t.Errorf("sections = %+v, want one collapsed pipeline section", tmpl.Sections)
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
		for _, g := range tmpl.Groups {
			check(g.ID, g.Fields)
		}
	}
}

// TestGroupSchemaErrors: the schema's own rules about groups — usable ids,
// no collisions, bounds inside the engine's cap, and the "_" prefix the
// machinery reserves for the row label.
func TestGroupSchemaErrors(t *testing.T) {
	src := func(groups, fields string) string {
		return "---\nname: t\n" + fields + groups + "---\nbody\n"
	}
	one := "groups:\n  - id: %s\n    fields:\n      - name: x\n        type: string\n        default: v\n"
	for _, tc := range []struct{ name, content, wantIn string }{
		{"bad id", src(strings.Replace(one, "%s", "Backends", 1), ""), "must be lowercase"},
		{"id collides with a field", src(strings.Replace(one, "%s", "a", 1),
			"fields:\n  - name: a\n    type: string\n    default: v\n"), "already a field"},
		{"duplicate group ids", src(strings.Replace(one, "%s", "a", 1)+"  - id: a\n    fields:\n      - name: y\n        type: string\n        default: v\n", ""), "already a field"},
		{"no fields", src("groups:\n  - id: a\n    fields: []\n", ""), "at least one field"},
		{"max above the cap", src("groups:\n  - id: a\n    max: 99\n    fields:\n      - name: x\n        type: string\n        default: v\n", ""), "row bounds"},
		{"min above max", src("groups:\n  - id: a\n    min: 3\n    max: 2\n    fields:\n      - name: x\n        type: string\n        default: v\n", ""), "row bounds"},
		{"reserved row-field name", src("groups:\n  - id: a\n    fields:\n      - name: _label\n        type: string\n        default: v\n", ""), "reserved"},
		{"reserved config-field name", src("", "fields:\n  - name: _x\n    type: string\n    default: v\n"), "reserved"},
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

// TestRowLabels is the row-identity rule: the label is what the user typed,
// its slug is what the yaml uses, an absent label defaults by position, and
// two rows may not slug to the same thing. Renaming a row moves its derived
// env var name — the secret VALUE rides in the row and is never touched.
func TestRowLabels(t *testing.T) {
	tmpl := get(t, "rich")
	norm, err := tmpl.NormalizeBag(map[string]any{"backends": []any{
		map[string]any{"endpoint": "https://a.example"},
		map[string]any{"_label": "  EU prod  ", "endpoint": "https://b.example"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rows := norm["backends"].([]any)
	if got := rows[0].(map[string]any)[LabelKey]; got != "backend 1" {
		t.Errorf("default label = %v, want %q", got, "backend 1")
	}
	if got := rows[1].(map[string]any)[LabelKey]; got != "EU prod" {
		t.Errorf("label not trimmed: %v", got)
	}

	for _, tc := range []struct {
		name, wantIn string
		rows         []any
	}{
		{"colliding labels", "is the same name as", []any{
			map[string]any{"_label": "EU prod", "endpoint": "https://a.example"},
			map[string]any{"_label": "eu-prod", "endpoint": "https://b.example"},
		}},
		{"unsluggable label", "no letters or digits", []any{
			map[string]any{"_label": "!!!", "endpoint": "https://a.example"},
		}},
	} {
		_, err := tmpl.NormalizeBag(map[string]any{"backends": tc.rows})
		if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wantIn)
		}
	}

	// A rename moves the derived name; the value stays with the row.
	row := map[string]any{"_label": "EU prod", "endpoint": "https://a.example", "api_key": "k"}
	bag := map[string]any{"backends": []any{row}}
	if env := tmpl.SecretEnv(bag); env["EU_PROD_API_KEY"] != "k" {
		t.Errorf("SecretEnv before rename = %v", env)
	}
	row[LabelKey] = "US prod"
	env := tmpl.SecretEnv(bag)
	if env["US_PROD_API_KEY"] != "k" || len(env) != 1 {
		t.Errorf("SecretEnv after rename = %v, want just US_PROD_API_KEY", env)
	}
	out, err := tmpl.Render(bag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "otlp_http/us-prod:") {
		t.Errorf("rename did not move the exporter id:\n%s", out)
	}
}

// TestNormalizeBag is the validation matrix: every rejection is
// BadRequest-marked and names the offending field.
func TestNormalizeBag(t *testing.T) {
	tmpl := get(t, "rich")
	be := func(rows ...map[string]any) map[string]any {
		var l []any
		for _, r := range rows {
			l = append(l, r)
		}
		return map[string]any{"backends": l}
	}
	ok := map[string]any{"endpoint": "https://x.example"}

	bad := []struct {
		name, wantIn string
		knobs        map[string]any
	}{
		{"no backends", "backends: need 1 to 8", map[string]any{}},
		{"too many backends", "backends: need 1 to 8", be(ok, ok, ok, ok, ok, ok, ok, ok, ok)},
		{"backends not a list", "backends: not a list", map[string]any{"backends": "x"}},
		{"row not an object", "backends[0]: not an object", map[string]any{"backends": []any{"x"}}},
		{"missing endpoint", "backends[0].endpoint: required", be(map[string]any{})},
		{"bad url", "backends[0].endpoint", be(map[string]any{"endpoint": "not a url"})},
		{"bad choice", "backends[0].auth_scheme", be(map[string]any{"endpoint": "https://x.example", "auth_scheme": "Digest"})},
		{"bad multi member", "backends[0].signals", be(map[string]any{"endpoint": "https://x.example", "signals": []any{"traces", "profiles"}})},
		{"empty multi", "backends[0].signals", be(map[string]any{"endpoint": "https://x.example", "signals": []any{}})},
		{"unknown field", "backends[0].port: unknown field", be(map[string]any{"endpoint": "https://x.example", "port": 1})},
		{"secret non-string", "backends[0].api_key", be(map[string]any{"endpoint": "https://x.example", "api_key": 5})},
		{"too many rows in the second group", "receivers: need 0 to 16", func() map[string]any {
			m := be(ok)
			rows := make([]any, maxGroupRows+1)
			for i := range rows {
				rows[i] = map[string]any{"port": "1"}
			}
			m["receivers"] = rows
			return m
		}()},
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
	// (never defaulted, never demanded — the pre-flight's business). A group
	// with min 0 normalizes to no rows rather than failing.
	norm, err := tmpl.NormalizeBag(be(ok))
	if err != nil {
		t.Fatal(err)
	}
	row := norm["backends"].([]any)[0].(map[string]any)
	if row["auth_scheme"] != "none" {
		t.Errorf("row defaults not filled: %v", row)
	}
	if !reflect.DeepEqual(row["signals"], []string{"traces", "metrics", "logs"}) {
		t.Errorf("signals default = %v", row["signals"])
	}
	if rows, ok := norm["receivers"].([]any); !ok || len(rows) != 0 {
		t.Errorf("empty group = %v, want an empty list", norm["receivers"])
	}
	if norm["memory_limiter"] != true || norm["debug_tee"] != false {
		t.Errorf("toggle defaults not filled: %v", norm)
	}
	if _, present := row["api_key"]; present {
		t.Error("absent secret invented by normalization")
	}

	// A set secret rides along in the bag (Amendment 4: presets own
	// everything; secrets are ordinary bag members).
	norm, err = tmpl.NormalizeBag(be(map[string]any{
		"endpoint": "https://x.example", "api_key": "shh",
	}))
	if err != nil {
		t.Fatal(err)
	}
	row = norm["backends"].([]any)[0].(map[string]any)
	if row["api_key"] != "shh" {
		t.Errorf("secret dropped from normalized bag: %v", row)
	}

	// A free var — an unknown top-level STRING — rides through untouched
	// (tier 3 contains tier 2; the "unknown config field" case above locks
	// that non-strings still 400).
	withFree := be(ok)
	withFree["ASDF"] = "v"
	norm, err = tmpl.NormalizeBag(withFree)
	if err != nil {
		t.Fatal(err)
	}
	if norm["ASDF"] != "v" {
		t.Errorf("free var dropped by normalization: %v", norm)
	}
}

// TestRenderGolden locks the exact rendered YAML — comments included — for
// the shipped otlp-forward and the canonical knob set. If this changes
// deliberately, run with UPDATE_GOLDEN=1.
func TestRenderGolden(t *testing.T) {
	tmpl := get(t, "otlp-forward")
	got, err := tmpl.Render(canonicalKnobs(t), "/home/u/compy/storage")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile("testdata/otlp-forward-golden.yaml", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/otlp-forward-golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("render drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestTierInvariant: the rendered config, fed back through the tier-2 vars
// parser, yields the secret refs under their derived names — tier 3
// contains tier 2 by construction.
func TestTierInvariant(t *testing.T) {
	tmpl := get(t, "rich")
	out, err := tmpl.Render(map[string]any{"backends": []any{
		map[string]any{"_label": "EU prod", "endpoint": "https://api.example.com"},
		map[string]any{"_label": "vendor two", "endpoint": "https://otlp.example.net",
			"auth_header": "x-example-key"},
	}}, "/home/u/compy/storage")
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
	// Only the second row declares an auth header, so only its key is
	// referenced — per-preset structure decides what exists.
	if want := []string{"VENDOR_TWO_API_KEY"}; !reflect.DeepEqual(required, want) {
		t.Fatalf("required vars = %v, want %v", required, want)
	}
	if d := byName["VENDOR_TWO_API_KEY"].Description; d != "vendor two api key" {
		t.Errorf("VENDOR_TWO_API_KEY description = %q", d)
	}
	// The env-with-default port refs survive as the shipped configs' idiom.
	if !byName["COMPY_GRPC_PORT"].HasDefault {
		t.Error("COMPY port ref lost its default")
	}
}

// TestSecretEnvName: dashes and spaces cannot appear in env var names.
func TestSecretEnvName(t *testing.T) {
	if got := secretEnvName("my-backend", "api_key"); got != "MY_BACKEND_API_KEY" {
		t.Errorf("secretEnvName = %q", got)
	}
	g := Group{ID: "backends", Item: "backend"}
	if got := g.rowSlug(map[string]any{LabelKey: "EU Prod (main)"}, 0); got != "eu-prod-main" {
		t.Errorf("rowSlug = %q", got)
	}
	if got := g.rowSlug(map[string]any{}, 2); got != "backend-3" {
		t.Errorf("positional rowSlug = %q", got)
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
// defaults, options, group bounds — and renders the identical output.
func TestYAMLFrontMatterTwin(t *testing.T) {
	body := "a: {{.g}}\n{{range .backends}}b: {{._slug}}\n{{end}}"
	jsonSrc := `{"name": "twin", "description": "d",
 "sections": [{"id": "s", "label": "S", "collapsed": true}],
 "fields": [
   {"name": "g", "type": "string", "label": "G", "default": "x", "section": "s"},
   {"name": "on", "type": "toggle", "label": "O", "default": true},
   {"name": "k", "type": "secret", "label": "K", "description": "a key"}],
 "groups": [{"id": "backends", "label": "B", "item": "backend", "min": 1, "max": 3, "fields": [
   {"name": "signals", "type": "multi", "label": "Sig", "options": ["a", "b"], "default": ["a"], "advanced": true}]}]}
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
groups:
  - id: backends
    label: B
    item: backend
    min: 1
    max: 3
    fields:
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
	bag := map[string]any{"backends": []any{map[string]any{"_label": "one", "signals": []string{"b"}}}}
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
		{"the retired backends key is gone", "---\nname: t\nbackends:\n  min: 1\n---\nbody\n", "backends"},
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

	// label: is optional — a missing one derives from the field name, and a
	// group's label/item derive from its id.
	lt, err := ParseSource(`{"name": "t",
 "fields": [{"name": "auth_header", "type": "string", "default": "x"}],
 "groups": [{"id": "ottl_policies", "min": 1, "max": 2, "fields": [{"name": "statement", "type": "string", "default": ""}]}]}
---
b
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := lt.Fields[0].Label; got != "Auth header" {
		t.Errorf("derived label = %q, want %q", got, "Auth header")
	}
	if got := lt.Groups[0].Fields[0].Label; got != "Statement" {
		t.Errorf("derived group field label = %q, want %q", got, "Statement")
	}
	if lt.Groups[0].Label != "Ottl policies" || lt.Groups[0].Item != "ottl policie" {
		t.Errorf("derived group naming = %q/%q", lt.Groups[0].Label, lt.Groups[0].Item)
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

// TestPruneUnknown: fields the schema no longer declares vanish, group rows
// included — how stored bags survive a schema edit — while declared values,
// secrets among them (they are declared fields, and pruning a secret would
// delete a key) and the row LABEL, pass through untouched. Unknown top-level
// STRINGS are free vars and survive this pass (only Reconcile, render in
// hand, prunes those); unknown non-strings still vanish.
func TestPruneUnknown(t *testing.T) {
	tmpl := get(t, "rich")
	knobs := map[string]any{
		"debug_tee": true,
		"gone":      7,
		"ASDF":      "free-value",
		"backends": []any{map[string]any{
			"_label": "EU prod", "endpoint": "https://x.example",
			"api_key": "shh", "old_field": 1,
		}},
	}
	out := tmpl.PruneUnknown(knobs)
	if _, has := out["gone"]; has {
		t.Errorf("unknown non-string field survived: %v", out)
	}
	if out["ASDF"] != "free-value" {
		t.Errorf("free var (unknown string) did not survive the prune: %v", out)
	}
	if out["debug_tee"] != true {
		t.Errorf("declared field lost: %v", out)
	}
	row := out["backends"].([]any)[0].(map[string]any)
	if row["api_key"] != "shh" {
		t.Errorf("secret did not survive the prune: %v", row)
	}
	if row[LabelKey] != "EU prod" {
		t.Errorf("row label pruned: %v", row)
	}
	if _, has := row["old_field"]; has {
		t.Errorf("unknown row field survived: %v", row)
	}
	if row["endpoint"] != "https://x.example" {
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
	tmpl := get(t, "rich")
	bag := map[string]any{
		"backends": []any{map[string]any{
			"_label": "hc", "endpoint": "https://x.example",
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

	// Free vars share the rule: a bag value for a hand-written ${env:} ref
	// never bakes — the ref stays in the render and the collector expands
	// it from the environment (that is the point of a free var).
	free, err := ParseSource(freeSrc)
	if err != nil {
		t.Fatal(err)
	}
	out, err = free.Render(map[string]any{"ASDF": "10.0.0.5"}, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "10.0.0.5") {
		t.Fatalf("free-var value baked into the render:\n%s", out)
	}
	if !strings.Contains(out, "${env:ASDF:-fallback}") {
		t.Errorf("free-var env reference missing:\n%s", out)
	}
}

// TestSecretEnv: the env split's mapping — a row secret becomes
// UPPER(row_slug)_UPPER(field), agreeing exactly with the rendered ${env:}
// references; blanks are omitted.
func TestSecretEnv(t *testing.T) {
	tmpl := get(t, "rich")
	bag := map[string]any{
		"backends": []any{
			map[string]any{"_label": "my backend", "endpoint": "https://x.example",
				"auth_header": "Authorization", "api_key": "k1"},
			map[string]any{"_label": "other", "endpoint": "https://y.example",
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
			map[string]any{"_label": "my backend", "api_key": "a"},
			map[string]any{"_label": "other", "api_key": "b"},
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
// defaulted filled, row labels defaulted by position, required-without-
// default left absent (lenient — the strict answer belongs to that preset's
// next write or activation). An unrenderable bag (missing endpoint here)
// keeps its free-var values — pruning free vars needs a render to judge
// against.
func TestReconcile(t *testing.T) {
	tmpl := get(t, "rich")
	bag := tmpl.Reconcile(map[string]any{
		"gone": 1,
		"ASDF": "kept",
		"backends": []any{map[string]any{
			"api_key": "shh", "old": true,
		}},
	}, "/s")
	if _, has := bag["gone"]; has {
		t.Errorf("unknown survived: %v", bag)
	}
	if bag["ASDF"] != "kept" {
		t.Errorf("unrenderable bag lost its free var: %v", bag)
	}
	if bag["memory_limiter"] != true || bag["debug_tee"] != false {
		t.Errorf("defaults not filled: %v", bag)
	}
	row := bag["backends"].([]any)[0].(map[string]any)
	if row["api_key"] != "shh" || row["auth_scheme"] != "none" {
		t.Errorf("row not reconciled: %v", row)
	}
	if row[LabelKey] != "backend 1" {
		t.Errorf("row label not defaulted: %v", row)
	}
	if _, has := row["endpoint"]; has {
		t.Errorf("reconcile invented a required value: %v", row)
	}
	// A bag with no entry for a group at all seeds Min rows.
	seeded := tmpl.Reconcile(nil, "/s")
	if rows, _ := seeded["backends"].([]any); len(rows) != 1 {
		t.Errorf("min rows not seeded: %v", seeded["backends"])
	}
	if rows, _ := seeded["receivers"].([]any); len(rows) != 0 {
		t.Errorf("min-0 group seeded rows: %v", seeded["receivers"])
	}
}

// freeSrc is a mini tier-3 source whose body hand-writes ${env:} refs
// beyond the schema: one with a default, one gated by a toggle, one
// colliding with a schema field name, plus the derived secret ref and a
// COMPY_* ref. The free-vars test bed.
const freeSrc = `{"name": "free", "fields": [
  {"name": "greeting", "type": "string", "label": "G", "default": "hello"},
  {"name": "token", "type": "secret", "label": "T", "description": "the key"},
  {"name": "tee", "type": "toggle", "label": "D", "default": false}
]}
---
a: {{.greeting}}
key: ${env:{{._env.token}}}  # the key
host: ${env:ASDF:-fallback}  # target host
collide: ${env:greeting}
port: ${env:COMPY_HTTP_PORT:-14318}
{{if .tee}}extra: ${env:ONLY_TEE}{{end}}
`

// TestFreeVars: discovery = the render's ${env:} refs minus secret-derived
// names, minus COMPY_*, minus schema-field collisions (schema wins) — with
// tier 2's comment descriptions and defaults riding along, per preset
// (a ref an {{if}} kept out of THIS render doesn't exist for it).
func TestFreeVars(t *testing.T) {
	tmpl, err := ParseSource(freeSrc)
	if err != nil {
		t.Fatal(err)
	}
	bag := map[string]any{}
	rendered, err := tmpl.Render(bag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "key: ${env:TOKEN}") {
		t.Errorf("top-level _env did not name the secret:\n%s", rendered)
	}
	free := tmpl.FreeVars(rendered, bag)
	if len(free) != 1 || free[0].Name != "ASDF" {
		t.Fatalf("FreeVars = %+v, want exactly ASDF", free)
	}
	if !free[0].HasDefault || free[0].Default != "fallback" || free[0].Description != "target host" {
		t.Errorf("tier-2 machinery lost: %+v", free[0])
	}

	// The toggle flips the render, and discovery follows it: per-preset
	// structure is real.
	teeBag := map[string]any{"tee": true}
	rendered, err = tmpl.Render(teeBag, "/s")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range tmpl.FreeVars(rendered, teeBag) {
		names = append(names, v.Name)
	}
	if !reflect.DeepEqual(names, []string{"ASDF", "ONLY_TEE"}) {
		t.Errorf("FreeVars with tee = %v, want [ASDF ONLY_TEE]", names)
	}
}

// TestEnvFor: the tier-3 activation env = secret values under derived
// names + free-var values verbatim; schema non-secrets and blanks never
// travel (they are baked / not values).
func TestEnvFor(t *testing.T) {
	tmpl, err := ParseSource(freeSrc)
	if err != nil {
		t.Fatal(err)
	}
	env := tmpl.EnvFor(map[string]any{
		"greeting": "hi", "tee": true, "token": "shh",
		"ASDF": "10.0.0.5", "BLANK": "   ",
	})
	want := map[string]string{"TOKEN": "shh", "ASDF": "10.0.0.5"}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("EnvFor = %v, want %v", env, want)
	}
}

// TestReconcilePrunesStaleFreeVars: a free var the bag's own render no
// longer references gets the removed-field treatment at reconcile — while
// a referenced one survives. Free vars are not secrets; pruning is safe.
func TestReconcilePrunesStaleFreeVars(t *testing.T) {
	tmpl, err := ParseSource(freeSrc)
	if err != nil {
		t.Fatal(err)
	}
	bag := tmpl.Reconcile(map[string]any{
		"ASDF": "kept", "STALE": "dropped", "token": "shh",
	}, "/s")
	if bag["ASDF"] != "kept" {
		t.Errorf("referenced free var pruned: %v", bag)
	}
	if _, has := bag["STALE"]; has {
		t.Errorf("stale free var survived reconcile: %v", bag)
	}
	if bag["token"] != "shh" {
		t.Errorf("secret did not survive reconcile: %v", bag)
	}
	// ONLY_TEE is gated off (tee defaults false): a value for it is stale
	// FOR THIS PRESET and goes too — discovery is per-preset.
	bag = tmpl.Reconcile(map[string]any{"ONLY_TEE": "x"}, "/s")
	if _, has := bag["ONLY_TEE"]; has {
		t.Errorf("free var outside this preset's render survived: %v", bag)
	}
	bag = tmpl.Reconcile(map[string]any{"ONLY_TEE": "x", "tee": true}, "/s")
	if bag["ONLY_TEE"] != "x" {
		t.Errorf("free var inside this preset's render pruned: %v", bag)
	}
}

// TestMissingRequiredBag: the generalized pre-flight — schema-required
// fields (secrets and non-secrets alike) absent or blank in the bag, named
// as field paths under their OWN group's id.
func TestMissingRequiredBag(t *testing.T) {
	tmpl := get(t, "rich")
	missing := tmpl.MissingRequired(map[string]any{
		"backends": []any{
			map[string]any{"_label": "hc", "endpoint": "https://x.example", "api_key": "k"},
			map[string]any{"_label": "dd", "endpoint": "  ", "api_key": ""},
		},
		"receivers": []any{map[string]any{"port": ""}},
	})
	want := []string{"backends[1].endpoint", "backends[1].api_key", "receivers[0].port"}
	if !reflect.DeepEqual(missing, want) {
		t.Errorf("MissingRequired = %v, want %v", missing, want)
	}
	if got := tmpl.MissingRequired(map[string]any{"backends": []any{
		map[string]any{"_label": "hc", "endpoint": "https://x.example", "api_key": "k"},
	}}); got != nil {
		t.Errorf("complete bag reported missing: %v", got)
	}
}

// TestUserDefinedGroups is Amendment 8's point: nothing about "backends" is
// built in. A hand-written template naming its groups whatever it likes
// renders both, each row identified by its own label, with no Go behind it —
// the list funcs do the assembling.
func TestUserDefinedGroups(t *testing.T) {
	src := `---
name: mine
description: d
groups:
  - id: taps
    item: tap
    min: 1
    max: 3
    fields:
      - name: port
        type: string
        default: "4318"
  - id: ottl_statements
    label: OTTL statements
    item: statement
    fields:
      - name: statement
        type: string
        default: ""
---
receivers:
{{- range .taps}}
  otlp/{{._slug}}:
    endpoint: 127.0.0.1:{{.port}}
{{- end}}
{{- if .ottl_statements}}
processors:
  transform:
    log_statements:
{{- range .ottl_statements}}
      - {{.statement}}  # {{._label}}
{{- end}}
{{- end}}
`
	tmpl, err := ParseSource(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tmpl.Render(map[string]any{
		"taps": []any{
			map[string]any{"_label": "grpc in", "port": "4317"},
			map[string]any{},
		},
		"ottl_statements": []any{
			map[string]any{"_label": "drop health", "statement": `delete_key(attributes, "health")`},
		},
	}, "/s")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  otlp/grpc-in:\n    endpoint: 127.0.0.1:4317\n",
		"  otlp/tap-2:\n    endpoint: 127.0.0.1:4318\n",
		"      - delete_key(attributes, \"health\")  # drop health\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing:\n%s\nin:\n%s", want, out)
		}
	}
	// An empty group renders nothing at all.
	out, err = tmpl.Render(map[string]any{"taps": []any{map[string]any{}}}, "/s")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "processors") {
		t.Errorf("empty group rendered its block:\n%s", out)
	}
}

// richSrc is the engine's fixture: two author-defined groups (one required,
// one optional), every field shape (url, string, secret, choice, multi), a
// collapsed section, and a body that assembles its lists with list/append/
// join. Not shipped — the shipped catalog stays small on purpose.
const richSrc = `---
name: rich
description: Two groups, every field shape.
sections:
  - id: pipeline
    label: Pipeline options
    collapsed: true
groups:
  - id: backends
    min: 1
    max: 8
    fields:
      - name: endpoint
        type: url
        description: Base OTLP/HTTP URL
      - name: auth_header
        type: string
        optional: true
        description: Header that carries the API key; leave empty for no auth
      - name: api_key
        type: secret
        label: API key
        description: Stored in the preset
      - name: auth_scheme
        type: choice
        options: [none, Bearer, Basic]
        default: none
        advanced: true
      - name: signals
        type: multi
        options: [traces, metrics, logs]
        default: [traces, metrics, logs]
        advanced: true
  - id: receivers
    item: receiver
    fields:
      - name: port
        type: string
fields:
  - name: memory_limiter
    type: toggle
    default: true
    section: pipeline
  - name: batch
    type: toggle
    default: true
    section: pipeline
  - name: debug_tee
    type: toggle
    default: false
    section: pipeline
---
{{- $procs := list -}}
{{- if .memory_limiter}}{{$procs = append $procs "memory_limiter"}}{{end -}}
{{- if .batch}}{{$procs = append $procs "batch"}}{{end -}}
{{- $exp := list -}}
{{- range .backends}}{{$exp = append $exp (printf "otlp_http/%s" ._slug)}}{{end -}}
{{- if .debug_tee}}{{$exp = append $exp "debug"}}{{end -}}
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 127.0.0.1:${env:COMPY_GRPC_PORT:-14317}  # compy's local gRPC port
{{- range .receivers}}
  prometheus/{{._slug}}:
    endpoint: 127.0.0.1:{{.port}}
{{- end}}
exporters:
{{- range .backends}}
  otlp_http/{{._slug}}:
    endpoint: {{.endpoint}}
{{- if .auth_header}}
    headers:
      {{.auth_header}}: {{if ne .auth_scheme "none"}}{{.auth_scheme}} {{end}}${env:{{._env.api_key}}}  # {{._label}} api key
{{- end}}
{{- end}}
service:
  pipelines:
    traces:
      receivers: [otlp]
{{- if $procs}}
      processors: [{{join $procs ", "}}]
{{- end}}
      exporters: [{{join $exp ", "}}]
`
