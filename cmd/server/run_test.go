package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/config"
)

func TestRunRejectsProductionWithoutCredentials(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run(
		t.Context(),
		[]string{"server"},
		fakeEnv(map[string]string{config.EnvAppEnv: "production"}),
		strings.NewReader(""),
		&stdout,
		&stderr,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TRUST_PROXY_HEADERS")
	// The failure must arrive before anything binds a port or dials the database.
	assert.Empty(t, stdout.String())
}

func TestRunReportsAnUnusableDatabase(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run(
		t.Context(),
		[]string{"server"},
		fakeEnv(map[string]string{
			config.EnvDatabaseURL: "postgres://nobody@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1",
			config.EnvAutoMigrate: "false",
		}),
		strings.NewReader(""),
		&stdout,
		&stderr,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "connecting to database")
}

func TestRunRejectsUnknownFlags(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	err := run(
		t.Context(),
		[]string{"server", "-not-a-flag"},
		fakeEnv(nil),
		strings.NewReader(""),
		&stdout,
		&stderr,
	)

	require.Error(t, err)
	assert.Contains(t, stderr.String(), "not-a-flag")
}

// TestRunServesEndToEnd starts the real wired server on an OS-assigned port and
// exercises it as an ordinary HTTP client, then checks that cancelling the context
// shuts it down cleanly. This is the test the run() signature exists to make
// possible: no process, no fixed port, no global state.
// global OTel TracerProvider, and the subtests share one server and one container.
//
// fakeEnv returns a getenv function backed by a map.
func fakeEnv(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}
