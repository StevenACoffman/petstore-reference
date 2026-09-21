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
	Handler  http.Handler                // the Prometheus scrape endpoint
	Shutdown func(context.Context) error // flushes and stops the provider
}

// InitMetrics installs a global MeterProvider backed by Prometheus and returns the
// handler exposing it.
//
// Installing it globally is what gives the service RED metrics for free:
// otelconnect already records rate, errors and duration per RPC, and stays a no-op
// until a provider exists. Duration is a histogram, so p50/p95/p99 are queryable
// rather than only the mean. The Go and process collectors add saturation.
//
// Pass the same resource the tracer uses, so metrics and traces match.
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
