package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/gen/go/pet/v2/petv2connect"
)

// fakeEnv returns a getenv function backed by a map, so config tests never touch
// the process environment and can therefore run in parallel.
func fakeEnv(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func assertConfig(t *testing.T, want, got Config) {
	t.Helper()
	assert.Equal(t, want.ServiceName, got.ServiceName, "ServiceName")
	assert.Equal(t, want.ServiceVersion, got.ServiceVersion, "ServiceVersion")
	assert.Equal(t, want.OTLPEndpoint, got.OTLPEndpoint, "OTLPEndpoint")
	assert.Equal(t, want.Insecure, got.Insecure, "Insecure")
	assert.Equal(t, want.ExporterType, got.ExporterType, "ExporterType")
	assert.InDelta(t, want.SamplePercentage, got.SamplePercentage, 1e-9, "SamplePercentage")
}

func TestLoadConfig_Environment(t *testing.T) {
	t.Parallel()

	base := DefaultConfig()
	base.ExporterType = "none"

	withDefaults := func(mutate func(*Config)) Config {
		cfg := base
		mutate(&cfg)
		return cfg
	}

	cases := map[string]struct {
		env  map[string]string
		want Config
	}{
		"empty environment yields defaults": {
			env:  nil,
			want: base,
		},
		"service identity and endpoint imply the otlp exporter": {
			env: map[string]string{
				EnvServiceName:   "custom-petstore",
				EnvOTLPEndpoint:  "localhost:4317",
				EnvSamplePercent: "25%",
			},
			want: withDefaults(func(c *Config) {
				c.ServiceName = "custom-petstore"
				c.OTLPEndpoint = "localhost:4317"
				c.ExporterType = "otlp"
				c.SamplePercentage = 25
			}),
		},
		"zero sample percentage is honoured, not treated as unset": {
			env:  map[string]string{EnvSamplePercent: "0"},
			want: withDefaults(func(c *Config) { c.SamplePercentage = 0 }),
		},
		"sampler arg below one is a ratio": {
			env:  map[string]string{EnvSamplerArg: "0.5"},
			want: withDefaults(func(c *Config) { c.SamplePercentage = 50 }),
		},
		"sampler arg above one is already a percentage": {
			env:  map[string]string{EnvSamplerArg: "25"},
			want: withDefaults(func(c *Config) { c.SamplePercentage = 25 }),
		},
		"explicit sample percentage wins over sampler arg": {
			env: map[string]string{
				EnvSamplePercent: "10",
				EnvSamplerArg:    "0.9",
			},
			want: withDefaults(func(c *Config) { c.SamplePercentage = 10 }),
		},
		"unparsable sample percentage leaves the default in place": {
			env:  map[string]string{EnvSamplePercent: "banana"},
			want: base,
		},
		"sample percentage above the range is clamped": {
			env:  map[string]string{EnvSamplePercent: "500"},
			want: withDefaults(func(c *Config) { c.SamplePercentage = 100 }),
		},
		"negative sample percentage is clamped to zero": {
			env:  map[string]string{EnvSamplePercent: "-5"},
			want: withDefaults(func(c *Config) { c.SamplePercentage = 0 }),
		},
		"insecure can be turned off": {
			env:  map[string]string{EnvOTLPInsecure: "false"},
			want: withDefaults(func(c *Config) { c.Insecure = false }),
		},
		"exporter name is normalised to lower case": {
			env:  map[string]string{EnvTracesExporter: "STDOUT"},
			want: withDefaults(func(c *Config) { c.ExporterType = "stdout" }),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg, err := LoadConfig(fakeEnv(tc.env))

			require.NoError(t, err)
			assertConfig(t, tc.want, cfg)
		})
	}
}

func TestLoadConfig_NilGetenvIsTreatedAsEmptyEnvironment(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(nil)

	require.NoError(t, err)
	assert.Equal(t, "pets-service", cfg.ServiceName)
	assert.Equal(t, "none", cfg.ExporterType)
}

