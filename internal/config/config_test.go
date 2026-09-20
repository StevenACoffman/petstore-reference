package config_test

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/config"
)

// fakeEnv returns a getenv function backed by a map, so these tests never mutate the
// process environment and can run in parallel.
func fakeEnv(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// production is the smallest environment that puts Load into a production posture.
func production(extra map[string]string) map[string]string {
	env := map[string]string{config.EnvAppEnv: "production"}
	maps.Copy(env, extra)
	return env
}

func TestLoad(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		env    map[string]string
		assert func(t *testing.T, cfg *config.Config)
	}{
		"production invents no credentials": {
			env: production(nil),
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.False(t, cfg.DevMode)
				assert.Empty(t, cfg.AuthTokens)
				assert.False(t, cfg.TrustProxyHeaders)
				assert.Empty(t, cfg.AllowedOrigins)
			},
		},
		"production honours explicit security settings": {
			env: production(map[string]string{
				config.EnvAuthTokens:        " first-token, ,second-token ",
				config.EnvTrustProxyHeaders: "true",
				config.EnvAllowedOrigins:    "https://app.example.com, https://admin.example.com",
			}),
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.Equal(t, []string{"first-token", "second-token"}, cfg.AuthTokens)
				assert.True(t, cfg.TrustProxyHeaders)
				assert.Equal(t, []string{"https://app.example.com", "https://admin.example.com"}, cfg.AllowedOrigins)
			},
		},
		"a wildcard origin is dropped because credentialed CORS forbids it": {
			env: production(map[string]string{config.EnvAllowedOrigins: "*"}),
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.Empty(t, cfg.AllowedOrigins)
			},
		},
		"a wildcard is dropped but its siblings survive": {
			env: production(map[string]string{
				config.EnvAllowedOrigins: "https://app.example.com,*",
			}),
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.Equal(t, []string{"https://app.example.com"}, cfg.AllowedOrigins)
			},
		},
		"development defaults make an unconfigured checkout runnable": {
			env: map[string]string{config.EnvAppEnv: "development"},
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.True(t, cfg.DevMode)
				assert.True(t, cfg.AutoMigrate)
				assert.True(t, cfg.AuthEnabled)
				assert.Equal(t, "8080", cfg.Port)
				assert.Equal(t, "developer@local.test", cfg.DevEmail)
				assert.Equal(t, []string{"dev-secret-token"}, cfg.AuthTokens)
				assert.Equal(t, []string{"https://localhost:4321", "http://localhost:4321"}, cfg.AllowedOrigins)
				assert.Equal(t, "debug", cfg.LogLevel)
				assert.Equal(t, "text", cfg.LogFormat)
			},
		},
		"an empty environment defaults to development": {
			env: nil,
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.True(t, cfg.DevMode)
				assert.Equal(t, "8080", cfg.Port)
			},
		},
		"every value can be overridden": {
			env: map[string]string{
				config.EnvPort:        "9090",
				config.EnvDatabaseURL: "postgres://custom:5432/db",
				config.EnvDevMode:     "false",
				config.EnvAutoMigrate: "false",
				config.EnvAuthEnabled: "false",
				config.EnvDevEmail:    "custom@example.com",
				config.EnvTLSCertFile: "custom-cert.pem",
				config.EnvTLSKeyFile:  "custom-key.pem",
				config.EnvLogLevel:    "warn",
				config.EnvLogFormat:   "json",
			},
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.Equal(t, "9090", cfg.Port)
				assert.Equal(t, "postgres://custom:5432/db", cfg.DatabaseURL)
				assert.False(t, cfg.DevMode)
				assert.False(t, cfg.AutoMigrate)
				assert.False(t, cfg.AuthEnabled)
				assert.Equal(t, "custom@example.com", cfg.DevEmail)
				assert.Equal(t, "custom-cert.pem", cfg.CertFile)
				assert.Equal(t, "custom-key.pem", cfg.KeyFile)
				assert.Equal(t, "warn", cfg.LogLevel)
				assert.Equal(t, "json", cfg.LogFormat)
			},
		},
		"DEV_MODE overrides APP_ENV": {
			env: production(map[string]string{config.EnvDevMode: "true"}),
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.True(t, cfg.DevMode)
			},
		},
		"a malformed boolean falls back to the default rather than failing": {
			env: map[string]string{config.EnvAuthEnabled: "yes-please"},
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				assert.True(t, cfg.AuthEnabled)
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Load(fakeEnv(tc.env))

			require.NotNil(t, cfg)
			tc.assert(t, cfg)
		})
	}
}

func TestLoadNilGetenvIsTreatedAsEmptyEnvironment(t *testing.T) {
	t.Parallel()

	cfg := config.Load(nil)

	require.NotNil(t, cfg)
	assert.True(t, cfg.DevMode)
	assert.Equal(t, config.DefaultPort, cfg.Port)
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		env     map[string]string
		wantErr bool
	}{
		"production with no credential source is rejected": {
			env:     production(nil),
			wantErr: true,
		},
		"production with static tokens is accepted": {
			env:     production(map[string]string{config.EnvAuthTokens: "a-token"}),
			wantErr: false,
		},
		"production behind a trusted proxy is accepted": {
			env:     production(map[string]string{config.EnvTrustProxyHeaders: "true"}),
			wantErr: false,
		},
		"production with authentication disabled is accepted": {
			env:     production(map[string]string{config.EnvAuthEnabled: "false"}),
			wantErr: false,
		},
		"development is always accepted": {
			env:     nil,
			wantErr: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := config.Load(fakeEnv(tc.env)).Validate()

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
