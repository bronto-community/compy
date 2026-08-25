// Package vars extracts ${VAR} / ${VAR:-default} / ${env:VAR} /
// ${env:VAR:-default} references from collector configuration YAML.
//
// STOPGAP: this file is a minimal implementation of the interface pinned in
// docs/superpowers/plans/2026-08-25-compy-v2-p1-core.md (Task 1), written so
// internal/cfgstore can compile and be tested while Task 1 is built in
// parallel. It WILL be replaced by Task 1's canonical version at merge time
// (conflicts resolved in its favor). Keep this package to this single file,
// with no test file — Task 1 owns the tests.
package vars

import (
	"sort"
	"strings"
)

// Var describes one variable reference found in a collector config.
type Var struct {
	Name        string `json:"name"`
	Default     string `json:"default"` // from :-fallback, "" if none
	HasDefault  bool   `json:"has_default"`
	Description string `json:"description"` // trailing same-line YAML comment, "" if none
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isNameChar(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// Parse scans collector YAML text for ${NAME}, ${NAME:-def}, ${env:NAME},
// ${env:NAME:-def} references. Other schemes (${file:...}, ${secretsmanager:...},
// anything with a scheme: prefix other than env) are ignored. Deduplicated by
// name (first occurrence wins for default/description), sorted by name.
func Parse(yaml string) []Var {
	seen := map[string]*Var{}
	var order []string

	n := len(yaml)
	for i := 0; i < n-1; i++ {
		if yaml[i] != '$' || yaml[i+1] != '{' {
			continue
		}
		j := i + 2

		tok1Start := j
		k := j
		for k < n && isNameChar(yaml[k]) {
			k++
		}
		if k == tok1Start || !isNameStart(yaml[tok1Start]) {
			continue // not a valid reference start
		}
		tok1 := yaml[tok1Start:k]

		nameStart := tok1Start
		nameEnd := k
		ignored := false

		if k < n && yaml[k] == ':' && !(k+1 < n && yaml[k+1] == '-') {
			// scheme:rest form
			if tok1 != "env" {
				ignored = true
			}
			k++ // past ':'
			nameStart = k
			for k < n && isNameChar(yaml[k]) {
				k++
			}
			nameEnd = k
		}

		if nameStart == nameEnd {
			continue // no name found (malformed / passthrough scheme with no name-shaped rest)
		}

		hasDefault := false
		def := ""
		end := k // index right after the name, still need to find closing '}'
		if k < n && yaml[k] == '}' {
			end = k + 1
		} else if k+1 < n && yaml[k] == ':' && yaml[k+1] == '-' {
			hasDefault = true
			defStart := k + 2
			depth := 1
			p := defStart
			for p < n && depth > 0 {
				switch yaml[p] {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						def = yaml[defStart:p]
					}
				}
				p++
			}
			if depth != 0 {
				continue // unterminated reference
			}
			end = p
		} else {
			continue // malformed
		}

		i = end - 1 // resume scanning after this reference (loop will i++)

		if ignored {
			continue
		}

		name := yaml[nameStart:nameEnd]

		desc := ""
		if nl := strings.IndexByte(yaml[end:], '\n'); nl >= 0 {
			desc = restOfLineComment(yaml[end : end+nl])
		} else {
			desc = restOfLineComment(yaml[end:])
		}

		if _, exists := seen[name]; exists {
			continue // first occurrence wins
		}
		seen[name] = &Var{Name: name, Default: def, HasDefault: hasDefault, Description: desc}
		order = append(order, name)
	}

	result := make([]Var, 0, len(order))
	for _, name := range order {
		result = append(result, *seen[name])
	}
	sort.Slice(result, func(a, b int) bool { return result[a].Name < result[b].Name })
	return result
}

func restOfLineComment(line string) string {
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		return strings.TrimSpace(line[idx+1:])
	}
	return ""
}
