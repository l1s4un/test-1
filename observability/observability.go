// Package observability provides framework-agnostic observability primitives
// (tracing, logging, metrics) that respect Clean Architecture boundaries.
//
// Design principles:
//   - The domain layer NEVER imports this package directly. Domain methods
//     remain pure; instrumentation is added at the use-case/infra boundary
//     via decorators or via raised domain events.
//   - The use-case layer depends ONLY on the small interfaces declared here
//     (Tracer, Logger, Meter). Concrete OTel/Prometheus wiring lives in the
//     composition root (cmd/main.go).
//   - All implementations are no-op safe so tests can pass nil-equivalent
//     values without panicking.
package observability

import "context"

// Tracer starts spans. Use-cases depend on this minimal interface, not on
// go.opentelemetry.io/otel/trace, so swapping vendors does not ripple.
type Tracer interface {
	Start(ctx context.Context, name string, attrs ...Attr) (context.Context, Span)
}

// Span represents an in-progress operation.
type Span interface {
	SetAttributes(attrs ...Attr)
	AddEvent(name string, attrs ...Attr)
	RecordError(err error)
	End()
	// TraceID returns the W3C trace id; "" if span is no-op. Used to enrich
	// logs/metrics for correlation.
	TraceID() string
	SpanID() string
}

// Attr is a typed key/value pair, deliberately small to avoid leaking vendor
// types into the use-case layer.
type Attr struct {
	Key   string
	Value any
}

// String / Int64 / Bool helpers keep call sites readable.
func String(k, v string) Attr  { return Attr{Key: k, Value: v} }
func Int64(k string, v int64) Attr { return Attr{Key: k, Value: v} }
func Bool(k string, v bool) Attr   { return Attr{Key: k, Value: v} }