package pet

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/internal/db"
)

// This file is the functional core: validation, decisions, and wire/storage
// translation, with no I/O. The imperative shell lives in handler.go.

// errInvalid marks a validation failure, the only error class the core produces.
// The shell maps it onto connect.CodeInvalidArgument.
var errInvalid = errors.New("invalid argument")

const (
	defaultPageSize int32 = 20
	// maxPageSize stops one caller asking for an unbounded result set.
	maxPageSize     int32 = 200
	birthDateLayout       = "2006-01-02"
	statusPrefix          = "PET_STATUS_"
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

// newPetInput validates and normalizes a creation request. Updates go through
// newUpdateParams instead, which needs per-field presence.
//
// Ensures: on success Name and Species are trimmed and non-empty, the slices are
// non-nil, and Status is concrete. Failures wrap errInvalid and name the field.
func newPetInput(msg *petv2.CreatePetRequest) (petInput, error) {
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
	if status == petv2.PetStatus_PET_STATUS_UNSPECIFIED {
		status = petv2.PetStatus_PET_STATUS_AVAILABLE
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

// parseDate converts a YYYY-MM-DD string into a Postgres date. A birth date is
// optional, so an empty value is an absent date (SQL NULL), not an error.
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

// parseUUID converts a caller-supplied UUID. A failure wraps errInvalid and names
// the field.
func parseUUID(field, value string) (pgtype.UUID, error) {
	var uid pgtype.UUID
	if err := uid.Scan(value); err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: %s is not a valid UUID", errInvalid, field)
	}
	return uid, nil
}

// clampToInt32 narrows a count to int32, saturating rather than wrapping.
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

// statusFromDB maps a stored status onto the enum, with or without the
// PET_STATUS_ prefix. An unrecognised value maps to UNSPECIFIED rather than
// failing: a reader should not error on a value a newer writer introduced.
func statusFromDB(stored string) petv2.PetStatus {
	name := stored
	if !strings.HasPrefix(name, statusPrefix) {
		name = statusPrefix + name
	}
	return petv2.PetStatus(petv2.PetStatus_value[name])
}

// toProtoPet renders a stored pet into the wire type.
func toProtoPet(p db.Pet) *petv2.Pet {
	var birthDate string
	if p.BirthDate.Valid {
		birthDate = p.BirthDate.Time.Format(birthDateLayout)
	}

	protoPet := &petv2.Pet{
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

// updatePaths are the writable update_mask paths. An unknown path is rejected
// rather than ignored: half-applying a misunderstood request is worse than
// refusing it.
var updatePaths = map[string]bool{
	"name":                 true,
	"species":              true,
	"birth_date":           true,
	"birth_date_estimated": true,
	"status":               true,
	"photo_urls":           true,
	"tags":                 true,
}

// newUpdateParams builds the UpdatePet parameters, where a null means "leave this
// column". The mask semantics are documented on UpdatePetRequest in the proto.
//
// An unspecified status always means "leave alone": a zero enum cannot be told
// from an unsent one, and resetting an adopted pet to available is never intended.
func newUpdateParams(
	msg *petv2.UpdatePetRequest, id pgtype.UUID, modifiedBy string,
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
	if writes("status", true) && msg.GetStatus() != petv2.PetStatus_PET_STATUS_UNSPECIFIED {
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

// orEmpty replaces nil with empty, so a masked write of a repeated field clears it
// rather than reading as "leave alone" in the SQL.
func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// cursorSeparator splits the two components of a page token.
const cursorSeparator = "|"

// encodeCursor builds the opaque page token identifying the last row of a page.
//
// The token is opaque by convention, not secret: it carries a timestamp and an id
// the caller already holds, and forging one only reaches a page they could have
// paged to anyway. It is not an authorization boundary.
func encodeCursor(createdAt time.Time, id pgtype.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + cursorSeparator + uuid.UUID(id.Bytes).String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor. A malformed token wraps errInvalid, so a
// caller pasting junk gets InvalidArgument rather than an internal error.
func decodeCursor(token string) (createdAt time.Time, id pgtype.UUID, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, pgtype.UUID{}, fmt.Errorf("%w: page_token is not valid base64", errInvalid)
	}

	timestamp, rawID, found := strings.Cut(string(raw), cursorSeparator)
	if !found {
		return time.Time{}, pgtype.UUID{}, fmt.Errorf("%w: page_token is malformed", errInvalid)
	}

	createdAt, err = time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return time.Time{}, pgtype.UUID{}, fmt.Errorf("%w: page_token has an invalid timestamp", errInvalid)
	}
	if err := id.Scan(rawID); err != nil {
		return time.Time{}, pgtype.UUID{}, fmt.Errorf("%w: page_token has an invalid id", errInvalid)
	}

	return createdAt, id, nil
}

// listBounds resolves a list request's paging inputs.
//
// Requires: page is the deprecated offset field; a non-zero value is refused
// rather than honoured, because offset paging is what served rows twice.
func listBounds(pageSize, page int32, pageToken string) (limit int32, cursor pgtype.Timestamptz, cursorID pgtype.UUID, err error) {
	if page != 0 {
		return 0, cursor, cursorID, fmt.Errorf(
			"%w: page is no longer supported because offset paging skipped and repeated rows; use page_token",
			errInvalid)
	}

	limit = defaultPageSize
	if pageSize > 0 {
		limit = min(pageSize, maxPageSize)
	}
	if pageToken == "" {
		return limit, cursor, cursorID, nil
	}

	createdAt, id, err := decodeCursor(pageToken)
	if err != nil {
		return 0, cursor, cursorID, err
	}
	return limit, pgtype.Timestamptz{Time: createdAt, Valid: true}, id, nil
}

// petFromListRow drops the window's total_count so the row can share toProtoPet
// with every other read.
func petFromListRow(row db.ListPetsRow) db.Pet {
	return db.Pet{
		ID:                 row.ID,
		Name:               row.Name,
		Species:            row.Species,
		BirthDate:          row.BirthDate,
		BirthDateEstimated: row.BirthDateEstimated,
		Status:             row.Status,
		Tags:               row.Tags,
		CreatedAt:          row.CreatedAt,
		ModifiedAt:         row.ModifiedAt,
		CreatedBy:          row.CreatedBy,
		ModifiedBy:         row.ModifiedBy,
		PhotoUrls:          row.PhotoUrls,
	}
}

// splitPage trims the extra row fetched to probe for a next page, and returns the
// token to reach it.
//
// The query asks for limit+1 rows. Getting them back is how we know another page
// exists without a second query, and it means a final page that happens to be
// exactly full does not hand the caller an empty page whose total_count would
// be unknowable.
//
// Requires: limit > 0; rows holds at most limit+1 entries.
// Ensures:  the returned slice holds at most limit rows; the token is empty
//
//	exactly when no further rows exist.
func splitPage(rows []db.ListPetsRow, limit int32) (page []db.ListPetsRow, nextToken string) {
	if len(rows) <= int(limit) {
		return rows, ""
	}
	page = rows[:limit]
	last := page[len(page)-1]
	return page, encodeCursor(last.CreatedAt.Time, last.ID)
}
