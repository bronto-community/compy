package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI builds an API with no-op closures, overridable per test.
func fakeAPI() API {
	return API{
		Status:   func() (map[string]any, error) { return map[string]any{"running": true}, nil },
		Backends: func() ([]map[string]any, error) { return []map[string]any{}, nil },
		AddBackend: func(name, kind, endpoint, apiKey string) error {
			return nil
		},
		RemoveBackend: func(name string) error { return nil },
		SetEnabled:    func(name string, enabled bool) error { return nil },
		Apply:         func() error { return nil },
		Rollback:      func() error { return nil },
		ReadFragment:  func(name string) (string, error) { return "", nil },
		WriteFragment: func(name, content string) error { return nil },
		SetRawMode:    func(on bool) error { return nil },
		ReadRaw:       func() (string, error) { return "", nil },
		WriteRaw:      func(content string) error { return nil },
		LastError:     func() (string, error) { return "", nil },
	}
}

func TestStatusRoute(t *testing.T) {
	api := fakeAPI()
	api.Status = func() (map[string]any, error) {
		return map[string]any{"running": true, "distro": "otel-contrib"}, nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["distro"] != "otel-contrib" {
		t.Fatalf("body = %v, want distro=otel-contrib", body)
	}
}

func TestAddBackendParsesJSON(t *testing.T) {
	api := fakeAPI()
	var gotName, gotKind, gotEndpoint, gotKey string
	api.AddBackend = func(name, kind, endpoint, apiKey string) error {
		gotName, gotKind, gotEndpoint, gotKey = name, kind, endpoint, apiKey
		return nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	payload := `{"name":"my-backend","kind":"otlp-grpc","endpoint":"localhost:4317","api_key":"secret"}`
	resp, err := http.Post(srv.URL+"/api/backends", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotName != "my-backend" || gotKind != "otlp-grpc" || gotEndpoint != "localhost:4317" || gotKey != "secret" {
		t.Fatalf("AddBackend got (%q,%q,%q,%q)", gotName, gotKind, gotEndpoint, gotKey)
	}
}

func TestEnableTogglesAndAppliesViaClosure(t *testing.T) {
	api := fakeAPI()
	var gotName string
	var gotEnabled bool
	applyCalled := false
	api.SetEnabled = func(name string, enabled bool) error {
		gotName, gotEnabled = name, enabled
		applyCalled = true // SetEnabled implies apply per spec; closure itself handles the apply
		return nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	payload := `{"enabled":true}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/backends/my-backend/enabled", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotName != "my-backend" || !gotEnabled || !applyCalled {
		t.Fatalf("SetEnabled got (%q,%v), applyCalled=%v", gotName, gotEnabled, applyCalled)
	}
}

func TestHostCheckRejectsEvilHost(t *testing.T) {
	api := fakeAPI()
	handler := Handler(api)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "evil.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestErrorPassthrough(t *testing.T) {
	api := fakeAPI()
	api.Apply = func() error { return errWithMessage("collector said no") }
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/apply", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "collector said no" {
		t.Fatalf("body = %v, want error=\"collector said no\"", body)
	}
}

func TestRawModeToggle(t *testing.T) {
	api := fakeAPI()
	var gotOn bool
	var writtenRaw, readRaw string
	api.SetRawMode = func(on bool) error {
		gotOn = on
		return nil
	}
	api.WriteRaw = func(content string) error {
		writtenRaw = content
		return nil
	}
	api.ReadRaw = func() (string, error) { return readRaw, nil }
	// All closures are wired before the server starts: Handler(api) closes
	// over a copy of api, so later reassignment of api's fields would not
	// reach the running handlers.
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	payload := `{"on":true}`
	resp, err := http.Post(srv.URL+"/api/raw-mode", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !gotOn {
		t.Fatalf("SetRawMode got on=%v, want true", gotOn)
	}

	putReq, err := http.NewRequest(http.MethodPut, srv.URL+"/api/raw", bytes.NewBufferString("receivers: {}"))
	if err != nil {
		t.Fatal(err)
	}
	putResp, err := srv.Client().Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/raw status = %d, want 200", putResp.StatusCode)
	}
	if writtenRaw != "receivers: {}" {
		t.Fatalf("WriteRaw got %q", writtenRaw)
	}

	readRaw = "receivers: {}"
	getResp, err := http.Get(srv.URL + "/api/raw")
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(getResp.Body)
	if buf.String() != "receivers: {}" {
		t.Fatalf("GET /api/raw body = %q", buf.String())
	}
}

// errWithMessage returns an error whose Error() is exactly msg, for testing
// verbatim error passthrough.
type simpleErr string

func (e simpleErr) Error() string { return string(e) }

func errWithMessage(msg string) error { return simpleErr(msg) }
