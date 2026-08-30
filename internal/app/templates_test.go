package app_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/catalog"
	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/state"
)

// catalogKnobs is a minimal valid knob set for custom-endpoints.
func catalogKnobs(name string) map[string]any {
	return map[string]any{
		"backends": []any{map[string]any{
			"name":        name,
			"endpoint":    "https://" + name + ".example",
			"auth_header": "Authorization",
			"auth_scheme": "Bearer",
		}},
	}
}

// srcWith is a tiny hand-written tier-3 source: a defaulted greeting knob
// plus whatever extra field JSON is spliced in (empty = none).
func srcWith(extra, body string) string {
	fields := `{"name": "greeting", "type": "string", "label": "G", "default": "hello"}`
	if extra != "" {
		fields += ", " + extra
	}
	return `{"name": "t", "description": "d", "fields": [` + fields + `]}
---
` + body + "\n"
}

// TestCreateFromCatalog: creating from a catalog entry COPIES its source
// into the new config — tier 3, provenance local, nothing special about it
// afterward — rendered with the given knobs, secrets surviving as env refs.
func TestCreateFromCatalog(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateFromCatalog("mine", "custom-endpoints", catalogKnobs("hc")); err != nil {
		t.Fatal(err)
	}

	info, yaml, err := a.Config("mine")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || info.Provenance != "local" || info.Modified {
		t.Errorf("info = %+v, want has_template local unmodified", info)
	}
	src, ok, err := a.ConfigSource("mine")
	if err != nil || !ok {
		t.Fatalf("ConfigSource = %v %v", ok, err)
	}
	entry, err := catalog.Get("custom-endpoints")
	if err != nil {
		t.Fatal(err)
	}
	if src != entry.Source() {
		t.Error("created config's source is not a copy of the catalog entry's")
	}
	if !strings.Contains(yaml, "Authorization: Bearer ${env:HC_API_KEY}  # hc api key") {
		t.Errorf("secret reference missing from rendered yaml:\n%s", yaml)
	}
	if strings.Contains(yaml, "{{") {
		t.Errorf("unrendered template syntax leaked:\n%s", yaml)
	}
	// Normalized knobs recorded, secrets excluded, defaults filled.
	row := info.Meta.Knobs["backends"].([]any)[0].(map[string]any)
	if _, has := row["api_key"]; has {
		t.Error("secret recorded in knobs")
	}
	if row["temporality"] != "as-is" {
		t.Errorf("knob defaults not normalized into meta: %v", row)
	}
	if _, ok := info.Meta.Presets[cfgstore.DefaultPreset]; !ok {
		t.Errorf("no default preset: %v", info.Meta.Presets)
	}

	// Caller mistakes are BadRequest: unknown template, taken name, bad
	// knobs (strict on create — a typo'd field deserves an answer).
	if err := a.CreateFromCatalog("x", "nope", catalogKnobs("a")); !state.IsBadRequest(err) {
		t.Errorf("unknown template err = %v, want BadRequest", err)
	}
	if err := a.CreateFromCatalog("mine", "custom-endpoints", catalogKnobs("a")); !state.IsBadRequest(err) {
		t.Errorf("name collision err = %v, want BadRequest", err)
	}
	err = a.CreateFromCatalog("y", "custom-endpoints", map[string]any{"sampling": true})
	if !state.IsBadRequest(err) || !strings.Contains(err.Error(), "sampling") {
		t.Errorf("bad knobs err = %v, want BadRequest naming the field", err)
	}
}

// TestWriteConfigSourcePipeline is the save-pipeline matrix over a
// hand-written (pasted) tier-3 config: a schema edit re-derives the knobs
// (removed pruned, new defaulted), a knob-only save re-renders over the
// stored source, and the entry-path judgments hold (plain yaml refused on
// the source route, front matter refused... routed on the yaml route).
func TestWriteConfigSourcePipeline(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	// Born by pasting source into the plain create path (tier detection).
	if err := a.CreateConfig("tpl", srcWith("", "a: {{.greeting}}")); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := a.Config("tpl"); !info.HasTemplate {
		t.Fatal("pasted source not detected as tier 3")
	}

	// Knob-only save: empty source, explicit knobs.
	if _, err := a.WriteConfigSource("tpl", "", map[string]any{"greeting": "hey"}, true); err != nil {
		t.Fatal(err)
	}
	if _, yaml, _ := a.Config("tpl"); yaml != "a: hey\n" {
		t.Errorf("knob save render = %q", yaml)
	}

	// Source edit adds a field: stored knobs carry over, the new field
	// takes its default.
	src2 := srcWith(`{"name": "loud", "type": "toggle", "label": "L", "default": false}`,
		"a: {{.greeting}}{{if .loud}}!{{end}}")
	if _, err := a.WriteConfigSource("tpl", src2, nil, true); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := a.Config("tpl")
	if err != nil {
		t.Fatal(err)
	}
	if yaml != "a: hey\n" {
		t.Errorf("schema-edit render = %q, want the stored greeting kept", yaml)
	}
	if info.Meta.Knobs["loud"] != false {
		t.Errorf("new field not defaulted into knobs: %v", info.Meta.Knobs)
	}

	// Both dirty in one call: source first, then knobs.
	if _, err := a.WriteConfigSource("tpl", src2, map[string]any{"greeting": "hey", "loud": true}, true); err != nil {
		t.Fatal(err)
	}
	if _, yaml, _ := a.Config("tpl"); yaml != "a: hey!\n" {
		t.Errorf("both-dirty render = %q", yaml)
	}

	// Source edit removes the field: the stored knob is pruned.
	src3 := srcWith("", "a: {{.greeting}}")
	if _, err := a.WriteConfigSource("tpl", src3, nil, true); err != nil {
		t.Fatal(err)
	}
	info, _, err = a.Config("tpl")
	if err != nil {
		t.Fatal(err)
	}
	if _, has := info.Meta.Knobs["loud"]; has {
		t.Errorf("removed field survived in knobs: %v", info.Meta.Knobs)
	}

	// Plain yaml has no business on the source route.
	if _, err := a.WriteConfigSource("tpl", "plain: true\n", nil, true); !state.IsBadRequest(err) {
		t.Errorf("plain yaml on the source route = %v, want BadRequest", err)
	}
	// A knob save needs a templated config.
	if err := a.CreateConfig("plain", "a: 1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.WriteConfigSource("plain", "", map[string]any{"x": true}, true); !state.IsBadRequest(err) {
		t.Errorf("knob save on plain config = %v, want BadRequest", err)
	}
}

