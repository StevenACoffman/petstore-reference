package pet

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"

	petv1 "github.com/example/pets/gen/go/pet/v1"
	"github.com/example/pets/internal/db"
)

// This file is the functional core: decisions, validation, and translation between
// wire and storage shapes. Nothing here performs I/O, so every function in it is
// testable by passing values in and comparing values out. The imperative shell —
// transactions, queries, HTTP — lives in handler.go.

// errInvalid marks a validation failure. The shell maps it onto
// connect.CodeInvalidArgument; it is the only error class the core produces.
var errInvalid = errors.New("invalid argument")

const (
	// defaultPageSize is the page size used when a request does not ask for one.
	defaultPageSize int32 = 20
	// maxPageSize caps how much a single request may fetch, so one caller cannot ask
	// the database for an unbounded result set.
	maxPageSize int32 = 200
	// birthDateLayout is the wire format for Pet.birth_date.
	birthDateLayout = "2006-01-02"
	// statusPrefix is the generated enum's value prefix.
	statusPrefix = "PET_STATUS_"
)

// petInput is a create or update request after validation and normalization.
type petInput struct {
	Name               string
	Species            string
	BirthDate          pgtype.Date
	BirthDateEstimated bool
	Status             string
	Tags               []string
	PhotoUrls          []string
}

// newPetInput validates and normalizes a creation request.
//
// Updates do not come through here: partial-update semantics need per-field
// presence, which newUpdateParams handles instead.
//
// Requires: msg is non-nil.
// Ensures:  on success, Name and Species are trimmed and non-empty, Tags is non-nil
//
//	(so it encodes as [] rather than null), Status is a concrete enum name
//	rather than UNSPECIFIED, and BirthDate is either a valid date or an
//	explicit null. On failure the error wraps errInvalid and names the
//	offending field.
func newPetInput(msg *petv1.CreatePetRequest) (petInput, error) {
	name := strings.TrimSpace(msg.GetName())
	species := strings.TrimSpace(msg.GetSpecies())
	if name == "" || species == "" {
		return petInput{}, fmt.Errorf("%w: name and species cannot be blank", errInvalid)
	}

	birthDate, err := parseDate(msg.GetBirthDate())
	if err != nil {
		return petInput{}, err
	}

	status := msg.GetStatus()
	if status == petv1.PetStatus_PET_STATUS_UNSPECIFIED {
		status = petv1.PetStatus_PET_STATUS_AVAILABLE
	}

	// Nil slices are normalised to empty so they encode as [] rather than null, and
	// so the NOT NULL columns behind them never receive a null.
	tags := msg.GetTags()
	if tags == nil {
		tags = []string{}
	}
	photoURLs := msg.GetPhotoUrls()
	if photoURLs == nil {
		photoURLs = []string{}
	}

	return petInput{
		Name:               name,
		Species:            species,
		BirthDate:          birthDate,
		BirthDateEstimated: msg.GetBirthDateEstimated(),
		Status:             status.String(),
		Tags:               tags,
		PhotoUrls:          photoURLs,
	}, nil
}

// parseDate converts a YYYY-MM-DD string into a Postgres date.
//
// A birth date is optional: an empty value is not an error, it is the absence of a
// known date, and maps to SQL NULL. Only a non-empty value that fails to parse is
// rejected.
//
// Requires: nothing.
// Ensures:  err is nil for an empty input, and the returned Date is then invalid
//
//	(SQL NULL); for a parsable input the Date is Valid.
func parseDate(dateStr string) (pgtype.Date, error) {
	trimmed := strings.TrimSpace(dateStr)
	if trimmed == "" {
		return pgtype.Date{Valid: false}, nil
	}
	t, err := time.Parse(birthDateLayout, trimmed)
	if err != nil {
		return pgtype.Date{}, fmt.Errorf("%w: birth_date must be formatted YYYY-MM-DD", errInvalid)
	}
	return pgtype.Date{Time: t, Valid: true}, nil
}

// parseUUID converts a caller-supplied UUID string into its Postgres form.
//
// Requires: nothing.
// Ensures:  err wraps errInvalid and names the field when the value is not a UUID.
func parseUUID(field, value string) (pgtype.UUID, error) {
	var uid pgtype.UUID
	if err := uid.Scan(value); err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: %s is not a valid UUID", errInvalid, field)
	}
	return uid, nil
}

// pageBounds converts a page number and size into SQL LIMIT and OFFSET values.
//
// Requires: nothing; out-of-range input is reported rather than truncated silently.
// Ensures:  on success limit is in [1, maxPageSize] and offset is a non-negative
//
//	int32; on failure both are zero and the error wraps errInvalid.
func pageBounds(page, pageSize int32) (limit, offset int32, err error) {
	limit = defaultPageSize
	if pageSize > 0 {
		limit = min(pageSize, maxPageSize)
	}

	if page < 0 {
		return 0, 0, fmt.Errorf("%w: page cannot be negative", errInvalid)
	}
	// Compute in 64 bits so the overflow check itself cannot overflow.
	wide := int64(page) * int64(limit)
	if wide < 0 || wide > math.MaxInt32 {
		return 0, 0, fmt.Errorf("%w: page offset is too large", errInvalid)
	}
	return limit, int32(wide), nil
}

// clampToInt32 narrows a count to int32, saturating rather than wrapping.
//
// Ensures: the result is the nearest representable int32 to v.
func clampToInt32(v int64) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	default:
		return int32(v)
	}
}

