package webui

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// openapiSpec is the minimal shape TestOpenAPIDriftAgainstRoutes needs out of
// api/openapi.json: the path table and, per path, which HTTP methods it
// defines (as raw operation objects — schemas are not inspected here).
type openapiSpec struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

// paramSeg normalizes a mux/OpenAPI path template's "{whatever}" segments to
// a bare "{}" so route-table param names ("{name}") and any differently
// named spec params still compare as the same shape.
var paramSeg = regexp.MustCompile(`\{[^{}]*\}`)

func normalizePattern(p string) string {
	return paramSeg.ReplaceAllString(p, "{}")
}

// endpointSet returns "METHOD normalized-path" entries.
func specEndpointSet(spec openapiSpec) map[string]bool {
	set := map[string]bool{}
	for path, methods := range spec.Paths {
		for method := range methods {
			set[strings.ToUpper(method)+" "+normalizePattern(path)] = true
		}
	}
	return set
}

func routeEndpointSet(rts []route) map[string]bool {
	set := map[string]bool{}
	for _, rt := range rts {
		set[strings.ToUpper(rt.Method)+" "+normalizePattern(rt.Pattern)] = true
	}
	return set
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestOpenAPIDriftAgainstRoutes guards api/openapi.json against ever
// drifting from the route table: every route must be documented and every
// documented path+method must be routed. Add an endpoint = update both, or
// this fails with the exact offending entries.
func TestOpenAPIDriftAgainstRoutes(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.json")
	if err != nil {
		t.Fatalf("read api/openapi.json: %v", err)
	}
	var spec openapiSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse api/openapi.json: %v", err)
	}

	specSet := specEndpointSet(spec)
	routeSet := routeEndpointSet(routes())

	missingFromSpec := diff(routeSet, specSet) // routed but not documented
	extraInSpec := diff(specSet, routeSet)     // documented but not routed

	if len(missingFromSpec) > 0 || len(extraInSpec) > 0 {
		t.Fatalf("openapi.json drifted from routes():\n  in routes() but missing from api/openapi.json: %v\n  in api/openapi.json but missing from routes(): %v",
			missingFromSpec, extraInSpec)
	}
}