func TestLoadConfig_FromConfigFile(t *testing.T) {
	t.Parallel()

	const yamlContent = `
service_name: "yaml-pets-service"
service_version: "2.1.0"
exporter_type: "stdout"
sample_percentage: 45.5
insecure: false
`
	writeConfig := func(t *testing.T, name, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}

	t.Run("file supplies every field", func(t *testing.T) {
		t.Parallel()
		path := writeConfig(t, "telemetry.yaml", yamlContent)

		cfg, err := LoadConfig(fakeEnv(nil), path)

		require.NoError(t, err)
		assertConfig(t, Config{
			ServiceName:      "yaml-pets-service",
			ServiceVersion:   "2.1.0",
			OTLPEndpoint:     "",
			Insecure:         false,
			ExporterType:     "stdout",
			SamplePercentage: 45.5,
		}, cfg)
	})

	t.Run("environment overrides the file field by field", func(t *testing.T) {
		t.Parallel()
		path := writeConfig(t, "telemetry.yaml", yamlContent)

		cfg, err := LoadConfig(fakeEnv(map[string]string{
			EnvServiceName:   "env-override-service",
			EnvSamplePercent: "75",
		}), path)

		require.NoError(t, err)
		assert.Equal(t, "env-override-service", cfg.ServiceName)
		assert.InDelta(t, 75.0, cfg.SamplePercentage, 1e-9)
		// Fields the environment did not mention still come from the file.
		assert.Equal(t, "2.1.0", cfg.ServiceVersion)
		assert.Equal(t, "stdout", cfg.ExporterType)
	})

	t.Run("file can be named by OTEL_CONFIG_FILE", func(t *testing.T) {
		t.Parallel()
		path := writeConfig(t, "telemetry.yaml", yamlContent)

		cfg, err := LoadConfig(fakeEnv(map[string]string{EnvConfigFile: path}))

		require.NoError(t, err)
		assert.Equal(t, "yaml-pets-service", cfg.ServiceName)
		assert.InDelta(t, 45.5, cfg.SamplePercentage, 1e-9)
	})

	t.Run("file can be named by CONFIG_FILE", func(t *testing.T) {
		t.Parallel()
		path := writeConfig(t, "telemetry.yaml", yamlContent)

		cfg, err := LoadConfig(fakeEnv(map[string]string{EnvFallbackFile: path}))

		require.NoError(t, err)
		assert.Equal(t, "yaml-pets-service", cfg.ServiceName)
	})

	t.Run("a missing file is not an error", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "absent.yaml")

		cfg, err := LoadConfig(fakeEnv(nil), missing)

		require.NoError(t, err)
		assertConfig(t, Config{
			ServiceName:      "pets-service",
			ServiceVersion:   "1.0.0",
			Insecure:         true,
			ExporterType:     "none",
			SamplePercentage: 100,
		}, cfg)
	})

	t.Run("a malformed file reports an error but still yields a usable config", func(t *testing.T) {
		t.Parallel()
		path := writeConfig(t, "telemetry.yaml", "service_name: [this is not a string")

		cfg, err := LoadConfig(fakeEnv(map[string]string{EnvServiceName: "from-env"}), path)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading telemetry config")
		// The environment overlay still ran, so the caller can carry on.
		assert.Equal(t, "from-env", cfg.ServiceName)
		assert.Equal(t, "none", cfg.ExporterType)
	})
}

func TestParseSamplePercentage(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		pct, samplerArg string
		want            float64
		wantOK          bool
	}{
		"both unset":            {"", "", 0, false},
		"plain number":          {"42", "", 42, true},
		"trailing percent":      {"42%", "", 42, true},
		"surrounding space":     {"  42 % ", "", 42, true},
		"zero":                  {"0", "", 0, true},
		"unparsable":            {"banana", "", 0, false},
		"unparsable wins":       {"banana", "0.5", 0, false},
		"ratio from sampler":    {"", "0.25", 25, true},
		"one is a full ratio":   {"", "1", 100, true},
		"above one stays as is": {"", "40", 40, true},
		"clamped high":          {"1000", "", 100, true},
		"clamped low":           {"-1", "", 0, true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseSamplePercentage(tc.pct, tc.samplerArg)

			assert.Equal(t, tc.wantOK, ok)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

//nolint:paralleltest // Init calls otel.SetTracerProvider, which is process-global: concurrent cases would each install a provider the others then observe.
func TestInit_Sampling(t *testing.T) {
	ctx := context.Background()

	t.Run("0% sampling drops root spans but respects remote parent", func(t *testing.T) {
		cfg := Config{
			ServiceName:      "test-sampler-0",
			ServiceVersion:   "1.0.0",
			ExporterType:     "none",
			SamplePercentage: 0.0,
		}
		shutdown, err := Init(ctx, cfg, false)
		require.NoError(t, err)
		defer func() { _ = shutdown(ctx) }()

		tracer := otel.Tracer("test")

		// Root span should NOT be sampled with 0%
		_, rootSpan := tracer.Start(ctx, "root-span")
		assert.False(t, rootSpan.SpanContext().IsSampled())
		rootSpan.End()

		// Remote sampled parent should STILL be sampled (ParentBased)
		remoteSpanContext := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
			SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
			TraceFlags: trace.FlagsSampled,
			Remote:     true,
		})
		parentCtx := trace.ContextWithRemoteSpanContext(ctx, remoteSpanContext)
		_, childSpan := tracer.Start(parentCtx, "child-span")
		assert.True(t, childSpan.SpanContext().IsSampled(), "expected ParentBased to respect remote sampled parent")
		childSpan.End()
	})

	t.Run("100% sampling samples root spans", func(t *testing.T) {
		cfg := Config{
			ServiceName:      "test-sampler-100",
			ServiceVersion:   "1.0.0",
			ExporterType:     "none",
			SamplePercentage: 100.0,
		}
		shutdown, err := Init(ctx, cfg, false)
		require.NoError(t, err)
		defer func() { _ = shutdown(ctx) }()

		tracer := otel.Tracer("test")
		_, rootSpan := tracer.Start(ctx, "root-span")
		assert.True(t, rootSpan.SpanContext().IsSampled())
		rootSpan.End()
	})
}

