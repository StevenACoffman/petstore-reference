package pet

import (
	"context"
	"errors"

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
// A nil pool is accepted so the routing table can be constructed without a
// database — cmd/server builds the whole mux that way in its routing tests. Every
// RPC on such a handler answers Unavailable; none of them panics. That guarantee is
// enforced in one place, by query and exec below, and asserted by
// TestHandlerWithoutADatabase.
func NewHandler(pool *pgxpool.Pool) *Handler {
	h := &Handler{}
	if pool != nil {
		h.queries = db.New(pool)
	}
	return h
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

// errNoDatabase is returned when a handler was built without a pool. It is not a
// condition a deployed service reaches — run() always supplies a pool — but
// answering Unavailable beats panicking if one ever does.
var errNoDatabase = errors.New("database is not configured")

// query runs an idempotent read under the retry and circuit-breaker policies.
//
// Requires: op is idempotent; it may be invoked more than once.
// Ensures:  returns errNoDatabase without invoking op when no pool was supplied.
func query[T any](ctx context.Context, h *Handler, op func(context.Context) (T, error)) (T, error) {
	var zero T
	if h.queries == nil {
		return zero, errNoDatabase
	}
	return resilience.Read(ctx, h.resilientDB, op)
}

// exec runs a non-idempotent write under the circuit breaker alone.
//
// Writes are never retried: replaying one that may already have committed is worse
// than surfacing the error, and there is no idempotency key to make a replay safe.
// They still go through the breaker so an outage fails fast instead of queueing on
// the connection pool, and so a failing write helps open it.
//
// Requires: op has side effects; it is invoked at most once.
// Ensures:  returns errNoDatabase without invoking op when no pool was supplied.
func exec[T any](ctx context.Context, h *Handler, op func(context.Context) (T, error)) (T, error) {
	var zero T
	if h.queries == nil {
		return zero, errNoDatabase
	}
	return resilience.Write(ctx, h.resilientDB, op)
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

	created, err := exec(ctx, h, func(c context.Context) (db.Pet, error) {
		return h.queries.CreatePet(c, db.CreatePetParams{
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

	item, err := query(ctx, h, func(c context.Context) (db.Pet, error) {
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

	pets, err := query(ctx, h, func(c context.Context) ([]db.Pet, error) {
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

	totalCount, err := query(ctx, h, func(c context.Context) (int64, error) {
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

	updated, err := exec(ctx, h, func(c context.Context) (db.Pet, error) {
		return h.queries.UpdatePet(c, db.UpdatePetParams{
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

	rowsAffected, err := exec(ctx, h, func(c context.Context) (int64, error) {
		return h.queries.DeletePet(c, uid)
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}
	if rowsAffected == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errNotFound)
	}

	return connect.NewResponse(&petv1.DeletePetResponse{Success: true}), nil
}
