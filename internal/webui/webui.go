// Package webui serves compy's localhost-only web UI: a small JSON API plus
// an embedded single-page HTML app. It has no internal dependencies — the
// caller (cmd/compy) wires behavior in through the API closure struct.
package webui

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// API is filled with closures by cmd/compy (internal/app.WebUIAPI); this is
// the full v2 REST surface (docs/superpowers/plans/2026-08-25-compy-v2-p2-rest.md,
// "The REST surface"). Status, Configs, Activate, and Log are also the
// frozen P1 stopgap shapes the embedded static page already calls.
type API struct {
	Status   func() (map[string]any, error)            // service state, distro, ports, active config
	Log      func(lines int) (string, error)           // tail of collector log
	Env      func() (map[string]string, string, error) // OTEL_* vars, shell script
	SetOSEnv func(on bool) error

	GetSettings func() (map[string]any, error)
	PutSettings func(grpcPort, httpPort *int, protocol *string) error // partial: nil = unchanged
	AdoptPorts  func(grpcPort, httpPort *int) error                   // both nil = classify the running config's detected ports; explicit values resolve ambiguity

	Health       func() (any, error) // collector's own metrics; {"available": false} when stopped
	Apply        func() error
	Stop         func() error // stop the collector; the active configuration stays named
	Start        func() error // run the active configuration again
	Validate     func() error
	FactoryReset func() error // uninstall the job, wipe the state dir, re-create the shipped defaults

	Configs       func() (any, error) // configurations, JSON-marshalable
	CreateConfig  func(name, yaml string) error
	CreateFromURL func(name, url string) error
	GetConfig     func(name string) (any, error) // {"info":..., "yaml":...}
	PutConfigYAML func(name, yaml string) error
	// PutConfigYAMLNoValidate writes without validating and never touches
	// the running collector; returns whether the active running collector
	// is now on a stale (previous) version of this config.
	PutConfigYAMLNoValidate func(name, yaml string) (bool, error)
	PutConfigMeta           func(name string, remoteURL *string) error // nil = unchanged
	DeleteConfig            func(name string) error
	CopyConfig              func(src, dst string) error
	Activate                func(name, preset string) error // make a configuration the running one; "" preset keeps the current one
	ValidateConfig          func(name string) error         // validate this config (any config, not just the active one) against its own distro
	Sync                    func(name string) error
	Resync                  func(name string) error
	Reset                   func(name string) error // restore a modified built-in config to its shipped version
	RenameConfig            func(from, to string) error
	SyncAll                 func() ([]string, error)

	PutPreset    func(name, preset string, values map[string]string) error // create/replace a whole preset
	DeletePreset func(name, preset string) error
	UsePreset    func(name, preset string) error
	RenamePreset func(name, from, to string) error

	Distros          func() (any, error)
	AddDistro        func(name, path string) (string, error) // returns the override warning, "" if none
	SetDistroPath    func(name, path string) (string, error) // returns the override warning, "" if none
	RemoveDistro     func(name string) (bool, error)         // returns whether removing it reverted to a shipped definition
	UseDistro        func(name string) error
	FetchDistro      func(name string) error // starts the download and returns; poll DownloadProgress
	DownloadProgress func(name string) (any, error)

	CheckDistroUpdate func(name string) (current, latest string, err error)               // on-demand upstream release check, no download
	UpdateDistro      func(name string) (current, latest string, started bool, err error) // starts the pull when newer; poll DownloadProgress
}

// badRequester is how a closure error asks to be reported as 400 Bad
// Request rather than 500, the default for every other closure error.
// state.BadRequest is what actually marks them (a leaf package everything
// already imports); webui matches the behaviour rather than the type, which
// keeps this package free of internal dependencies in both directions.
type badRequester interface{ BadRequest() bool }