//nolint:paralleltest // Init calls otel.SetTracerProvider, which is process-global: concurrent cases would each install a provider the others then observe.
func TestInit(t *testing.T) {
	ctx := context.Background()

	t.Run("none exporter", func(t *testing.T) {
		cfg := Config{
			ServiceName:    "test-service",
			ServiceVersion: "1.0.0",
			ExporterType:   "none",
		}
		shutdown, err := Init(ctx, cfg, false)
		require.NoError(t, err)
		require.NotNil(t, shutdown)
		err = shutdown(ctx)
		require.NoError(t, err)
	})

	t.Run("stdout exporter", func(t *testing.T) {
		cfg := Config{
			ServiceName:    "test-service",
			ServiceVersion: "1.0.0",
			ExporterType:   "stdout",
		}
		shutdown, err := Init(ctx, cfg, false)
		require.NoError(t, err)
		require.NotNil(t, shutdown)
		err = shutdown(ctx)
		require.NoError(t, err)
	})
}

//nolint:paralleltest // installs a global TracerProvider and propagator via otel.Set*, so it cannot share the process with another tracing test.
func TestInitAndConnectInterceptor(t *testing.T) {
	ctx := context.Background()

	// Use an in-memory span recorder to verify trace extraction
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	interceptor, err := NewConnectInterceptor()
	require.NoError(t, err)
	require.NotNil(t, interceptor)

	// Create a dummy service to test interceptor with incoming W3C traceparent
	dummySvc := &mockPetService{}
	_, handler := petv2connect.NewPetServiceHandler(
		dummySvc,
		connect.WithInterceptors(interceptor),
	)

	server := httptest.NewServer(handler)
	defer server.Close()

	client := petv2connect.NewPetServiceClient(server.Client(), server.URL)

	// Simulate frontend-created W3C traceparent header
	frontendTraceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	frontendSpanID := "00f067aa0ba902b7"
	traceparent := "00-" + frontendTraceID + "-" + frontendSpanID + "-01"

	req := connect.NewRequest(&petv2.GetPetRequest{Id: "test-id"})
	req.Header().Set("traceparent", traceparent)

	_, err = client.GetPet(ctx, req)
	require.NoError(t, err)

	spans := spanRecorder.Ended()
	require.NotEmpty(t, spans)

	// Verify that the backend span adopted the frontend trace ID
	var foundAdoptedTrace bool
	for _, span := range spans {
		if span.SpanContext().TraceID().String() == frontendTraceID {
			foundAdoptedTrace = true
			assert.Equal(t, frontendSpanID, span.Parent().SpanID().String())
			break
		}
	}

	assert.True(t, foundAdoptedTrace, "expected backend span to adopt frontend trace ID %s", frontendTraceID)
}

// mockPetService implements petv2connect.PetServiceHandler for testing
type mockPetService struct {
	petv2connect.UnimplementedPetServiceHandler
}

func (m *mockPetService) GetPet(ctx context.Context, req *connect.Request[petv2.GetPetRequest]) (*connect.Response[petv2.GetPetResponse], error) {
	// Span should be active in context
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return nil, connect.NewError(connect.CodeInternal, errors.New("no valid span in context"))
	}
	return connect.NewResponse(&petv2.GetPetResponse{
		Pet: &petv2.Pet{Id: req.Msg.GetId(), Name: "Fido"},
	}), nil
}

// TestInitWrapsTheProviderForProfiling pins the correlation wiring: with profiling
// on, the installed provider is the Pyroscope wrapper, so spans carry a profile id.
func TestInitWrapsTheProviderForProfiling(t *testing.T) { //nolint:paralleltest // installs a global TracerProvider.
	ctx := t.Context()
	cfg := Config{ServiceName: "wrap-test", ServiceVersion: "1.0.0", ExporterType: "none"}

	shutdownPlain, err := Init(ctx, cfg, false)
	require.NoError(t, err)
	plain := fmt.Sprintf("%T", otel.GetTracerProvider())
	require.NoError(t, shutdownPlain(ctx))

	shutdownWrapped, err := Init(ctx, cfg, true)
	require.NoError(t, err)
	wrapped := fmt.Sprintf("%T", otel.GetTracerProvider())
	require.NoError(t, shutdownWrapped(ctx))

	assert.NotEqual(t, plain, wrapped, "profiling must install a different provider")
	assert.Contains(t, wrapped, "pyroscope", "want the pyroscope wrapper, got %s", wrapped)
}
