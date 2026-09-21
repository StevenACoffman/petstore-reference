package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/cors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/config"
)

func TestCORS_PreflightWithCredentials(t *testing.T) {
	t.Parallel()

	c := cors.New(corsOptions(&config.Config{AllowedOrigins: []string{"https://localhost:4321"}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/pet.v2.PetService/ListPets", http.NoBody)
	req.Header.Set("Origin", "https://localhost:4321")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "connect-protocol-version,content-type")

	w := httptest.NewRecorder()
	c.Handler(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})).ServeHTTP(w, req)

	resp := w.Result()
	require.True(t, resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK)

	assert.Equal(t, "https://localhost:4321", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", resp.Header.Get("Access-Control-Allow-Credentials"))
}

func TestCORS_RejectsUnconfiguredOrigin(t *testing.T) {
	t.Parallel()

	c := cors.New(corsOptions(&config.Config{AllowedOrigins: []string{"https://app.example.com"}}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/pet.v2.PetService/ListPets", http.NoBody)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")
	w := httptest.NewRecorder()

	c.Handler(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})).ServeHTTP(w, req)

	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.NotEqual(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
}

// testConfig returns a server config that needs no external services.
func testConfig() *config.Config {
	return &config.Config{
		AuthEnabled:       true,
		DevMode:           true,
		DevEmail:          "dev@example.com",
		AuthTokens:        []string{"token-123"},
		AllowedOrigins:    []string{"https://localhost:4321"},
		TrustProxyHeaders: true,
	}
}

// TestServerHandlerEndpoints covers the routes that do not need a database. A nil
// pool is a legitimate input here: it is how the handler behaves before Postgres is
// reachable, and /healthz is expected to say so rather than panic.
func TestServerHandlerEndpoints(t *testing.T) {
	t.Parallel()

	handler, err := newServerHandler(testConfig(), nil, nil)
	require.NoError(t, err)

	cases := map[string]struct {
		path       string
		wantStatus int
		wantInBody string
	}{
		"healthz reports process liveness without touching the database": {
			path:       "/healthz",
			wantStatus: http.StatusOK,
			wantInBody: `"status":"ok"`,
		},
		"readyz reports the database as unavailable": {
			path:       "/readyz",
			wantStatus: http.StatusServiceUnavailable,
			wantInBody: `"database":"disconnected"`,
		},
		"an unregistered path is an explicit 404": {
			path:       "/no-such-route",
			wantStatus: http.StatusNotFound,
			wantInBody: "",
		},
		"docs serves the API reference page": {
			path:       "/docs",
			wantStatus: http.StatusOK,
			wantInBody: "Petstore API Reference - OpenAPI",
		},
		"openapi.yaml serves the generated spec": {
			path:       "/openapi.yaml",
			wantStatus: http.StatusOK,
			wantInBody: "openapi:",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, http.NoBody))

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.wantInBody)
		})
	}
}
