package app_test

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/catalog"
	"github.com/bronto-community/compy/internal/cfgstore"
	"github.com/bronto-community/compy/internal/state"
	cfgvars "github.com/bronto-community/compy/internal/vars"
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
	// The normalized values seed the fresh default preset (Amendment 4:
	// presets own everything), defaults filled, and nothing lands in knobs.
	if info.Meta.Knobs != nil {
		t.Errorf("options-era knobs written: %v", info.Meta.Knobs)
	}
	bag, ok := info.Meta.Presets[cfgstore.DefaultPreset]
	if !ok {
		t.Fatalf("no default preset: %v", info.Meta.Presets)
	}
	row := bag["backends"].([]any)[0].(map[string]any)
	if row["temporality"] != "as-is" {
		t.Errorf("defaults not normalized into the preset bag: %v", row)
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
// hand-written (pasted) tier-3 config: value saves go through the preset
// pipeline and re-render the derived yaml; a schema edit reconciles every
// preset's bag (removed pruned, new defaulted); and the entry-path
// judgments hold (plain yaml refused on the source route, an empty source
// refused — values belong to the preset routes).
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

	// A value save is a preset write: the active preset's bag re-renders
	// the derived yaml.
	if _, err := a.ReplacePreset("tpl", "default", map[string]any{"greeting": "hey"}, true); err != nil {
		t.Fatal(err)
	}
	if _, yaml, _ := a.Config("tpl"); yaml != "a: hey\n" {
		t.Errorf("preset save render = %q", yaml)
	}

	// Source edit adds a field: stored bags carry over, the new field takes
	// its default — in every preset (a second one proves the per-preset
	// reconcile).
	if _, err := a.ReplacePreset("tpl", "other", map[string]any{"greeting": "yo"}, true); err != nil {
		t.Fatal(err)
	}
	src2 := srcWith(`{"name": "loud", "type": "toggle", "label": "L", "default": false}`,
		"a: {{.greeting}}{{if .loud}}!{{end}}")
	if _, err := a.WriteConfigSource("tpl", src2, true); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := a.Config("tpl")
	if err != nil {
		t.Fatal(err)
	}
	if yaml != "a: hey\n" {
		t.Errorf("schema-edit render = %q, want the stored greeting kept", yaml)
	}
	for _, preset := range []string{"default", "other"} {
		if info.Meta.Presets[preset]["loud"] != false {
			t.Errorf("%s: new field not defaulted into the bag: %v", preset, info.Meta.Presets[preset])
		}
	}

	// A value flip through the preset route changes the render.
	if _, err := a.ReplacePreset("tpl", "default", map[string]any{"greeting": "hey", "loud": true}, true); err != nil {
		t.Fatal(err)
	}
	if _, yaml, _ := a.Config("tpl"); yaml != "a: hey!\n" {
		t.Errorf("value-flip render = %q", yaml)
	}

	// Source edit removes the field: the stored value is pruned from every
	// bag.
	src3 := srcWith("", "a: {{.greeting}}")
	if _, err := a.WriteConfigSource("tpl", src3, true); err != nil {
		t.Fatal(err)
	}
	info, _, err = a.Config("tpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, preset := range []string{"default", "other"} {
		if _, has := info.Meta.Presets[preset]["loud"]; has {
			t.Errorf("%s: removed field survived in the bag: %v", preset, info.Meta.Presets[preset])
		}
	}

	// Plain yaml has no business on the source route; neither has an empty
	// source (values travel through the preset routes now).
	if _, err := a.WriteConfigSource("tpl", "plain: true\n", true); !state.IsBadRequest(err) {
		t.Errorf("plain yaml on the source route = %v, want BadRequest", err)
	}
	if _, err := a.WriteConfigSource("tpl", "", true); !state.IsBadRequest(err) {
		t.Errorf("empty source = %v, want BadRequest", err)
	}
}