// upstreamer is how a closure error asks to be reported as 502 Bad Gateway:
// an upstream service (the GitHub release check) failed, so neither the
// caller's 400 nor our 500 — and the page appends its collector log tail to
// a 500 only, which keeps an upstream hiccup from wearing an unrelated
// collector diagnostic. state.Upstream marks them; webui matches the
// behaviour rather than the type, as with badRequester.
type upstreamer interface{ Upstream() bool }

// stillRunner is how a closure error says what kept running when it failed:
// the error body gains a "still_running" field naming it. state.StillRunning
// marks them; webui matches the behaviour rather than the type, as with
// badRequester.
type stillRunner interface{ StillRunning() string }

// isBadRequest reports whether err, or anything it wraps, is marked. It
// unwraps rather than type-asserting: callers routinely add context with
// fmt.Errorf("...: %w", err), and a marker that a single %w silently drops
// is a marker you cannot rely on.
func isBadRequest(err error) bool {
	var b badRequester
	return errors.As(err, &b) && b.BadRequest()
}

func isUpstream(err error) bool {
	var u upstreamer
	return errors.As(err, &u) && u.Upstream()
}

// route is one API endpoint: the drift test (TestOpenAPIDriftAgainstRoutes)
// compares this table to api/openapi.json, and Handler builds the mux from
// it. Adding an endpoint means updating both this table and the spec.
type route struct {
	Method  string
	Pattern string // mux path pattern, e.g. "/api/configs/{name}/activate"
	H       func(API) http.HandlerFunc
}

// routes is the full REST surface (docs/superpowers/plans/2026-08-25-compy-v2-p2-rest.md,
// "The REST surface"); api/openapi.json mirrors it exactly (TestOpenAPIDriftAgainstRoutes).
func routes() []route {
	return []route{
		{"GET", "/api/status", handleStatus},
		{"GET", "/api/log", handleLog},
		{"GET", "/api/env", handleEnv},
		{"POST", "/api/os-env", handleSetOSEnv},

		{"GET", "/api/settings", handleGetSettings},
		{"PUT", "/api/settings", handlePutSettings},

		{"GET", "/api/collector/health", handleHealth},

		{"POST", "/api/service/adopt-ports", handleAdoptPorts},
		{"POST", "/api/service/apply", handleApply},
		{"POST", "/api/service/stop", handleStop},
		{"POST", "/api/service/start", handleStart},
		{"POST", "/api/service/validate", handleValidate},

		{"POST", "/api/factory-reset", handleFactoryReset},

		{"GET", "/api/configs", handleConfigs},
		{"POST", "/api/configs", handleCreateConfig},
		{"POST", "/api/configs/from-url", handleCreateFromURL},
		{"GET", "/api/configs/{name}", handleGetConfig},
		{"PUT", "/api/configs/{name}/yaml", handlePutConfigYAML},
		{"PUT", "/api/configs/{name}/meta", handlePutConfigMeta},
		{"DELETE", "/api/configs/{name}", handleDeleteConfig},
		{"POST", "/api/configs/{name}/copy", handleCopyConfig},
		{"POST", "/api/configs/{name}/activate", handleActivate},
		{"POST", "/api/configs/{name}/validate", handleValidateConfig},
		{"POST", "/api/configs/{name}/sync", handleSync},
		{"POST", "/api/configs/{name}/resync", handleResync},
		{"POST", "/api/configs/{name}/reset", handleReset},
		{"POST", "/api/configs/{name}/rename", handleRenameConfig},
		{"POST", "/api/configs/sync-all", handleSyncAll},
		{"PUT", "/api/configs/{name}/presets/{preset}", handlePutPreset},
		{"DELETE", "/api/configs/{name}/presets/{preset}", handleDeletePreset},
		{"POST", "/api/configs/{name}/presets/{preset}/use", handleUsePreset},
		{"POST", "/api/configs/{name}/presets/{preset}/rename", handleRenamePreset},

		{"GET", "/api/distros", handleDistros},
		{"POST", "/api/distros", handleAddDistro},
		{"PUT", "/api/distros/{name}", handleSetDistroPath},
		{"DELETE", "/api/distros/{name}", handleRemoveDistro},
		{"POST", "/api/distros/{name}/use", handleUseDistro},
		{"POST", "/api/distros/{name}/fetch", handleFetchDistro},
		{"GET", "/api/distros/{name}/progress", handleDownloadProgress},
		{"GET", "/api/distros/{name}/update", handleCheckDistroUpdate},
		{"POST", "/api/distros/{name}/update", handleUpdateDistro},
	}
}