// TestWriteConfigSourceValidateOrRestore: a rendered config the collector
// rejects restores BOTH files and the knobs — nothing was saved — and the
// same promise holds when the failed save was a source pasted over a plain
// config (it comes back plain).
func TestWriteConfigSourceValidateOrRestore(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	goodSrc := srcWith("", "a: {{.greeting}}")
	if err := a.CreateConfig("tpl", goodSrc); err != nil {
		t.Fatal(err)
	}

	fakeDistro(t, "exit 1") // the collector now rejects everything
	src2 := srcWith("", "b: {{.greeting}}")
	_, err = a.WriteConfigSource("tpl", src2, map[string]any{"greeting": "boom"}, true)
	if !state.IsBadRequest(err) {
		t.Fatalf("rejected save = %v, want BadRequest", err)
	}
	info, yaml, err := a.Config("tpl")
	if err != nil {
		t.Fatal(err)
	}
	src, _, _ := a.ConfigSource("tpl")
	if src != goodSrc {
		t.Errorf("source not restored: %q", src)
	}
	if yaml != "a: hello\n" {
		t.Errorf("rendered yaml not restored: %q", yaml)
	}
	if info.Meta.Knobs["greeting"] != "hello" {
		t.Errorf("knobs not restored: %v", info.Meta.Knobs)
	}

	// The skip-validation escape stores anyway (the yaml route's twin).
	stale, err := a.WriteConfigSource("tpl", src2, map[string]any{"greeting": "boom"}, false)
	if err != nil || stale {
		t.Fatalf("skip-validate save = %v stale=%v", err, stale)
	}
	if _, yaml, _ := a.Config("tpl"); yaml != "b: boom\n" {
		t.Errorf("skip-validate render = %q", yaml)
	}

	// Pasting a bad source over a PLAIN config restores the plain config.
	if err := a.CreateConfig("plain", "keep: me\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteConfigYAML("plain", goodSrc); !state.IsBadRequest(err) {
		t.Fatalf("pasted source under a rejecting collector = %v, want BadRequest", err)
	}
	info, yaml, err = a.Config("plain")
	if err != nil {
		t.Fatal(err)
	}
	if info.HasTemplate || yaml != "keep: me\n" || info.Meta.Knobs != nil {
		t.Errorf("plain config not restored: %+v yaml=%q", info, yaml)
	}
}

// TestPastedSourceRoundTrip: front-matter text through the YAML write is a
// source write (tier 3 born), and plain yaml written back demotes it.
func TestPastedSourceRoundTrip(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("c", "plain: true\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteConfigYAML("c", srcWith("", "a: {{.greeting}}")); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := a.Config("c")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || yaml != "a: hello\n" {
		t.Errorf("pasted source: %+v yaml=%q", info, yaml)
	}
	if err := a.WriteConfigYAML("c", "plain: again\n"); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := a.Config("c"); info.HasTemplate {
		t.Error("plain yaml write did not demote the templated config")
	}
}

// TestWriteConfigSourceReactivatesWhenActive: saving the active running
// config's source re-applies it (the shared reactivateIf rule); an inactive
// one never touches launchd.
func TestWriteConfigSourceReactivatesWhenActive(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateConfig("tpl", srcWith("", "a: {{.greeting}}")); err != nil {
		t.Fatal(err)
	}

	*calls = nil
	if _, err := a.WriteConfigSource("tpl", "", map[string]any{"greeting": "hey"}, true); err != nil {
		t.Fatal(err)
	}
	if called(*calls, "bootstrap") {
		t.Errorf("saving an inactive config touched launchd: %v", *calls)
	}

	if err := a.Activate("tpl", ""); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if _, err := a.WriteConfigSource("tpl", "", map[string]any{"greeting": "ho"}, true); err != nil {
		t.Fatal(err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("saving the active running config did not re-apply: %v", *calls)
	}
}

// TestKnobsSurviveJSONRoundTrip: knobs read back from meta.json (JSON
// shapes: []any, not []string) still normalize and render on a nil-knob
// save from a fresh App.
func TestKnobsSurviveJSONRoundTrip(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateFromCatalog("mine", "custom-endpoints", catalogKnobs("hc")); err != nil {
		t.Fatal(err)
	}
	_, before, _ := a.Config("mine")
	// A second App re-reads meta.json from disk — the round trip.
	b := &app.App{Dir: a.Dir}
	if _, err := b.WriteConfigSource("mine", "", nil, true); err != nil {
		t.Fatalf("save from persisted knobs: %v", err)
	}
	if _, after, _ := b.Config("mine"); after != before {
		t.Error("nil-knobs save changed the yaml")
	}
}

// TestTemplatesList: the catalog reaches the app surface.
func TestTemplatesList(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	ts, err := a.Templates()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tm := range ts {
		names = append(names, tm.Name)
	}
	if !reflect.DeepEqual(names, []string{"custom-endpoints"}) {
		t.Errorf("templates = %v", names)
	}
}