// TestYAMLFrontMatterAsymmetry: the same broken YAML front matter is a
// QUIET plain config on the paste path (a plain collector config may
// legally open with "---") but a LOUD schema error on the source-save
// route of a templated config — and a valid YAML-fronted source promotes
// through the yaml paste path exactly as the JSON form always has.
func TestYAMLFrontMatterAsymmetry(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}

	// Paste path: a valid YAML-fronted source promotes to tier 3.
	ysrc := "---\nname: t\ndescription: d\nfields:\n  - name: g\n    type: string\n    label: G\n    default: hello\n---\na: {{.g}}\n"
	if err := a.CreateConfig("ytpl", ysrc); err != nil {
		t.Fatal(err)
	}
	info, yaml, err := a.Config("ytpl")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasTemplate || yaml != "a: hello\n" {
		t.Fatalf("yaml-fronted source: has_template=%v yaml=%q", info.HasTemplate, yaml)
	}

	// Paste path: a plain config that merely opens with "---" (even with a
	// second "---" line later) stays plain, no error.
	plainish := "---\nreceivers: {}\n---\nexporters: {}\n"
	if err := a.CreateConfig("plain", plainish); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := a.Config("plain"); info.HasTemplate {
		t.Error("plain --- config misdetected as tier 3")
	}
	if err := a.WriteConfigYAML("plain", "---\nreceivers: {}\n"); err != nil {
		t.Errorf("plain doc-marker yaml over a plain config = %v, want quiet", err)
	}

	// Source-save route: broken YAML front matter errors loudly with the
	// real schema diagnostic, not a demotion and not the generic sentence.
	_, err = a.WriteConfigSource("ytpl", "---\nname: t\nwat: 1\n---\nbody\n", true)
	if !state.IsBadRequest(err) || !strings.Contains(err.Error(), "wat") {
		t.Errorf("broken yaml schema on the source route = %v, want BadRequest naming wat", err)
	}
	// The templated pair is untouched by the refused save.
	if info, yaml, _ := a.Config("ytpl"); !info.HasTemplate || yaml != "a: hello\n" {
		t.Errorf("refused save disturbed the config: has_template=%v yaml=%q", info.HasTemplate, yaml)
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
	_, err = a.WriteConfigSource("tpl", src2, true)
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
	if info.Meta.Presets["default"]["greeting"] != "hello" {
		t.Errorf("preset bags not restored: %v", info.Meta.Presets)
	}

	// A preset write the collector rejects stores NOTHING (validated in a
	// scratch file before any write — no restore needed).
	if _, err := a.ReplacePreset("tpl", "default", map[string]any{"greeting": "boom"}, true); !state.IsBadRequest(err) {
		t.Fatalf("rejected preset write = %v, want BadRequest", err)
	}
	if info, _, _ := a.Config("tpl"); info.Meta.Presets["default"]["greeting"] != "hello" {
		t.Errorf("rejected preset write changed the bag: %v", info.Meta.Presets)
	}

	// The skip-validation escapes store anyway (the yaml route's twin):
	// first the source, then a preset value — the render tracks both.
	stale, err := a.WriteConfigSource("tpl", src2, false)
	if err != nil || stale {
		t.Fatalf("skip-validate save = %v stale=%v", err, stale)
	}
	if _, yaml, _ := a.Config("tpl"); yaml != "b: hello\n" {
		t.Errorf("skip-validate render = %q", yaml)
	}
	if stale, err := a.ReplacePreset("tpl", "default", map[string]any{"greeting": "boom"}, false); err != nil || stale {
		t.Fatalf("skip-validate preset write = %v stale=%v", err, stale)
	}
	if _, yaml, _ := a.Config("tpl"); yaml != "b: boom\n" {
		t.Errorf("skip-validate preset render = %q", yaml)
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
	if info.HasTemplate || yaml != "keep: me\n" {
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

// TestPresetWriteReactivatesWhenActive: a preset save on the active running
// config re-applies it (the shared reactivateIf rule); an inactive one
// never touches launchd.
func TestPresetWriteReactivatesWhenActive(t *testing.T) {
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
	if _, err := a.ReplacePreset("tpl", "default", map[string]any{"greeting": "hey"}, true); err != nil {
		t.Fatal(err)
	}
	if called(*calls, "bootstrap") {
		t.Errorf("saving an inactive config's preset touched launchd: %v", *calls)
	}

	if err := a.Activate("tpl", ""); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if _, err := a.ReplacePreset("tpl", "default", map[string]any{"greeting": "ho"}, true); err != nil {
		t.Fatal(err)
	}
	if !called(*calls, "bootstrap") {
		t.Errorf("saving the active running config's preset did not re-apply: %v", *calls)
	}
}

// TestBagsSurviveJSONRoundTrip: preset bags read back from meta.json (JSON
// shapes: []any, not []string) still normalize and render on a source
// re-save from a fresh App.
func TestBagsSurviveJSONRoundTrip(t *testing.T) {
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
	src, _, err := a.ConfigSource("mine")
	if err != nil {
		t.Fatal(err)
	}
	// A second App re-reads meta.json from disk — the round trip.
	b := &app.App{Dir: a.Dir}
	if _, err := b.WriteConfigSource("mine", src, true); err != nil {
		t.Fatalf("save from persisted bags: %v", err)
	}
	if _, after, _ := b.Config("mine"); after != before {
		t.Error("same-source save changed the yaml")
	}
}

// TestActivateRendersSelectedPreset is THE per-preset-structure proof:
// activation renders the source with the SELECTED preset's bag, so two
// presets with different backends produce different rendered pipelines —
// switching presets switches structure.
func TestActivateRendersSelectedPreset(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateFromCatalog("mine", "custom-endpoints", catalogKnobs("hc")); err != nil {
		t.Fatal(err)
	}
	two := map[string]any{"backends": []any{
		map[string]any{"name": "hc", "endpoint": "https://hc.example",
			"auth_header": "Authorization", "auth_scheme": "Bearer"},
		map[string]any{"name": "bronto", "endpoint": "https://in.bronto.example",
			"auth_header": "x-bronto-api-key", "temporality": "to-delta"},
	}}
	if _, err := a.ReplacePreset("mine", "two", two, true); err != nil {
		t.Fatal(err)
	}

	if err := a.Activate("mine", "default"); err != nil {
		t.Fatal(err)
	}
	_, yaml, _ := a.Config("mine")
	if !strings.Contains(yaml, "otlphttp/hc") || strings.Contains(yaml, "otlphttp/bronto") {
		t.Errorf("default-preset render wrong:\n%s", yaml)
	}

	if err := a.Activate("mine", "two"); err != nil {
		t.Fatal(err)
	}
	_, yaml, _ = a.Config("mine")
	if !strings.Contains(yaml, "otlphttp/bronto") || !strings.Contains(yaml, "metrics/delta") {
		t.Errorf("two-preset render did not switch structure:\n%s", yaml)
	}

	// And back: the derived yaml always tracks the activated preset.
	if err := a.Activate("mine", "default"); err != nil {
		t.Fatal(err)
	}
	if _, yaml, _ = a.Config("mine"); strings.Contains(yaml, "otlphttp/bronto") {
		t.Errorf("switching back kept the other preset's structure:\n%s", yaml)
	}
}

// TestActivationEnvSplitTier3 locks the env split: a tier-3 activation's
// plist environment dict holds ONLY the preset's secret values (under
// their derived env names) plus COMPY_* — non-secret values are baked into
// the render and must NOT be exported.
func TestActivationEnvSplitTier3(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	values := catalogKnobs("hc")
	values["backends"].([]any)[0].(map[string]any)["api_key"] = "s3cret-key"
	values["debug_tee"] = true
	if err := a.CreateFromCatalog("mine", "custom-endpoints", values); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("mine", ""); err != nil {
		t.Fatal(err)
	}
	plist := readPlist(t)
	if !strings.Contains(plist, "<key>HC_API_KEY</key><string>s3cret-key</string>") {
		t.Errorf("secret missing from the plist env:\n%s", plist)
	}
	_, envDict, found := strings.Cut(plist, "<key>EnvironmentVariables</key><dict>")
	if !found {
		t.Fatalf("no EnvironmentVariables dict in plist:\n%s", plist)
	}
	envDict, _, _ = strings.Cut(envDict, "</dict>")
	envKeys := regexp.MustCompile(`<key>([^<]+)</key>`).FindAllStringSubmatch(envDict, -1)
	for _, m := range envKeys {
		switch m[1] {
		case "HC_API_KEY", "COMPY_GRPC_PORT", "COMPY_HTTP_PORT":
		default:
			t.Errorf("non-secret value exported as env: %s", m[1])
		}
	}
	// The tier-2 rule is untouched: TestActivateHappyPath locks the full
	// export for plain configs.
}

// TestActivationEnvFreeVarsTier3 is Amendment 6's half of the env split: a
// hand-written ${env:} ref in the body is tier-2 capability inside tier 3 —
// its bag value exports at activation (never baked into the render), no bag
// value means the ref's own :-default holds (nothing exported), and schema
// non-secret values STILL never travel via env.
func TestActivationEnvFreeVarsTier3(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")
	listenPort(t)
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	src := srcWith("", "a: {{.greeting}}\nhost: ${env:ASDF:-fallback}  # target host")
	if err := a.CreateConfig("freeform", src); err != nil {
		t.Fatal(err)
	}

	// No bag value: the render keeps the ref, the env exports nothing for
	// it — the collector's own :-fallback holds.
	if err := a.Activate("freeform", ""); err != nil {
		t.Fatal(err)
	}
	if plist := readPlist(t); strings.Contains(plist, "ASDF") {
		t.Errorf("unset free var exported:\n%s", plist)
	}
	_, yaml, _ := a.Config("freeform")
	if !strings.Contains(yaml, "${env:ASDF:-fallback}") {
		t.Errorf("free-var ref missing from the render:\n%s", yaml)
	}

	// With a bag value (via the ordinary preset write — free vars are
	// ordinary bag members): the value travels via env, never the yaml.
	if err := a.SetVar("freeform", "default", "ASDF", "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	if err := a.Activate("freeform", ""); err != nil {
		t.Fatal(err)
	}
	plist := readPlist(t)
	if !strings.Contains(plist, "<key>ASDF</key><string>10.0.0.5</string>") {
		t.Errorf("free var missing from the plist env:\n%s", plist)
	}
	if strings.Contains(plist, "greeting") || strings.Contains(plist, "hello") {
		t.Errorf("schema non-secret leaked into the env:\n%s", plist)
	}
	if _, yaml, _ = a.Config("freeform"); strings.Contains(yaml, "10.0.0.5") {
		t.Errorf("free-var value baked into the render:\n%s", yaml)
	}

	// Pre-flight speaks the tier-2 rule for free vars: a no-default ref
	// with no bag value is missing; the defaulted ASDF never is.
	if err := a.WriteConfigYAML("freeform", srcWith("",
		"a: {{.greeting}}\nhost: ${env:ASDF:-fallback}\nneed: ${env:NEEDED}  # required thing")); err != nil {
		t.Fatal(err)
	}
	info, _, err := a.Config("freeform")
	if err != nil {
		t.Fatal(err)
	}
	missing := cfgstore.MissingRequired(a.Dir, info, "")
	if !reflect.DeepEqual(missing, []string{"NEEDED"}) {
		t.Errorf("MissingRequired = %v, want [NEEDED]", missing)
	}
	if err := a.SetVar("freeform", "default", "NEEDED", "x"); err != nil {
		t.Fatal(err)
	}
	info, _, _ = a.Config("freeform")
	if missing := cfgstore.MissingRequired(a.Dir, info, ""); len(missing) != 0 {
		t.Errorf("MissingRequired after set = %v, want none", missing)
	}

	// And a source edit that drops the ref prunes the stored value — the
	// removed-field reconcile, applied to free vars (they are not secrets).
	if err := a.WriteConfigYAML("freeform", srcWith("", "a: {{.greeting}}")); err != nil {
		t.Fatal(err)
	}
	info, _, _ = a.Config("freeform")
	bag := info.Meta.Presets["default"]
	if _, has := bag["ASDF"]; has {
		t.Errorf("free var survived losing its ref: %v", bag)
	}
	if bag["greeting"] != "hello" {
		t.Errorf("schema value lost in reconcile: %v", bag)
	}
}

// TestConfigDetailFreeVars: GET /api/configs/{name}'s payload carries the
// discovered free vars PER PRESET (vars.Var shape — name, default,
// description), so the UI round can render cards; values stay in
// info.meta.presets. Presets whose renders differ report different vars.
func TestConfigDetailFreeVars(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	src := srcWith(`{"name": "tee", "type": "toggle", "label": "T", "default": false}`,
		"a: {{.greeting}}\nhost: ${env:ASDF:-fallback}  # target host\n{{if .tee}}extra: ${env:ONLY_TEE}{{end}}")
	if err := a.CreateConfig("detail", src); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReplacePreset("detail", "teed", map[string]any{"tee": true, "ONLY_TEE": "v"}, true); err != nil {
		t.Fatal(err)
	}
	got, err := a.WebUIAPI().GetConfig("detail")
	if err != nil {
		t.Fatal(err)
	}
	free, ok := got.(map[string]any)["free_vars"].(map[string]any)
	if !ok {
		t.Fatalf("no free_vars in config detail: %v", got)
	}
	names := func(preset string) []string {
		var out []string
		for _, v := range free[preset].([]cfgvars.Var) {
			out = append(out, v.Name)
		}
		return out
	}
	if !reflect.DeepEqual(names("default"), []string{"ASDF"}) {
		t.Errorf("default preset free vars = %v, want [ASDF]", names("default"))
	}
	if !reflect.DeepEqual(names("teed"), []string{"ASDF", "ONLY_TEE"}) {
		t.Errorf("teed preset free vars = %v, want [ASDF ONLY_TEE]", names("teed"))
	}
	v := free["default"].([]cfgvars.Var)[0]
	if !v.HasDefault || v.Default != "fallback" || v.Description != "target host" {
		t.Errorf("free var lost its tier-2 card material: %+v", v)
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