// Handler builds the mux: host-check middleware wrapping /api/* routes and
// the embedded static / file server.
func Handler(api API) http.Handler {
	mux := http.NewServeMux()

	for _, rt := range routes() {
		h := rt.H(api)
		mux.HandleFunc(rt.Method+" "+rt.Pattern, func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
			h(w, r)
		})
	}

	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // embed misconfigured; can only happen at build time
	}
	mux.Handle("/", http.FileServerFS(static))

	return hostCheck(mux)
}

// ServeListener serves the web UI on an existing listener. Keep-alives are
// off deliberately: macOS's URL loader silently retries an idempotent
// request when a pooled connection has gone stale, but never a POST — which
// surfaced as "the network connection was lost" on the first button pressed
// after the window sat idle. One connection per request costs nothing on
// localhost and removes the class.
func ServeListener(ln net.Listener, api API) error {
	srv := &http.Server{Handler: Handler(api)}
	srv.SetKeepAlivesEnabled(false)
	return srv.Serve(ln)
}

// hostCheck rejects (403) any request whose Host header is not localhost,
// 127.0.0.1, or [::1] (with optional :port) — a DNS-rebinding guard — and
// any request that looks cross-site: a non-localhost Origin header (a
// cross-site form POST still carries one even though the Host check passes,
// since Host is just this server's own address), or a Sec-Fetch-Site value
// other than same-origin/none.
func hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLocalHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !isLocalOrigin(origin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalHost(host string) bool {
	h := host
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		h = hostOnly
	}
	h = strings.Trim(h, "[]")
	switch h {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// isLocalOrigin reports whether origin (an Origin header value) is
// http://localhost, http://127.0.0.1, or http://[::1], any port.
func isLocalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Scheme == "http" && isLocalHost(u.Host)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeErr renders an error body. A state.StillRunning-marked error (an
// activation that failed and left the previous configuration running) also
// carries "still_running", so the failure panel can name what survived
// without parsing the message.
func writeErr(w http.ResponseWriter, status int, err error) {
	body := map[string]string{"error": err.Error()}
	var sr stillRunner
	if errors.As(err, &sr) && sr.StillRunning() != "" {
		body["still_running"] = sr.StillRunning()
	}
	writeJSON(w, status, body)
}

// writeClosureErr reports an error returned by an App closure: a
// state.BadRequest-marked one (a bad name, an unknown configuration, a
// config the collector rejects) as 400, a state.Upstream-marked one (the
// GitHub release check failed) as 502, everything else as the default 500.
// Every handler routes its closure's error through here so any closure gets
// its status just by marking its error, with no per-handler check.
func writeClosureErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if isBadRequest(err) {
		status = http.StatusBadRequest
	} else if isUpstream(err) {
		status = http.StatusBadGateway
	}
	writeErr(w, status, err)
}

// writeBodyErr reports a request-body error: 413 if it's the request
// exceeding the body-size cap (http.MaxBytesError, from the MaxBytesReader
// Handler wraps every request body in), 400 for any other decode/read
// failure (malformed JSON, a client hangup, etc).
func writeBodyErr(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeErr(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	writeErr(w, http.StatusBadRequest, err)
}

func handleStatus(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := api.Status()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func handleConfigs(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		configs, err := api.Configs()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, configs)
	}
}

// handleActivate reads an OPTIONAL JSON body {"preset"}: a POST with no body
// at all activates the configuration's current preset, so an empty body is
// not an error — only a malformed (non-empty, invalid) one is.
func handleActivate(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Preset string `json:"preset"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.Activate(r.PathValue("name"), body.Preset); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleValidateConfig(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.ValidateConfig(r.PathValue("name")); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// defaultLogLines and maxLogLines bound GET /api/log's "lines" query param:
// no param keeps the stopgap page's existing 50-line behavior, and any
// value above the cap is silently clamped (not an error — only an
// unparseable or non-positive value is).
const (
	defaultLogLines = 50
	maxLogLines     = 2000
)

func handleLog(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := defaultLogLines
		if q := r.URL.Query().Get("lines"); q != "" {
			v, err := strconv.Atoi(q)
			if err != nil || v <= 0 {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid lines %q", q))
				return
			}
			n = min(v, maxLogLines)
		}
		tail, err := api.Log(n)
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"log": tail})
	}
}

func handleEnv(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars, script, err := api.Env()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"vars": vars, "script": script})
	}
}

func handleSetOSEnv(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			On bool `json:"on"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.SetOSEnv(body.On); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleGetSettings(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		settings, err := api.GetSettings()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	}
}

// handlePutSettings applies a partial update, then responds with the full
// resulting settings (the openapi PUT response schema is Settings, not a
// bare ok).
func handlePutSettings(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			GRPCPort *int    `json:"grpc_port"`
			HTTPPort *int    `json:"http_port"`
			Protocol *string `json:"protocol"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.PutSettings(body.GRPCPort, body.HTTPPort, body.Protocol); err != nil {
			writeClosureErr(w, err)
			return
		}
		settings, err := api.GetSettings()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	}
}

// handleAdoptPorts sets the advertised grpc/http ports to the running
// config's actual OTLP listeners. The body is optional: empty means
// classify the detected ports; {"grpc_port","http_port"} assigns them
// explicitly when classification refused as ambiguous (a 400 naming the
// candidates). Responds with the resulting settings, like PUT /api/settings.
func handleAdoptPorts(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			GRPCPort *int `json:"grpc_port"`
			HTTPPort *int `json:"http_port"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.AdoptPorts(body.GRPCPort, body.HTTPPort); err != nil {
			writeClosureErr(w, err)
			return
		}
		settings, err := api.GetSettings()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	}
}

func handleApply(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Apply(); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleHealth(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		health, err := api.Health()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, health)
	}
}

