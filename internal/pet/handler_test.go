package pet

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	petv1 "github.com/example/pets/gen/go/pet/v1"
)

// These exercise the handler shell without a database. The core's own behaviour is
// covered in core_test.go; what matters here is that the shell translates a core
// failure into the right RPC code rather than letting it reach the database.

func TestListPetsRejectsAnOverflowingPageOffset(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil)
	req := connect.NewRequest(&petv1.ListPetsRequest{PageSize: 100, Page: 21_474_837})

	_, err := handler.ListPets(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err),
		"an out-of-range page is the caller's mistake, not an internal error")
}

func TestHandlerRejectsMalformedUUIDs(t *testing.T) {
	t.Parallel()

	handler := NewHandler(nil)
	ctx := context.Background()

	cases := map[string]func() error{
		"GetPet": func() error {
			_, err := handler.GetPet(ctx, connect.NewRequest(&petv1.GetPetRequest{Id: "not-a-uuid"}))
			return err
		},
		"UpdatePet": func() error {
			_, err := handler.UpdatePet(ctx, connect.NewRequest(&petv1.UpdatePetRequest{Id: "not-a-uuid"}))
			return err
		},
		"DeletePet": func() error {
			_, err := handler.DeletePet(ctx, connect.NewRequest(&petv1.DeletePetRequest{Id: "not-a-uuid"}))
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
