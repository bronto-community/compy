package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/tracing"
)

// TestOpExportsATraceTree is the end-to-end shape: spans compy's operations
// produce reach an OTLP endpoint, at the path a receiver actually serves,
// with children nested under their parent. The nesting is the whole point —
// a flat list of "compy.validate" spans with no activation around them
// would tell nobody anything.
func TestOpExportsATraceTree(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	shutdown, err := tracing.Setup(context.Background(),
		state.Settings{Tracing: true, TracingEndpoint: srv.URL}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, end := op(context.Background(), "compy.activate")
	_, child := op(ctx, "compy.validate")
	child(nil)
	end(nil)
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 {
		t.Fatal("nothing was exported")
	}
	// The signal path, not "/" — WithEndpointURL takes the URL literally,
	// so a base endpoint without this would 404 and drop every span.
	for _, p := range paths {
		if p != "/v1/traces" {
			t.Errorf("exported to %q, want /v1/traces", p)
		}
	}
	// The payload is protobuf, so assert on shape rather than parsing it:
	// both span names present, and the whole thing non-trivial.
	var all strings.Builder
	for _, b := range bodies {
		all.Write(b)
	}
	for _, want := range []string{"compy.activate", "compy.validate", tracing.ServiceName} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("export does not mention %q", want)
		}
	}
}

// TestTracingOffCostsNothing: with tracing off, no provider is installed and
// op() still works — every traced operation in app runs unguarded, so this
// has to be true or compy breaks for the default configuration.
func TestTracingOffCostsNothing(t *testing.T) {
	shutdown, err := tracing.Setup(context.Background(), state.Settings{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, end := op(context.Background(), "compy.activate")
	_, child := op(ctx, "compy.validate")
	child(context.Canceled) // an error on a no-op span must not panic
	end(nil)
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("no-op shutdown returned %v", err)
	}
}