func handleStop(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Stop(); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleStart(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Start(); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleValidate(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Validate(); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleFactoryReset wipes the state directory and starts over. Nothing
// here is a user mistake — any failure is a plain 500.
func handleFactoryReset(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.FactoryReset(); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleCreateConfig(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			Yaml string `json:"yaml"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.CreateConfig(body.Name, body.Yaml); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleCreateFromURL(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.CreateFromURL(body.Name, body.URL); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleGetConfig(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		detail, err := api.GetConfig(r.PathValue("name"))
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	}
}

// maxBodyBytes caps every request body Handler routes through (applied in
// Handler's route loop), mirroring cfgstore.HTTPFetch's 5MB fetch cap.
const maxBodyBytes = 5 << 20

// handlePutConfigYAML's body is text/plain, not JSON: the whole body is the
// new YAML content. The size cap itself is applied by Handler, not here.
// ?validate=false writes without validating and never touches the running
// collector; the response's running_stale says when the active running
// collector kept its previous version.
func handlePutConfigYAML(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeBodyErr(w, err)
			return
		}
		name := r.PathValue("name")
		if r.URL.Query().Get("validate") == "false" {
			stale, err := api.PutConfigYAMLNoValidate(name, string(data))
			if err != nil {
				writeClosureErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "running_stale": stale})
			return
		}
		if err := api.PutConfigYAML(name, string(data)); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handlePutConfigMeta(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RemoteURL *string `json:"remote_url"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.PutConfigMeta(r.PathValue("name"), body.RemoteURL); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleDeleteConfig(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.DeleteConfig(r.PathValue("name")); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleCopyConfig(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Dst string `json:"dst"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.CopyConfig(r.PathValue("name"), body.Dst); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleSync(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Sync(r.PathValue("name")); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleResync(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Resync(r.PathValue("name")); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleReset restores a modified built-in configuration to its shipped
// version (the builtin twin of resync).
func handleReset(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Reset(r.PathValue("name")); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleRenameConfig's body is {"to"}; the active configuration follows the
// rename, and a running one is re-applied (the closure's job, not this
// handler's).
func handleRenameConfig(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			To string `json:"to"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.RenameConfig(r.PathValue("name"), body.To); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleSyncAll(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		synced, err := api.SyncAll()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"synced": synced})
	}
}

// handlePutPreset treats an absent or null "values" as {}: the closure never
// sees a nil map.
func handlePutPreset(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preset := r.PathValue("preset")
		if preset == "" {
			writeErr(w, http.StatusBadRequest, errors.New("preset name required"))
			return
		}
		var body struct {
			Values map[string]string `json:"values"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if body.Values == nil {
			body.Values = map[string]string{}
		}
		if err := api.PutPreset(r.PathValue("name"), preset, body.Values); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleDeletePreset(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preset := r.PathValue("preset")
		if preset == "" {
			writeErr(w, http.StatusBadRequest, errors.New("preset name required"))
			return
		}
		if err := api.DeletePreset(r.PathValue("name"), preset); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleUsePreset(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preset := r.PathValue("preset")
		if preset == "" {
			writeErr(w, http.StatusBadRequest, errors.New("preset name required"))
			return
		}
		if err := api.UsePreset(r.PathValue("name"), preset); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleRenamePreset's body is {"to"}; renaming the active preset follows it
// (the closure's job, not this handler's).
func handleRenamePreset(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		preset := r.PathValue("preset")
		if preset == "" {
			writeErr(w, http.StatusBadRequest, errors.New("preset name required"))
			return
		}
		var body struct {
			To string `json:"to"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		if err := api.RenamePreset(r.PathValue("name"), preset, body.To); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleDistros(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		distros, err := api.Distros()
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, distros)
	}
}

func handleAddDistro(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		warning, err := api.AddDistro(body.Name, body.Path)
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"warning": warning})
	}
}

func handleSetDistroPath(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeBodyErr(w, err)
			return
		}
		warning, err := api.SetDistroPath(r.PathValue("name"), body.Path)
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"warning": warning})
	}
}

