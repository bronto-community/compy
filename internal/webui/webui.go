// Package webui serves compy's localhost-only web UI: a small JSON API plus
// an embedded single-page HTML app. It has no internal dependencies — the
// caller (cmd/compy) wires behavior in through the API closure struct.
package webui

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// API is filled with closures by cmd/compy (Task 7). All funcs required.
type API struct {
	Status        func() (map[string]any, error)   // service state, distro, ports, enabled, raw_mode, env script
	Backends      func() ([]map[string]any, error) // [{"name":..., "enabled":bool}]
	AddBackend    func(name, kind, endpoint, apiKey string) error
	RemoveBackend func(name string) error
	SetEnabled    func(name string, enabled bool) error // implies apply
	Apply         func() error
	Rollback      func() error
	ReadFragment  func(name string) (string, error)
	WriteFragment func(name, content string) error // implies apply if enabled
	SetRawMode    func(on bool) error              // toggle + apply
	ReadRaw       func() (string, error)           // config/custom.yaml content ("" if missing)
	WriteRaw      func(content string) error       // write custom.yaml (+apply if raw mode on)
	LastError     func() (string, error)           // tail of collector log
	Distros       func() ([]map[string]any, error) // [{"name":..., "path":..., "selected":bool}]
	UseDistro     func(name string) error          // switch distro (implies apply)
	SetOSEnv      func(on bool) error              // OS-level env injection toggle
}

// Handler builds the mux: host-check middleware wrapping /api/* routes and
// the embedded static / file server.
func Handler(api API) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", handleStatus(api))
	mux.HandleFunc("GET /api/backends", handleBackends(api))
	mux.HandleFunc("POST /api/backends", handleAddBackend(api))
	mux.HandleFunc("DELETE /api/backends/{name}", handleRemoveBackend(api))
	mux.HandleFunc("POST /api/backends/{name}/enabled", handleSetEnabled(api))
	mux.HandleFunc("GET /api/backends/{name}/fragment", handleReadFragment(api))
	mux.HandleFunc("PUT /api/backends/{name}/fragment", handleWriteFragment(api))
	mux.HandleFunc("POST /api/apply", handleApply(api))
	mux.HandleFunc("POST /api/rollback", handleRollback(api))
	mux.HandleFunc("GET /api/log", handleLog(api))
	mux.HandleFunc("POST /api/raw-mode", handleSetRawMode(api))
	mux.HandleFunc("GET /api/distros", handleDistros(api))
	mux.HandleFunc("POST /api/distros/{name}/use", handleUseDistro(api))
	mux.HandleFunc("POST /api/os-env", handleSetOSEnv(api))
	mux.HandleFunc("GET /api/raw", handleReadRaw(api))
	mux.HandleFunc("PUT /api/raw", handleWriteRaw(api))

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

func handleBackends(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		backends, err := api.Backends()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, backends)
	}
}

func handleAddBackend(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name     string `json:"name"`
			Kind     string `json:"kind"`
			Endpoint string `json:"endpoint"`
			APIKey   string `json:"api_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := api.AddBackend(body.Name, body.Kind, body.Endpoint, body.APIKey); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleRemoveBackend(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if err := api.RemoveBackend(name); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleSetEnabled(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := api.SetEnabled(name, body.Enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleReadFragment(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		content, err := api.ReadFragment(name)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(content))
	}
}

func handleWriteFragment(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := api.WriteFragment(name, string(body)); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleApply(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Apply(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleRollback(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.Rollback(); err != nil {
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

func handleDistros(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := api.Distros()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	}
}

func handleUseDistro(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := api.UseDistro(r.PathValue("name")); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleSetOSEnv(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := api.SetOSEnv(body.On); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleSetRawMode(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := api.SetRawMode(body.On); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleReadRaw(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		content, err := api.ReadRaw()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(content))
	}
}

func handleWriteRaw(api API) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := api.WriteRaw(string(body)); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
