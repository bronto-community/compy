//go:build darwin

package tray

import (
	"reflect"
	"testing"

	"github.com/bronto-io/compy/internal/app"
	"github.com/bronto-io/compy/internal/cfgstore"
)

func TestAssignSlots(t *testing.T) {
	cases := []struct {
		name         string
		configs      []string
		active       string
		slots        int
		wantInline   []string
		wantOverflow []string
	}{
		{"fits", []string{"a", "b"}, "a", 4, []string{"a", "b"}, nil},
		{"exact", []string{"a", "b"}, "", 2, []string{"a", "b"}, nil},
		{"overflow", []string{"a", "b", "c", "d"}, "", 2, []string{"a", "b"}, []string{"c", "d"}},
		{"active promoted from overflow", []string{"a", "b", "c", "d"}, "d", 2, []string{"a", "d"}, []string{"b", "c"}},
		{"active already inline unchanged", []string{"a", "b", "c"}, "a", 2, []string{"a", "b"}, []string{"c"}},
		{"empty", nil, "", 4, nil, nil},
	}
	for _, c := range cases {
		inline, overflow := assignSlots(c.configs, c.active, c.slots)
		if !reflect.DeepEqual(inline, c.wantInline) || !reflect.DeepEqual(overflow, c.wantOverflow) {
			t.Errorf("%s: got inline=%v overflow=%v, want %v / %v", c.name, inline, overflow, c.wantInline, c.wantOverflow)
		}
	}
}

func TestStatusLines(t *testing.T) {
	cases := []struct {
		name        string
		st          app.Status
		errs, warns int
		wantLine1   string
		wantLine2   string
	}{
		{
			name:      "no configuration",
			st:        app.Status{Running: true, GRPCPort: 14317, HTTPPort: 14318},
			wantLine1: "no configuration",
			wantLine2: "grpc :14317 · http :14318",
		},
		{
			name:      "running with set",
			st:        app.Status{Running: true, Config: "prod", Set: "eu", GRPCPort: 14317, HTTPPort: 14318},
			wantLine1: "running — prod (eu)",
			wantLine2: "grpc :14317 · http :14318",
		},
		{
			name:      "running without set omits parens",
			st:        app.Status{Running: true, Config: "prod", GRPCPort: 14317, HTTPPort: 14318},
			wantLine1: "running — prod",
		},
		{
			name:      "stopped",
			st:        app.Status{Running: false, Config: "prod", GRPCPort: 14317, HTTPPort: 14318},
			wantLine1: "stopped — prod",
		},
		{
			name:      "errors and warnings appended",
			st:        app.Status{Running: true, Config: "prod", GRPCPort: 14317, HTTPPort: 14318},
			errs:      2,
			warns:     1,
			wantLine1: "running — prod",
			wantLine2: "grpc :14317 · http :14318 · 2 err · 1 warn",
		},
		{
			name:      "zero errors/warnings omit the tail",
			st:        app.Status{Running: true, Config: "prod", GRPCPort: 14317, HTTPPort: 14318},
			errs:      0,
			warns:     0,
			wantLine1: "running — prod",
			wantLine2: "grpc :14317 · http :14318",
		},
	}
	for _, c := range cases {
		if c.wantLine2 == "" {
			c.wantLine2 = "grpc :14317 · http :14318"
		}
		line1, line2 := statusLines(c.st, c.errs, c.warns)
		if line1 != c.wantLine1 || line2 != c.wantLine2 {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, line1, line2, c.wantLine1, c.wantLine2)
		}
	}
}

func TestActiveVariableSets(t *testing.T) {
	configs := []cfgstore.Info{
		{Name: "solo", Meta: cfgstore.Meta{VariableSets: map[string]map[string]string{"default": {}}, ActiveSet: "default"}},
		{Name: "multi", Meta: cfgstore.Meta{
			VariableSets: map[string]map[string]string{"eu": {}, "default": {}, "us": {}},
			ActiveSet:    "us",
		}},
	}
	cases := []struct {
		name      string
		active    string
		wantNames []string
		wantSet   string
		wantShow  bool
	}{
		{"no active config", "", nil, "", false},
		{"unknown active config", "ghost", nil, "", false},
		{"single set hidden", "solo", nil, "", false},
		{"multi set shown sorted", "multi", []string{"default", "eu", "us"}, "us", true},
	}
	for _, c := range cases {
		names, set, show := activeVariableSets(configs, c.active)
		if show != c.wantShow || set != c.wantSet || !reflect.DeepEqual(names, c.wantNames) {
			t.Errorf("%s: got names=%v set=%q show=%v, want names=%v set=%q show=%v",
				c.name, names, set, show, c.wantNames, c.wantSet, c.wantShow)
		}
	}
}
