package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleOutput = `goos: linux
goarch: amd64
pkg: github.com/example/pets/internal/pet
BenchmarkToProtoPet-8	6231312	207.6 ns/op	416 B/op	5 allocs/op
BenchmarkToProtoPet-8	6231312	209.0 ns/op	416 B/op	5 allocs/op
BenchmarkNewPetInput-8	11374406	89.91 ns/op	0 B/op	0 allocs/op
PASS
`

// writeBenchFile puts benchmark output in a temp file and returns its path.
func writeBenchFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bench.txt")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// envFunc builds a getenv from a map, so a test never touches the real
// environment and can run in parallel.
func envFunc(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// capture runs the command and returns stdout, stderr and the error.
func capture(
	t *testing.T, args []string, env map[string]string, client *http.Client,
) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = run(t.Context(), args, envFunc(env), &out, &errOut, client)
	return out.String(), errOut.String(), err
}

// TestWritesPointsToInfluxDB is the contract: the configured endpoint receives
// line protocol carrying the run's identity.
func TestWritesPointsToInfluxDB(t *testing.T) {
	t.Parallel()

	var (
		gotPath  string
		gotQuery url.Values
		gotAuth  string
		gotBody  string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		body, _ := readAll(r)
		gotBody = body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	path := writeBenchFile(t, sampleOutput)
	stdout, _, err := capture(t, []string{"benchpublish", path}, map[string]string{
		"INFLUXDB_URL":    server.URL,
		"INFLUXDB_TOKEN":  "secret-token",
		"INFLUXDB_ORG":    "acme",
		"INFLUXDB_BUCKET": "perf",
		"GITHUB_SHA":      "abc1234567890",
		"GITHUB_REF_NAME": "main",
	}, server.Client())

	require.NoError(t, err)
	assert.Equal(t, "/api/v2/write", gotPath)
	assert.Equal(t, "acme", gotQuery.Get("org"))
	assert.Equal(t, "perf", gotQuery.Get("bucket"))
	assert.Equal(t, "ns", gotQuery.Get("precision"))
	assert.Equal(t, "Token secret-token", gotAuth)

	assert.Contains(t, gotBody, "commit=abc12345", "the SHA is shortened to 8 characters")
	assert.Contains(t, gotBody, "branch=main")
	assert.Contains(t, gotBody, "name=BenchmarkToProtoPet-8")
	assert.Contains(t, gotBody, "ns_per_op=208.3", "two samples must reduce to their median")
	assert.Contains(t, stdout, "wrote 2 benchmark points")
}

// TestUnconfiguredIsANoOp is what keeps a fork's pull request green: it has no
// secret, so there is nowhere to publish, and that must not fail the build.
func TestUnconfiguredIsANoOp(t *testing.T) {
	t.Parallel()

	path := writeBenchFile(t, sampleOutput)

	stdout, _, err := capture(t, []string{"benchpublish", path}, map[string]string{}, http.DefaultClient)

	require.NoError(t, err)
	assert.Contains(t, stdout, "is not set")
	assert.Contains(t, stdout, "ns_per_op=", "the line protocol is still shown for inspection")
}

// TestURLWithoutTokenIsAnError separates "not configured" from "configured
// wrongly": the second would fail at the server, so it fails here instead.
func TestURLWithoutTokenIsAnError(t *testing.T) {
	t.Parallel()

	path := writeBenchFile(t, sampleOutput)

	_, _, err := capture(t, []string{"benchpublish", path}, map[string]string{
		"INFLUXDB_URL": "http://influx.invalid:8086",
	}, http.DefaultClient)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFLUXDB_TOKEN")
}

func TestDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	t.Cleanup(server.Close)

	path := writeBenchFile(t, sampleOutput)
	stdout, _, err := capture(t, []string{"benchpublish", "-dry-run", path}, map[string]string{
		"INFLUXDB_URL":   server.URL,
		"INFLUXDB_TOKEN": "secret",
	}, server.Client())

	require.NoError(t, err)
	assert.False(t, called, "a dry run must not reach the server")
	assert.Contains(t, stdout, "dry run")
}

// TestServerRejectionIsReported surfaces the body, because a 400 alone does not
// say which line the server disliked.
func TestServerRejectionIsReported(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"unable to parse point"}`, http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	path := writeBenchFile(t, sampleOutput)
	_, _, err := capture(t, []string{"benchpublish", path}, map[string]string{
		"INFLUXDB_URL":   server.URL,
		"INFLUXDB_TOKEN": "secret",
	}, server.Client())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	assert.Contains(t, err.Error(), "unable to parse point")
}

func TestEmptyBenchmarkFileIsAnError(t *testing.T) {
	t.Parallel()

	path := writeBenchFile(t, "PASS\nok\tgithub.com/example/pets\t0.1s\n")

	_, _, err := capture(t, []string{"benchpublish", path}, map[string]string{}, http.DefaultClient)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no benchmark results")
}

func TestMissingFileIsAnError(t *testing.T) {
	t.Parallel()

	_, _, err := capture(t, []string{"benchpublish", "does-not-exist.txt"},
		map[string]string{}, http.DefaultClient)

	require.Error(t, err)
}

func TestWrongArgumentCountIsAnError(t *testing.T) {
	t.Parallel()

	_, _, err := capture(t, []string{"benchpublish"}, map[string]string{}, http.DefaultClient)

	require.Error(t, err)
}

// readAll returns a request body as a string.
func readAll(r *http.Request) (string, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.String(), err
}
