package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/example/pets/internal/logging"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		input string
		want  slog.Level
	}{
		"lower case":               {"debug", slog.LevelDebug},
		"upper case":               {"DEBUG", slog.LevelDebug},
		"surrounded by whitespace": {" warn ", slog.LevelWarn},
		"the long spelling":        {"warning", slog.LevelWarn},
		"error":                    {"error", slog.LevelError},
		"info":                     {"info", slog.LevelInfo},
		"empty defaults to info":   {"", slog.LevelInfo},
		"unknown defaults to info": {"nonsense", slog.LevelInfo},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, logging.ParseLevel(tc.input))
		})
	}
}

func TestNewJSONFormat(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logging.New(&buf, "info", "json").Info("hello", "key", "value")

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.Equal(t, "hello", record["msg"])
	assert.Equal(t, "value", record["key"])
}

func TestNewTextFormat(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logging.New(&buf, "info", "text").Info("hello", "key", "value")

	assert.Contains(t, buf.String(), "msg=hello")
	assert.Contains(t, buf.String(), "key=value")
	assert.False(t, json.Valid(bytes.TrimSpace(buf.Bytes())), "text format should not be JSON")
}

func TestNewRespectsLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := logging.New(&buf, "warn", "json")

	logger.Info("suppressed")
	assert.Empty(t, buf.String())

	logger.Warn("emitted")
	assert.Contains(t, buf.String(), "emitted")
}

// TestTraceCorrelation is the reason the handler is wrapped: a log line emitted
// inside a span must carry the ids needed to find that span.
func TestTraceCorrelation(t *testing.T) {
	t.Parallel()

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)

	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(
		trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled},
	))

	var buf bytes.Buffer
	logging.New(&buf, "info", "json").InfoContext(ctx, "in a span")

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", record[logging.TraceIDKey])
	assert.Equal(t, "00f067aa0ba902b7", record[logging.SpanIDKey])
}

func TestTraceCorrelationOmittedOutsideASpan(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logging.New(&buf, "info", "json").InfoContext(context.Background(), "no span here")

	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.NotContains(t, record, logging.TraceIDKey)
	assert.NotContains(t, record, logging.SpanIDKey)
}

// TestTraceCorrelationSurvivesWith pins the rewrapping in WithAttrs and WithGroup: a
// derived logger must keep the decoration. It also pins where the ids land once a
// group is open — slog nests every attribute under the group, so they move with it.
func TestTraceCorrelationSurvivesWith(t *testing.T) {
	t.Parallel()

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(
		trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled},
	))

	var buf bytes.Buffer
	base := logging.New(&buf, "info", "json").With("component", "test")

	// Without a group the ids sit at the top level, which is what collectors want.
	base.InfoContext(ctx, "still correlated")
	var record map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))
	assert.Equal(t, "test", record["component"])
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", record[logging.TraceIDKey])

	// With a group open, slog nests every record attribute under it, the correlation
	// ids included. Documented in WithGroup; asserted here so it cannot change silently.
	buf.Reset()
	base.WithGroup("req").InfoContext(ctx, "correlated inside a group")
	var grouped map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &grouped))
	group, ok := grouped["req"].(map[string]any)
	require.True(t, ok, "expected a req group, got %v", grouped)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", group[logging.TraceIDKey])
}
