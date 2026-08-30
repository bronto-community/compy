package app_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/state"
)

// templateKnobs is a minimal valid knob set for custom-endpoints.
func templateKnobs(name string) map[string]any {
	return map[string]any{
		"backends": []any{map[string]any{
			"name":        name,
			"endpoint":    "https://" + name + ".example",
			"auth_header": "Authorization",
			"auth_scheme": "Bearer",
		}},
	}
}

func createFromTemplate(t *testing.T, a *app.App, name string) {
	t.Helper()
	if err := a.CreateFromTemplate(name, "custom-endpoints", templateKnobs("hc")); err != nil {
		t.Fatal(err)
	}
}

// TestCreateFromTemplate: the created config is plain YAML with the secret
// env references, meta records template + normalized knobs, provenance is
// "template", and the default preset exists — the tier invariant: apart
// from meta, indistinguishable from a pasted config.
func TestCreateFromTemplate(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	createFromTemplate(t, a, "mine")

	info, yaml, err := a.Config("mine")
	if err != nil {
		t.Fatal(err)
	}
	if info.Provenance != "template" || info.Modified {
		t.Errorf("provenance %q modified %v, want template/false", info.Provenance, info.Modified)
	}
	if info.Meta.Template != "custom-endpoints" {
		t.Errorf("meta.template = %q", info.Meta.Template)
	}
	if !strings.Contains(yaml, "Authorization: Bearer ${env:HC_API_KEY}  # hc api key") {
		t.Errorf("secret reference missing from yaml:\n%s", yaml)
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
	// Byte-indistinguishable from the same YAML pasted (Create).
	if err := a.CreateConfig("pasted", yaml); err != nil {
		t.Fatal(err)
	}
	_, pastedYAML, err := a.Config("pasted")
	if err != nil {
		t.Fatal(err)
	}
	if pastedYAML != yaml {
		t.Error("template-born yaml differs from the same yaml pasted")
	}

	// Caller mistakes are BadRequest: unknown template, taken name, bad knobs.
	if err := a.CreateFromTemplate("x", "nope", templateKnobs("a")); !state.IsBadRequest(err) {
		t.Errorf("unknown template err = %v, want BadRequest", err)
	}
	if err := a.CreateFromTemplate("mine", "custom-endpoints", templateKnobs("a")); !state.IsBadRequest(err) {
		t.Errorf("name collision err = %v, want BadRequest", err)
	}
	if err := a.CreateFromTemplate("y", "custom-endpoints", map[string]any{}); !state.IsBadRequest(err) {
		t.Errorf("bad knobs err = %v, want BadRequest", err)
	}
}

// TestReRenderSemantics is the resync-semantics matrix: unmodified
// re-renders cleanly (yaml replaced, knobs updated, presets kept); modified
// refuses; forced discards; a non-template config is refused either way.
func TestReRenderSemantics(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	createFromTemplate(t, a, "mine")
	if err := cfgstore.SetVar(a.Dir, "mine", "prod", "HC_API_KEY", "k"); err != nil {
		t.Fatal(err)
	}

	newKnobs := templateKnobs("hc")
	newKnobs["debug_tee"] = true

	// Unmodified: replace yaml, update knobs, keep presets.
	if err := a.ReRender("mine", newKnobs); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := a.Config("mine")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(yaml, "debug") {
		t.Errorf("re-render did not apply the new knob:\n%s", yaml)
	}
	if info.Modified {
		t.Error("fresh re-render reports modified — pristine hash not updated")
	}
	if info.Meta.Knobs["debug_tee"] != true {
		t.Errorf("knobs not updated in meta: %v", info.Meta.Knobs)
	}
	if info.Meta.Presets["prod"]["HC_API_KEY"] != "k" {
		t.Errorf("presets lost across re-render: %v", info.Meta.Presets)
	}

	// Hand-edit → tier 2: plain re-render refuses, forced discards.
	if err := cfgstore.WriteYAML(a.Dir, "mine", yaml+"# edited\n"); err != nil {
		t.Fatal(err)
	}
	err = a.ReRender("mine", newKnobs)
	if !state.IsBadRequest(err) || !strings.Contains(err.Error(), "locally modified") {
		t.Errorf("re-render of modified = %v, want BadRequest naming the edits", err)
	}
	if _, y, _ := a.Config("mine"); !strings.Contains(y, "# edited") {
		t.Error("refused re-render must leave the edits in place")
	}
	if err := a.ReRenderForce("mine", newKnobs); err != nil {
		t.Fatal(err)
	}
	if info, y, _ := a.Config("mine"); strings.Contains(y, "# edited") || info.Modified {
		t.Error("forced re-render must discard the edits and be unmodified")
	}

	// nil knobs = re-render with the stored ones (bit-stable).
	_, before, _ := a.Config("mine")
	if err := a.ReRender("mine", nil); err != nil {
		t.Fatal(err)
	}
	if _, after, _ := a.Config("mine"); after != before {
		t.Error("nil-knobs re-render changed the yaml")
	}

	// Not template-born: refused, forced or not.
	if err := a.ReRender("debug", nil); !state.IsBadRequest(err) {
		t.Errorf("re-render of shipped config = %v, want BadRequest", err)
	}
	if err := a.ReRenderForce("debug", nil); !state.IsBadRequest(err) {
		t.Errorf("forced re-render of shipped config = %v, want BadRequest", err)
	}
}

// TestReRenderReactivatesWhenActive: re-rendering the active running
// configuration re-applies it (the shared reactivateIf rule); an inactive
// one never touches launchd.
func TestReRenderReactivatesWhenActive(t *testing.T) {
	calls := setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	createFromTemplate(t, a, "mine")

	*calls = nil
	if err := a.ReRender("mine", nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("re-rendering an inactive config touched launchd: %v", *calls)
	}

	if err := a.Activate("mine", ""); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := a.ReRender("mine", nil); err != nil {
		t.Fatal(err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("re-rendering the active running config did not re-apply: %v", *calls)
	}
}

// TestReRenderKnobsSurviveJSONRoundTrip: knobs read back from meta.json
// (JSON shapes: []any, not []string) still normalize and render.
func TestReRenderKnobsSurviveJSONRoundTrip(t *testing.T) {
	setup(t, "")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	createFromTemplate(t, a, "mine")
	// A second App re-reads meta.json from disk — the round trip.
	b := &app.App{Dir: a.Dir}
	if err := b.ReRender("mine", nil); err != nil {
		t.Fatalf("re-render from persisted knobs: %v", err)
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
