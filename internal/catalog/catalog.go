// Package catalog is compy's config-template engine: it parses tier-3
// config SOURCES (front-matter schema + "---" + Go text/template body),
// validates knob values against the schema, and renders the body into plain
// collector YAML. Only `type: secret` fields survive the render as
// ${env:NAME} references (with trailing comments, so the vars parser gives
// them cards for free); everything else bakes as literals. See
// docs/design/2026-08-30-config-templates.md, Amendment 3: templating is a
// property of the config source — anyone can WRITE a templated config, and
// the embedded catalog/*.tmpl entries are just starters whose source is
// copied into a new config.
//
// The front matter comes in two forms, both first-class forever: JSON
// (first non-blank byte '{', then a "---" separator line — the original
// form) and YAML between "---" marker lines (the standard front-matter
// convention). Both decode into the same schema structs; fields are arrays
// either way, which preserves declaration order (form order) for free.
//
// Repeat groups are AUTHOR-DEFINED (Amendment 8): a schema declares any
// number of them under `groups:`, each with its own id, and the id is both
// the bag key and the render data key — {{range .backends}}, {{range
// .receivers}}, {{range .ottl_statements}}. Nothing about "backends" is
// built in. A row's identity is a LABEL the user edits at the top of the
// row card (reserved bag key "_label", so no schema field competes with
// it); its slug derives exporter ids and secret env names.
//
// Boring rule (authoring guidance for user templates, enforced for shipped
// ones): template bodies get `if` and `range` plus five helper funcs —
// `upper`, `slug`, and the list trio `list`/`append`/`join` that builds a
// processor or exporter list without any Go behind it.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/vars"
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

// Group is one author-defined repeat group: Min..Max rows of Fields, listed
// under `groups:`. ID is the bag key AND the render data key, so the body
// says {{range .<id>}}; Item names ONE row in the UI ("+ add backend") and
// seeds a new row's default label.
type Group struct {
	ID     string  `json:"id"`
	Label  string  `json:"label,omitempty"`
	Item   string  `json:"item,omitempty"`
	Min    int     `json:"min,omitempty"`
	Max    int     `json:"max,omitempty"`
	Fields []Field `json:"fields"`
}

// LabelKey is the machinery-owned row identity every group row carries —
// the editable label at the top of the row card, the preset-tab idiom
// (Amendment 8: rows are named the way presets are, never through a `name`
// field). Schema fields may not use "_"-prefixed names, so it can never
// collide with one.
const LabelKey = "_label"

// Template is one parsed config source: the schema (serialized to the UI
// as-is), the parsed body, and the raw source text it came from.
type Template struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Sections    []Section `json:"sections,omitempty"`
	Fields      []Field   `json:"fields,omitempty"` // config-level
	Groups      []Group   `json:"groups,omitempty"` // repeat groups
	body        *template.Template
	raw         string
}

// group finds a declared group by id.
func (t Template) group(id string) (Group, bool) {
	for _, g := range t.Groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// rowLabel is the default label for row i — "backend 1", "receiver 2".
func (g Group) rowLabel(i int) string { return fmt.Sprintf("%s %d", g.Item, i+1) }

// rowSlug is a row's identity in the rendered yaml: the slug of its label,
// defaulted by position when the bag has none (a bag straight off disk may
// predate the label). It derives exporter ids and secret env names, so
// everything downstream agrees by construction.
func (g Group) rowSlug(row map[string]any, i int) string {
	label, _ := row[LabelKey].(string)
	if strings.TrimSpace(label) == "" {
		label = g.rowLabel(i)
	}
	return slugify(label)
}

// Source is the raw source text this template was parsed from — what
// creating a config from a catalog entry copies into configs/<name>/config.tmpl.
func (t Template) Source() string { return t.raw }

var fieldTypes = map[string]bool{
	"slug": true, "url": true, "string": true, "choice": true,
	"multi": true, "toggle": true, "secret": true,
}

// userErrf marks an error as the caller's mistake (400), like cfgstore's.
func userErrf(format string, a ...any) error {
	return state.BadRequest(fmt.Errorf(format, a...))
}

// funcs is the whole template vocabulary. upper/slug are string helpers;
// list/append/join are what lets a BODY build the lists that used to need Go
// behind it — a processor order, an exporter set — without any knowledge of
// what the group is called:
//
//	{{$e := list}}{{range .backends}}{{$e = append $e (printf "otlp_http/%s" ._slug)}}{{end}}
//	exporters: [{{join $e ", "}}]
var funcs = template.FuncMap{
	"upper": strings.ToUpper,
	"slug":  slugify,
	"list":  func(items ...any) []any { return items },
	"append": func(l []any, items ...any) []any {
		return append(slices.Clip(slices.Clone(l)), items...)
	},
	"join": func(l []any, sep string) string {
		parts := make([]string, len(l))
		for i, v := range l {
			parts[i] = fmt.Sprint(v)
		}
		return strings.Join(parts, sep)
	},
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
		name := strings.TrimSuffix(e.Name(), path.Ext(e.Name()))
		t, err := ParseSource(string(data))
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", e.Name(), err)
		}
		if t.Name != name {
			return nil, fmt.Errorf("template %s: schema name %q does not match filename", e.Name(), t.Name)
		}
		ts = append(ts, t)
	}
	slices.SortFunc(ts, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	return ts, nil
})

