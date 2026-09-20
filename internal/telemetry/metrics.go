package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Metrics holds the metric pipeline and the handler that exposes it.
type Metrics struct {
	// Handler serves the Prometheus scrape endpoint.
	Handler http.Handler
	// Shutdown flushes and stops the meter provider.
	Shutdown func(context.Context) error
}

// InitMetrics installs a global MeterProvider backed by a Prometheus exporter and
// returns the handler that exposes it.
//
// Installing the provider globally is what gives the service its RED metrics without
// any per-handler instrumentation: otelconnect already records request counts,
// durations, and error codes for every RPC, and otelhttp does the same for the photo
// endpoint. Both resolve the global provider, and both stay no-ops until one exists.
// Duration is recorded as a histogram, so p50/p95/p99 are queryable rather than only
// the mean — §14 asks for percentiles because tail latency is what users feel.
//
// The Go and process collectors supply the saturation signal (goroutines, heap, GC,
// file descriptors) to sit alongside rate, errors, and duration.
//
// Requires: res describes this service. Pass the same resource the tracer uses so
//
//	metrics and traces carry matching service attributes.
//
// Ensures: on success the global MeterProvider is installed and Handler is non-nil.
func InitMetrics(res *resource.Resource) (*Metrics, error) {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	exporter, err := promexporter.New(promexporter.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("creating prometheus metric exporter: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(exporter),
	)
	otel.SetMeterProvider(provider)

	return &Metrics{
		Handler: promhttp.HandlerFor(registry, promhttp.HandlerOpts{
			ErrorHandling: promhttp.ContinueOnError,
		}),
		Shutdown: provider.Shutdown,
	}, nil
}
