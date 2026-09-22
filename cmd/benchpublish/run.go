package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/example/pets/internal/benchmark"
)

// writeTimeout bounds the single HTTP write. A CI step should fail quickly on an
// unreachable database rather than hold the job until the runner times out.
const writeTimeout = 30 * time.Second

// Environment variables. The endpoint is whatever you point it at, so a
// self-hosted InfluxDB is configuration rather than a code change.
const (
	envURL         = "INFLUXDB_URL"
	envToken       = "INFLUXDB_TOKEN"
	envOrg         = "INFLUXDB_ORG"
	envBucket      = "INFLUXDB_BUCKET"
	envMeasurement = "INFLUXDB_MEASUREMENT"
)

const (
	defaultOrg         = "petstore"
	defaultBucket      = "benchmarks"
	defaultMeasurement = "benchmark"
)

// config is the resolved destination and the tags identifying this run.
type config struct {
	url         string
	token       string
	org         string
	bucket      string
	measurement string
	tags        map[string]string
}

// run parses a benchmark file and writes one point per benchmark to InfluxDB.
//
// Requires: args is non-empty (args[0] is the program name); getenv behaves like
// os.Getenv; stdout and stderr are non-nil.
//
// Ensures: never calls os.Exit. With INFLUXDB_URL unset it prints what it would
// have written and returns nil, so a fork's pull request without secrets is a
// no-op rather than a red build.
func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	stdout, stderr io.Writer,
	client *http.Client,
) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "print the line protocol instead of writing it")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s [flags] <benchmark-output-file>\n\n"+
			"Reads `go test -bench` output and writes one point per benchmark to InfluxDB.\n"+
			"Set %s to enable writing; without it the run is a no-op.\n\nFlags:\n",
			args[0], envURL)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return fmt.Errorf("expected one benchmark output file, got %d", flags.NArg())
	}

	aggregates, err := readBenchmarks(flags.Arg(0))
	if err != nil {
		return err
	}
	if len(aggregates) == 0 {
		return fmt.Errorf("%s contained no benchmark results", flags.Arg(0))
	}

	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}

	points := benchmark.NewPoints(cfg.measurement, aggregates, cfg.tags, time.Now().UTC())
	body := benchmark.Encode(points)

	// Unconfigured is a deliberate no-op, not an error: benchmarks still run and
	// still gate the build on a fork's pull request, they just go unrecorded.
	if cfg.url == "" {
		fmt.Fprintf(stdout, "%s is not set, so nothing was written. Line protocol:\n\n%s", envURL, body)
		return nil
	}
	if *dryRun {
		fmt.Fprintf(stdout, "dry run, not writing to %s:\n\n%s", cfg.url, body)
		return nil
	}

	if err := write(ctx, client, cfg, body); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %d benchmark points to %s (bucket %q)\n", len(points), cfg.url, cfg.bucket)
	return nil
}

// readBenchmarks parses one `go test -bench` output file into aggregates.
func readBenchmarks(path string) ([]benchmark.Aggregate, error) {
	// The path is this command's only argument: naming the file to read is what
	// the tool is for, and it runs in CI against a file the same job produced.
	file, err := os.Open(path) //nolint:gosec // G703: the path is the user-supplied argument
	if err != nil {
		return nil, fmt.Errorf("opening benchmark output: %w", err)
	}
	defer file.Close()

	samples, err := benchmark.ParseSamples(file)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return benchmark.AggregateSamples(samples), nil
}

// loadConfig reads the destination and the run's identity from the environment.
//
// A URL without a token is a misconfiguration rather than a no-op: it means
// someone intended to publish and the write would fail at the server.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		url:         strings.TrimRight(getenv(envURL), "/"),
		token:       getenv(envToken),
		org:         orDefault(getenv(envOrg), defaultOrg),
		bucket:      orDefault(getenv(envBucket), defaultBucket),
		measurement: orDefault(getenv(envMeasurement), defaultMeasurement),
	}
	if cfg.url != "" && cfg.token == "" {
		return config{}, fmt.Errorf("%s is set but %s is empty", envURL, envToken)
	}

	// GitHub Actions supplies these; locally they are simply absent and the tag
	// is dropped.
	commit := getenv("GITHUB_SHA")
	if len(commit) > 8 {
		commit = commit[:8]
	}
	cfg.tags = map[string]string{
		"commit": commit,
		"branch": getenv("GITHUB_REF_NAME"),
		"run_id": getenv("GITHUB_RUN_ID"),
		"goos":   getenv("BENCH_GOOS"),
		"goarch": getenv("BENCH_GOARCH"),
	}
	return cfg, nil
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// write POSTs line protocol to the InfluxDB v2 write API.
func write(ctx context.Context, client *http.Client, cfg config, body string) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	endpoint := cfg.url + "/api/v2/write?" + url.Values{
		"org":       {cfg.org},
		"bucket":    {cfg.bucket},
		"precision": {"ns"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(body))
	if err != nil {
		return fmt.Errorf("building the write request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+cfg.token)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	// The endpoint comes from INFLUXDB_URL by design, so that a self-hosted
	// InfluxDB is configuration rather than a code change.
	resp, err := client.Do(req) //nolint:gosec // G704: the destination is deliberately configurable
	if err != nil {
		return fmt.Errorf("writing to InfluxDB at %s: %w", cfg.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		// The server explains a rejected write in the body; without it the caller
		// only learns the status and has to guess which line was malformed.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("InfluxDB rejected the write: %s: %s",
			resp.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}
