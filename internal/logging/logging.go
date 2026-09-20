// Package logging builds the structured logger used by the service binaries.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// ParseLevel maps a configured level name onto a slog.Level.
//
// Requires: name is a level name in any case, or "" .
// Ensures:  returns the matching level, or slog.LevelInfo for an empty or
//
//	unrecognised name. Never fails — an operator typo degrades to the
//	default level rather than preventing the process from starting.
func ParseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New builds a slog.Logger writing to w.
//
// A format of "json" selects slog's JSON handler, which is what production log
// collectors want; anything else selects the human-readable text handler. The
// handler is wrapped so records logged with a context carry trace correlation ids;
// see WithTraceContext.
//
// Requires: w is non-nil.
// Ensures:  returns a non-nil logger and mutates no global state — callers decide
//
//	whether to install it with slog.SetDefault.
func New(w io.Writer, level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: ParseLevel(level)}

	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	// Every record logged with a context inside a span carries that span's ids, so
	// logs and traces can be joined on trace_id.
	return slog.New(WithTraceContext(handler))
}
