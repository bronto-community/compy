//go:build darwin

package window

import (
	"net/http/httptest"
	"testing"

	"github.com/bronto-community/compy/internal/webui"
)

// The requests Wails' in-process asset server hands us look like this:
// Host "wails", no Origin on GETs, and WebKit-set fetch metadata on API
// calls. webui's hostCheck (correctly) rejects that shape from the network;
// inProcess must rewrite it so the same handler serves the window.
func TestInProcessShimPassesHostCheck(t *testing.T) {
	h := webui.Handler(webui.API{}) // static routes only; no API calls made

	raw := httptest.NewRequest("GET", "http://wails/", nil)
	raw.Host = "wails"

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, raw)
	if rec.Code != 403 {
		t.Fatalf("raw wails request without shim: got %d, want 403 (hostCheck should reject non-localhost Host)", rec.Code)
	}

	rec = httptest.NewRecorder()
	inProcess(h).ServeHTTP(rec, raw)
	if rec.Code != 200 {
		t.Fatalf("shimmed request: got %d, want 200", rec.Code)
	}
}

func TestInProcessRewritesCrossSiteHeaders(t *testing.T) {
	h := webui.Handler(webui.API{})

	r := httptest.NewRequest("GET", "http://wails/", nil)
	r.Host = "wails"
	r.Header.Set("Origin", "wails://wails")
	r.Header.Set("Sec-Fetch-Site", "cross-site")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("unshimmed: got %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	inProcess(h).ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("shimmed: got %d, want 200", rec.Code)
	}
}