// handleRemoveDistro's closure error goes through writeClosureErr, so a
// state.BadRequest-marked one (the selected distro, or a pure definition
// name with no user entry) reports as 400; every other error is the usual
// 500.
func handleRemoveDistro(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reverted, err := api.RemoveDistro(r.PathValue("name"))
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"reverted": reverted})
	}
}

func handleUseDistro(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.UseDistro(r.PathValue("name")); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleFetchDistro(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.FetchDistro(r.PathValue("name")); err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleDownloadProgress answers the poll that follows POST .../fetch.
func handleDownloadProgress(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		progress, err := api.DownloadProgress(r.PathValue("name"))
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, progress)
	}
}

// handleCheckDistroUpdate answers the on-demand release check: the version
// in effect and the latest upstream release. Nothing is downloaded.
func handleCheckDistroUpdate(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current, latest, err := api.CheckDistroUpdate(r.PathValue("name"))
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"current": current, "latest": latest})
	}
}

// handleUpdateDistro starts pulling the latest upstream release and returns;
// "started": false with current == latest is the honest no-op ("already
// newest"). The download reports through the same progress route a fetch
// uses.
func handleUpdateDistro(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		current, latest, started, err := api.UpdateDistro(r.PathValue("name"))
		if err != nil {
			writeClosureErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"current": current, "latest": latest, "started": started})
	}
}

// decodeBody decodes r's JSON body into v. An empty body (no Content-Length,
// nothing read) is not an error — v keeps its zero value — since several
// routes' bodies are wholly or partially optional (the frozen stopgap
// index.html posts /activate with no body at all). Only a non-empty,
// malformed body is reported as an error.
func decodeBody(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return nil
}
