package webui

import (
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
		Configs:  func() (any, error) { return []map[string]any{}, nil },
		Activate: func(name, set string) error { return nil },
		Log:      func(lines int) (string, error) { return "", nil },

		Env:      func() (map[string]string, string, error) { return map[string]string{}, "", nil },
		SetOSEnv: func(on bool) error { return nil },

		GetSettings: func() (map[string]any, error) { return map[string]any{}, nil },
		PutSettings: func(grpcPort, httpPort *int, menuDistroSwap *bool) error { return nil },

		Apply:    func() error { return nil },
		Rollback: func() error { return nil },
		Validate: func() error { return nil },

		CreateConfig:   func(name, yaml string) error { return nil },
		CreateFromURL:  func(name, url string) error { return nil },
		GetConfig:      func(name string) (any, error) { return map[string]any{}, nil },
		PutConfigYAML:  func(name, yaml string) error { return nil },
		PutConfigMeta:  func(name string, distro, remoteURL *string) error { return nil },
		DeleteConfig:   func(name string) error { return nil },
		CopyConfig:     func(src, dst string) error { return nil },
		ValidateConfig: func(name string) error { return nil },
		Sync:           func(name string) error { return nil },
		Resync:         func(name string) error { return nil },
		SyncAll:        func() ([]string, error) { return nil, nil },

		PutSet:    func(name, set string, values map[string]string) error { return nil },
		DeleteSet: func(name, set string) error { return nil },
		UseSet:    func(name, set string) error { return nil },
		RenameSet: func(name, from, to string) error { return nil },

		Distros:       func() (any, error) { return []map[string]any{}, nil },
		AddDistro:     func(name, path string) (string, error) { return "", nil },
		SetDistroPath: func(name, path string) (string, error) { return "", nil },
		RemoveDistro:  func(name string) (bool, error) { return false, nil },
		UseDistro:     func(name string) error { return nil },
		FetchDistro:   func(name string) error { return nil },
	}
}

func TestConfigsRoutes(t *testing.T) {
	api := fakeAPI()
	api.Configs = func() (any, error) {
		return []map[string]any{{"name": "debug", "provenance": "shipped"}}, nil
	}
	var activated string
	api.Activate = func(name, set string) error { activated = name; return nil }
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
	var gotLines int
	api.Log = func(lines int) (string, error) { gotLines = lines; return "boom\n", nil }
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
	// no-param behavior must stay 50 (the stopgap page relies on it).
	if gotLines != 50 {
		t.Fatalf("Log got lines=%d, want the default 50", gotLines)
	}
}

// TestLogRouteLines covers the ?lines=N query param: a valid value passes
// through, an over-cap value clamps to 2000, and junk is a 400.
func TestLogRouteLines(t *testing.T) {
	api := fakeAPI()
	var gotLines int
	api.Log = func(lines int) (string, error) { gotLines = lines; return "", nil }
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/log?lines=10")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gotLines != 10 {
		t.Fatalf("lines=10: status=%d gotLines=%d, want 200/10", resp.StatusCode, gotLines)
	}

	resp, err = http.Get(srv.URL + "/api/log?lines=999999")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gotLines != 2000 {
		t.Fatalf("lines=999999: status=%d gotLines=%d, want 200/2000 (clamped)", resp.StatusCode, gotLines)
	}

	for _, junk := range []string{"abc", "-5", "0"} {
		resp, err = http.Get(srv.URL + "/api/log?lines=" + junk)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("lines=%q: status=%d, want 400", junk, resp.StatusCode)
		}
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

