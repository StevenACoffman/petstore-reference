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

// Config holds configuration options for OpenTelemetry.
//
// The struct tags describe the config-file shape only. Environment variables are
// applied separately by applyEnv so that loading never touches process-global
// state; see LoadConfig.
type Config struct {
	ServiceName      string  `json:"service_name"      toml:"service_name"      yaml:"service_name"`
	ServiceVersion   string  `json:"service_version"   toml:"service_version"   yaml:"service_version"`
	OTLPEndpoint     string  `json:"otlp_endpoint"     toml:"otlp_endpoint"     yaml:"otlp_endpoint"`
	Insecure         bool    `json:"insecure"          toml:"insecure"          yaml:"insecure"`
	ExporterType     string  `json:"exporter_type"     toml:"exporter_type"     yaml:"exporter_type"`
	SamplePercentage float64 `json:"sample_percentage" toml:"sample_percentage" yaml:"sample_percentage"`
}

// DefaultConfig returns the configuration used before any file or environment
// override is applied.
func DefaultConfig() Config {
	return Config{
		ServiceName:      "pets-service",
		ServiceVersion:   "1.0.0",
		Insecure:         true,
		SamplePercentage: 100.0,
	}
}

// LoadConfig builds a Config from an optional config file overlaid with environment
// variables read through getenv. Precedence, lowest to highest: defaults, config
// file, environment.
//
// The config file is located from the first non-empty of: the configPath argument,
// OTEL_CONFIG_FILE, CONFIG_FILE. A named file that does not exist is not an error —
// it means "configure from the environment". A file that exists but cannot be parsed
// is reported through the returned error; the Config is still usable.
//
// Requires: getenv behaves like os.Getenv, returning "" for unset names. A nil
//
//	getenv is treated as "no environment".
//
// Ensures: never reads or writes process-global state, so callers may run it
//
//	concurrently; ExporterType is always non-empty in the returned Config.
func LoadConfig(getenv func(string) string, configPath ...string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	cfg := DefaultConfig()

	var err error
	if path := resolveConfigPath(getenv, configPath...); path != "" {
		// cleanenv owns file location and format dispatch. The Config struct carries no
		// `env` tags, so ReadConfig's environment pass is a no-op and the file is the
		// only thing applied here — environment handling stays in applyEnv, below.
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

// resolveConfigPath picks the config file to read, preferring an explicit argument
// over the environment. It returns "" when no file was named.
//
// The chosen path is cleaned so that a value carrying "." or ".." segments resolves
// to one canonical form before it reaches the filesystem.
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

// applyEnv overlays environment values onto cfg. An unset or unparsable variable
// leaves the existing value untouched.
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

// parseSamplePercentage resolves the trace sampling rate from its two environment
// spellings. OTEL_SAMPLE_PERCENTAGE is this service's own knob and accepts an
// optional trailing "%". OTEL_TRACES_SAMPLER_ARG is the OpenTelemetry-standard
// spelling and carries a ratio in (0, 1]; a value above 1 is already a percentage.
//
// Requires: pct and samplerArg are the raw variable values, "" when unset.
// Ensures:  returns (rate, true) with rate in [0, 100] when a value parses,
//
//	preferring pct; returns (0, false) otherwise so the caller keeps
//	whatever value it already had.
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
