package webui

import (
	"encoding/json"
	"fmt"
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
		Activate: func(name, preset string) error { return nil },
		Log:      func(lines int) (string, error) { return "", nil },

		Env:      func() (map[string]string, string, error) { return map[string]string{}, "", nil },
		SetOSEnv: func(on bool) error { return nil },

		GetSettings: func() (map[string]any, error) { return map[string]any{}, nil },
		PutSettings: func(grpcPort, httpPort *int, protocol *string) error { return nil },
		AdoptPorts:  func(grpcPort, httpPort *int) error { return nil },

		Health:       func() (any, error) { return map[string]any{"available": false}, nil },
		Apply:        func() error { return nil },
		Stop:         func() error { return nil },
		Start:        func() error { return nil },
		Validate:     func() error { return nil },
		FactoryReset: func() error { return nil },

		CreateConfig:            func(name, yaml string) error { return nil },
		CreateFromURL:           func(name, url string) error { return nil },
		GetConfig:               func(name string) (any, error) { return map[string]any{}, nil },
		PutConfigYAML:           func(name, yaml string) error { return nil },
		PutConfigYAMLNoValidate: func(name, yaml string) (bool, error) { return false, nil },
		PutConfigMeta:           func(name string, remoteURL *string) error { return nil },
		DeleteConfig:            func(name string) error { return nil },
		CopyConfig:              func(src, dst string) error { return nil },
		ValidateConfig:          func(name string) error { return nil },
		Sync:                    func(name string) error { return nil },
		Resync:                  func(name string) error { return nil },
		Reset:                   func(name string) error { return nil },
		RenameConfig:            func(from, to string) error { return nil },
		SyncAll:                 func() ([]string, error) { return nil, nil },

		PutPreset:    func(name, preset string, values map[string]string) error { return nil },
		DeletePreset: func(name, preset string) error { return nil },
		UsePreset:    func(name, preset string) error { return nil },
		RenamePreset: func(name, from, to string) error { return nil },

		Distros:          func() (any, error) { return []map[string]any{}, nil },
		AddDistro:        func(name, path string) (string, error) { return "", nil },
		SetDistroPath:    func(name, path string) (string, error) { return "", nil },
		RemoveDistro:     func(name string) (bool, error) { return false, nil },
		UseDistro:        func(name string) error { return nil },
		FetchDistro:      func(name string) error { return nil },
		DownloadProgress: func(name string) (any, error) { return map[string]any{"status": "idle", "pct": 0}, nil },

		CheckDistroUpdate: func(name string) (string, string, error) { return "0.135.0", "0.135.0", nil },
		UpdateDistro:      func(name string) (string, string, bool, error) { return "0.135.0", "0.135.0", false, nil },
	}
}

