// Package profiling pushes continuous CPU and memory profiles to Pyroscope.
//
// Continuous profiling answers what an on-demand pprof dump cannot: why the
// service was slow twenty minutes ago, when nobody was holding a profiler open.
// Profiles are tagged with the same service name and version the traces carry, so
// a slow span can be opened as the flame graph recorded while it ran.
package profiling

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/grafana/pyroscope-go"
	// x/k6 is an experimental module of pyroscope-go; it carries no compatibility
	// promise, which is the trade for not reimplementing its baggage parsing.
	k6 "github.com/grafana/pyroscope-go/x/k6"
)

// Environment variables recognised by LoadConfig.
const (
	EnvEndpoint    = "PYROSCOPE_ENDPOINT"
	EnvAuthUser    = "PYROSCOPE_BASIC_AUTH_USER"
	EnvAuthPass    = "PYROSCOPE_BASIC_AUTH_PASSWORD"
	EnvEnvironment = "DEPLOYMENT_ENVIRONMENT"
)

// Sampling rates for the two profile types Go collects only when asked.
//
// Both default to zero, which means the mutex and block profiles exist but are
// always empty. Sampling one contention event in five is Pyroscope's own
// recommendation: enough signal to find a hot lock, little enough overhead to
// leave on in production.
const (
	mutexProfileFraction = 5
	blockProfileRate     = 5
)

// Config describes where profiles go and how they are labelled.
type Config struct {
	// Endpoint is the Pyroscope server. Empty disables profiling entirely.
	Endpoint string
	// ServiceName and ServiceVersion are taken from the telemetry config, so a
	// profile and a trace agree on which deployment they came from.
	ServiceName    string
	ServiceVersion string
	// Environment tags profiles so production and staging stay distinguishable.
	Environment string
	// BasicAuthUser and BasicAuthPassword authenticate to Grafana Cloud.
	BasicAuthUser     string
	BasicAuthPassword string
}

// Enabled reports whether profiles should be pushed.
func (c Config) Enabled() bool { return strings.TrimSpace(c.Endpoint) != "" }

// LoadConfig reads the profiling settings from the environment exposed by getenv.
//
// serviceName and serviceVersion come from the telemetry config rather than their
// own variables: one name for one concept. A nil getenv means no environment.
func LoadConfig(getenv func(string) string, serviceName, serviceVersion string) Config {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	environment := strings.TrimSpace(getenv(EnvEnvironment))
	if environment == "" {
		environment = "development"
	}
	return Config{
		Endpoint:          strings.TrimSpace(getenv(EnvEndpoint)),
		ServiceName:       serviceName,
		ServiceVersion:    serviceVersion,
		Environment:       environment,
		BasicAuthUser:     getenv(EnvAuthUser),
		BasicAuthPassword: getenv(EnvAuthPass),
	}
}

// profileTypes is everything Pyroscope can collect from a Go process: the five it
// gathers by default, plus the goroutine, mutex and block profiles that need the
// runtime rates set above.
func profileTypes() []pyroscope.ProfileType {
	return []pyroscope.ProfileType{
		pyroscope.ProfileCPU,
		pyroscope.ProfileAllocObjects,
		pyroscope.ProfileAllocSpace,
		pyroscope.ProfileInuseObjects,
		pyroscope.ProfileInuseSpace,
		pyroscope.ProfileGoroutines,
		pyroscope.ProfileMutexCount,
		pyroscope.ProfileMutexDuration,
		pyroscope.ProfileBlockCount,
		pyroscope.ProfileBlockDuration,
	}
}

// Start begins pushing profiles and returns a function that stops them.
//
// A disabled config is not an error: the returned stop is a usable no-op, so the
// caller needs no branch and the runtime sampling rates stay at zero.
//
// Mutates process-global runtime state, so it belongs in main's wiring rather
// than in a library path.
func Start(cfg Config) (stop func(), err error) {
	if !cfg.Enabled() {
		return func() {}, nil
	}

	// Set before Start: the profiler reads these rates when it first collects.
	runtime.SetMutexProfileFraction(mutexProfileFraction)
	runtime.SetBlockProfileRate(blockProfileRate)

	profiler, err := pyroscope.Start(pyroscope.Config{
		ApplicationName:   cfg.ServiceName,
		ServerAddress:     cfg.Endpoint,
		BasicAuthUser:     cfg.BasicAuthUser,
		BasicAuthPassword: cfg.BasicAuthPassword,
		ProfileTypes:      profileTypes(),
		Tags: map[string]string{
			"service_version": cfg.ServiceVersion,
			"environment":     cfg.Environment,
		},
	})
	if err != nil {
		// Leave the rates as they were; nothing is collecting them now.
		runtime.SetMutexProfileFraction(0)
		runtime.SetBlockProfileRate(0)
		return func() {}, fmt.Errorf("starting pyroscope profiler: %w", err)
	}

	return func() {
		_ = profiler.Stop()
		runtime.SetMutexProfileFraction(0)
		runtime.SetBlockProfileRate(0)
	}, nil
}

// K6LabelsMiddleware tags profile samples with the k6 test run and scenario that
// produced them, by reading the Baggage header.
//
// k6 does not send that header on its own — the test script sets it. Only
// `k6.`-prefixed keys are kept, with dots rewritten to underscores, so
// `k6.test_run_id` becomes the label `k6_test_run_id`.
//
// Costs ~13ns per request with no Baggage header, so it is applied
// unconditionally; the labels also reach anything reading pprof directly, not
// only a Pyroscope push.
func K6LabelsMiddleware() func(http.Handler) http.Handler {
	return k6.LabelsFromBaggageHandler
}
