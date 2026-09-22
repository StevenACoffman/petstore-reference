package telemetry

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	otelpyroscope "github.com/grafana/otel-profiling-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Init initializes the OpenTelemetry TracerProvider and global propagators.
// It returns a shutdown function that flushes and cleans up the TracerProvider.
//
// withProfiling wraps the provider so each span carries the id of the profile
// recorded while it ran, which is what lets a slow trace open as a flame graph.
// Pass false when no profiler is running: the attribute would resolve to nothing.
func Init(ctx context.Context, cfg Config, withProfiling bool) (func(context.Context) error, error) {
	// Set global W3C TraceContext and Baggage propagators.
	// This enables distributed tracing across frontend, connect-rpc, and downstream services.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	res, err := NewResource(ctx, cfg)
	if err != nil {
		return nil, err
	}

	var opts []sdktrace.TracerProviderOption
	opts = append(opts, sdktrace.WithResource(res))

	// Configure sampler: ParentBased ensures remote parent sampling decisions are respected.
	// For root spans (no parent), the sample percentage is used.
	var rootSampler sdktrace.Sampler
	switch {
	case cfg.SamplePercentage >= 100.0:
		rootSampler = sdktrace.AlwaysSample()
	case cfg.SamplePercentage <= 0.0:
		rootSampler = sdktrace.NeverSample()
	default:
		rootSampler = sdktrace.TraceIDRatioBased(cfg.SamplePercentage / 100.0)
	}
	opts = append(opts, sdktrace.WithSampler(sdktrace.ParentBased(rootSampler)))

	switch cfg.ExporterType {
	case "stdout":
		exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("failed to create stdout trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	case "otlp":
		var grpcOpts []otlptracegrpc.Option
		if cfg.OTLPEndpoint != "" {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint))
		}
		if cfg.Insecure {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithInsecure())
		}
		exporter, err := otlptracegrpc.New(ctx, grpcOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to create otlp grpc trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	case "none":
		// No exporter, but tracer provider remains active in memory for context propagation and span creation
	}

	tp := sdktrace.NewTracerProvider(opts...)
	if withProfiling {
		otel.SetTracerProvider(otelpyroscope.NewTracerProvider(tp))
	} else {
		otel.SetTracerProvider(tp)
	}

	// Shut down the real provider; the wrapper holds no resources of its own.
	return tp.Shutdown, nil
}

// NewConnectInterceptor creates a ConnectRPC interceptor instrumented with OpenTelemetry.
// It is configured with WithTrustRemote to adopt frontend-initiated trace contexts (W3C traceparent),
// and WithPropagateResponseHeader to include trace context in RPC responses.
func NewConnectInterceptor() (connect.Interceptor, error) {
	return otelconnect.NewInterceptor(
		otelconnect.WithTrustRemote(),
		otelconnect.WithPropagateResponseHeader(),
	)
}

// NewResource describes this service. Traces and metrics share it so a span and a
// metric series attribute to the same deployment.
func NewResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.ServiceName),
			semconv.ServiceVersionKey.String(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("creating otel resource: %w", err)
	}
	return res, nil
}
