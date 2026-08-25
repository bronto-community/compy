// Package vars extracts environment-variable references from collector YAML
// text, e.g. ${NAME}, ${NAME:-default}, ${env:NAME}, ${env:NAME:-default}.
package vars

import (
	"regexp"
	"sort"
	"strings"
)

// Var describes one variable reference found in collector YAML.
type Var struct {
	Name        string `json:"name"`
	Default     string `json:"default"` // from :-fallback, "" if none
	HasDefault  bool   `json:"has_default"`
	Description string `json:"description"` // trailing same-line YAML comment, "" if none
}

// nameDefault matches "NAME" or "NAME:-default" (default captured verbatim,
// including any nested ${...}).
var nameDefault = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(:-(.*))?$`)

// Parse scans collector YAML text for ${NAME}, ${NAME:-def}, ${env:NAME},
// ${env:NAME:-def} references. Other schemes (${file:...}, ${secretsmanager:...},
// anything with a scheme: prefix other than env) are ignored. Deduplicated by
// name (first occurrence wins for default/description), sorted by name.
func Parse(yaml string) []Var {
	seen := map[string]Var{}

	for i := 0; i < len(yaml)-1; i++ {
		if yaml[i] != '$' || yaml[i+1] != '{' {
			continue
		}

		// Find the matching closing brace, allowing one level of nested
		// ${...} inside the default (ponytail: nested refs not recursed,
		// kept verbatim in Default).
		depth := 1
		j := i + 2
		for ; j < len(yaml) && depth > 0; j++ {
			switch yaml[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth != 0 {
			continue // unterminated reference; not a match, keep scanning
		}
		end := j // index just past the closing brace
		content := yaml[i+2 : end-1]

		if name, def, hasDefault, ok := parseRef(content); ok {
			if _, dup := seen[name]; !dup {
				seen[name] = Var{
					Name:        name,
					Default:     def,
					HasDefault:  hasDefault,
					Description: trailingComment(yaml, end),
				}
			}
		}

		i = end - 1 // resume scanning right after this reference
	}

	if len(seen) == 0 {
		return nil
	}
	out := make([]Var, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parseRef parses the inside of ${...}: either NAME[:-default] directly, or
// env:NAME[:-default]. Any other scheme: prefix is not ours to handle.
func parseRef(content string) (name, def string, hasDefault, ok bool) {
	if m := nameDefault.FindStringSubmatch(content); m != nil {
		return m[1], m[3], m[2] != "", true
	}
	if rest, isEnv := strings.CutPrefix(content, "env:"); isEnv {
		if m := nameDefault.FindStringSubmatch(rest); m != nil {
			return m[1], m[3], m[2] != "", true
		}
	}
	return "", "", false, false
}

// trailingComment returns the trimmed text of a "# ..." comment on the same
// line as the reference ending at byte offset from, or "" if none.
func trailingComment(s string, from int) string {
	line := s[from:]
	if nl := strings.IndexByte(line, '\n'); nl != -1 {
		line = line[:nl]
	}
	if h := strings.IndexByte(line, '#'); h != -1 {
		return strings.TrimSpace(line[h+1:])
	}
	return ""
}
