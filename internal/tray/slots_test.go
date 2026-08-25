//go:build darwin

package tray

import (
	"reflect"
	"testing"
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
