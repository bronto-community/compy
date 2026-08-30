// Package catalog holds compy's config templates: embedded catalog/*.tmpl
// files (front-matter schema + "---" + Go text/template body) that a
// creation form renders ONCE into plain collector YAML. Only `type: secret`
// fields survive the render as ${env:NAME} references (with trailing
// comments, so the vars parser gives them cards for free); everything else
// bakes as literals. See docs/design/2026-08-30-config-templates.md.
//
// The front matter is the JSON subset of YAML: templates ship compiled in
// and compy is stdlib-only, so the schema is decoded with encoding/json —
// fields are arrays, which preserves declaration order (form order) for
// free.
//
// Boring rule, enforced by construction: template bodies get `if` and
// `range` only, plus the two helper funcs `upper` and `slug`. Anything
// needing logic (temporality splits, processor order, exporter lists) is
// computed here in Go and handed to the body as flat values.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/template"

	"github.com/bronto-community/compy/internal/state"
)

//go:embed catalog
var embedded embed.FS

// Field is one form input in a template's schema. Declaration order in the
// front matter IS form order.
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // slug | url | string | choice | multi | toggle | secret
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"` // choice/multi
	Optional    bool     `json:"optional,omitempty"`
	Section     string   `json:"section,omitempty"`
	Advanced    bool     `json:"advanced,omitempty"` // per-repeat-row disclosure
}

// Section is a named group of fields; a collapsed one must be safely
// ignorable (every field in it defaults to the common case — the lint).
type Section struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// Repeat is the template's repeat group ("backends"): Min..Max rows of
// Fields.
type Repeat struct {
	Min    int     `json:"min"`
	Max    int     `json:"max"`
	Fields []Field `json:"fields"`
}

// Template is one catalog entry: the schema (serialized to the UI as-is)
// plus the parsed body.
type Template struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Sections    []Section `json:"sections,omitempty"`
	Fields      []Field   `json:"fields,omitempty"`   // config-level
	Backends    *Repeat   `json:"backends,omitempty"` // repeat group
	body        *template.Template
}

var fieldTypes = map[string]bool{
	"slug": true, "url": true, "string": true, "choice": true,
	"multi": true, "toggle": true, "secret": true,
}

// userErrf marks an error as the caller's mistake (400), like cfgstore's.
func userErrf(format string, a ...any) error {
	return state.BadRequest(fmt.Errorf(format, a...))
}

var funcs = template.FuncMap{
	"upper": strings.ToUpper,
	"slug":  slugify,
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// load parses every embedded catalog/*.tmpl once. Templates ship compiled
// in, so a load error is a build bug — surfaced as an error (and caught by
// TestLoadTemplates) rather than a panic.
var load = sync.OnceValues(func() ([]Template, error) {
	entries, err := embedded.ReadDir("catalog")
	if err != nil {
		return nil, err
	}
	var ts []Template
	for _, e := range entries {
		data, err := embedded.ReadFile("catalog/" + e.Name())
		if err != nil {
			return nil, err
		}
		t, err := parse(strings.TrimSuffix(e.Name(), path.Ext(e.Name())), string(data))
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", e.Name(), err)
		}
		ts = append(ts, t)
	}
	slices.SortFunc(ts, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return ts, nil
})

// parse splits front matter from body at the first "---" line and checks
// the schema's internal consistency.
func parse(name, content string) (Template, error) {
	front, body, found := strings.Cut(content, "\n---\n")
	if !found {
		return Template{}, fmt.Errorf(`missing "---" separator between schema and body`)
	}
	var t Template
	dec := json.NewDecoder(strings.NewReader(front))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return Template{}, fmt.Errorf("schema: %w", err)
	}
	if t.Name != name {
		return Template{}, fmt.Errorf("schema name %q does not match filename %q", t.Name, name)
	}
	if err := t.checkSchema(); err != nil {
		return Template{}, err
	}
	body = strings.TrimPrefix(body, "\n")
	tmpl, err := template.New(name).Funcs(funcs).Option("missingkey=error").Parse(body)
	if err != nil {
		return Template{}, fmt.Errorf("body: %w", err)
	}
	t.body = tmpl
	return t, nil
}

