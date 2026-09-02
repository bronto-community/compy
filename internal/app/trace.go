package app

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracer is compy's own instrumentation scope. When tracing is off — the
// default — the global provider is OTel's no-op and every Start below is a
// nil check, so nothing here needs a guard and nothing in app may depend on
// a span existing.
var tracer = otel.Tracer("github.com/bronto-community/compy/internal/app")

// op starts a span for one compy operation. The returned end() records the
// error (if any) and closes the span, so a caller writes:
//
//	ctx, end := op(ctx, "compy.activate", attribute.String("compy.config", name))
//	defer func() { end(err) }()
//
// The deferred closure reads the NAMED return, which is why every traced
// method below names its error return — without that, `defer end(err)`
// would capture the zero value at defer time and every span would look
// successful.
func op(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}
