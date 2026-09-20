package pet

import (
	"context"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	petv1 "github.com/example/pets/gen/go/pet/v1"
	"github.com/example/pets/gen/go/pet/v1/petv1connect"
	"github.com/example/pets/internal/auth"
	"github.com/example/pets/internal/db"
	"github.com/example/pets/internal/resilience"
)

// This file is the imperative shell: it opens transactions, runs queries, and turns
// their results into responses. Every decision it makes — what is valid, what a value
// means, how a row becomes a message — is delegated to the pure core in core.go.

// Handler serves the PetService RPCs.
type Handler struct {
	queries *db.Queries
	// resilientDB applies retry and circuit-breaker policies to read operations. It
	// may be nil, in which case reads run unprotected.
	resilientDB *resilience.DB
}

var _ petv1connect.PetServiceHandler = (*Handler)(nil)

// NewHandler builds a Handler over the given pool.
//
// A nil pool is permitted so that routing and middleware can be exercised without a
// database; every RPC will then fail with an internal error rather than panic.
func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{queries: db.New(pool)}
}

// WithResilience returns a copy of h whose read paths run under the given policies.
//
// Only reads are wrapped. A retry replays the operation, which is safe for a query
// and unsafe for a write that may already have committed — the write paths here are
// transactional and non-idempotent, so they deliberately stay outside the retry
// policy. They still surface a broken database through the breaker, because the
// reads they are interleaved with trip it.
func (h *Handler) WithResilience(policies *resilience.DB) *Handler {
	clone := *h
	clone.resilientDB = policies
	return &clone
}

// read runs a read-only query under the handler's resilience policies.
func read[T any](ctx context.Context, h *Handler, op func(context.Context) (T, error)) (T, error) {
	return resilience.Get(ctx, h.resilientDB, op)
}

// callerEmail reads the authenticated identity that audit columns record.
func callerEmail(ctx context.Context) (string, error) {
	email, ok := auth.UserEmailFromContext(ctx)
	if !ok {
		return "", connect.NewError(connect.CodeUnauthenticated, errUnauthClaims)
	}
	return email, nil
}

func (h *Handler) CreatePet(
	ctx context.Context, req *connect.Request[petv1.CreatePetRequest],
) (*connect.Response[petv1.CreatePetResponse], error) {
	const op = "Handler.CreatePet"

	input, err := newPetInput(req.Msg)
	if err != nil {
		return nil, translate(ctx, op, err)
	}
	email, err := callerEmail(ctx)
	if err != nil {
		return nil, err
	}

	created, err := h.queries.CreatePet(ctx, db.CreatePetParams{
		Name:               input.Name,
		Species:            input.Species,
		BirthDate:          input.BirthDate,
		BirthDateEstimated: input.BirthDateEstimated,
		Status:             input.Status,
		PhotoUrls:          input.PhotoUrls,
		Tags:               input.Tags,
		CreatedBy:          email,
		ModifiedBy:         email,
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	return connect.NewResponse(&petv1.CreatePetResponse{Pet: toProtoPet(created)}), nil
}

func (h *Handler) GetPet(
	ctx context.Context, req *connect.Request[petv1.GetPetRequest],
) (*connect.Response[petv1.GetPetResponse], error) {
	const op = "Handler.GetPet"

	uid, err := parseUUID("id", req.Msg.GetId())
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	item, err := read(ctx, h, func(c context.Context) (db.Pet, error) {
		return h.queries.GetPet(c, uid)
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}
	return connect.NewResponse(&petv1.GetPetResponse{Pet: toProtoPet(item)}), nil
}

func (h *Handler) ListPets(
	ctx context.Context, req *connect.Request[petv1.ListPetsRequest],
) (*connect.Response[petv1.ListPetsResponse], error) {
	const op = "Handler.ListPets"
	msg := req.Msg

	limit, offset, err := pageBounds(msg.GetPage(), msg.GetPageSize())
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	var statusParam pgtype.Text
	if msg.GetStatus() != petv1.PetStatus_PET_STATUS_UNSPECIFIED {
		statusParam = pgtype.Text{String: msg.GetStatus().String(), Valid: true}
	}
	var speciesParam pgtype.Text
	if msg.GetSpecies() != "" {
		speciesParam = pgtype.Text{String: msg.GetSpecies(), Valid: true}
	}

	pets, err := read(ctx, h, func(c context.Context) ([]db.Pet, error) {
		return h.queries.ListPets(c, db.ListPetsParams{
			Limit:   limit,
			Offset:  offset,
			Status:  statusParam,
			Species: speciesParam,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	totalCount, err := read(ctx, h, func(c context.Context) (int64, error) {
		return h.queries.CountPets(c, db.CountPetsParams{
			Status:  statusParam,
			Species: speciesParam,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	protoPets := make([]*petv1.Pet, len(pets))
	for i := range pets {
		protoPets[i] = toProtoPet(pets[i])
	}

	return connect.NewResponse(&petv1.ListPetsResponse{
		Pets:       protoPets,
		TotalCount: clampToInt32(totalCount),
	}), nil
}

func (h *Handler) UpdatePet(
	ctx context.Context, req *connect.Request[petv1.UpdatePetRequest],
) (*connect.Response[petv1.UpdatePetResponse], error) {
	const op = "Handler.UpdatePet"

	uid, err := parseUUID("id", req.Msg.GetId())
	if err != nil {
		return nil, translate(ctx, op, err)
	}
	input, err := newPetInput(req.Msg)
	if err != nil {
		return nil, translate(ctx, op, err)
	}
	email, err := callerEmail(ctx)
	if err != nil {
		return nil, err
	}

	updated, err := h.queries.UpdatePet(ctx, db.UpdatePetParams{
		ID:                 uid,
		Name:               input.Name,
		Species:            input.Species,
		BirthDate:          input.BirthDate,
		BirthDateEstimated: input.BirthDateEstimated,
		Status:             input.Status,
		PhotoUrls:          input.PhotoUrls,
		Tags:               input.Tags,
		ModifiedBy:         email,
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	return connect.NewResponse(&petv1.UpdatePetResponse{Pet: toProtoPet(updated)}), nil
}

func (h *Handler) DeletePet(
	ctx context.Context, req *connect.Request[petv1.DeletePetRequest],
) (*connect.Response[petv1.DeletePetResponse], error) {
	const op = "Handler.DeletePet"

	uid, err := parseUUID("id", req.Msg.GetId())
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	rowsAffected, err := h.queries.DeletePet(ctx, uid)
	if err != nil {
		return nil, translate(ctx, op, err)
	}
	if rowsAffected == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errNotFound)
	}

	return connect.NewResponse(&petv1.DeletePetResponse{Success: true}), nil
}