// checkSchema validates the schema itself: known types, options where the
// type needs them, defaults among the options, sections that exist, repeat
// bounds inside 1..8, unique field names.
func (t Template) checkSchema() error {
	sections := map[string]bool{}
	for _, s := range t.Sections {
		if s.ID == "" {
			return fmt.Errorf("section with empty id")
		}
		sections[s.ID] = true
	}
	checkFields := func(where string, fields []Field) error {
		seen := map[string]bool{}
		for _, f := range fields {
			if f.Name == "" || seen[f.Name] {
				return fmt.Errorf("%s: empty or duplicate field name %q", where, f.Name)
			}
			seen[f.Name] = true
			if !fieldTypes[f.Type] {
				return fmt.Errorf("%s.%s: unknown type %q", where, f.Name, f.Type)
			}
			if (f.Type == "choice" || f.Type == "multi") && len(f.Options) == 0 {
				return fmt.Errorf("%s.%s: %s field needs options", where, f.Name, f.Type)
			}
			if f.Section != "" && !sections[f.Section] {
				return fmt.Errorf("%s.%s: unknown section %q", where, f.Name, f.Section)
			}
			if f.Default != nil {
				if _, err := checkValue(f, f.Default); err != nil {
					return fmt.Errorf("%s.%s: bad default: %w", where, f.Name, err)
				}
			}
		}
		return nil
	}
	if err := checkFields("fields", t.Fields); err != nil {
		return err
	}
	if t.Backends != nil {
		if t.Backends.Min < 1 || t.Backends.Max > 8 || t.Backends.Min > t.Backends.Max {
			return fmt.Errorf("backends: repeat bounds %d..%d outside 1..8", t.Backends.Min, t.Backends.Max)
		}
		if err := checkFields("backends", t.Backends.Fields); err != nil {
			return err
		}
	}
	return nil
}

// Templates lists every catalog entry (schemas included, for the form).
func Templates() ([]Template, error) { return load() }

// Get returns one template by name; unknown names are the caller's mistake.
func Get(name string) (Template, error) {
	ts, err := load()
	if err != nil {
		return Template{}, err
	}
	for _, t := range ts {
		if t.Name == name {
			return t, nil
		}
	}
	return Template{}, userErrf("unknown template %q", name)
}

// checkValue validates one knob value against its field, returning the
// value in canonical Go shape (strings, bools, []string).
func checkValue(f Field, v any) (any, error) {
	switch f.Type {
	case "slug":
		s, ok := v.(string)
		if !ok || !state.ValidBackendName(s) {
			return nil, fmt.Errorf("%v is not a valid name (lowercase letters, digits, dashes)", v)
		}
		return s, nil
	case "url":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%v is not a URL", v)
		}
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%q is not an http(s) URL", s)
		}
		return s, nil
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%v is not a string", v)
		}
		return s, nil
	case "choice":
		s, ok := v.(string)
		if !ok || !slices.Contains(f.Options, s) {
			return nil, fmt.Errorf("%v is not one of %s", v, strings.Join(f.Options, ", "))
		}
		return s, nil
	case "multi":
		var out []string
		switch vv := v.(type) {
		case []string:
			out = vv
		case []any:
			for _, e := range vv {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("%v is not a list of strings", v)
				}
				out = append(out, s)
			}
		default:
			return nil, fmt.Errorf("%v is not a list", v)
		}
		for _, s := range out {
			if !slices.Contains(f.Options, s) {
				return nil, fmt.Errorf("%q is not one of %s", s, strings.Join(f.Options, ", "))
			}
		}
		if len(out) == 0 && !f.Optional {
			return nil, fmt.Errorf("pick at least one of %s", strings.Join(f.Options, ", "))
		}
		return out, nil
	case "toggle":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%v is not true/false", v)
		}
		return b, nil
	}
	return nil, fmt.Errorf("internal: unvalidatable type %q", f.Type)
}

