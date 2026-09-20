// Package config reads the service's runtime settings.
package config

import (
	"slices"
	"strconv"
	"strings"
)

// Environment variables recognised by Load.
const (
	EnvPort              = "PORT"
	EnvDatabaseURL       = "DATABASE_URL"
	EnvAppEnv            = "APP_ENV"
	EnvDevMode           = "DEV_MODE"
	EnvAutoMigrate       = "AUTO_MIGRATE"
	EnvDevEmail          = "DEV_EMAIL"
	EnvAuthEnabled       = "AUTH_ENABLED"
	EnvAuthTokens        = "AUTH_TOKENS"
	EnvTrustProxyHeaders = "TRUST_PROXY_HEADERS"
	EnvAllowedOrigins    = "CORS_ALLOWED_ORIGINS"
	EnvTLSCertFile       = "TLS_CERT_FILE"
	EnvTLSKeyFile        = "TLS_KEY_FILE"
	EnvLogLevel          = "LOG_LEVEL"
	EnvLogFormat         = "LOG_FORMAT"
	EnvAdminAddr         = "ADMIN_ADDR"
	EnvTraceSnapshotDir  = "TRACE_SNAPSHOT_DIR"
	EnvRateLimitRPS      = "RATE_LIMIT_RPS"
)

// Defaults applied when the environment says nothing.
const (
	DefaultPort        = "8080"
	DefaultDatabaseURL = "postgres://postgres:password@localhost:5432/pets_db?sslmode=disable"
	DefaultDevEmail    = "developer@local.test"
	DefaultDevToken    = "dev-secret-token"
	DefaultCertFile    = ".certs/cert.pem"
	DefaultKeyFile     = ".certs/key.pem"
	// DefaultAdminAddr binds the admin listener to loopback only. Metrics, pprof, and
	// trace snapshots must never be reachable from outside the host by default.
	DefaultAdminAddr = "127.0.0.1:9090"
	// DefaultRateLimitRPS is the per-instance admission rate. Set RATE_LIMIT_RPS=0 to
	// turn admission control off.
	DefaultRateLimitRPS uint = 200
)

type Config struct {
	Port              string
	DatabaseURL       string
	AuthEnabled       bool
	DevMode           bool
	DevEmail          string
	AuthTokens        []string
	TrustProxyHeaders bool
	AllowedOrigins    []string
	CertFile          string
	KeyFile           string
	AutoMigrate       bool
	LogLevel          string
	LogFormat         string

	// AdminAddr is where metrics, pprof, and trace snapshots are served. Set it to
	// "off" to disable the admin listener entirely.
	AdminAddr string
	// TraceSnapshotDir enables the execution-trace flight recorder and names the
	// directory snapshots are written to. Empty disables the recorder.
	TraceSnapshotDir string
	// RateLimitRPS is the sustained request rate this instance admits. Zero disables
	// admission control.
	RateLimitRPS uint
}

// AdminEnabled reports whether the admin listener should be started.
func (c *Config) AdminEnabled() bool {
	return c.AdminAddr != "" && !strings.EqualFold(c.AdminAddr, "off")
}

// Load builds a Config from the environment exposed by getenv.
//
// Taking getenv as a parameter rather than calling os.Getenv keeps this free of
// process-global state: tests supply a map instead of mutating the environment with
// t.Setenv, which in turn lets them run in parallel.
//
// Development is the default posture, because an unconfigured checkout should run.
// Production is entered by setting APP_ENV=production (or DEV_MODE=false), and in
// that posture no credential, token, or CORS origin is ever invented — an operator
// must name them explicitly.
//
// Requires: getenv behaves like os.Getenv, returning "" for unset names. A nil
//
//	getenv is treated as an empty environment.
//
// Ensures: returns a non-nil Config with every field populated; AllowedOrigins never
//
//	contains "*", which cannot be combined with credentialed CORS.
func Load(getenv func(string) string) *Config {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	devMode := getenv(EnvAppEnv) != "production"
	devMode = boolOr(getenv(EnvDevMode), devMode)

	tokens := getenv(EnvAuthTokens)
	if tokens == "" && devMode {
		tokens = DefaultDevToken
	}

	allowedOrigins := splitNonEmpty(getenv(EnvAllowedOrigins))
	// A credentialed CORS response may not use the "*" wildcard, so drop it rather
	// than emit a configuration the browser will reject.
	allowedOrigins = slices.DeleteFunc(allowedOrigins, func(origin string) bool {
		return origin == "*"
	})
	if len(allowedOrigins) == 0 && devMode {
		allowedOrigins = []string{"https://localhost:4321", "http://localhost:4321"}
	}

	logLevel, logFormat := "info", "json"
	if devMode {
		logLevel, logFormat = "debug", "text"
	}

	return &Config{
		Port:              stringOr(getenv(EnvPort), DefaultPort),
		DatabaseURL:       stringOr(getenv(EnvDatabaseURL), DefaultDatabaseURL),
		AuthEnabled:       boolOr(getenv(EnvAuthEnabled), true),
		DevMode:           devMode,
		DevEmail:          stringOr(getenv(EnvDevEmail), DefaultDevEmail),
		AuthTokens:        splitNonEmpty(tokens),
		TrustProxyHeaders: boolOr(getenv(EnvTrustProxyHeaders), false),
		AllowedOrigins:    allowedOrigins,
		CertFile:          stringOr(getenv(EnvTLSCertFile), DefaultCertFile),
		KeyFile:           stringOr(getenv(EnvTLSKeyFile), DefaultKeyFile),
		AutoMigrate:       boolOr(getenv(EnvAutoMigrate), devMode),
		LogLevel:          stringOr(getenv(EnvLogLevel), logLevel),
		LogFormat:         stringOr(getenv(EnvLogFormat), logFormat),
		AdminAddr:         stringOr(getenv(EnvAdminAddr), DefaultAdminAddr),
		TraceSnapshotDir:  getenv(EnvTraceSnapshotDir),
		RateLimitRPS:      uintOr(getenv(EnvRateLimitRPS), DefaultRateLimitRPS),
	}
}

// Validate reports whether the configuration is safe to serve with.
//
// Ensures: returns an error when authentication is enabled but no credential source
//
//	is configured, which would otherwise reject every request in production.
func (c *Config) Validate() error {
	if c.AuthEnabled && !c.DevMode && !c.TrustProxyHeaders && len(c.AuthTokens) == 0 {
		return errNoCredentialSource
	}
	return nil
}

// splitNonEmpty splits a comma-separated list, discarding blank entries.
func splitNonEmpty(value string) []string {
	var values []string
	for part := range strings.SplitSeq(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}

// stringOr returns value when it is non-empty, otherwise fallback.
func stringOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// boolOr parses value as a bool, returning fallback when it is empty or malformed.
func boolOr(value string, fallback bool) bool {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

// uintOr parses value as an unsigned integer, returning fallback when it is empty or
// malformed. A parsed zero is honoured, because zero means "disabled".
func uintOr(value string, fallback uint) uint {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return fallback
	}
	return uint(parsed)
}
