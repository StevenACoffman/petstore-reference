package pet

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/internal/auth"
)

// These exercise the handler shell without a database. The core's own behaviour is
// covered in core_test.go; what matters here is that the shell translates a core
// failure into the right RPC code rather than letting it reach the database.

// The deprecated offset field is refused at the shell, before the nil pool would
// otherwise turn it into Unavailable — so the caller learns what to change.
func TestListPetsRejectsTheDeprecatedPageField(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil)
	//nolint:staticcheck // SA1019: sending the deprecated field is the thing under test.
	req := connect.NewRequest(&petv2.ListPetsRequest{PageSize: 10, Page: 1})

	_, err := handler.ListPets(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "page_token")
}

func TestListPetsRejectsAMalformedPageToken(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil)
	req := connect.NewRequest(&petv2.ListPetsRequest{PageToken: "not-a-token"})

	_, err := handler.ListPets(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
		"a bad token is the caller's mistake, not an internal error")
}

func TestHandlerRejectsMalformedUUIDs(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil)
	ctx := context.Background()

	cases := map[string]func() error{
		"GetPet": func() error {
			_, err := handler.GetPet(ctx, connect.NewRequest(&petv2.GetPetRequest{Id: "not-a-uuid"}))
			return err
		},
		"UpdatePet": func() error {
			_, err := handler.UpdatePet(ctx, connect.NewRequest(&petv2.UpdatePetRequest{Id: "not-a-uuid"}))
			return err
		},
		"DeletePet": func() error {
			_, err := handler.DeletePet(ctx, connect.NewRequest(&petv2.DeletePetRequest{Id: "not-a-uuid"}))
			return err
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := call()

			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

// TestHandlerWithoutADatabase pins the guarantee NewHandler's comment makes. An
// earlier version of that comment promised this and was false — every RPC
// panicked on a nil pool — so the promise is now asserted rather than asserted-to.
func TestHandlerWithoutADatabase(t *testing.T) {
	t.Parallel()

	const validUUID = "123e4567-e89b-12d3-a456-426614174000"
	h := NewHandler(nil)
	ctx := auth.WithClaims(context.Background(), &auth.Claims{Email: "caller@example.com"})

	cases := map[string]func() error{
		"GetPet": func() error {
			_, err := h.GetPet(ctx, connect.NewRequest(&petv2.GetPetRequest{Id: validUUID}))
			return err
		},
		"ListPets": func() error {
			_, err := h.ListPets(ctx, connect.NewRequest(&petv2.ListPetsRequest{}))
			return err
		},
		"CreatePet": func() error {
			_, err := h.CreatePet(ctx, connect.NewRequest(&petv2.CreatePetRequest{
				Name: "Rex", Species: "dog",
			}))
			return err
		},
		"UpdatePet": func() error {
			_, err := h.UpdatePet(ctx, connect.NewRequest(&petv2.UpdatePetRequest{
				Id: validUUID, Name: new("Rex"), Species: new("dog"),
			}))
			return err
		},
		"DeletePet": func() error {
			_, err := h.DeletePet(ctx, connect.NewRequest(&petv2.DeletePetRequest{Id: validUUID}))
			return err
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.NotPanics(t, func() {
				err := call()

				require.Error(t, err)
				assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			})
		})
	}
}
