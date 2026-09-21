package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	petv1 "github.com/example/pets/gen/go/pet/v1"
	"github.com/example/pets/gen/go/pet/v1/petv1connect"
	"github.com/example/pets/internal/auth"
)

// mockPetService records the claims the interceptor put on the context. The mutex
// matters because the handler writes from the server goroutine while the test reads
// from its own.
type mockPetService struct {
	petv1connect.UnimplementedPetServiceHandler

	mu         sync.Mutex
	lastClaims *auth.Claims
}

// claims returns the claims seen by the most recent GetPet call, or nil.
func (m *mockPetService) claims() *auth.Claims {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastClaims
}

func (m *mockPetService) GetPet(ctx context.Context, req *connect.Request[petv1.GetPetRequest]) (*connect.Response[petv1.GetPetResponse], error) {
	if claims, ok := auth.FromContext(ctx); ok {
		m.mu.Lock()
		m.lastClaims = claims
		m.mu.Unlock()
	}
	return connect.NewResponse(&petv1.GetPetResponse{
		Pet: &petv1.Pet{
			Id:   req.Msg.GetId(),
			Name: "TestPet",
		},
	}), nil
}

func (m *mockPetService) ListPets(_ context.Context, _ *connect.Request[petv1.ListPetsRequest]) (*connect.Response[petv1.ListPetsResponse], error) {
	return connect.NewResponse(&petv1.ListPetsResponse{}), nil
}

// newAuthTestServer stands up a PetService behind cfg and returns a client and the
// mock it talks to. Each call gets its own server and its own mock, so cases share
// nothing and may run in parallel.
func newAuthTestServer(t *testing.T, cfg auth.Config) (petv1connect.PetServiceClient, *mockPetService) {
	t.Helper()

	svc := &mockPetService{}
	path, handler := petv1connect.NewPetServiceHandler(
		svc,
		connect.WithInterceptors(auth.NewInterceptor(cfg)),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return petv1connect.NewPetServiceClient(server.Client(), server.URL), svc
}

func TestAuthInterceptor(t *testing.T) {
	t.Parallel()

	const petID = "123e4567-e89b-12d3-a456-426614174000"

	// proxyConfig trusts upstream identity headers and also accepts a static token.
	proxyConfig := auth.Config{
		Enabled:           true,
		DevMode:           false,
		StaticTokens:      []string{"valid-token-123"},
		TrustProxyHeaders: true,
		SkipProcedures: map[string]bool{
			petv1connect.PetServiceListPetsProcedure: true,
		},
	}
	// validatorConfig delegates to a custom verifier, as a real IAP deployment would.
	validatorConfig := auth.Config{
		Enabled:           true,
		TrustProxyHeaders: true,
		Validator: func(_ context.Context, _ string) (*auth.Claims, error) {
			return &auth.Claims{Email: "verified@example.com", Provider: "iap"}, nil
		},
	}

	cases := map[string]struct {
		cfg          auth.Config
		headers      map[string]string
		callListPets bool
		wantCode     connect.Code // zero means the call is expected to succeed
		wantEmail    string
		wantProvider string
		wantRoles    []string
	}{
		"no credentials at all is rejected": {
			cfg:      proxyConfig,
			wantCode: connect.CodeUnauthenticated,
		},
		"google cloud IAP headers identify the caller": {
			cfg: proxyConfig,
			headers: map[string]string{
				"X-Goog-Authenticated-User-Email": "accounts.google.com:alice@example.com",
				"X-Goog-Authenticated-User-Id":    "accounts.google.com:10987654321",
			},
			wantEmail:    "alice@example.com",
			wantProvider: "iap",
		},
		"oauth2-proxy headers identify the caller and their groups": {
			cfg: proxyConfig,
			headers: map[string]string{
				"X-Forwarded-Email":  "bob@example.com",
				"X-Forwarded-User":   "bob123",
				"X-Forwarded-Groups": "engineering, devops",
			},
			wantEmail:    "bob@example.com",
			wantProvider: "oauth2-proxy",
			wantRoles:    []string{"engineering", "devops"},
		},
		"a valid static bearer token is accepted": {
			cfg:          proxyConfig,
			headers:      map[string]string{"Authorization": "Bearer valid-token-123"},
			wantProvider: "bearer",
		},
		"an unknown bearer token is rejected": {
			cfg:      proxyConfig,
			headers:  map[string]string{"Authorization": "Bearer not-the-token"},
			wantCode: connect.CodeUnauthenticated,
		},
		"a procedure on the skip list needs no credentials": {
			cfg:          proxyConfig,
			callListPets: true,
		},
		"a validator rejects an IAP header with no JWT assertion": {
			cfg: validatorConfig,
			headers: map[string]string{
				"X-Goog-Authenticated-User-Email": "accounts.google.com:unverified@example.com",
			},
			wantCode: connect.CodeUnauthenticated,
		},
		"a validator accepts a JWT assertion": {
			cfg:          validatorConfig,
			headers:      map[string]string{"X-Goog-IAP-JWT-Assertion": "valid-jwt-token"},
			wantEmail:    "verified@example.com",
			wantProvider: "iap",
		},
		"dev mode falls back to a synthetic identity": {
			cfg:          auth.Config{Enabled: true, DevMode: true},
			wantProvider: "dev",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, svc := newAuthTestServer(t, tc.cfg)
			ctx := t.Context()

			if tc.callListPets {
				req := connect.NewRequest(&petv1.ListPetsRequest{})
				resp, err := client.ListPets(ctx, req)
				require.NoError(t, err)
				assert.NotNil(t, resp.Msg)
				return
			}

			req := connect.NewRequest(&petv1.GetPetRequest{Id: petID})
			for k, v := range tc.headers {
				req.Header().Set(k, v)
			}
			resp, err := client.GetPet(ctx, req)

			if tc.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tc.wantCode, connect.CodeOf(err))
				return
			}

			require.NoError(t, err)
			assert.Equal(t, "TestPet", resp.Msg.GetPet().GetName())

			claims := svc.claims()
			require.NotNil(t, claims, "interceptor did not put claims on the context")
			if tc.wantEmail != "" {
				assert.Equal(t, tc.wantEmail, claims.Email)
			}
			assert.Equal(t, tc.wantProvider, claims.Provider)
			if tc.wantRoles != nil {
				assert.Equal(t, tc.wantRoles, claims.Roles)
			}
		})
	}
}