// yamlFront splits a YAML-fronted source — "---" line, schema, "---" line,
// body — the standard front-matter convention. ok demands only the SHAPE:
// the first non-blank line is exactly "---" (trailing whitespace allowed)
// and a closing "---" line exists. front comes back padded with one blank
// line per consumed line before the schema (leading blanks + the opening
// marker), so yaml.v3's error line numbers land relative to the FULL source
// with no arithmetic.
func yamlFront(content string) (front, body string, ok bool) {
	marker := func(line string) bool { return strings.TrimRight(line, " \t\r") == "---" }
	rest := content
	var b strings.Builder
	for {
		line, tail, found := strings.Cut(rest, "\n")
		if !found {
			return "", "", false
		}
		rest = tail
		b.WriteByte('\n')
		if marker(line) {
			break
		}
		if strings.TrimSpace(line) != "" {
			return "", "", false
		}
	}
	for {
		line, tail, found := strings.Cut(rest, "\n")
		if marker(line) {
			return b.String(), tail, true
		}
		if !found {
			return "", "", false
		}
		b.WriteString(line)
		b.WriteByte('\n')
		rest = tail
	}
}

// parseYAMLSchema strictly decodes YAML front matter into the schema —
// KnownFields mirrors the JSON path's DisallowUnknownFields. The struct
// field names lowercase to exactly the schema's keys, so no yaml tags are
// needed.
func parseYAMLSchema(front string) (Template, error) {
	dec := yaml.NewDecoder(strings.NewReader(front))
	dec.KnownFields(true)
	var t Template
	if err := dec.Decode(&t); err != nil {
		return Template{}, fmt.Errorf("template schema: %v", err)
	}
	return t, nil
}

// LooksLikeSource reports whether content is textually SHAPED like a tier-3
// source in either front-matter form, without judging whether the schema
// parses. The source-save route uses it to decide between "this was never
// meant as a source" and "this is a source attempt whose schema deserves a
// loud parse error".
func LooksLikeSource(content string) bool {
	if _, _, ok := yamlFront(content); ok {
		return true
	}
	trimmed := strings.TrimLeft(content, " \t\r\n")
	return strings.HasPrefix(trimmed, "{") && strings.Contains(content, "\n---\n")
}

// IsSource reports whether content is tier-3 template source rather than
// plain collector YAML. Two front-matter forms commit differently:
//
//   - JSON (the original): textual, like ${env:} detection — first
//     non-blank byte '{' plus a "---" separator line. Plain collector YAML
//     never starts with '{', so text is enough.
//   - YAML ("---" schema "---" body): a plain collector config may legally
//     open with "---" (the YAML document marker), so shape alone cannot
//     commit. The between-text must also strictly decode into the schema
//     struct AND carry a name — otherwise the content is a plain config
//     and falls through QUIETLY (the paste/demote path must not error).
//
// Once a form commits, broken details (bad field types, a body that will
// not compile) error loudly in ParseSource, same as JSON always has.
// KEEP IN LOCKSTEP with isSourceText in webui/static/helpers.js.
func IsSource(content string) bool {
	if front, _, ok := yamlFront(content); ok {
		t, err := parseYAMLSchema(front)
		return err == nil && t.Name != ""
	}
	trimmed := strings.TrimLeft(content, " \t\r\n")
	return strings.HasPrefix(trimmed, "{") && strings.Contains(content, "\n---\n")
}

