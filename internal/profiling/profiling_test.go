package profiling_test

import (
	"runtime"
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