func TestAuthInterceptorRejectsUntrustedProxyHeaders(t *testing.T) {
	t.Parallel()

	svc := &mockPetService{}
	path, handler := petv1connect.NewPetServiceHandler(
		svc,
		connect.WithInterceptors(auth.NewInterceptor(auth.Config{Enabled: true})),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := petv1connect.NewPetServiceClient(server.Client(), server.URL)
	req := connect.NewRequest(&petv1.GetPetRequest{Id: "123e4567-e89b-12d3-a456-426614174000"})
	req.Header().Set("X-Forwarded-Email", "attacker@example.com")
	req.Header().Set("X-Forwarded-User", "attacker")

	_, err := client.GetPet(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDevIdentityMiddleware(t *testing.T) {
	t.Parallel()

	middleware := auth.DevIdentityMiddleware("local-dev@example.com", "local-dev-user")

	t.Run("injects headers when missing", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", http.NoBody)
		rec := httptest.NewRecorder()

		var capturedReq *http.Request
		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedReq = r
			w.WriteHeader(http.StatusOK)
		}))

		handler.ServeHTTP(rec, req)

		assert.Equal(t, "accounts.google.com:local-dev@example.com", capturedReq.Header.Get("X-Goog-Authenticated-User-Email"))
		assert.Equal(t, "local-dev@example.com", capturedReq.Header.Get("X-Forwarded-Email"))
		assert.Equal(t, "local-dev-user", capturedReq.Header.Get("X-Forwarded-User"))
	})

	t.Run("preserves existing IAP headers", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", http.NoBody)
		req.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:custom@example.com")
		rec := httptest.NewRecorder()

		var capturedReq *http.Request
		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedReq = r
			w.WriteHeader(http.StatusOK)
		}))

		handler.ServeHTTP(rec, req)

		assert.Equal(t, "accounts.google.com:custom@example.com", capturedReq.Header.Get("X-Goog-Authenticated-User-Email"))
		assert.Empty(t, capturedReq.Header.Get("X-Forwarded-Email"))
	})

	t.Run("preserves existing Authorization header", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", http.NoBody)
		req.Header.Set("Authorization", "Bearer custom-token")
		rec := httptest.NewRecorder()

		var capturedReq *http.Request
		handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedReq = r
			w.WriteHeader(http.StatusOK)
		}))

		handler.ServeHTTP(rec, req)

		assert.Empty(t, capturedReq.Header.Get("X-Goog-Authenticated-User-Email"))
		assert.Equal(t, "Bearer custom-token", capturedReq.Header.Get("Authorization"))
	})
}

func TestUserEmailFromContextRequiresIdentity(t *testing.T) {
	t.Parallel()

	email, ok := auth.UserEmailFromContext(context.Background())
	assert.False(t, ok)
	assert.Empty(t, email)

	email, ok = auth.UserEmailFromContext(auth.WithClaims(context.Background(), &auth.Claims{Email: "user@example.com"}))
	assert.True(t, ok)
	assert.Equal(t, "user@example.com", email)
}