// ParseSource parses a config source: front matter split from body (YAML
// form: between the "---" marker lines; JSON form: at the first "---"
// line), schema checked for internal consistency, body compiled. Schema
// errors carry line numbers relative to the full source (the YAML decoder
// sees position-preserving padding). Errors are BadRequest-marked — the
// source is the user's file.
func ParseSource(content string) (Template, error) {
	var t Template
	var body string
	if front, b, ok := yamlFront(content); ok {
		var err error
		if t, err = parseYAMLSchema(front); err != nil {
			return Template{}, state.BadRequest(err)
		}
		if t.Name == "" {
			return Template{}, userErrf("template schema: name is required")
		}
		body = b
	} else {
		front, b, found := strings.Cut(content, "\n---\n")
		if !found {
			return Template{}, userErrf(`template source: missing "---" separator between schema and body`)
		}
		dec := json.NewDecoder(strings.NewReader(front))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&t); err != nil {
			return Template{}, userErrf("template schema: %v", err)
		}
		body = b
	}
	// An omitted max means "as many as the engine allows" — the bound has to
	// exist before checkSchema judges it, and the UI reads it back.
	for i := range t.Groups {
		if t.Groups[i].Max == 0 {
			t.Groups[i].Max = maxGroupRows
		}
	}
	if err := t.checkSchema(); err != nil {
		return Template{}, state.BadRequest(err)
	}
	fillLabels(t.Fields)
	for i := range t.Groups {
		fillGroup(&t.Groups[i])
	}
	body = strings.TrimPrefix(body, "\n")
	tmpl, err := template.New(t.Name).Funcs(funcs).Option("missingkey=error").Parse(body)
	if err != nil {
		return Template{}, userErrf("template body: %v", err)
	}
	t.body = tmpl
	t.raw = content
	return t, nil
}

