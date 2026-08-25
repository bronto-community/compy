// Package webui serves compy's localhost-only web UI: a small JSON API plus
// an embedded single-page HTML app. It has no internal dependencies — the
// caller (cmd/compy) wires behavior in through the API closure struct.
package webui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// API is filled with closures by cmd/compy. All funcs required. This is
// the v2 stopgap surface (P2 replaces it with the REST API + OpenAPI spec).
type API struct {
	Status    func() (map[string]any, error) // service state, distro, ports, active config
	Configs   func() (any, error)            // configurations, JSON-marshalable
	Activate  func(name string) error        // make a configuration the running one
	LastError func() (string, error)         // tail of collector log
}

// Handler builds the mux: host-check middleware wrapping /api/* routes and
// the embedded static / file server.
func Handler(api API) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", handleStatus(api))
	mux.HandleFunc("GET /api/configs", handleConfigs(api))
	mux.HandleFunc("POST /api/configs/{name}/activate", handleActivate(api))
	mux.HandleFunc("GET /api/log", handleLog(api))

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
		tail, err := api.LastError()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"log": tail})
	}
}
