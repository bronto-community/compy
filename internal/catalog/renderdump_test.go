package catalog

import (
	"encoding/json"
	"os"
	"testing"
)

// TestRenderDump renders knobs from RENDER_KNOBS to RENDER_OUT — the
// sandbox drive's hook, a no-op in normal runs.
func TestRenderDump(t *testing.T) {
	kf, of := os.Getenv("RENDER_KNOBS"), os.Getenv("RENDER_OUT")
	if kf == "" || of == "" {
		t.Skip("RENDER_KNOBS/RENDER_OUT not set")
	}
	data, err := os.ReadFile(kf)
	if err != nil {
		t.Fatal(err)
	}
	var knobs map[string]any
	if err := json.Unmarshal(data, &knobs); err != nil {
		t.Fatal(err)
	}
	tmpl := get(t, "otlp-forward")
	out, err := tmpl.Render(knobs, os.Getenv("RENDER_STORAGE"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(of, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}
