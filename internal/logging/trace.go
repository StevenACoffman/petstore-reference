package logging

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Attribute keys added to every log record emitted inside a span.
const (
	TraceIDKey = "trace_id"
	SpanIDKey  = "span_id"
)

// traceHandler copies the active trace and span ids onto every record.
//
// Without this, logs and traces are two disconnected stores: an operator looking at
// a slow trace has no way to find the log lines it produced, and vice versa. With
// it, the trace id is the join key between them.
//
// It only works for the *Context variants — slog.InfoContext, ErrorContext, and so
// on — because a plain slog.Info call has no context to read the span from.
type traceHandler struct {
	inner slog.Handler
}

// Enabled reports whether the inner handler wants records at this level.
func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle annotates the record with the active span's identifiers, when there is one.
func (h traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.IsValid() {
		record.AddAttrs(
			slog.String(TraceIDKey, spanCtx.TraceID().String()),
			slog.String(SpanIDKey, spanCtx.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, record)
}

// WithAttrs must rewrap, or the decoration would be dropped by a logger built with
// slog.With.
func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup must rewrap, for the same reason as WithAttrs.
//
// Note that slog applies the open group to every attribute a record carries, so a
// logger derived with WithGroup("req") emits the ids at req.trace_id rather than at
// the top level. This service does not use groups on its service loggers, which
// keeps trace_id at the top level where log collectors expect it.
func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{inner: h.inner.WithGroup(name)}
}

// WithTraceContext wraps a handler so that records logged inside a span carry that
// span's trace and span ids.
//
// Requires: inner is non-nil.
// Ensures:  the returned handler delegates every decision to inner and only adds
//
//	attributes; it never suppresses or rewrites a record.
func WithTraceContext(inner slog.Handler) slog.Handler {
	return traceHandler{inner: inner}
}
