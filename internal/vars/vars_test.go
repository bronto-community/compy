package vars_test

import (
	"reflect"
	"testing"

	"github.com/bronto-community/compy/internal/vars"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want []vars.Var
	}{
		{"bare", "endpoint: ${OTLP_ENDPOINT}", []vars.Var{{Name: "OTLP_ENDPOINT"}}},
		{"default", "endpoint: ${EP:-http://localhost:4318}", []vars.Var{{Name: "EP", Default: "http://localhost:4318", HasDefault: true}}},
		{"env form + desc", "key: ${env:API_KEY}  # vendor API key", []vars.Var{{Name: "API_KEY", Description: "vendor API key"}}},
		{"env with default", "x: ${env:A:-b}", []vars.Var{{Name: "A", Default: "b", HasDefault: true}}},
		{"other schemes ignored", "a: ${file:/etc/x}\nb: ${secretsmanager:arn}", nil},
		{"dedup first wins", "a: ${X:-1}  # first\nb: ${X:-2}  # second", []vars.Var{{Name: "X", Default: "1", HasDefault: true, Description: "first"}}},
		{"sorted", "a: ${B}\nb: ${A}", []vars.Var{{Name: "A"}, {Name: "B"}}},
		{"nested default kept verbatim", "a: ${E:-${F}}", []vars.Var{{Name: "E", Default: "${F}", HasDefault: true}}}, // ponytail: nested refs not recursed
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := vars.Parse(c.yaml)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Parse(%q) = %#v, want %#v", c.yaml, got, c.want)
			}
		})
	}
}