// humanize turns a schema identifier into prose — "api_key" → "Api key".
func humanize(s string) string {
	s = strings.NewReplacer("_", " ", "-", " ").Replace(s)
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// fillLabels derives a missing label from the field name — "api_key" →
// "Api key" — so label: is optional in the schema. Names are non-empty
// (checkSchema runs first).
func fillLabels(fields []Field) {
	for i, f := range fields {
		if f.Label == "" {
			fields[i].Label = humanize(f.Name)
		}
	}
}

// fillGroup derives a group's missing label and item name from its id —
// "backends" → "Backends" / "backend" — so only `id` and `fields` are
// mandatory. ponytail: the singular is a trailing-"s" strip; declare `item`
// when that reads wrong ("ottl_policies").
func fillGroup(g *Group) {
	fillLabels(g.Fields)
	if g.Label == "" {
		g.Label = humanize(g.ID)
	}
	if g.Item == "" {
		s := strings.NewReplacer("_", " ", "-", " ").Replace(g.ID)
		g.Item = strings.TrimSuffix(s, "s")
		if g.Item == "" {
			g.Item = s
		}
	}
}

// maxGroupRows caps every repeat group. Groups themselves are unlimited in
// number; ONE group's row count is capped because each row is a rendered
// exporter/receiver block and a form card.
const maxGroupRows = 16

var groupID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// checkSchema validates the schema itself: known types, options where the
// type needs them, defaults among the options, sections that exist, group
// ids that are usable as template data keys, row bounds inside 0..16, unique
// field names, and the "_" prefix reserved for the machinery.
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
			if strings.HasPrefix(f.Name, "_") {
				return fmt.Errorf("%s.%s: names starting with _ are reserved (%s is the row label)", where, f.Name, LabelKey)
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
	taken := map[string]bool{}
	for _, f := range t.Fields {
		taken[f.Name] = true
	}
	for _, g := range t.Groups {
		if !groupID.MatchString(g.ID) {
			return fmt.Errorf("groups: id %q must be lowercase letters, digits and underscores, starting with a letter", g.ID)
		}
		if taken[g.ID] {
			return fmt.Errorf("groups: id %q is already a field or group name", g.ID)
		}
		taken[g.ID] = true
		if g.Min < 0 || g.Max > maxGroupRows || g.Min > g.Max {
			return fmt.Errorf("%s: row bounds %d..%d outside 0..%d", g.ID, g.Min, g.Max, maxGroupRows)
		}
		if len(g.Fields) == 0 {
			return fmt.Errorf("%s: a group needs at least one field", g.ID)
		}
		if err := checkFields(g.ID, g.Fields); err != nil {
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

// checkValue validates one bag value against its field, returning the
// value in canonical Go shape (strings, bools, []string).
func checkValue(f Field, v any) (any, error) {
	switch f.Type {
	case "secret":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%v is not a string", v)
		}
		return s, nil
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

// zeroValue is a field's empty-but-present value, by type. KEEP IN LOCKSTEP
// with helpers.js's fieldDefault, which seeds the same shapes in the form.
func zeroValue(f Field) any {
	switch f.Type {
	case "toggle":
		return false
	case "multi":
		return []string{}
	default:
		return ""
	}
}

// normalizeFields validates bag values against fields and fills defaults,
// returning the normalized map. where prefixes error messages
// ("backends[1].endpoint: ..."). Secret fields are ordinary bag members
// (Amendment 4: presets own everything) — validated as strings when present,
// never defaulted or demanded here: a missing key is the pre-flight's
// business (MissingRequired), not the render's.
func normalizeFields(where string, fields []Field, bag map[string]any) (map[string]any, error) {
	known := map[string]Field{}
	for _, f := range fields {
		known[f.Name] = f
	}
	for k := range bag {
		if _, ok := known[k]; !ok {
			return nil, userErrf("%s%s: unknown field", where, k)
		}
	}
	out := map[string]any{}
	for _, f := range fields {
		v, present := bag[f.Name]
		if !present {
			if f.Type == "secret" {
				continue
			}
			if f.Default != nil {
				v = f.Default
			} else if f.Optional {
				// An optional field with no declared default still has to
				// EXIST for the body: `missingkey=error` turns an absent key
				// into a render failure, so the type's zero stands in — the
				// same value the form seeds and the same one an emptied
				// control stores.
				out[f.Name] = zeroValue(f)
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

// NormalizeBag validates a preset's value bag against the template's schema
// and returns a defaults-filled copy — what a preset write stores and what
// the render draws on. Secret values ride along as strings; every other
// missing required field errors. Unknown top-level keys holding a STRING
// pass through untouched: they are free-var material (hand-written ${env:}
// refs in the body — tier 3 contains tier 2), stored in the bag under the
// var's own name and exported at activation. A key naming a schema field is
// always the schema's (schema wins the collision by construction: it never
// reaches this pass-through), and an unknown key holding anything but a
// string is still the caller's typo. All errors are BadRequest-marked and
// name the offending field.
func (t Template) NormalizeBag(bag map[string]any) (map[string]any, error) {
	known := t.knownTop()
	rest := map[string]any{}
	free := map[string]any{}
	groups := map[string]any{}
	for k, v := range bag {
		if _, isGroup := t.group(k); isGroup {
			groups[k] = v
			continue
		}
		if s, isStr := v.(string); isStr && !known[k] {
			free[k] = s
			continue
		}
		rest[k] = v
	}
	out, err := normalizeFields("", t.Fields, rest)
	if err != nil {
		return nil, err
	}
	maps.Copy(out, free)
	for _, g := range t.Groups {
		rows, err := normalizeGroup(g, groups[g.ID])
		if err != nil {
			return nil, err
		}
		out[g.ID] = rows
	}
	return out, nil
}

// normalizeGroup validates one group's rows: bounds, per-row fields, and the
// row LABEL — defaulted by position when absent, and unique after slugging
// (two rows sharing a slug would share an exporter id and a secret env
// name).
func normalizeGroup(g Group, v any) ([]any, error) {
	var rows []any
	if v != nil {
		switch vv := v.(type) {
		case []any:
			rows = vv
		case []map[string]any:
			for _, r := range vv {
				rows = append(rows, r)
			}
		default:
			return nil, userErrf("%s: not a list", g.ID)
		}
	}
	if len(rows) < g.Min || len(rows) > g.Max {
		return nil, userErrf("%s: need %d to %d entries, got %d", g.ID, g.Min, g.Max, len(rows))
	}
	out := make([]any, 0, len(rows))
	slugs := map[string]string{}
	for i, r := range rows {
		row, ok := r.(map[string]any)
		if !ok {
			return nil, userErrf("%s[%d]: not an object", g.ID, i)
		}
		label, _ := row[LabelKey].(string)
		if label = strings.TrimSpace(label); label == "" {
			label = g.rowLabel(i)
		}
		s := slugify(label)
		if s == "" {
			return nil, userErrf("%s[%d].%s: %q has no letters or digits to name it by", g.ID, i, LabelKey, label)
		}
		if other, dup := slugs[s]; dup {
			return nil, userErrf("%s[%d].%s: %q is the same name as %q", g.ID, i, LabelKey, label, other)
		}
		slugs[s] = label
		plain := maps.Clone(row)
		delete(plain, LabelKey)
		norm, err := normalizeFields(fmt.Sprintf("%s[%d].", g.ID, i), g.Fields, plain)
		if err != nil {
			return nil, err
		}
		norm[LabelKey] = label
		out = append(out, norm)
	}
	return out, nil
}

// knownTop is the set of top-level bag keys the schema owns: field names
// plus every group's id. Any other top-level key holding a string is
// free-var material.
func (t Template) knownTop() map[string]bool {
	known := map[string]bool{}
	for _, f := range t.Fields {
		known[f.Name] = true
	}
	for _, g := range t.Groups {
		known[g.ID] = true
	}
	return known
}

// PruneUnknown drops bag keys the schema does not declare (secrets are
// declared fields and stay), backend rows' unknown keys too — EXCEPT
// unknown top-level string values, which are free vars (tier 2 inside tier
// 3) and must survive a schema pass; only Reconcile, with a render in hand,
// may prune those. It is how stored bags survive a schema edit: removed
// fields vanish here, new fields pick up their defaults in NormalizeBag. A
// nil map prunes to an empty one.
func (t Template) PruneUnknown(bag map[string]any) map[string]any {
	keep := func(fields []Field, in map[string]any) map[string]any {
		known := map[string]bool{}
		for _, f := range fields {
			known[f.Name] = true
		}
		out := map[string]any{}
		for k, v := range in {
			if known[k] {
				out[k] = v
			}
		}
		return out
	}
	out := keep(t.Fields, bag)
	known := t.knownTop()
	for k, v := range bag {
		if s, isStr := v.(string); isStr && !known[k] {
			out[k] = s
		}
	}
	for _, g := range t.Groups {
		rows, ok := bag[g.ID].([]any)
		if !ok {
			continue
		}
		var pruned []any
		for _, r := range rows {
			row, ok := r.(map[string]any)
			if !ok {
				continue
			}
			kept := keep(g.Fields, row)
			if label, ok := row[LabelKey].(string); ok {
				kept[LabelKey] = label // the row's identity is never pruned
			}
			pruned = append(pruned, kept)
		}
		out[g.ID] = pruned
	}
	return out
}

// Reconcile adapts a stored preset bag to this (possibly newer) schema:
// unknown fields are pruned, fields the schema defaults are filled in —
// per preset, at every source save and sync. Free vars get the same
// removed-field treatment, judged against this bag's OWN render (per-preset
// structure is real: an ${env:} ref inside an {{if}} that didn't render
// doesn't exist for this bag): a stored free-var value whose name the
// render no longer references is dropped. Free vars are not secrets
// (secrets are schema fields, kept above), so pruning them is safe.
// Lenient by design: a required field with no default stays absent, and a
// bag that cannot render keeps its free-var values (that preset's next
// write or activation answers strictly); reconciliation must never invent
// values or fail over a preset that isn't the one running. storageDir is
// Render's — the caller's state directory.
func (t Template) Reconcile(bag map[string]any, storageDir string) map[string]any {
	fill := func(fields []Field, in map[string]any) {
		for _, f := range fields {
			if _, ok := in[f.Name]; !ok && f.Default != nil {
				in[f.Name] = f.Default
			}
		}
	}
	out := t.PruneUnknown(bag)
	fill(t.Fields, out)
	for _, g := range t.Groups {
		if _, ok := out[g.ID]; !ok {
			// A bag that predates this group (a plain config upgraded to a
			// templated default, or a group the author just added) seeds Min
			// default rows — the repeat-group version of "fields the schema
			// defaults are filled in". Row fields without defaults stay
			// absent, lenient as ever.
			rows := make([]any, g.Min)
			for i := range rows {
				rows[i] = map[string]any{}
			}
			out[g.ID] = rows
		}
		if rows, ok := out[g.ID].([]any); ok {
			for i, r := range rows {
				row, ok := r.(map[string]any)
				if !ok {
					continue
				}
				fill(g.Fields, row)
				if s, _ := row[LabelKey].(string); strings.TrimSpace(s) == "" {
					row[LabelKey] = g.rowLabel(i)
				}
			}
		}
	}
	if rendered, err := t.Render(out, storageDir); err == nil {
		live := map[string]bool{}
		for _, v := range t.FreeVars(rendered, out) {
			live[v.Name] = true
		}
		known := t.knownTop()
		for k := range out {
			if !known[k] && !live[k] {
				delete(out, k)
			}
		}
	}
	return out
}

// secretEnvName derives a secret's environment variable name from its bag
// path parts: lowercase-dashed names become UPPER_SNAKE ("honeycomb",
// "api_key" → "HONEYCOMB_API_KEY"). The render vocabulary and SecretEnv
// share it, so the yaml's ${env:...} references and the activation
// environment always agree on names.
func secretEnvName(parts ...string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.Join(parts, "_"), "-", "_"))
}

// secretWalk visits every secret field's derived env name with the bag's
// value for it (possibly nil/blank — the walk covers unset secrets too, so
// FreeVars can subtract the NAMES regardless of value).
func (t Template) secretWalk(bag map[string]any, fn func(name string, v any)) {
	for _, f := range t.Fields {
		if f.Type == "secret" {
			fn(secretEnvName(f.Name), bag[f.Name])
		}
	}
	for _, g := range t.Groups {
		rows, _ := bag[g.ID].([]any)
		for i, r := range rows {
			row, _ := r.(map[string]any)
			s := g.rowSlug(row, i)
			if s == "" {
				continue
			}
			for _, f := range g.Fields {
				if f.Type == "secret" {
					fn(secretEnvName(s, f.Name), row[f.Name])
				}
			}
		}
	}
}

// SecretEnv extracts a bag's secret values as environment variables — the
// one invisible rule: `type: secret` values travel via the environment,
// never baked into rendered yaml. A top-level secret field F maps to
// UPPER(F); a repeat-row secret field F in the row named N maps to
// UPPER(N_F). Empty and whitespace-only values are omitted, per the
// activation-env rule (set-but-empty defeats the yaml's own fallbacks).
func (t Template) SecretEnv(bag map[string]any) map[string]string {
	env := map[string]string{}
	t.secretWalk(bag, func(name string, v any) {
		if s, _ := v.(string); strings.TrimSpace(s) != "" {
			env[name] = s
		}
	})
	return env
}

// FreeVars is tier 2 living inside tier 3: the ${env:} references a
// preset's RENDER carries that the schema does not own — hand-written env
// refs in the template body. Extracted by the tier-2 vars parser (so
// trailing-comment descriptions and :-defaults come for free), minus
// COMPY_* (compy injects those), minus the bag's derived secret env names
// (schema fields in disguise), minus any name colliding with a top-level
// schema key (schema wins: such a name stays a form field, never a free
// var). Discovery is per-preset — the caller renders THIS bag first; a ref
// inside an {{if}} that didn't render doesn't exist for this preset.
func (t Template) FreeVars(rendered string, bag map[string]any) []vars.Var {
	skip := t.knownTop()
	t.secretWalk(bag, func(name string, _ any) { skip[name] = true })
	out := []vars.Var{}
	for _, v := range vars.Parse(rendered) {
		if strings.HasPrefix(v.Name, "COMPY_") || skip[v.Name] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// EnvFor composes a tier-3 activation environment from one preset's bag:
// the secret values (SecretEnv's rule) plus every free-var value — the
// bag's non-empty string values under keys the schema does not own,
// exported verbatim so the render's hand-written ${env:} refs resolve.
// The render never bakes free vars; the collector expands them, exactly as
// in tier 2. Schema non-secret values still never travel via env (they are
// baked). COMPY_* is the caller's to add (and it wins, added after).
func (t Template) EnvFor(bag map[string]any) map[string]string {
	env := t.SecretEnv(bag)
	known := t.knownTop()
	for k, v := range bag {
		if known[k] {
			continue
		}
		if s, _ := v.(string); strings.TrimSpace(s) != "" {
			env[k] = s
		}
	}
	return env
}

// MissingRequired names the schema-required fields a preset bag leaves
// without a value — non-optional fields with no default (secrets never have
// one) that are absent or blank — as form-keyed paths
// ("backends[0].api_key"). The activation pre-flight's rule, generalized
// from tier 2's yaml-vars version.
func (t Template) MissingRequired(bag map[string]any) []string {
	var missing []string
	check := func(prefix string, fields []Field, in map[string]any) {
		for _, f := range fields {
			if f.Optional || f.Default != nil {
				continue
			}
			if s, isStr := in[f.Name].(string); in[f.Name] == nil || (isStr && strings.TrimSpace(s) == "") {
				missing = append(missing, prefix+f.Name)
			}
		}
	}
	check("", t.Fields, bag)
	for _, g := range t.Groups {
		rows, _ := bag[g.ID].([]any)
		for i, r := range rows {
			row, _ := r.(map[string]any)
			check(fmt.Sprintf("%s[%d].", g.ID, i), g.Fields, row)
		}
	}
	return missing
}

// stripSecrets removes secret-typed entries (top-level and per backend row)
// from a bag copy: render inputs never include a secret, so a template body
// that references one fails loudly instead of baking it.
func (t Template) stripSecrets(bag map[string]any) map[string]any {
	strip := func(fields []Field, in map[string]any) map[string]any {
		out := maps.Clone(in)
		for _, f := range fields {
			if f.Type == "secret" {
				delete(out, f.Name)
			}
		}
		return out
	}
	out := strip(t.Fields, bag)
	for _, g := range t.Groups {
		rows, ok := out[g.ID].([]any)
		if !ok {
			continue
		}
		stripped := make([]any, 0, len(rows))
		for _, r := range rows {
			if row, ok := r.(map[string]any); ok {
				stripped = append(stripped, strip(g.Fields, row))
			} else {
				stripped = append(stripped, r)
			}
		}
		out[g.ID] = stripped
	}
	return out
}

// Render validates a preset's bag and executes the template body. Secret
// values are stripped from the inputs first (they travel via the
// environment; the rendered yaml keeps its ${env:} references), and each is
// replaced by its NAME under the row's `_env` map — the body writes
// ${env:{{._env.api_key}}} and can never accidentally bake a value.
//
// The data is the normalized bag: every top-level field under its own name,
// every group's rows under the group's id, each row carrying `_label` (what
// the user typed), `_slug` (its identity in the yaml) and `_env`. Plus
// `_env` for top-level secrets and `StorageDir` — where the offline queue's
// file_storage extension keeps its state, the caller's state directory,
// baked in as a literal. Execution errors are BadRequest-marked: the body is
// the user's file.
func (t Template) Render(bag map[string]any, storageDir string) (string, error) {
	norm, err := t.NormalizeBag(bag)
	if err != nil {
		return "", err
	}
	norm = t.stripSecrets(norm)
	data := map[string]any{}
	maps.Copy(data, norm)
	envNames := func(fields []Field, parts ...string) map[string]string {
		env := map[string]string{}
		for _, f := range fields {
			if f.Type == "secret" {
				env[f.Name] = secretEnvName(append(parts, f.Name)...)
			}
		}
		return env
	}
	for _, g := range t.Groups {
		rows, _ := norm[g.ID].([]any)
		out := make([]any, 0, len(rows))
		for i, r := range rows {
			row, _ := r.(map[string]any)
			rr := maps.Clone(row)
			if rr == nil {
				rr = map[string]any{}
			}
			s := g.rowSlug(row, i)
			rr["_slug"] = s
			rr["_env"] = envNames(g.Fields, s)
			out = append(out, rr)
		}
		data[g.ID] = out
	}
	data["_env"] = envNames(t.Fields)
	data["StorageDir"] = storageDir
	var b strings.Builder
	if err := t.body.Execute(&b, data); err != nil {
		return "", userErrf("render %s: %v", t.Name, err)
	}
	return b.String(), nil
}