func TestConfigsRoutes(t *testing.T) {
	api := fakeAPI()
	api.Configs = func() (any, error) {
		return []map[string]any{{"name": "debug", "provenance": "shipped"}}, nil
	}
	var activated string
	api.Activate = func(name, preset string) error { activated = name; return nil }
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
	var gotProto *string
	api.PutSettings = func(grpcPort, httpPort *int, protocol *string) error {
		gotGRPC, gotHTTP, gotProto = grpcPort, httpPort, protocol
		return nil
	}
	api.GetSettings = func() (map[string]any, error) {
		return map[string]any{"grpc_port": 5000, "http_port": 14318}, nil
	}

	rec := call(handlePutSettings(api), http.MethodPut, `{"grpc_port":5000}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotGRPC == nil || *gotGRPC != 5000 || gotHTTP != nil || gotProto != nil {
		t.Fatalf("PutSettings got grpc=%v http=%v proto=%v, want grpc=5000 http=nil proto=nil", gotGRPC, gotHTTP, gotProto)
	}

	rec = call(handlePutSettings(api), http.MethodPut, `{"protocol":"grpc"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("protocol update status = %d, want 200", rec.Code)
	}
	if gotProto == nil || *gotProto != "grpc" || gotGRPC != nil || gotHTTP != nil {
		t.Fatalf("PutSettings got grpc=%v http=%v proto=%v, want only proto=grpc", gotGRPC, gotHTTP, gotProto)
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

	api.PutSettings = func(grpcPort, httpPort *int, protocol *string) error {
		return errWithMessage("port out of range")
	}
	rec = call(handlePutSettings(api), http.MethodPut, `{"grpc_port":0}`, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

// TestAdoptPortsRoute: an empty body classifies (nil, nil reaches the
// closure), an explicit body assigns, an ambiguity refusal is a 400 whose
// body is message-only, and success answers with the resulting settings.
func TestAdoptPortsRoute(t *testing.T) {
	api := fakeAPI()
	var gotGRPC, gotHTTP *int
	api.AdoptPorts = func(grpcPort, httpPort *int) error {
		gotGRPC, gotHTTP = grpcPort, httpPort
		return nil
	}
	api.GetSettings = func() (map[string]any, error) {
		return map[string]any{"grpc_port": 6000, "http_port": 6001}, nil
	}

	rec := call(handleAdoptPorts(api), http.MethodPost, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotGRPC != nil || gotHTTP != nil {
		t.Fatalf("empty body got grpc=%v http=%v, want nil/nil (classify)", gotGRPC, gotHTTP)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["http_port"] != float64(6001) {
		t.Fatalf("response = %v, want the resulting settings", body)
	}

	rec = call(handleAdoptPorts(api), http.MethodPost, `{"grpc_port":6000,"http_port":6001}`, nil)
	if rec.Code != http.StatusOK || gotGRPC == nil || *gotGRPC != 6000 || gotHTTP == nil || *gotHTTP != 6001 {
		t.Fatalf("explicit body: status %d, grpc=%v http=%v", rec.Code, gotGRPC, gotHTTP)
	}

	api.AdoptPorts = func(grpcPort, httpPort *int) error {
		return markBadRequest(errWithMessage("can't tell which of :6000 :6001 is the otlp/http port"))
	}
	rec = call(handleAdoptPorts(api), http.MethodPost, "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguity status = %d, want 400", rec.Code)
	}
	body = nil
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] == "" || len(body) != 1 {
		t.Fatalf("400 body = %v, want message-only", body)
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

// TestPutConfigYAMLValidateFalse pins the escape hatch: ?validate=false
// routes to the no-validate closure (never the validating one) and reports
// running_stale so the UI can say the running collector kept the previous
// version. A plain PUT keeps today's validating path exactly.
func TestPutConfigYAMLValidateFalse(t *testing.T) {
	api := fakeAPI()
	var gotName, gotYAML string
	api.PutConfigYAML = func(name, yaml string) error {
		t.Fatal("PutConfigYAML called for a validate=false write")
		return nil
	}
	api.PutConfigYAMLNoValidate = func(name, yaml string) (bool, error) {
		gotName, gotYAML = name, yaml
		return true, nil
	}

	req := httptest.NewRequest(http.MethodPut, "/api/configs/debug/yaml?validate=false", strings.NewReader("a: 1\n"))
	req.Host = "localhost"
	req.SetPathValue("name", "debug")
	rec := httptest.NewRecorder()
	handlePutConfigYAML(api)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" || gotYAML != "a: 1\n" {
		t.Fatalf("PutConfigYAMLNoValidate got (%q, %q)", gotName, gotYAML)
	}
	var got map[string]bool
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || !got["ok"] || !got["running_stale"] {
		t.Fatalf("body = %v, %v, want ok and running_stale true", got, err)
	}

	// Without the param, the validating path runs and the answer stays the
	// plain {"ok":true} it has always been.
	var validated bool
	api.PutConfigYAML = func(name, yaml string) error { validated = true; return nil }
	api.PutConfigYAMLNoValidate = func(name, yaml string) (bool, error) {
		t.Fatal("PutConfigYAMLNoValidate called for a plain PUT")
		return false, nil
	}
	req = httptest.NewRequest(http.MethodPut, "/api/configs/debug/yaml", strings.NewReader("a: 1\n"))
	req.Host = "localhost"
	req.SetPathValue("name", "debug")
	rec = httptest.NewRecorder()
	handlePutConfigYAML(api)(rec, req)
	if rec.Code != http.StatusOK || !validated {
		t.Fatalf("plain PUT: status = %d, validated = %v, want 200 and the validating path", rec.Code, validated)
	}
	var plain map[string]bool
	if err := json.NewDecoder(rec.Body).Decode(&plain); err != nil || !plain["ok"] {
		t.Fatalf("plain PUT body = %v, %v, want {\"ok\":true}", plain, err)
	}
	if _, present := plain["running_stale"]; present {
		t.Fatal("plain PUT body carries running_stale; that field belongs to validate=false only")
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

func TestPutPresetRoute(t *testing.T) {
	api := fakeAPI()
	var gotName, gotPreset string
	var gotValues map[string]string
	api.PutPreset = func(name, preset string, values map[string]string) error {
		gotName, gotPreset, gotValues = name, preset, values
		return nil
	}
	pv := map[string]string{"name": "debug", "preset": "prod"}

	rec := call(handlePutPreset(api), http.MethodPut, `{"values":{"k":"v"}}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" || gotPreset != "prod" || gotValues["k"] != "v" {
		t.Fatalf("PutPreset got name=%q preset=%q values=%v", gotName, gotPreset, gotValues)
	}

	// absent "values": {} not nil.
	gotValues = nil
	rec = call(handlePutPreset(api), http.MethodPut, `{}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotValues == nil || len(gotValues) != 0 {
		t.Fatalf("PutPreset got values=%v, want empty non-nil map", gotValues)
	}

	// null "values": {} not nil.
	gotValues = nil
	rec = call(handlePutPreset(api), http.MethodPut, `{"values":null}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotValues == nil || len(gotValues) != 0 {
		t.Fatalf("PutPreset got values=%v, want empty non-nil map", gotValues)
	}

	// empty preset name: 400.
	rec = call(handlePutPreset(api), http.MethodPut, `{"values":{}}`, map[string]string{"name": "debug", "preset": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty preset name status = %d, want 400", rec.Code)
	}

	rec = call(handlePutPreset(api), http.MethodPut, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.PutPreset = func(name, preset string, values map[string]string) error { return errWithMessage("no such config") }
	rec = call(handlePutPreset(api), http.MethodPut, `{"values":{}}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

func TestDeletePresetAndUsePresetRejectEmptyPresetName(t *testing.T) {
	api := fakeAPI()
	pv := map[string]string{"name": "debug", "preset": ""}

	rec := call(handleDeletePreset(api), http.MethodDelete, "", pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("DeletePreset empty preset status = %d, want 400", rec.Code)
	}

	rec = call(handleUsePreset(api), http.MethodPost, "", pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("UsePreset empty preset status = %d, want 400", rec.Code)
	}
}

func TestUpdateMetaRoute(t *testing.T) {
	api := fakeAPI()
	var gotName string
	var gotRemoteURL *string
	api.PutConfigMeta = func(name string, remoteURL *string) error {
		gotName, gotRemoteURL = name, remoteURL
		return nil
	}
	pv := map[string]string{"name": "debug"}

	rec := call(handlePutConfigMeta(api), http.MethodPut, `{"remote_url":"https://x/y.yaml"}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" || gotRemoteURL == nil || *gotRemoteURL != "https://x/y.yaml" {
		t.Fatalf("PutConfigMeta got name=%q remoteURL=%v", gotName, gotRemoteURL)
	}

	rec = call(handlePutConfigMeta(api), http.MethodPut, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	api.PutConfigMeta = func(name string, remoteURL *string) error { return errWithMessage("write failed") }
	rec = call(handlePutConfigMeta(api), http.MethodPut, `{"remote_url":"x"}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}

	// A BadRequest-marked error (an unknown config) reports 400, not 500.
	api.PutConfigMeta = func(name string, remoteURL *string) error {
		return markBadRequest(errWithMessage(`config "bogus" not found`))
	}
	rec = call(handlePutConfigMeta(api), http.MethodPut, `{"remote_url":"x"}`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown config (BadRequest-marked) status = %d, want 400", rec.Code)
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

func TestRenamePresetRoute(t *testing.T) {
	api := fakeAPI()
	var gotName, gotFrom, gotTo string
	api.RenamePreset = func(name, from, to string) error {
		gotName, gotFrom, gotTo = name, from, to
		return nil
	}
	pv := map[string]string{"name": "debug", "preset": "prod"}

	rec := call(handleRenamePreset(api), http.MethodPost, `{"to":"production"}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" || gotFrom != "prod" || gotTo != "production" {
		t.Fatalf("RenamePreset got name=%q from=%q to=%q", gotName, gotFrom, gotTo)
	}

	rec = call(handleRenamePreset(api), http.MethodPost, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	rec = call(handleRenamePreset(api), http.MethodPost, `{"to":"x"}`, map[string]string{"name": "debug", "preset": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty preset name status = %d, want 400", rec.Code)
	}

	api.RenamePreset = func(name, from, to string) error { return errWithMessage("already exists") }
	rec = call(handleRenamePreset(api), http.MethodPost, `{"to":"production"}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

func TestResetRoute(t *testing.T) {
	api := fakeAPI()
	var gotName string
	api.Reset = func(name string) error { gotName = name; return nil }
	pv := map[string]string{"name": "debug"}

	rec := call(handleReset(api), http.MethodPost, "", pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotName != "debug" {
		t.Fatalf("Reset got name=%q, want debug", gotName)
	}

	// A user mistake (unmodified builtin, non-builtin) is BadRequest-marked
	// by the closure and must arrive as 400.
	api.Reset = func(name string) error {
		return markBadRequest(errWithMessage(`config "debug" already matches the shipped version; nothing to reset`))
	}
	rec = call(handleReset(api), http.MethodPost, "", pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("BadRequest-marked closure error status = %d, want 400", rec.Code)
	}

	api.Reset = func(name string) error { return errWithMessage("disk on fire") }
	rec = call(handleReset(api), http.MethodPost, "", pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("closure error status = %d, want 500", rec.Code)
	}
}

func TestRenameConfigRoute(t *testing.T) {
	api := fakeAPI()
	var gotFrom, gotTo string
	api.RenameConfig = func(from, to string) error {
		gotFrom, gotTo = from, to
		return nil
	}
	pv := map[string]string{"name": "old"}

	rec := call(handleRenameConfig(api), http.MethodPost, `{"to":"new"}`, pv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotFrom != "old" || gotTo != "new" {
		t.Fatalf("RenameConfig got from=%q to=%q", gotFrom, gotTo)
	}

	rec = call(handleRenameConfig(api), http.MethodPost, `not json`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", rec.Code)
	}

	// A collision is the caller's mistake: BadRequest-marked, 400.
	api.RenameConfig = func(from, to string) error {
		return markBadRequest(errWithMessage(`config "new" already exists`))
	}
	rec = call(handleRenameConfig(api), http.MethodPost, `{"to":"new"}`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("collision (BadRequest-marked) status = %d, want 400", rec.Code)
	}

	api.RenameConfig = func(from, to string) error { return errWithMessage("rename failed") }
	rec = call(handleRenameConfig(api), http.MethodPost, `{"to":"new"}`, pv)
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

	api.SetDistroPath = func(name, path string) (string, error) { return "", errWithMessage("disk error") }
	rec = call(handleSetDistroPath(api), http.MethodPut, `{"path":"/nope"}`, pv)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("plain closure error status = %d, want 500", rec.Code)
	}

	// A BadRequest-marked validation failure (app.SetDistroPath's bad path or
	// invalid name) must map to 400, same as handleRemoveDistro's.
	api.SetDistroPath = func(name, path string) (string, error) {
		return "", markBadRequest(errWithMessage("not executable"))
	}
	rec = call(handleSetDistroPath(api), http.MethodPut, `{"path":"/nope"}`, pv)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("BadRequest-marked closure error status = %d, want 400", rec.Code)
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
		return false, markBadRequest(errWithMessage("no user distro entry named \"x\""))
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

func TestActivateWithPreset(t *testing.T) {
	api := fakeAPI()
	var gotName, gotPreset string
	api.Activate = func(name, preset string) error {
		gotName, gotPreset = name, preset
		return nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/configs/debug/activate", "application/json", strings.NewReader(`{"preset":"prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotName != "debug" || gotPreset != "prod" {
		t.Fatalf("Activate got name=%q preset=%q", gotName, gotPreset)
	}
}

func TestActivateWithoutBodyKeepsWorking(t *testing.T) {
	api := fakeAPI()
	var gotName, gotPreset string
	calledWithoutBody := false
	api.Activate = func(name, preset string) error {
		gotName, gotPreset = name, preset
		calledWithoutBody = true
		return nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	// a POST with no body at all: activate the config's current preset.
	resp, err := http.Post(srv.URL+"/api/configs/debug/activate", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !calledWithoutBody || gotName != "debug" || gotPreset != "" {
		t.Fatalf("Activate got name=%q preset=%q, want debug/\"\"", gotName, gotPreset)
	}
}

func TestErrorPassthrough(t *testing.T) {
	api := fakeAPI()
	api.Activate = func(name, preset string) error { return errWithMessage("collector said no") }
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

// TestServesVendoredCodeMirror confirms the go:embed directive on "static"
// picks up the static/vendor subdirectory too, and Handler serves its
// contents as plain static files (embed wiring for the editor's JS).
// Also covers the vendored Lucide icon map and the OFL fonts of the v3
// handoff type ramp, under the same go:embed directive.
func TestServesVendoredCodeMirror(t *testing.T) {
	api := fakeAPI()
	for _, path := range []string{
		"/vendor/codemirror.min.js",
		"/vendor/lucide-icons.js",
		"/vendor/fonts/jetbrains-mono-latin-400.woff2",
		"/vendor/fonts/ibm-plex-sans-latin-400.woff2",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		Handler(api).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
		}
	}
}

// TestServesAppShellStaticFiles is the T3 static-serve smoke test: the
// real four-view app (index.html + app.css + app.js) is embedded and
// served, replacing the P1 stopgap page.
func TestServesAppShellStaticFiles(t *testing.T) {
	api := fakeAPI()
	for _, path := range []string{"/", "/app.css", "/app.js"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		Handler(api).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	Handler(api).ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "<title>compy</title>") {
		t.Fatalf("index.html missing expected title, got: %s", body)
	}
	if !strings.Contains(body, `href="app.css"`) || !strings.Contains(body, `src="app.js"`) {
		t.Fatalf("index.html doesn't reference app.css/app.js, got: %s", body)
	}
	// T4's editor needs the vendored CodeMirror loaded locally (no CDN).
	for _, ref := range []string{"vendor/codemirror.min.css", "vendor/codemirror.min.js", "vendor/yaml.min.js"} {
		if !strings.Contains(body, ref) {
			t.Fatalf("index.html doesn't reference %s, got: %s", ref, body)
		}
	}
}

// TestNoNativeDialogsInAppJS guards a bug that shipped once and is invisible
// in a browser: compy's own window (internal/window) is a WKWebView whose
// WKUIDelegate implements no JavaScript panel methods, so window.prompt()
// returns null and window.confirm() returns false without showing anything.
// Every action gated on one silently did nothing there — copy, del, rename,
// "+ new set", unlocking a protected config, roll back — while working fine
// in a test browser. app.js uses its own <dialog>-based ask() instead.
func TestNoNativeDialogsInAppJS(t *testing.T) {
	src, err := staticFiles.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"window.prompt(", "window.confirm(", "window.alert("} {
		// The explanatory comment names them without calling them.
		for _, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if strings.Contains(line, banned) {
				t.Errorf("app.js calls %s — dead in compy's WKWebView window; use ask()/askText()/askConfirm():\n%s", banned, trimmed)
			}
		}
	}
	if !strings.Contains(string(src), "function ask(") {
		t.Error("app.js is missing the ask() dialog helper")
	}
}

// TestNoInnerHTMLInAppJS guards the house rule that every API-derived string
// reaches the DOM through textContent: el() and createElementNS are the only
// two ways this app builds nodes, which keeps the XSS surface auditable in
// one place. The v3 window builds Lucide glyphs from vendored path data, so
// the tempting shortcut — assigning author-controlled SVG markup — is right
// there; this test is what makes taking it a build failure.
func TestNoInnerHTMLInAppJS(t *testing.T) {
	src, err := staticFiles.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{".innerHTML", ".outerHTML", "insertAdjacentHTML", "document.write("} {
		for _, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			// The header comment names innerHTML to say it is not used.
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if strings.Contains(line, banned) {
				t.Errorf("app.js uses %s — build nodes with el()/createElementNS instead:\n%s", banned, trimmed)
			}
		}
	}
}

// markBadRequest stands in for internal/state.BadRequest: webui recognises a
// marked error structurally (a BadRequest() bool method), so these tests
// don't need — and this package must not have — the import.
type badRequestFake struct{ error }

func (badRequestFake) BadRequest() bool { return true }

func markBadRequest(err error) error { return badRequestFake{err} }

// stillRunningFake stands in for internal/state.StillRunning, matched
// structurally the same way badRequestFake is.
type stillRunningFake struct {
	error
	desc string
}

func (e stillRunningFake) StillRunning() string { return e.desc }

// TestActivationFailureCarriesStillRunning: the failure panel names what
// survived the failed activation, and reads it from a field rather than
// parsing the diagnostic.
func TestActivationFailureCarriesStillRunning(t *testing.T) {
	api := fakeAPI()
	api.Activate = func(name, preset string) error {
		return stillRunningFake{
			error: errWithMessage("collector did not come up: probe failed\npanic: bad exporter"),
			desc:  "otlp-to-bronto · staging",
		}
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/configs/wont-start/activate", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a collector that won't start is not a caller mistake)", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["still_running"] != "otlp-to-bronto · staging" {
		t.Errorf("still_running = %q, want %q", body["still_running"], "otlp-to-bronto · staging")
	}
	if !strings.Contains(body["error"], "panic: bad exporter") {
		t.Errorf("error = %q, want the collector's diagnostic", body["error"])
	}

	// An ordinary error carries no such field.
	api.Activate = func(name, preset string) error { return errWithMessage("boom") }
	srv2 := httptest.NewServer(Handler(api))
	defer srv2.Close()
	resp2, err := http.Post(srv2.URL+"/api/configs/x/activate", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body = nil
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["still_running"]; ok {
		t.Errorf("plain error body = %v, want no still_running", body)
	}
}

func TestStopAndStartRoutes(t *testing.T) {
	api := fakeAPI()
	stopped, started := false, false
	api.Stop = func() error { stopped = true; return nil }
	api.Start = func() error { started = true; return nil }
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	for _, tc := range []struct {
		path string
		done *bool
	}{{"/api/service/stop", &stopped}, {"/api/service/start", &started}} {
		resp, err := http.Post(srv.URL+tc.path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s = %d, want 200", tc.path, resp.StatusCode)
		}
		if !*tc.done {
			t.Errorf("POST %s did not reach its closure", tc.path)
		}
	}
}

// TestDownloadProgressRoute: the Settings screen POSTs a fetch, which
// returns at once, and follows it here.
func TestDownloadProgressRoute(t *testing.T) {
	api := fakeAPI()
	var gotName string
	api.DownloadProgress = func(name string) (any, error) {
		gotName = name
		return map[string]any{"status": "downloading", "pct": 51}, nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/distros/otelcol-k8s/progress")
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
	if gotName != "otelcol-k8s" || body["status"] != "downloading" || body["pct"] != float64(51) {
		t.Fatalf("progress for %q = %v", gotName, body)
	}
}

// TestDistroUpdateRoutes: GET checks, POST pulls — the no-op answer
// (started false, current == latest) is a 200 the screen turns into an
// "already newest" note, and a refusal (the bundled collector, a
// user-managed entry) is a 400 whose message is the whole answer.
func TestDistroUpdateRoutes(t *testing.T) {
	api := fakeAPI()
	api.CheckDistroUpdate = func(name string) (string, string, error) {
		if name == "compy" {
			return "", "", markBadRequest(fmt.Errorf("the bundled collector updates with compy releases"))
		}
		return "0.135.0", "0.160.0", nil
	}
	var started string
	api.UpdateDistro = func(name string) (string, string, bool, error) {
		started = name
		return "0.135.0", "0.160.0", true, nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/distros/otlp/update")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || body["current"] != "0.135.0" || body["latest"] != "0.160.0" {
		t.Fatalf("GET update = %d %v", resp.StatusCode, body)
	}

	resp, err = http.Post(srv.URL+"/api/distros/otlp/update", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body = nil
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || body["started"] != true || started != "otlp" {
		t.Fatalf("POST update = %d %v (started %q)", resp.StatusCode, body, started)
	}

	resp, err = http.Get(srv.URL + "/api/distros/compy/update")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET update compy = %d, want 400", resp.StatusCode)
	}
}

// TestHealthRoute: the Collector screen's four numbers reach it through the
// route, and a stopped collector is a 200 with available:false, never an
// error the screen would have to render as a failure.
func TestHealthRoute(t *testing.T) {
	api := fakeAPI()
	api.Health = func() (any, error) {
		return map[string]any{"available": true, "received": 12, "exported": 8, "queue": 2, "dropped": 1}, nil
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/collector/health")
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
	if body["available"] != true || body["received"] != float64(12) || body["dropped"] != float64(1) {
		t.Fatalf("health body = %v", body)
	}
}

// upstreamFake stands in for internal/state.Upstream, matched structurally
// the same way badRequestFake is.
type upstreamFake struct{ error }

func (upstreamFake) Upstream() bool { return true }

// An upstream-marked closure error (the GitHub release check failing) is a
// 502, not a 500 — the page appends its collector log tail to a 500 only,
// and the collector has nothing to do with GitHub being down (G3). The mark
// survives fmt.Errorf wrapping like the others.
func TestUpstreamFailureIs502(t *testing.T) {
	api := fakeAPI()
	api.CheckDistroUpdate = func(name string) (string, string, error) {
		return "", "", fmt.Errorf("distro %q: %w", name, upstreamFake{errWithMessage("release check: rate limited")})
	}
	srv := httptest.NewServer(Handler(api))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/distros/otlp/update")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body["error"], "rate limited") {
		t.Errorf("error = %q, want the check's own message", body["error"])
	}
}