// statusFromDB maps a stored status onto the generated enum, tolerating values
// written with or without the enum's PET_STATUS_ prefix.
//
// Ensures: an unrecognised value maps to PET_STATUS_UNSPECIFIED rather than failing;
//
//	the database is the source of truth and a reader should not error on a
//	value a newer writer introduced.
func statusFromDB(stored string) petv1.PetStatus {
	name := stored
	if !strings.HasPrefix(name, statusPrefix) {
		name = statusPrefix + name
	}
	return petv1.PetStatus(petv1.PetStatus_value[name])
}

// toProtoPet renders a stored pet into the wire type.
//
// Requires: nothing.
// Ensures:  the returned Pet is non-nil; PhotoUrls and Tags are carried through as
//
//	stored, which the NOT NULL columns guarantee are non-null.
func toProtoPet(p db.Pet) *petv1.Pet {
	var birthDate string
	if p.BirthDate.Valid {
		birthDate = p.BirthDate.Time.Format(birthDateLayout)
	}

	protoPet := &petv1.Pet{
		Id:                 uuid.UUID(p.ID.Bytes).String(),
		Name:               p.Name,
		Species:            p.Species,
		BirthDate:          birthDate,
		BirthDateEstimated: p.BirthDateEstimated,
		Status:             statusFromDB(p.Status),
		PhotoUrls:          p.PhotoUrls,
		Tags:               p.Tags,
		CreatedBy:          p.CreatedBy,
		ModifiedBy:         p.ModifiedBy,
	}
	if p.CreatedAt.Valid {
		protoPet.CreatedAt = timestamppb.New(p.CreatedAt.Time)
	}
	if p.ModifiedAt.Valid {
		protoPet.ModifiedAt = timestamppb.New(p.ModifiedAt.Time)
	}
	return protoPet
}

// updatePaths are the update_mask paths this service understands. A mask naming
// anything else is rejected rather than silently ignored: a client that asks to
// change a field the server does not know about has misunderstood the API, and
// discarding the request half-applied would be worse than refusing it.
var updatePaths = map[string]bool{
	"name":                 true,
	"species":              true,
	"birth_date":           true,
	"birth_date_estimated": true,
	"status":               true,
	"photo_urls":           true,
	"tags":                 true,
}

// newUpdateParams turns an update request into the nullable parameter set the
// UpdatePet statement expects, where a null parameter means "leave this column".
//
// Two modes, per the contract documented on UpdatePetRequest:
//   - mask present: only the named paths are written.
//   - mask absent: full replacement of every field the caller can set.
//
// In both modes an unspecified status leaves the stored status alone. That is a
// deliberate deviation from strict replacement: a zero enum is indistinguishable
// from an unsent one, and resetting an adopted pet to available is never intended.
//
// Requires: msg is non-nil; id identifies the row; modifiedBy is the caller.
// Ensures:  on success every field the mask names (or, with no mask, every field
//
//	the caller set) appears in the result, and no other column is
//	touched. A mask naming an unknown path is an errInvalid failure.
func newUpdateParams(
	msg *petv1.UpdatePetRequest, id pgtype.UUID, modifiedBy string,
) (db.UpdatePetParams, error) {
	params := db.UpdatePetParams{ID: id, ModifiedBy: modifiedBy}

	mask := msg.GetUpdateMask()
	masked := mask != nil
	if masked {
		for _, path := range mask.GetPaths() {
			if !updatePaths[path] {
				return db.UpdatePetParams{}, fmt.Errorf(
					"%w: update_mask names unknown field %q", errInvalid, path)
			}
		}
	}
	// writes reports whether a field should be written: everything the caller set
	// when there is no mask, only the named paths when there is one.
	writes := func(path string, setWithoutMask bool) bool {
		if masked {
			return slices.Contains(mask.GetPaths(), path)
		}
		return setWithoutMask
	}

	if writes("name", msg.Name != nil) {
		name := strings.TrimSpace(msg.GetName())
		if name == "" {
			return db.UpdatePetParams{}, fmt.Errorf("%w: name cannot be blank", errInvalid)
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if writes("species", msg.Species != nil) {
		species := strings.TrimSpace(msg.GetSpecies())
		if species == "" {
			return db.UpdatePetParams{}, fmt.Errorf("%w: species cannot be blank", errInvalid)
		}
		params.Species = pgtype.Text{String: species, Valid: true}
	}
	if writes("birth_date", msg.BirthDate != nil) {
		birthDate, err := parseDate(msg.GetBirthDate())
		if err != nil {
			return db.UpdatePetParams{}, err
		}
		// SetBirthDate is what distinguishes "store NULL" from "leave alone"; the
		// column is nullable so COALESCE cannot tell those apart.
		params.SetBirthDate = true
		params.BirthDate = birthDate
	}
	if writes("birth_date_estimated", msg.BirthDateEstimated != nil) {
		params.BirthDateEstimated = pgtype.Bool{Bool: msg.GetBirthDateEstimated(), Valid: true}
	}
	// An unspecified status is always "leave it", never "reset to available".
	if writes("status", true) && msg.GetStatus() != petv1.PetStatus_PET_STATUS_UNSPECIFIED {
		params.Status = pgtype.Text{String: msg.GetStatus().String(), Valid: true}
	}
	if writes("photo_urls", msg.GetPhotoUrls() != nil) {
		params.PhotoUrls = orEmpty(msg.GetPhotoUrls())
	}
	if writes("tags", msg.GetTags() != nil) {
		params.Tags = orEmpty(msg.GetTags())
	}

	return params, nil
}

// orEmpty replaces a nil slice with an empty one, so that a masked write of a
// repeated field clears it rather than being read as "leave alone" by the SQL.
func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