// normalizeFields validates knobs against fields and fills defaults,
// returning the normalized map. where prefixes error messages
// ("backends[1].endpoint: ..."). Secrets are refused: they have no value at
// render time — their value lives in presets.
func normalizeFields(where string, fields []Field, knobs map[string]any) (map[string]any, error) {
	known := map[string]Field{}
	for _, f := range fields {
		known[f.Name] = f
	}
	for k := range knobs {
		f, ok := known[k]
		if !ok {
			return nil, userErrf("%s%s: unknown field", where, k)
		}
		if f.Type == "secret" {
			return nil, userErrf("%s%s: secrets are not template knobs; set the value in a preset", where, k)
		}
	}
	out := map[string]any{}
	for _, f := range fields {
		if f.Type == "secret" {
			continue
		}
		v, present := knobs[f.Name]
		if !present {
			if f.Default != nil {
				v = f.Default
			} else if f.Optional {
				continue
			} else {
				return nil, userErrf("%s%s: required", where, f.Name)
			}
		}
		cv, err := checkValue(f, v)
		if err != nil {
			return nil, userErrf("%s%s: %v", where, f.Name, err)
		}
		out[f.Name] = cv
	}
	return out, nil
}

// NormalizeKnobs validates knobs against the template's schema and returns
// a defaults-filled copy — what gets rendered and what meta.json records.
// All errors are BadRequest-marked and name the offending field.
func (t Template) NormalizeKnobs(knobs map[string]any) (map[string]any, error) {
	rest := map[string]any{}
	var backends any
	hasBackends := false
	for k, v := range knobs {
		if k == "backends" && t.Backends != nil {
			backends, hasBackends = v, true
			continue
		}
		rest[k] = v
	}
	out, err := normalizeFields("", t.Fields, rest)
	if err != nil {
		return nil, err
	}
	if t.Backends != nil {
		var rows []any
		if hasBackends {
			var ok bool
			rows, ok = backends.([]any)
			if !ok {
				if typed, isTyped := backends.([]map[string]any); isTyped {
					for _, r := range typed {
						rows = append(rows, r)
					}
				} else {
					return nil, userErrf("backends: not a list")
				}
			}
		}
		if len(rows) < t.Backends.Min || len(rows) > t.Backends.Max {
			return nil, userErrf("backends: need %d to %d entries, got %d", t.Backends.Min, t.Backends.Max, len(rows))
		}
		var normRows []any
		names := map[string]bool{}
		for i, r := range rows {
			row, ok := r.(map[string]any)
			if !ok {
				return nil, userErrf("backends[%d]: not an object", i)
			}
			norm, err := normalizeFields(fmt.Sprintf("backends[%d].", i), t.Backends.Fields, row)
			if err != nil {
				return nil, err
			}
			if n, ok := norm["name"].(string); ok {
				if names[n] {
					return nil, userErrf("backends[%d].name: duplicate name %q", i, n)
				}
				names[n] = true
			}
			normRows = append(normRows, norm)
		}
		out["backends"] = normRows
	}
	return out, nil
}

// Render validates knobs and executes the template body over the computed
// data. storageDir is where the offline queue's file_storage extension
// keeps its state — the caller's state directory; it bakes in as a literal.
func (t Template) Render(knobs map[string]any, storageDir string) (string, error) {
	norm, err := t.NormalizeKnobs(knobs)
	if err != nil {
		return "", err
	}
	var data any = norm
	if t.Name == "custom-endpoints" {
		data = customEndpointsData(norm, storageDir)
	}
	var b strings.Builder
	if err := t.body.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render %s: %w", t.Name, err)
	}
	return b.String(), nil
}
