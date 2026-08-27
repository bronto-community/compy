package app_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bronto-community/compy/internal/app"
	"github.com/bronto-community/compy/internal/webui"
)

// TestWebUIWiring exercises app.WebUIAPI() wired for real into
// webui.Handler — not webui's own fakeAPI() closures, which only prove the
// route table dispatches correctly, never that App's methods are actually
// reachable through it. It reuses the app_test helpers (setup, fakeDistro)
// for a temp COMPY_HOME and a stubbed launchd.Exec, no network involved
// beyond the in-process httptest server.
func TestWebUIWiring(t *testing.T) {
	setup(t, "state = running")
	fakeDistro(t, "exit 0")

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(webui.Handler(a.WebUIAPI()))
	defer srv.Close()

	// GET /api/status
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/status = %d, want 200", resp.StatusCode)
	}
	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, ok := status["running"]; !ok {
		t.Fatalf("status body = %v, missing \"running\"", status)
	}

	// GET /api/configs: the shipped "debug" config must be there.
	resp, err = http.Get(srv.URL + "/api/configs")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/configs = %d, want 200", resp.StatusCode)
	}
	var configs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&configs); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	found := false
	for _, c := range configs {
		if c["name"] == "debug" {
			found = true
		}
	}
	if !found {
		t.Fatalf("configs = %v, missing shipped debug config", configs)
	}

	// PUT /api/configs/debug/presets/test, then GET it back and check the
	// round-trip.
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/configs/debug/presets/test",
		strings.NewReader(`{"values":{"FOO":"bar"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT presets/test = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/api/configs/debug")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/configs/debug = %d, want 200", resp.StatusCode)
	}
	var detail map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	info, _ := detail["info"].(map[string]any)
	meta, _ := info["meta"].(map[string]any)
	presets, _ := meta["presets"].(map[string]any)
	testPreset, _ := presets["test"].(map[string]any)
	if testPreset["FOO"] != "bar" {
		t.Fatalf("config detail = %v, want preset \"test\" with FOO=bar", detail)
	}

	// POST /api/configs/{name}/validate against the real fake-distro binary.
	resp, err = http.Post(srv.URL+"/api/configs/debug/validate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST validate = %d, want 200", resp.StatusCode)
	}
	var vresult map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&vresult); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !vresult["ok"] {
		t.Fatalf("validate result = %v, want ok:true", vresult)
	}

	// PUT a preset, POST its rename, then GET config and assert the renamed
	// preset is present with its values intact (and the old name is gone).
	req, err = http.NewRequest(http.MethodPut, srv.URL+"/api/configs/debug/presets/stage",
		strings.NewReader(`{"values":{"HOST":"stage.example.com"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT presets/stage = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Post(srv.URL+"/api/configs/debug/presets/stage/rename", "application/json",
		strings.NewReader(`{"to":"staging"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST presets/stage/rename = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/api/configs/debug")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/configs/debug = %d, want 200", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	info, _ = detail["info"].(map[string]any)
	meta, _ = info["meta"].(map[string]any)
	presets, _ = meta["presets"].(map[string]any)
	if _, stillThere := presets["stage"]; stillThere {
		t.Fatalf("config detail = %v, \"stage\" should be gone after rename", detail)
	}
	staging, _ := presets["staging"].(map[string]any)
	if staging["HOST"] != "stage.example.com" {
		t.Fatalf("config detail = %v, want preset \"staging\" with HOST intact after rename", detail)
	}
}

// Deleting a configuration's last preset is refused end-to-end — 400 with
// the keeps-at-least-one message. This closes the old API/UI divergence:
// the API allowed deleting the last preset while the UI refused it.
func TestWebUIDeleteLastPresetRefused(t *testing.T) {
	setup(t, "")
	fakeDistro(t, "exit 0")

	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(webui.Handler(a.WebUIAPI()))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/configs/debug/presets/default", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("DELETE last preset = %d, want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "at least one preset") {
		t.Fatalf("error = %q, want the keeps-at-least-one message", msg)
	}
}
