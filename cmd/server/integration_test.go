//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/config"
	"github.com/example/pets/internal/testutil"
)

// timeMultiplier scales async waits so a loaded CI machine does not fail on timing
// alone. Tests never sleep for a fixed duration; they poll against a deadline.
// It is an untyped constant, not a Duration, so that multiplying it by time.Second
// stays a duration-times-scalar rather than a meaningless duration-squared.
const timeMultiplier = 1

// TestServerHandlerReadyzWithDatabase asserts the healthy branch of /readyz against a
// real Postgres. It skips loudly when no container runtime is available, rather than
// passing quietly having asserted nothing.
//
// container-backed tests; running them concurrently would make failures ambiguous.
//
//nolint:paralleltest // shares the single package-wide Postgres container with the other
func TestServerHandlerReadyzWithDatabase(t *testing.T) {
	ctx := t.Context()

	testDB, err := testutil.StartTestDB(ctx)
	if err != nil {
		t.Skipf("skipping: postgres testcontainer unavailable: %v", err)
	}
	t.Cleanup(testDB.Close)

	handler, err := newServerHandler(testConfig(), testDB.Pool, nil)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"database":"connected"`)
}

// TestRunServesEndToEnd starts the real wired server on an OS-assigned port and
// exercises it as an ordinary HTTP client, then checks that cancelling the context
// shuts it down cleanly. This is the test the run() signature exists to make
// possible: no process, no fixed port, no global state.
//
// and a global OTel TracerProvider, and the subtests share one server and one container.
//
//nolint:paralleltest // run() installs a process-global default logger via slog.SetDefault
func TestRunServesEndToEnd(t *testing.T) {
	ctx := t.Context()

	testDB, err := testutil.StartTestDB(ctx)
	if err != nil {
		t.Skipf("skipping: postgres testcontainer unavailable: %v", err)
	}
	t.Cleanup(testDB.Close)

	dbURL, err := testDB.Container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	var stdout lockedBuffer
	done := make(chan error, 1)

	go func() {
		done <- run(
			runCtx,
			[]string{"server", "-addr", "127.0.0.1:0"},
			fakeEnv(map[string]string{
				config.EnvDatabaseURL:    dbURL,
				config.EnvAppEnv:         "development",
				config.EnvTLSCertFile:    "/nonexistent/cert.pem",
				config.EnvTLSKeyFile:     "/nonexistent/key.pem",
				config.EnvAutoMigrate:    "true",
				config.EnvAllowedOrigins: "https://localhost:4321",
			}),
			strings.NewReader(""),
			&stdout,
			io.Discard,
		)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		case <-time.After(20 * time.Second * timeMultiplier):
			t.Error("run did not return after its context was cancelled")
		}
	})

	baseURL := waitForListenAddr(t, &stdout)
	require.NoError(t, waitForReady(ctx, timeMultiplier*20*time.Second, baseURL+"/readyz"))

	t.Run("healthz reports liveness", func(t *testing.T) {
		body, status := get(ctx, t, baseURL+"/healthz")
		assert.Equal(t, http.StatusOK, status)
		assert.Contains(t, body, `"status":"ok"`)
	})

	t.Run("readyz reports a connected database", func(t *testing.T) {
		body, status := get(ctx, t, baseURL+"/readyz")
		assert.Equal(t, http.StatusOK, status)
		assert.Contains(t, body, `"database":"connected"`)
	})

	t.Run("docs are served", func(t *testing.T) {
		body, status := get(ctx, t, baseURL+"/docs")
		assert.Equal(t, http.StatusOK, status)
		assert.Contains(t, body, "Petstore API Reference")
	})

	// The shipped policy denies everything but admin, and the local dev identity is
	// an admin. This asserts that `just run` works with no AUTHZ_POLICY configured.
	// Verified non-vacuous: with DEV_ROLES=user it fails with permission_denied.
	t.Run("an RPC succeeds in dev with no policy configured", func(t *testing.T) {
		body, status := post(ctx, t, baseURL+"/pet.v2.PetService/ListPets", `{}`)
		assert.Equal(t, http.StatusOK, status, "body: %s", body)
	})

	t.Run("an unknown path is a 404", func(t *testing.T) {
		_, status := get(ctx, t, baseURL+"/no-such-route")
		assert.Equal(t, http.StatusNotFound, status)
	})
}

var listenLine = regexp.MustCompile(`listening on (https?)://(\S+)`)

// waitForListenAddr reads the address run() printed to stdout, so the test learns the
// OS-assigned port without guessing or racing on a pre-probed one.
func waitForListenAddr(t *testing.T, out *lockedBuffer) string {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second * timeMultiplier)
	for time.Now().Before(deadline) {
		if m := listenLine.FindStringSubmatch(out.String()); m != nil {
			return fmt.Sprintf("%s://%s", m[1], m[2])
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never reported a listen address; stdout was:\n%s", out.String())
	return ""
}

// waitForReady polls endpoint until it answers 200 or the timeout expires.
func waitForReady(ctx context.Context, timeout time.Duration, endpoint string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
		if err != nil {
			return fmt.Errorf("building readiness request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", endpoint)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// post sends a Connect JSON request and returns the body and status.
func post(ctx context.Context, t *testing.T, url, payload string) (body string, status int) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(raw), resp.StatusCode
}

func get(ctx context.Context, t *testing.T, url string) (body string, status int) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(raw), resp.StatusCode
}

// lockedBuffer is a bytes.Buffer that is safe to write from run's goroutine while the
// test reads it. Without the mutex this test would be a race detector finding.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// assert that lockedBuffer satisfies the writer run() expects.
var _ io.Writer = (*lockedBuffer)(nil)
