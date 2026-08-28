package version

import "testing"

func TestRender(t *testing.T) {
	cases := []struct {
		name              string
		release, revision string
		modified          bool
		want              string
	}{
		{"release wins", "0.1.0", "787da79a1b2c34567890abcd", true, "0.1.0"},
		{"dev short revision", "", "787da79a1b2c34567890abcd", false, "dev · 787da79a1b2c"},
		{"dev dirty", "", "787da79a1b2c34567890abcd", true, "dev · 787da79a1b2c+dirty"},
		{"short revision kept as-is", "", "abc123", false, "dev · abc123"},
		{"nothing known", "", "", false, "unknown"},
		{"modified without revision still unknown", "", "", true, "unknown"},
	}
	for _, c := range cases {
		if got := render(c.release, c.revision, c.modified); got != c.want {
			t.Errorf("%s: render(%q, %q, %v) = %q, want %q", c.name, c.release, c.revision, c.modified, got, c.want)
		}
	}
}

// String must never panic and never be empty, whatever the build.
func TestStringNonEmpty(t *testing.T) {
	if String() == "" {
		t.Fatal("String() is empty")
	}
}
