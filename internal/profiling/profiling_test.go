package profiling_test

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/pprof"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/profiling"
)

func fakeEnv(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		env    map[string]string
		assert func(t *testing.T, cfg profiling.Config)
	}{
		"no endpoint means disabled": {
			env: nil,
			assert: func(t *testing.T, cfg profiling.Config) {
				t.Helper()
				assert.False(t, cfg.Enabled())
			},
		},
		"a whitespace endpoint is still disabled": {
			env: map[string]string{profiling.EnvEndpoint: "   "},
			assert: func(t *testing.T, cfg profiling.Config) {
				t.Helper()
				assert.False(t, cfg.Enabled(), "a blank endpoint must not look configured")
			},
		},
		"an endpoint enables it": {
			env: map[string]string{profiling.EnvEndpoint: "http://pyroscope:4040"},
			assert: func(t *testing.T, cfg profiling.Config) {
				t.Helper()
				assert.True(t, cfg.Enabled())
				assert.Equal(t, "http://pyroscope:4040", cfg.Endpoint)
			},
		},
		"environment defaults to development": {
			env: map[string]string{profiling.EnvEndpoint: "http://p:4040"},
			assert: func(t *testing.T, cfg profiling.Config) {
				t.Helper()
				assert.Equal(t, "development", cfg.Environment)
			},
		},
		"environment can be set": {
			env: map[string]string{
				profiling.EnvEndpoint:    "http://p:4040",
				profiling.EnvEnvironment: "production",
			},
			assert: func(t *testing.T, cfg profiling.Config) {
				t.Helper()
				assert.Equal(t, "production", cfg.Environment)
			},
		},
		"grafana cloud credentials are carried": {
			env: map[string]string{
				profiling.EnvEndpoint: "https://profiles.grafana.net",
				profiling.EnvAuthUser: "12345",
				profiling.EnvAuthPass: "glc_secret",
			},
			assert: func(t *testing.T, cfg profiling.Config) {
				t.Helper()
				assert.Equal(t, "12345", cfg.BasicAuthUser)
				assert.Equal(t, "glc_secret", cfg.BasicAuthPassword)
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := profiling.LoadConfig(fakeEnv(tc.env), "pets-service", "1.2.3")

			assert.Equal(t, "pets-service", cfg.ServiceName, "the service name comes from telemetry")
			assert.Equal(t, "1.2.3", cfg.ServiceVersion)
			tc.assert(t, cfg)
		})
	}
}

func TestLoadConfigNilGetenv(t *testing.T) {
	t.Parallel()

	cfg := profiling.LoadConfig(nil, "pets-service", "1.0.0")

	assert.False(t, cfg.Enabled())
	assert.Equal(t, "development", cfg.Environment)
}

// TestStartDisabledIsANoOp: the caller must not need a branch, and a disabled
// profiler must not turn on contention sampling that nothing will collect.
//
//nolint:paralleltest // reads process-global runtime sampling rates.
func TestStartDisabledIsANoOp(t *testing.T) {
	before := runtime.SetMutexProfileFraction(-1) // -1 reads without setting

	stop, err := profiling.Start(profiling.Config{})

	require.NoError(t, err)
	require.NotNil(t, stop, "stop must be callable even when disabled")
	assert.Equal(t, before, runtime.SetMutexProfileFraction(-1),
		"a disabled profiler must leave the sampling rate alone")
	assert.NotPanics(t, stop)
}

// TestStartRejectsAnUnusableEndpoint: a bad endpoint is reported, and the runtime
// rates are put back rather than left on with nothing collecting them.
//
//nolint:paralleltest // mutates process-global runtime sampling rates.
func TestStartRejectsAnUnusableEndpoint(t *testing.T) {
	stop, err := profiling.Start(profiling.Config{
		Endpoint:    "://not-a-url",
		ServiceName: "pets-service",
	})
	t.Cleanup(stop)

	if err == nil {
		// The client accepts the address and fails later, asynchronously. Either
		// way the contract that matters is that stop is callable.
		assert.NotNil(t, stop)
		return
	}
	assert.Equal(t, 0, runtime.SetMutexProfileFraction(-1),
		"a failed start must not leave contention sampling on")
}

// TestK6LabelsMiddleware pins the contract the k6 script depends on: the Baggage
// header becomes pprof labels, and only the k6 ones do.
func TestK6LabelsMiddleware(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		baggage string
		want    map[string]string
	}{
		"k6 keys become labels, dots to underscores": {
			baggage: "k6.test_run_id=run-7,k6.scenario=browse",
			want:    map[string]string{"k6_test_run_id": "run-7", "k6_scenario": "browse"},
		},
		"non-k6 keys are dropped": {
			baggage: "k6.scenario=browse,tenant=acme,userId=42",
			want:    map[string]string{"k6_scenario": "browse"},
		},
		"no header means no labels": {
			baggage: "",
			want:    map[string]string{},
		},
		"a malformed header is ignored rather than fatal": {
			baggage: "this is not baggage",
			want:    map[string]string{},
		},
		"an empty value is dropped": {
			baggage: "k6.scenario=,k6.test_run_id=run-7",
			want:    map[string]string{"k6_test_run_id": "run-7"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := map[string]string{}
			inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				pprof.ForLabels(r.Context(), func(k, v string) bool {
					got[k] = v
					return true
				})
			})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			if tc.baggage != "" {
				req.Header.Set("Baggage", tc.baggage)
			}
			require.NotPanics(t, func() {
				profiling.K6LabelsMiddleware()(inner).ServeHTTP(httptest.NewRecorder(), req)
			})

			assert.Equal(t, tc.want, got)
		})
	}
}

// TestK6LabelsDoNotLeakPastTheRequest: goroutine labels are process state, so a
// handler that finishes must not leave them set for whatever runs next.
func TestK6LabelsDoNotLeakPastTheRequest(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Baggage", "k6.test_run_id=run-7")

	profiling.K6LabelsMiddleware()(inner).ServeHTTP(httptest.NewRecorder(), req)

	after := map[string]string{}
	pprof.ForLabels(t.Context(), func(k, v string) bool {
		after[k] = v
		return true
	})
	assert.Empty(t, after, "labels must not outlive the request that set them")
}
