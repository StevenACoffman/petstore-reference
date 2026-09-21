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

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/gen/go/pet/v2/petv2connect"
	"github.com/example/pets/internal/auth"
)

// mockPetService records the claims the interceptor put on the context. The mutex
// matters because the handler writes from the server goroutine while the test reads
// from its own.
type mockPetService struct {
	petv2connect.UnimplementedPetServiceHandler

	mu         sync.Mutex
	lastClaims *auth.Claims
}

// claims returns the claims seen by the most recent GetPet call, or nil.
func (m *mockPetService) claims() *auth.Claims {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastClaims
}

func (m *mockPetService) GetPet(ctx context.Context, req *connect.Request[petv2.GetPetRequest]) (*connect.Response[petv2.GetPetResponse], error) {
	if claims, ok := auth.FromContext(ctx); ok {
		m.mu.Lock()
		m.lastClaims = claims
		m.mu.Unlock()
	}
	return connect.NewResponse(&petv2.GetPetResponse{
		Pet: &petv2.Pet{
			Id:   req.Msg.GetId(),
			Name: "TestPet",
		},
	}), nil
}

func (m *mockPetService) ListPets(_ context.Context, _ *connect.Request[petv2.ListPetsRequest]) (*connect.Response[petv2.ListPetsResponse], error) {
	return connect.NewResponse(&petv2.ListPetsResponse{}), nil
}

// newAuthTestServer stands up a PetService behind cfg and returns a client and the
// mock it talks to. Each call gets its own server and its own mock, so cases share
// nothing and may run in parallel.
func newAuthTestServer(t *testing.T, cfg auth.Config) (petv2connect.PetServiceClient, *mockPetService) {
	t.Helper()

	svc := &mockPetService{}
	path, handler := petv2connect.NewPetServiceHandler(
		svc,
		connect.WithInterceptors(auth.NewInterceptor(cfg)),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return petv2connect.NewPetServiceClient(server.Client(), server.URL), svc
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
			petv2connect.PetServiceListPetsProcedure: true,
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
				req := connect.NewRequest(&petv2.ListPetsRequest{})
				resp, err := client.ListPets(ctx, req)
				require.NoError(t, err)
				assert.NotNil(t, resp.Msg)
				return
			}

			req := connect.NewRequest(&petv2.GetPetRequest{Id: petID})
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
	path, handler := petv2connect.NewPetServiceHandler(
		svc,
		connect.WithInterceptors(auth.NewInterceptor(auth.Config{Enabled: true})),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := petv2connect.NewPetServiceClient(server.Client(), server.URL)
	req := connect.NewRequest(&petv2.GetPetRequest{Id: "123e4567-e89b-12d3-a456-426614174000"})
	req.Header().Set("X-Forwarded-Email", "attacker@example.com")
	req.Header().Set("X-Forwarded-User", "attacker")

	_, err := client.GetPet(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDevIdentityMiddleware(t *testing.T) {
	t.Parallel()

	middleware := auth.DevIdentityMiddleware("local-dev@example.com", "local-dev-user", []string{"user", "admin"})

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

		// oauth2-proxy shape, not IAP: IAP carries no groups, so an IAP simulation
		// could never exercise a role.
		assert.Equal(t, "local-dev@example.com", capturedReq.Header.Get("X-Forwarded-Email"))
		assert.Equal(t, "local-dev-user", capturedReq.Header.Get("X-Forwarded-User"))
		assert.Equal(t, "user,admin", capturedReq.Header.Get("X-Forwarded-Groups"))
		assert.Empty(t, capturedReq.Header.Get("X-Goog-Authenticated-User-Email"))
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
		assert.Empty(t, capturedReq.Header.Get("X-Forwarded-Groups"))
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

// TestDevIdentityCarriesRoles pins the reason the dev middleware simulates
// oauth2-proxy rather than IAP: IAP conveys no groups, so before this the local
// identity was always exactly [user] and no other role could be exercised.
func TestDevIdentityCarriesRoles(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		devRoles  []string
		wantRoles []string
	}{
		"defaults to admin so a deny-by-default policy does not lock dev out": {
			devRoles:  nil,
			wantRoles: []string{"user", "admin"},
		},
		"can be narrowed to feel what a non-admin feels": {
			devRoles:  []string{"user"},
			wantRoles: []string{"user"},
		},
		"can name an arbitrary group": {
			devRoles:  []string{"shelter-staff"},
			wantRoles: []string{"shelter-staff"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := auth.Config{Enabled: true, DevMode: true, TrustProxyHeaders: true}
			client, svc := newAuthTestServer(t, cfg)

			// Drive the request through the dev middleware exactly as cmd/server wires it.
			recorder := httptest.NewRecorder()
			injected := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			auth.DevIdentityMiddleware("dev@example.com", "dev-1", tc.devRoles)(
				http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					injected = r
				}),
			).ServeHTTP(recorder, injected)

			req := connect.NewRequest(&petv2.GetPetRequest{Id: "123e4567-e89b-12d3-a456-426614174000"})
			for _, h := range []string{"X-Forwarded-Email", "X-Forwarded-User", "X-Forwarded-Groups"} {
				req.Header().Set(h, injected.Header.Get(h))
			}

			_, err := client.GetPet(t.Context(), req)

			require.NoError(t, err)
			claims := svc.claims()
			require.NotNil(t, claims)
			assert.Equal(t, "oauth2-proxy", claims.Provider,
				"dev simulates oauth2-proxy, the only provider shape that carries groups")
			assert.Equal(t, tc.wantRoles, claims.Roles)
			assert.Equal(t, "dev@example.com", claims.Email)
		})
	}
}
