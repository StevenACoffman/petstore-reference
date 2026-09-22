package authz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/gen/go/pet/v2/petv2connect"
	"github.com/example/pets/internal/auth"
	"github.com/example/pets/internal/authz"
)

type stubService struct {
	petv2connect.UnimplementedPetServiceHandler
	called bool
}

func (s *stubService) GetPet(
	context.Context, *connect.Request[petv2.GetPetRequest],
) (*connect.Response[petv2.GetPetResponse], error) {
	s.called = true
	return connect.NewResponse(&petv2.GetPetResponse{Pet: &petv2.Pet{Name: "Rex"}}), nil
}

// newServer stands up the interceptor behind an authentication stub that injects
// the given roles, mirroring how cmd/server orders the two.
func newServer(t *testing.T, policy *authz.Policy, roles []string) (petv2connect.PetServiceClient, *stubService) {
	t.Helper()

	svc := &stubService{}
	injectClaims := connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				if roles == nil {
					return next(ctx, req)
				}
				return next(auth.WithClaims(ctx, &auth.Claims{
					Subject: "caller", Email: "caller@example.com", Roles: roles,
				}), req)
			}
		},
	)
	path, handler := petv2connect.NewPetServiceHandler(svc,
		connect.WithInterceptors(injectClaims, authz.NewInterceptor(policy)))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return petv2connect.NewPetServiceClient(server.Client(), server.URL), svc
}

func TestInterceptor(t *testing.T) {
	t.Parallel()

	policy := authz.NewPolicy(map[string][]string{
		petv2connect.PetServiceGetPetProcedure: {"viewer"},
	})

	cases := map[string]struct {
		roles      []string
		wantCalled bool
		wantCode   connect.Code
	}{
		"a permitted role reaches the handler": {
			roles: []string{"viewer"}, wantCalled: true,
		},
		"admin reaches the handler": {
			roles: []string{authz.AdminRole}, wantCalled: true,
		},
		"an unpermitted role is refused": {
			roles: []string{"guest"}, wantCalled: false, wantCode: connect.CodePermissionDenied,
		},
		"no roles is refused": {
			roles: []string{}, wantCalled: false, wantCode: connect.CodePermissionDenied,
		},
		"absent claims are refused": {
			roles: nil, wantCalled: false, wantCode: connect.CodePermissionDenied,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, svc := newServer(t, policy, tc.roles)

			_, err := client.GetPet(t.Context(), connect.NewRequest(&petv2.GetPetRequest{
				Id: "123e4567-e89b-12d3-a456-426614174000",
			}))

			assert.Equal(t, tc.wantCalled, svc.called, "handler invocation")
			if tc.wantCalled {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, connect.CodeOf(err))
		})
	}
}

// TestInterceptorLeaksNothing: a caller who may not act does not get told what
// would have let them.
func TestInterceptorLeaksNothing(t *testing.T) {
	t.Parallel()

	policy := authz.NewPolicy(map[string][]string{
		petv2connect.PetServiceGetPetProcedure: {"secret-role"},
	})
	client, _ := newServer(t, policy, []string{"guest"})

	_, err := client.GetPet(t.Context(), connect.NewRequest(&petv2.GetPetRequest{
		Id: "123e4567-e89b-12d3-a456-426614174000",
	}))

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-role")
}

// TestInterceptorRunsBeforeValidation: an unauthorized caller is refused without
// the service parsing their body. The id here is not a UUID, so a validating
// chain would answer InvalidArgument if authorization ran second.
func TestInterceptorRunsBeforeValidation(t *testing.T) {
	t.Parallel()

	policy := authz.NewPolicy(nil)
	client, svc := newServer(t, policy, []string{"guest"})

	_, err := client.GetPet(t.Context(), connect.NewRequest(&petv2.GetPetRequest{
		Id: "not-a-uuid",
	}))

	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.False(t, svc.called)
}
