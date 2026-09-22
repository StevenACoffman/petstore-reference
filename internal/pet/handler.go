package pet

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/gen/go/pet/v2/petv2connect"
	"github.com/example/pets/internal/auth"
	"github.com/example/pets/internal/db"
	"github.com/example/pets/internal/resilience"
)

// This file is the imperative shell: queries in, responses out. Every decision is
// delegated to the pure core in core.go.

// Handler serves the PetService RPCs.
type Handler struct {
	queries *db.Queries
	// resilientDB applies retry and circuit-breaker policies to read operations. It
	// may be nil, in which case reads run unprotected.
	resilientDB *resilience.DB
}

var _ petv2connect.PetServiceHandler = (*Handler)(nil)

// NewHandler builds a Handler over the given pool.
//
// A nil pool is accepted so the routing table can be built without a database.
// Every RPC then answers Unavailable rather than panicking — enforced by query and
// exec below, asserted by TestHandlerWithoutADatabase.
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

// errNoDatabase marks a handler built without a pool. run() always supplies one,
// but answering Unavailable beats panicking if that ever changes.
var errNoDatabase = errors.New("database is not configured")

// query runs an idempotent read under retry and the breaker. op may run more than
// once.
func query[T any](ctx context.Context, h *Handler, op func(context.Context) (T, error)) (T, error) {
	var zero T
	if h.queries == nil {
		return zero, errNoDatabase
	}
	return resilience.Read(ctx, h.resilientDB, op)
}

// exec runs a write under the breaker alone, at most once. Writes are never
// retried: replaying one that may already have committed is worse than surfacing
// the error. The breaker still applies, so an outage fails fast and a failing write
// helps open it.
func exec[T any](ctx context.Context, h *Handler, op func(context.Context) (T, error)) (T, error) {
	var zero T
	if h.queries == nil {
		return zero, errNoDatabase
	}
	return resilience.Write(ctx, h.resilientDB, op)
}

// callerEmail reads the identity the audit columns record.
func callerEmail(ctx context.Context) (string, error) {
	email, ok := auth.UserEmailFromContext(ctx)
	if !ok {
		return "", connect.NewError(connect.CodeUnauthenticated, errUnauthClaims)
	}
	return email, nil
}

func (h *Handler) CreatePet(
	ctx context.Context, req *connect.Request[petv2.CreatePetRequest],
) (*connect.Response[petv2.CreatePetResponse], error) {
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

	return connect.NewResponse(&petv2.CreatePetResponse{Pet: toProtoPet(created)}), nil
}

func (h *Handler) GetPet(
	ctx context.Context, req *connect.Request[petv2.GetPetRequest],
) (*connect.Response[petv2.GetPetResponse], error) {
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
	return connect.NewResponse(&petv2.GetPetResponse{Pet: toProtoPet(item)}), nil
}

func (h *Handler) ListPets(
	ctx context.Context, req *connect.Request[petv2.ListPetsRequest],
) (*connect.Response[petv2.ListPetsResponse], error) {
	const op = "Handler.ListPets"
	msg := req.Msg

	// GetPage is read precisely so a non-zero value can be refused; see listBounds.
	limit, cursorAt, cursorID, err := listBounds(msg.GetPageSize(), msg.GetPage(), msg.GetPageToken()) //nolint:staticcheck // SA1019: the deprecated field is read only to reject it.
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	var statusParam pgtype.Text
	if msg.GetStatus() != petv2.PetStatus_PET_STATUS_UNSPECIFIED {
		statusParam = pgtype.Text{String: msg.GetStatus().String(), Valid: true}
	}
	var speciesParam pgtype.Text
	if msg.GetSpecies() != "" {
		speciesParam = pgtype.Text{String: msg.GetSpecies(), Valid: true}
	}

	// One query yields the page and the total, from one snapshot, so they cannot
	// disagree the way two round trips could.
	rows, err := query(ctx, h, func(c context.Context) ([]db.ListPetsRow, error) {
		return h.queries.ListPets(c, db.ListPetsParams{
			// One extra row probes for a next page; splitPage trims it.
			PageSize:        limit + 1,
			CursorCreatedAt: cursorAt,
			CursorID:        cursorID,
			Status:          statusParam,
			Species:         speciesParam,
		})
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	page, token := splitPage(rows, limit)
	protoPets := make([]*petv2.Pet, len(page))
	for i := range page {
		protoPets[i] = toProtoPet(petFromListRow(page[i]))
	}
	// Every row carries the same window total; an empty page means nothing matched.
	var total int64
	if len(page) > 0 {
		total = page[0].TotalCount
	}

	return connect.NewResponse(&petv2.ListPetsResponse{
		Pets:          protoPets,
		TotalCount:    clampToInt32(total),
		NextPageToken: token,
	}), nil
}

func (h *Handler) UpdatePet(
	ctx context.Context, req *connect.Request[petv2.UpdatePetRequest],
) (*connect.Response[petv2.UpdatePetResponse], error) {
	const op = "Handler.UpdatePet"

	uid, err := parseUUID("id", req.Msg.GetId())
	if err != nil {
		return nil, translate(ctx, op, err)
	}
	email, err := callerEmail(ctx)
	if err != nil {
		return nil, err
	}
	params, err := newUpdateParams(req.Msg, uid, email)
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	updated, err := exec(ctx, h, func(c context.Context) (db.Pet, error) {
		return h.queries.UpdatePet(c, params)
	})
	if err != nil {
		return nil, translate(ctx, op, err)
	}

	return connect.NewResponse(&petv2.UpdatePetResponse{Pet: toProtoPet(updated)}), nil
}

func (h *Handler) DeletePet(
	ctx context.Context, req *connect.Request[petv2.DeletePetRequest],
) (*connect.Response[petv2.DeletePetResponse], error) {
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

	return connect.NewResponse(&petv2.DeletePetResponse{Success: true}), nil
}
