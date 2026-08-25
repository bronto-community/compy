// Package webui serves compy's localhost-only web UI: a small JSON API plus
// an embedded single-page HTML app. It has no internal dependencies — the
// caller (cmd/compy) wires behavior in through the API closure struct.
package webui

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// API is filled with closures by cmd/compy (internal/app.WebUIAPI); this is
// the full v2 REST surface (docs/superpowers/plans/2026-08-25-compy-v2-p2-rest.md,
// "The REST surface"). Status, Configs, Activate, and Log are the frozen P1
// stopgap shapes the embedded static page already calls; everything else is
// wired by later tasks (nil until then — routes() must serve those as 501
// without dereferencing the closure).
type API struct {
	Status   func() (map[string]any, error)            // service state, distro, ports, active config
	Log      func() (string, error)                    // tail of collector log
	Env      func() (map[string]string, string, error) // OTEL_* vars, shell script
	SetOSEnv func(on bool) error

	GetSettings func() (map[string]any, error)
	PutSettings func(grpcPort, httpPort *int, menuDistroSwap *bool) error // partial: nil = unchanged

	Apply    func() error
	Rollback func() error
	Validate func() error

	Configs       func() (any, error) // configurations, JSON-marshalable
	CreateConfig  func(name, yaml string) error
	CreateFromURL func(name, url string) error
	GetConfig     func(name string) (any, error) // {"info":..., "yaml":...}
	PutConfigYAML func(name, yaml string) error
	PutConfigMeta func(name string, distro, remoteURL *string) error // partial: nil = unchanged
	DeleteConfig  func(name string) error
	CopyConfig    func(src, dst string) error
	Activate      func(name string) error // make a configuration the running one
	Sync          func(name string) error
	Resync        func(name string) error
	SyncAll       func() ([]string, error)

	PutSet    func(name, set string, values map[string]string) error // create/replace whole set
	DeleteSet func(name, set string) error
	UseSet    func(name, set string) error

	Distros     func() (any, error)
	AddDistro   func(name, path string) (string, error) // returns the override warning, "" if none
	UseDistro   func(name string) error
	FetchDistro func(name string) error
}

// route is one API endpoint: the drift test (TestOpenAPIDriftAgainstRoutes)
// compares this table to api/openapi.json, and Handler builds the mux from
// it. Adding an endpoint means updating both this table and the spec.
type route struct {
	Method  string
	Pattern string // mux path pattern, e.g. "/api/configs/{name}/activate"
	H       func(API) http.HandlerFunc
}

// routes is the full REST surface. New (not-yet-wired) endpoints use
// notImplemented, which never touches its API argument — so a nil closure
// field on an unwired endpoint is safe.
func routes() []route {
	return []route{
		{"GET", "/api/status", handleStatus},
		{"GET", "/api/log", handleLog},
		{"GET", "/api/env", notImplemented},
		{"POST", "/api/os-env", notImplemented},

		{"GET", "/api/settings", notImplemented},
		{"PUT", "/api/settings", notImplemented},

		{"POST", "/api/service/apply", notImplemented},
		{"POST", "/api/service/rollback", notImplemented},
		{"POST", "/api/service/validate", notImplemented},

		{"GET", "/api/configs", handleConfigs},
		{"POST", "/api/configs", notImplemented},
		{"POST", "/api/configs/from-url", notImplemented},
		{"GET", "/api/configs/{name}", notImplemented},
		{"PUT", "/api/configs/{name}/yaml", notImplemented},
		{"PUT", "/api/configs/{name}/meta", notImplemented},
		{"DELETE", "/api/configs/{name}", notImplemented},
		{"POST", "/api/configs/{name}/copy", notImplemented},
		{"POST", "/api/configs/{name}/activate", handleActivate},
		{"POST", "/api/configs/{name}/sync", notImplemented},
		{"POST", "/api/configs/{name}/resync", notImplemented},
		{"POST", "/api/configs/sync-all", notImplemented},
		{"PUT", "/api/configs/{name}/sets/{set}", notImplemented},
		{"DELETE", "/api/configs/{name}/sets/{set}", notImplemented},
		{"POST", "/api/configs/{name}/sets/{set}/use", notImplemented},

		{"GET", "/api/distros", notImplemented},
		{"POST", "/api/distros", notImplemented},
		{"POST", "/api/distros/{name}/use", notImplemented},
		{"POST", "/api/distros/{name}/fetch", notImplemented},
	}
}

// Handler builds the mux: host-check middleware wrapping /api/* routes and
// the embedded static / file server.
func Handler(api API) http.Handler {
	mux := http.NewServeMux()

	for _, rt := range routes() {
		mux.HandleFunc(rt.Method+" "+rt.Pattern, rt.H(api))
	}

	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err) // embed misconfigured; can only happen at build time
	}
	mux.Handle("/", http.FileServerFS(static))

	return hostCheck(mux)
}

// Serve starts the web UI listening on addr.
func Serve(addr string, api API) error {
	return http.ListenAndServe(addr, Handler(api))
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

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func handleStatus(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := api.Status()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func handleConfigs(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		configs, err := api.Configs()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, configs)
	}
}

func handleActivate(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Activate(r.PathValue("name")); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleLog(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tail, err := api.Log()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"log": tail})
	}
}

// notImplemented serves the still-unwired portion of the REST surface. It
// never touches api, so it is safe to use before the corresponding closure
// exists.
func notImplemented(API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotImplemented, errors.New("not implemented"))
	}
}
