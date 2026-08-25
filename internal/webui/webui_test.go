package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeAPI builds an API with no-op closures, overridable per test.
func fakeAPI() API {
	return API{
		Status:   func() (map[string]any, error) { return map[string]any{"running": true}, nil },
		Configs:  func() (any, error) { return []map[string]any{}, nil },
		Activate: func(name string) error { return nil },
		Log:      func() (string, error) { return "", nil },
	}
}

func TestConfigsRoutes(t *testing.T) {
	api := fakeAPI()
	api.Configs = func() (any, error) {
		return []map[string]any{{"name": "debug", "provenance": "shipped"}}, nil
	}
	var activated string
	api.Activate = func(name string) error { activated = name; return nil }
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/configs")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET configs: %v %v", resp.StatusCode, err)
	}
	var list []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list) != 1 || list[0]["name"] != "debug" {
		t.Fatalf("configs body wrong: %v %v", list, err)
	}

	resp, err = http.Post(srv.URL+"/api/configs/otlp/activate", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("POST activate: %v %v", resp.StatusCode, err)
	}
	if activated != "otlp" {
		t.Fatalf("Activate got %q, want otlp", activated)
	}
}

func TestLogRoute(t *testing.T) {
	api := fakeAPI()
	api.Log = func() (string, error) { return "boom\n", nil }
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/log")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["log"] != "boom\n" {
		t.Fatalf("body = %v, want the log tail", body)
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

func TestCSRFRejectsCrossOriginOrigin(t *testing.T) {
	api := fakeAPI()
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/configs/debug/activate", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.com")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestCSRFRejectsCrossSiteSecFetchSite(t *testing.T) {
	api := fakeAPI()
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/configs/debug/activate", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestCSRFAllowsLocalhostOriginWithAnyPort(t *testing.T) {
	api := fakeAPI()
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/configs/debug/activate", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:9999")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestCSRFAllowsRequestsWithoutOriginOrSecFetchSite(t *testing.T) {
	api := fakeAPI()
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/configs/debug/activate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestUnimplementedRouteReturns501(t *testing.T) {
	api := fakeAPI()
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/distros")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "not implemented" {
		t.Fatalf("body = %v, want error=\"not implemented\"", body)
	}
}

func TestErrorPassthrough(t *testing.T) {
	api := fakeAPI()
	api.Activate = func(name string) error { return errWithMessage("collector said no") }
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/configs/debug/activate", "application/json", nil)
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

// simpleErr is an error whose Error() is exactly its text, for testing
// verbatim error passthrough.
type simpleErr string

func (e simpleErr) Error() string { return string(e) }

func errWithMessage(msg string) error { return simpleErr(msg) }
