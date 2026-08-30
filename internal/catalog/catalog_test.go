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

// TestNormalizeKnobs is the validation matrix: every rejection is
// BadRequest-marked and names the offending field.
func TestNormalizeKnobs(t *testing.T) {
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
		{"secret as knob", "backends[0].api_key: secrets are not template knobs", be(map[string]any{"name": "a", "endpoint": "https://x.example", "api_key": "shh"})},
		{"duplicate names", "duplicate name", be(ok, map[string]any{"name": "a", "endpoint": "https://y.example"})},
		{"toggle non-bool", "debug_tee", func() map[string]any { m := be(ok); m["debug_tee"] = "yes"; return m }()},
		{"unknown config field", "sampling: unknown field", func() map[string]any { m := be(ok); m["sampling"] = true; return m }()},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tmpl.NormalizeKnobs(tc.knobs)
			if err == nil {
				t.Fatal("NormalizeKnobs = nil error, want rejection")
			}
			if !state.IsBadRequest(err) {
				t.Errorf("err %v not BadRequest-marked", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("err %q does not name the field (want %q)", err, tc.wantIn)
			}
		})
	}

	// The happy path fills every default.
	norm, err := tmpl.NormalizeKnobs(be(ok))
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
		t.Error("secret leaked into normalized knobs")
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
