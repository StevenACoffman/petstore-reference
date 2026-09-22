package telemetry

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ilyakaznacheev/cleanenv"
)

// Environment variables recognised by LoadConfig.
const (
	EnvServiceName    = "OTEL_SERVICE_NAME"
	EnvServiceVersion = "OTEL_SERVICE_VERSION"
	EnvOTLPEndpoint   = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvOTLPInsecure   = "OTEL_EXPORTER_OTLP_INSECURE"
	EnvTracesExporter = "OTEL_TRACES_EXPORTER"
	EnvSamplePercent  = "OTEL_SAMPLE_PERCENTAGE"
	EnvSamplerArg     = "OTEL_TRACES_SAMPLER_ARG"
	EnvConfigFile     = "OTEL_CONFIG_FILE"
	EnvFallbackFile   = "CONFIG_FILE"
)

// Config holds the OpenTelemetry settings. The tags describe the config-file shape
// only; the environment is applied separately by applyEnv.
type Config struct {
	ServiceName      string  `json:"service_name"      toml:"service_name"      yaml:"service_name"`
	ServiceVersion   string  `json:"service_version"   toml:"service_version"   yaml:"service_version"`
	OTLPEndpoint     string  `json:"otlp_endpoint"     toml:"otlp_endpoint"     yaml:"otlp_endpoint"`
	Insecure         bool    `json:"insecure"          toml:"insecure"          yaml:"insecure"`
	ExporterType     string  `json:"exporter_type"     toml:"exporter_type"     yaml:"exporter_type"`
	SamplePercentage float64 `json:"sample_percentage" toml:"sample_percentage" yaml:"sample_percentage"`
}

// DefaultConfig is the configuration before any override.
func DefaultConfig() Config {
	return Config{
		ServiceName:      "pets-service",
		ServiceVersion:   "1.0.0",
		Insecure:         true,
		SamplePercentage: 100.0,
	}
}

// LoadConfig builds a Config from defaults, then an optional file, then the
// environment. The file comes from configPath, OTEL_CONFIG_FILE or CONFIG_FILE; a
// missing one is not an error, a malformed one is reported but still leaves the
// Config usable.
//
// A nil getenv means "no environment". Touches no process-global state, so it is
// safe to call concurrently.
func LoadConfig(getenv func(string) string, configPath ...string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	cfg := DefaultConfig()

	var err error
	if path := resolveConfigPath(getenv, configPath...); path != "" {
		// Config carries no `env` tags, so ReadConfig's environment pass is a no-op
		// and only the file is applied here; applyEnv handles the rest.
		if readErr := cleanenv.ReadConfig(path, &cfg); readErr != nil {
			if !errors.Is(readErr, fs.ErrNotExist) {
				err = fmt.Errorf("reading telemetry config %q: %w", path, readErr)
			}
		}
	}

	applyEnv(&cfg, getenv)

	if cfg.ExporterType == "" {
		if cfg.OTLPEndpoint != "" {
			cfg.ExporterType = "otlp"
		} else {
			cfg.ExporterType = "none"
		}
	}

	return cfg, err
}

// resolveConfigPath picks the config file, preferring the argument over the
// environment, and cleans it. Returns "" when none was named.
func resolveConfigPath(getenv func(string) string, configPath ...string) string {
	if len(configPath) > 0 && configPath[0] != "" {
		return filepath.Clean(configPath[0])
	}
	if path := getenv(EnvConfigFile); path != "" {
		return filepath.Clean(path)
	}
	if path := getenv(EnvFallbackFile); path != "" {
		return filepath.Clean(path)
	}
	return ""
}

// applyEnv overlays the environment; an unset or unparsable variable is ignored.
func applyEnv(cfg *Config, getenv func(string) string) {
	if v := strings.TrimSpace(getenv(EnvServiceName)); v != "" {
		cfg.ServiceName = v
	}
	if v := strings.TrimSpace(getenv(EnvServiceVersion)); v != "" {
		cfg.ServiceVersion = v
	}
	if v := strings.TrimSpace(getenv(EnvOTLPEndpoint)); v != "" {
		cfg.OTLPEndpoint = v
	}
	if v := strings.TrimSpace(getenv(EnvTracesExporter)); v != "" {
		cfg.ExporterType = strings.ToLower(v)
	}
	if v := strings.TrimSpace(getenv(EnvOTLPInsecure)); v != "" {
		if parsed, parseErr := strconv.ParseBool(v); parseErr == nil {
			cfg.Insecure = parsed
		}
	}
	if pct, ok := parseSamplePercentage(getenv(EnvSamplePercent), getenv(EnvSamplerArg)); ok {
		cfg.SamplePercentage = pct
	}
}

// parseSamplePercentage resolves the sampling rate from its two spellings.
// OTEL_SAMPLE_PERCENTAGE is ours and accepts a trailing "%";
// OTEL_TRACES_SAMPLER_ARG is the OTel standard and carries a ratio in (0, 1].
//
// Returns (rate in [0,100], true), preferring pct, or (0, false) so the caller
// keeps what it had.
func parseSamplePercentage(pct, samplerArg string) (float64, bool) {
	if raw := strings.TrimSpace(pct); raw != "" {
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "%"))
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, false
		}
		return clampPercentage(parsed), true
	}

	if raw := strings.TrimSpace(samplerArg); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, false
		}
		if parsed > 0 && parsed <= 1.0 {
			parsed *= 100.0
		}
		return clampPercentage(parsed), true
	}

	return 0, false
}

// clampPercentage confines v to [0, 100]; NaN becomes 0.
func clampPercentage(v float64) float64 {
	switch {
	case math.IsNaN(v), v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}
