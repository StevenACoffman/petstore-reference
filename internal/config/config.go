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
	EnvDevRoles          = "DEV_ROLES"
	EnvAuthzPolicy       = "AUTHZ_POLICY"
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
	// DefaultAdminAddr is loopback: pprof must not be reachable off-host by default.
	DefaultAdminAddr = "127.0.0.1:9090"
	// DefaultRateLimitRPS per instance; RATE_LIMIT_RPS=0 disables it.
	DefaultRateLimitRPS uint = 200
)

type Config struct {
	Port        string
	DatabaseURL string
	AuthEnabled bool
	DevMode     bool
	DevEmail    string
	// DevRoles is the local development identity's role set. Narrow it to feel what
	// a non-admin caller feels.
	DevRoles          []string
	AuthTokens        []string
	TrustProxyHeaders bool
	AllowedOrigins    []string
	CertFile          string
	KeyFile           string
	AutoMigrate       bool
	LogLevel          string
	LogFormat         string

	// AdminAddr serves metrics, pprof and trace snapshots; "off" disables it.
	AdminAddr string
	// TraceSnapshotDir enables the flight recorder and names its output directory.
	TraceSnapshotDir string
	// RateLimitRPS admitted per instance; zero disables admission control.
	RateLimitRPS uint
	// AuthzPolicy is the raw role matrix, procedure=role[,role] separated by
	// semicolons or newlines. Empty denies everything but admin.
	AuthzPolicy string
}

// AdminEnabled reports whether the admin listener should be started.
func (c *Config) AdminEnabled() bool {
	return c.AdminAddr != "" && !strings.EqualFold(c.AdminAddr, "off")
}

// Load builds a Config from the environment exposed by getenv.
//
// getenv is a parameter, not os.Getenv, so tests pass a map instead of calling
// t.Setenv and can run in parallel. A nil getenv means an empty environment.
//
// Development is the default so an unconfigured checkout runs. APP_ENV=production
// (or DEV_MODE=false) switches posture, and there no credential, token, or CORS
// origin is ever invented — an operator must name them.
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
	// Credentialed CORS forbids "*", so drop it rather than emit a config the
	// browser will reject.
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
		DevRoles:          splitNonEmpty(getenv(EnvDevRoles)),
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
		AuthzPolicy:       getenv(EnvAuthzPolicy),
	}
}

// Validate rejects a configuration that would refuse every request: authentication
// enabled in production with no credential source.
func (c *Config) Validate() error {
	if c.AuthEnabled && !c.DevMode && !c.TrustProxyHeaders && len(c.AuthTokens) == 0 {
		return errNoCredentialSource
	}
	return nil
}

// splitNonEmpty splits a comma-separated list, discarding blanks.
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

// boolOr parses a bool, falling back when empty or malformed.
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

// uintOr parses an unsigned integer, falling back when empty or malformed. A
// parsed zero is honoured: zero means "disabled".
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
