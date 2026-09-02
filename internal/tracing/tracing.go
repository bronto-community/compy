// Package tracing is compy's own OpenTelemetry tracing: opt-in spans over
// compy's operations, exported as OTLP.
//
// The default destination is compy's OWN collector — 127.0.0.1 on the
// configured OTLP/HTTP port — so compy's traces travel the same path a
// user's applications do, and land wherever the active configuration sends
// them. Nothing leaves the machine that the user's own pipeline did not
// send. Pointing it at a backend directly (endpoint + headers in settings)
// is the escape hatch, not the default.
//
// Disabled is the default and it is genuinely free: without Setup the
// global provider stays OTel's no-op, so every tracer.Start() in compy
// costs a nil check. Nothing in compy is allowed to depend on tracing
// being on.
package tracing

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/bronto-community/compy/internal/state"
	"github.com/bronto-community/compy/internal/version"
)

// ServiceName is what compy calls itself on the wire.
const ServiceName = "compy"

// exportTimeout bounds one export attempt, and shutdownTimeout bounds the
// flush at exit. Both are deliberately short and retry is off: a CLI
// command must not sit waiting on a collector that is stopped — which is
// the NORMAL state for the default destination, since compy traces its own
// `compy stop`. The OTLP defaults (10s timeout, 5s initial backoff, retry
// on) would turn every command into a ten-second pause.
const (
	exportTimeout   = 2 * time.Second
	shutdownTimeout = 3 * time.Second
)

// Endpoint resolves where traces go: the configured endpoint, or compy's
// own OTLP/HTTP receiver when none is set. Exported so the UI and `compy
// settings` can show the effective destination rather than an empty field.
func Endpoint(s state.Settings) string {
	if e := strings.TrimSpace(s.TracingEndpoint); e != "" {
		return e
	}
	return "http://127.0.0.1:" + strconv.Itoa(s.HTTPPort)
}

// TracesURL turns the configured BASE endpoint into the URL traces are
// POSTed to. The setting is a base — the same shape otlp-forward's endpoint
// field takes, and the same shape OTEL_EXPORTER_OTLP_ENDPOINT has — so the
// signal path is appended here.
//
// WithEndpointURL does NOT do this: it takes the URL literally, so a base
// with no path posts to "/", which every OTLP receiver answers with 404 and
// every span is silently dropped. A base that already names a path is left
// alone, so a proxy on a subpath still works.
func TracesURL(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base // Setup validates the URL; let it report the real error
	}
	if p := strings.Trim(u.Path, "/"); p != "" {
		return base
	}
	u.Path = "/v1/traces"
	return u.String()
}

// ParseHeaders reads the "Name: value" lines the settings UI stores into
// the map the exporter wants. Blank lines and comments are skipped; a line
// with no colon is the user's mistake and is named. Values keep their
// internal spacing (a bearer token is one word, but a header value need not
// be), and only the separator's padding is trimmed.
func ParseHeaders(text string) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, state.BadRequest(fmt.Errorf("tracing header line %d: want \"Name: value\", got %q", i+1, line))
		}
		out[name] = strings.TrimSpace(value)
	}
	return out, nil
}

// Setup installs a global TracerProvider per settings and returns the
// shutdown to run before the process exits. Tracing off (or an unusable
// endpoint) yields a no-op shutdown and leaves the global provider alone —
// callers do not branch, they always defer what they get back.
//
// surface names which compy is running ("cli", "ui", "window", "tray"); it
// rides on the resource so one trace stream can be split by where the
// operation came from.
func Setup(ctx context.Context, s state.Settings, surface string) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if !s.Tracing {
		return noop, nil
	}
	endpoint := Endpoint(s)
	if _, err := url.Parse(endpoint); err != nil {
		return noop, state.BadRequest(fmt.Errorf("tracing endpoint %q: %v", endpoint, err))
	}
	headers, err := ParseHeaders(s.TracingHeaders)
	if err != nil {
		return noop, err
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(TracesURL(endpoint)),
		otlptracehttp.WithTimeout(exportTimeout),
		// Retry off: see exportTimeout. A dropped span is the right trade
		// for a CLI that exits when it is done.
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return noop, err
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(ServiceName),
		semconv.ServiceVersion(version.String()),
		attribute.String("compy.surface", surface),
	))
	if err != nil {
		// A schema-URL clash is the only way this fails; the attributes are
		// still worth having, so fall back to them alone rather than
		// refusing to trace.
		res = resource.NewSchemaless(
			semconv.ServiceName(ServiceName),
			semconv.ServiceVersion(version.String()),
			attribute.String("compy.surface", surface),
		)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithExportTimeout(exportTimeout)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	// W3C tracecontext both ways: compy has nothing upstream today, but a
	// span it exports should join a trace anything downstream continues.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()
		return tp.Shutdown(ctx)
	}, nil
}