// call invokes handler h directly with a body and the path values the mux
// would have extracted, bypassing the mux itself. That lets these tests
// exercise edge cases — like an empty {set} segment — that Go's ServeMux
// would never actually route through (it never matches an empty wildcard
// segment), while still testing the handler's own defense of them.
func call(h http.HandlerFunc, method, body string, pathValues map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/", nil)
	} else {
		r = httptest.NewRequest(method, "/", strings.NewReader(body))
	}
	for k, v := range pathValues {
		r.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

func TestPutSettingsRoute(t *testing.T) {
	api := fakeAPI()
	var gotGRPC, gotHTTP *int
	var gotSwap *bool
	api.PutSettings = func(grpcPort, httpPort *int, menuDistroSwap *bool) error {
		gotGRPC, gotHTTP, gotSwap = grpcPort, httpPort, menuDistroSwap
		return nil
	}
	api.GetSettings = func() (map[string]any, error) {
		return map[string]any{"grpc_port": 5000, "http_port": 14318, "menu_distro_swap": true}, nil
	}

	rec := call(handlePutSettings(api), http.MethodPut, `{"grpc_port":5000}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotGRPC == nil || *gotGRPC != 5000 || gotHTTP != nil || gotSwap != nil {
		t.Fatalf("PutSettings got grpc=%v http=%v swap=%v, want grpc=5000 http=nil swap=nil", gotGRPC, gotHTTP, gotSwap)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["grpc_port"] != float64(5000) {
		t.Fatalf("response = %v, want the resulting settings", body)
	}

	rec = call(handlePutSettings(api), http.MethodPut, `not json`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.PutSettings = func(grpcPort, httpPort *int, menuDistroSwap *bool) error {
		return errWithMessage("port out of range")
	}
	rec = call(handlePutSettings(api), http.MethodPut, `{"grpc_port":0}`, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

func TestCreateConfigRoute(t *testing.T) {
	api := fakeAPI()
	var gotName, gotYAML string
	api.CreateConfig = func(name, yaml string) error {
		gotName, gotYAML = name, yaml
		return nil
	}

	rec := call(handleCreateConfig(api), http.MethodPost, `{"name":"mine","yaml":"receivers: {}"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "mine" || gotYAML != "receivers: {}" {
		t.Fatalf("CreateConfig got name=%q yaml=%q", gotName, gotYAML)
	}

	rec = call(handleCreateConfig(api), http.MethodPost, `not json`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.CreateConfig = func(name, yaml string) error { return errWithMessage("config exists") }
	rec = call(handleCreateConfig(api), http.MethodPost, `{"name":"mine"}`, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

// TestPutConfigYAMLOversizedBody guards the http.MaxBytesReader cap Handler
// applies to every route: a body over the 5MB limit must fail with 413, not
// exhaust memory reading an unbounded body. Goes through Handler (not the
// bare handler via call()) since the cap is now applied by Handler's route
// loop, not the handler itself.
func TestPutConfigYAMLOversizedBody(t *testing.T) {
	api := fakeAPI()
	api.PutConfigYAML = func(name, yaml string) error {
		t.Fatal("PutConfigYAML should not be called for an oversized body")
		return nil
	}

	body := strings.Repeat("a", maxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPut, "/api/configs/debug/yaml", strings.NewReader(body))
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	Handler(api).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || got["error"] == "" {
		t.Fatalf("body = %v, %v, want {\"error\":...}", got, err)
	}
}

func TestCopyConfigRoute(t *testing.T) {
	api := fakeAPI()
	var gotSrc, gotDst string
	api.CopyConfig = func(src, dst string) error {
		gotSrc, gotDst = src, dst
		return nil
	}
	pv := map[string]string{"name": "debug"}

	rec := call(handleCopyConfig(api), http.MethodPost, `{"dst":"debug2"}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotSrc != "debug" || gotDst != "debug2" {
		t.Fatalf("CopyConfig got src=%q dst=%q", gotSrc, gotDst)
	}

	rec = call(handleCopyConfig(api), http.MethodPost, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.CopyConfig = func(src, dst string) error { return errWithMessage("dst exists") }
	rec = call(handleCopyConfig(api), http.MethodPost, `{"dst":"debug2"}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

// TestValidateConfigRoute covers POST /api/configs/{name}/validate: 200
// {"ok":true} on success, 500 with the closure's error (the collector's own
// output) verbatim on failure.
func TestValidateConfigRoute(t *testing.T) {
	api := fakeAPI()
	var gotName string
	api.ValidateConfig = func(name string) error {
		gotName = name
		return nil
	}

	rec := call(handleValidateConfig(api), http.MethodPost, "", map[string]string{"name": "debug"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" {
		t.Fatalf("ValidateConfig got name=%q, want debug", gotName)
	}
	var ok map[string]bool
	if err := json.NewDecoder(rec.Body).Decode(&ok); err != nil || !ok["ok"] {
		t.Fatalf("body = %v, %v, want ok:true", ok, err)
	}

	api.ValidateConfig = func(name string) error {
		return errWithMessage("error decoding 'exporters': unknown type")
	}
	rec = call(handleValidateConfig(api), http.MethodPost, "", map[string]string{"name": "debug"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var errBody map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil || errBody["error"] != "error decoding 'exporters': unknown type" {
		t.Fatalf("body = %v, %v, want the collector's output verbatim", errBody, err)
	}
}

func TestPutSetRoute(t *testing.T) {
	api := fakeAPI()
	var gotName, gotSet string
	var gotValues map[string]string
	api.PutSet = func(name, set string, values map[string]string) error {
		gotName, gotSet, gotValues = name, set, values
		return nil
	}
	pv := map[string]string{"name": "debug", "set": "prod"}

	rec := call(handlePutSet(api), http.MethodPut, `{"values":{"k":"v"}}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" || gotSet != "prod" || gotValues["k"] != "v" {
		t.Fatalf("PutSet got name=%q set=%q values=%v", gotName, gotSet, gotValues)
	}

	// absent "values": {} not nil.
	gotValues = nil
	rec = call(handlePutSet(api), http.MethodPut, `{}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotValues == nil || len(gotValues) != 0 {
		t.Fatalf("PutSet got values=%v, want empty non-nil map", gotValues)
	}

	// null "values": {} not nil.
	gotValues = nil
	rec = call(handlePutSet(api), http.MethodPut, `{"values":null}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotValues == nil || len(gotValues) != 0 {
		t.Fatalf("PutSet got values=%v, want empty non-nil map", gotValues)
	}

	// empty set name: 400.
	rec = call(handlePutSet(api), http.MethodPut, `{"values":{}}`, map[string]string{"name": "debug", "set": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty set name status = %d, want 400", rec.Code)
	}

	rec = call(handlePutSet(api), http.MethodPut, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.PutSet = func(name, set string, values map[string]string) error { return errWithMessage("no such config") }
	rec = call(handlePutSet(api), http.MethodPut, `{"values":{}}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

func TestDeleteSetAndUseSetRejectEmptySetName(t *testing.T) {
	api := fakeAPI()
	pv := map[string]string{"name": "debug", "set": ""}

	rec := call(handleDeleteSet(api), http.MethodDelete, "", pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("DeleteSet empty set status = %d, want 400", rec.Code)
	}

	rec = call(handleUseSet(api), http.MethodPost, "", pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("UseSet empty set status = %d, want 400", rec.Code)
	}
}

func TestUpdateMetaRoute(t *testing.T) {
	api := fakeAPI()
	var gotName string
	var gotDistro, gotRemoteURL *string
	api.PutConfigMeta = func(name string, distro, remoteURL *string) error {
		gotName, gotDistro, gotRemoteURL = name, distro, remoteURL
		return nil
	}
	pv := map[string]string{"name": "debug"}

	rec := call(handlePutConfigMeta(api), http.MethodPut, `{"distro":"core"}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" || gotDistro == nil || *gotDistro != "core" || gotRemoteURL != nil {
		t.Fatalf("PutConfigMeta got name=%q distro=%v remoteURL=%v", gotName, gotDistro, gotRemoteURL)
	}

	rec = call(handlePutConfigMeta(api), http.MethodPut, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.PutConfigMeta = func(name string, distro, remoteURL *string) error { return errWithMessage("no such distro") }
	rec = call(handlePutConfigMeta(api), http.MethodPut, `{"distro":"bogus"}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

func TestAddDistroRoute(t *testing.T) {
	api := fakeAPI()
	var gotName, gotPath string
	api.AddDistro = func(name, path string) (string, error) {
		gotName, gotPath = name, path
		return `"otlp" is a shipped distro definition; this path overrides it`, nil
	}

	rec := call(handleAddDistro(api), http.MethodPost, `{"name":"otlp","path":"/usr/bin/otelcol"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "otlp" || gotPath != "/usr/bin/otelcol" {
		t.Fatalf("AddDistro got name=%q path=%q", gotName, gotPath)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["warning"] == "" {
		t.Fatalf("body = %v, want a non-empty warning field", body)
	}

	rec = call(handleAddDistro(api), http.MethodPost, `not json`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.AddDistro = func(name, path string) (string, error) { return "", errWithMessage("not executable") }
	rec = call(handleAddDistro(api), http.MethodPost, `{"name":"x","path":"/nope"}`, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}

	// no override: warning is "".
	api.AddDistro = func(name, path string) (string, error) { return "", nil }
	rec = call(handleAddDistro(api), http.MethodPost, `{"name":"brand-new","path":"/usr/bin/otelcol"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body = nil
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if w, ok := body["warning"]; !ok || w != "" {
		t.Fatalf("body = %v, want warning=\"\"", body)
	}
}

func TestRenameSetRoute(t *testing.T) {
	api := fakeAPI()
	var gotName, gotFrom, gotTo string
	api.RenameSet = func(name, from, to string) error {
		gotName, gotFrom, gotTo = name, from, to
		return nil
	}
	pv := map[string]string{"name": "debug", "set": "prod"}

	rec := call(handleRenameSet(api), http.MethodPost, `{"to":"production"}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" || gotFrom != "prod" || gotTo != "production" {
		t.Fatalf("RenameSet got name=%q from=%q to=%q", gotName, gotFrom, gotTo)
	}

	rec = call(handleRenameSet(api), http.MethodPost, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	rec = call(handleRenameSet(api), http.MethodPost, `{"to":"x"}`, map[string]string{"name": "debug", "set": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty set name status = %d, want 400", rec.Code)
	}

	api.RenameSet = func(name, from, to string) error { return errWithMessage("already exists") }
	rec = call(handleRenameSet(api), http.MethodPost, `{"to":"production"}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

func TestSetDistroPathRoute(t *testing.T) {
	api := fakeAPI()
	var gotName, gotPath string
	api.SetDistroPath = func(name, path string) (string, error) {
		gotName, gotPath = name, path
		return `"core" is a shipped distro definition; this path overrides it`, nil
	}
	pv := map[string]string{"name": "core"}

	rec := call(handleSetDistroPath(api), http.MethodPut, `{"path":"/usr/bin/otelcol"}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "core" || gotPath != "/usr/bin/otelcol" {
		t.Fatalf("SetDistroPath got name=%q path=%q", gotName, gotPath)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["warning"] == "" {
		t.Fatalf("body = %v, want a non-empty warning field", body)
	}

	rec = call(handleSetDistroPath(api), http.MethodPut, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.SetDistroPath = func(name, path string) (string, error) { return "", errWithMessage("not executable") }
	rec = call(handleSetDistroPath(api), http.MethodPut, `{"path":"/nope"}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

// TestRemoveDistroRoute covers 200 {"reverted":...} on success and the
// webui.BadRequest-marked 400 vs the default 500 status split.
func TestRemoveDistroRoute(t *testing.T) {
	api := fakeAPI()
	var gotName string
	api.RemoveDistro = func(name string) (bool, error) {
		gotName = name
		return true, nil
	}
	pv := map[string]string{"name": "core"}

	rec := call(handleRemoveDistro(api), http.MethodDelete, "", pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "core" {
		t.Fatalf("RemoveDistro got name=%q, want core", gotName)
	}
	var body map[string]bool
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || !body["reverted"] {
		t.Fatalf("body = %v, %v, want reverted:true", body, err)
	}

	api.RemoveDistro = func(name string) (bool, error) {
		return false, BadRequest(errWithMessage("no user distro entry named \"x\""))
	}
	rec = call(handleRemoveDistro(api), http.MethodDelete, "", pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("BadRequest-marked closure error status = %d, want 400", rec.Code)
	}

	api.RemoveDistro = func(name string) (bool, error) { return false, errWithMessage("disk error") }
	rec = call(handleRemoveDistro(api), http.MethodDelete, "", pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("plain closure error status = %d, want 500", rec.Code)
	}
}

func TestActivateWithSet(t *testing.T) {
	api := fakeAPI()
	var gotName, gotSet string
	api.Activate = func(name, set string) error {
		gotName, gotSet = name, set
		return nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/configs/debug/activate", "application/json", strings.NewReader(`{"set":"prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotName != "debug" || gotSet != "prod" {
		t.Fatalf("Activate got name=%q set=%q", gotName, gotSet)
	}
}

func TestActivateWithoutBodyKeepsWorking(t *testing.T) {
	api := fakeAPI()
	var gotName, gotSet string
	calledWithoutBody := false
	api.Activate = func(name, set string) error {
		gotName, gotSet = name, set
		calledWithoutBody = true
		return nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	// exactly what the stopgap index.html does: fetch(path, {method:"POST"}) — no body at all.
	resp, err := http.Post(srv.URL+"/api/configs/debug/activate", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !calledWithoutBody || gotName != "debug" || gotSet != "" {
		t.Fatalf("Activate got name=%q set=%q, want debug/\"\"", gotName, gotSet)
	}
}

func TestErrorPassthrough(t *testing.T) {
	api := fakeAPI()
	api.Activate = func(name, set string) error { return errWithMessage("collector said no") }
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
