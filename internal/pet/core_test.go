package pet

import (
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/internal/db"
)

// These exercise the pure core. No database, no HTTP, no fixtures — construct a
// value, call the function, compare the result. Every one of them runs in parallel
// because none of them touches anything shared.

func TestNewPetInput(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		msg     *petv2.CreatePetRequest
		wantErr bool
		assert  func(t *testing.T, in petInput)
	}{
		"trims surrounding whitespace": {
			msg: &petv2.CreatePetRequest{
				Name: "  Rex  ", Species: "  dog  ", BirthDate: "2020-01-02",
			},
			assert: func(t *testing.T, in petInput) {
				t.Helper()
				assert.Equal(t, "Rex", in.Name)
				assert.Equal(t, "dog", in.Species)
			},
		},
		"defaults an unspecified status to available": {
			msg: &petv2.CreatePetRequest{Name: "Rex", Species: "dog", BirthDate: "2020-01-02"},
			assert: func(t *testing.T, in petInput) {
				t.Helper()
				assert.Equal(t, petv2.PetStatus_PET_STATUS_AVAILABLE.String(), in.Status)
			},
		},
		"carries the birth-date-estimated flag": {
			msg: &petv2.CreatePetRequest{
				Name: "Rex", Species: "dog", BirthDate: "2020-01-02",
				BirthDateEstimated: true,
			},
			assert: func(t *testing.T, in petInput) {
				t.Helper()
				assert.True(t, in.BirthDateEstimated)
			},
		},
		"leaves the birth-date-estimated flag false when unset": {
			msg: &petv2.CreatePetRequest{Name: "Rex", Species: "dog", BirthDate: "2020-01-02"},
			assert: func(t *testing.T, in petInput) {
				t.Helper()
				assert.False(t, in.BirthDateEstimated)
			},
		},
		"carries photo urls through": {
			msg: &petv2.CreatePetRequest{
				Name: "Rex", Species: "dog", BirthDate: "2020-01-02",
				PhotoUrls: []string{"https://example.com/a.jpg"},
			},
			assert: func(t *testing.T, in petInput) {
				t.Helper()
				assert.Equal(t, []string{"https://example.com/a.jpg"}, in.PhotoUrls)
			},
		},
		"turns nil photo urls into an empty slice": {
			msg: &petv2.CreatePetRequest{Name: "Rex", Species: "dog", BirthDate: "2020-01-02"},
			assert: func(t *testing.T, in petInput) {
				t.Helper()
				assert.NotNil(t, in.PhotoUrls)
				assert.Empty(t, in.PhotoUrls)
			},
		},
		"keeps an explicit status": {
			msg: &petv2.CreatePetRequest{
				Name: "Rex", Species: "dog", BirthDate: "2020-01-02",
				Status: petv2.PetStatus_PET_STATUS_ADOPTED,
			},
			assert: func(t *testing.T, in petInput) {
				t.Helper()
				assert.Equal(t, petv2.PetStatus_PET_STATUS_ADOPTED.String(), in.Status)
			},
		},
		"turns nil tags into an empty slice so they encode as []": {
			msg: &petv2.CreatePetRequest{Name: "Rex", Species: "dog", BirthDate: "2020-01-02"},
			assert: func(t *testing.T, in petInput) {
				t.Helper()
				assert.NotNil(t, in.Tags)
				assert.Empty(t, in.Tags)
			},
		},
		"rejects a blank name": {
			msg:     &petv2.CreatePetRequest{Name: "   ", Species: "dog", BirthDate: "2020-01-02"},
			wantErr: true,
		},
		"rejects a blank species": {
			msg:     &petv2.CreatePetRequest{Name: "Rex", Species: "", BirthDate: "2020-01-02"},
			wantErr: true,
		},
		"rejects a malformed birth date": {
			msg:     &petv2.CreatePetRequest{Name: "Rex", Species: "dog", BirthDate: "02/01/2020"},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			in, err := newPetInput(tc.msg)

			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, errInvalid)
				return
			}
			require.NoError(t, err)
			tc.assert(t, in)
		})
	}
}

func TestParseDate(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		input   string
		wantErr bool
	}{
		"a valid date":        {"2023-05-10", false},
		"a leap day":          {"2024-02-29", false},
		"an invalid leap day": {"2023-02-29", true},
		"the wrong layout":    {"10/05/2023", true},
		"a timestamp":         {"2023-05-10T00:00:00Z", true},
		"nonsense":            {"invalid-date", true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDate(tc.input)

			if tc.wantErr {
				require.ErrorIs(t, err, errInvalid)
				return
			}
			require.NoError(t, err)
			assert.True(t, got.Valid)
			assert.Equal(t, tc.input, got.Time.Format("2006-01-02"))
		})
	}
}

// TestParseDateTreatsEmptyAsUnknown pins the behaviour migration 004 introduced:
// birth_date is nullable, so an empty value is an absent date rather than an error.
func TestParseDateTreatsEmptyAsUnknown(t *testing.T) {
	t.Parallel()

	got, err := parseDate("")

	require.NoError(t, err)
	assert.False(t, got.Valid, "an empty birth date must map to SQL NULL")
}

func TestParseUUID(t *testing.T) {
	t.Parallel()

	t.Run("accepts a canonical uuid", func(t *testing.T) {
		t.Parallel()
		got, err := parseUUID("id", "123e4567-e89b-12d3-a456-426614174000")
		require.NoError(t, err)
		assert.True(t, got.Valid)
	})

	t.Run("names the offending field", func(t *testing.T) {
		t.Parallel()
		_, err := parseUUID("photo_id", "not-a-uuid")
		require.ErrorIs(t, err, errInvalid)
		assert.Contains(t, err.Error(), "photo_id")
	})
}

func TestClampToInt32(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in   int64
		want int32
	}{
		"zero":            {0, 0},
		"in range":        {1234, 1234},
		"at the maximum":  {math.MaxInt32, math.MaxInt32},
		"above the range": {math.MaxInt32 + 1, math.MaxInt32},
		"at the minimum":  {math.MinInt32, math.MinInt32},
		"below the range": {math.MinInt32 - 1, math.MinInt32},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, clampToInt32(tc.in))
		})
	}
}

func TestStatusFromDB(t *testing.T) {
	t.Parallel()

	cases := map[string]petv2.PetStatus{
		"AVAILABLE":            petv2.PetStatus_PET_STATUS_AVAILABLE,
		"PET_STATUS_AVAILABLE": petv2.PetStatus_PET_STATUS_AVAILABLE,
		"PENDING":              petv2.PetStatus_PET_STATUS_PENDING,
		"PET_STATUS_PENDING":   petv2.PetStatus_PET_STATUS_PENDING,
		"ADOPTED":              petv2.PetStatus_PET_STATUS_ADOPTED,
		"PET_STATUS_ADOPTED":   petv2.PetStatus_PET_STATUS_ADOPTED,
		"UNKNOWN_STATUS":       petv2.PetStatus_PET_STATUS_UNSPECIFIED,
		"":                     petv2.PetStatus_PET_STATUS_UNSPECIFIED,
	}

	for stored, want := range cases {
		t.Run("stored as "+stored, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, statusFromDB(stored))
		})
	}
}

func TestToProtoPet(t *testing.T) {
	t.Parallel()

	created := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	row := db.Pet{
		ID:                 pgUUID(t, "123e4567-e89b-12d3-a456-426614174000"),
		Name:               "Rex",
		Species:            "dog",
		BirthDate:          pgtype.Date{Time: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), Valid: true},
		BirthDateEstimated: true,
		Status:             "AVAILABLE",
		Tags:               []string{"good-boy"},
		PhotoUrls:          []string{"https://example.com/rex.jpg"},
		CreatedBy:          "alice@example.com",
		ModifiedBy:         "bob@example.com",
		CreatedAt:          pgtype.Timestamptz{Time: created, Valid: true},
	}

	got := toProtoPet(row)

	// Every field is asserted deliberately: a translation function that silently
	// drops one is the bug this test exists to catch, and an unasserted field is
	// exactly what a field-clearing mutant slips through.
	assert.Equal(t, "123e4567-e89b-12d3-a456-426614174000", got.GetId())
	assert.Equal(t, "Rex", got.GetName())
	assert.Equal(t, "dog", got.GetSpecies())
	assert.Equal(t, "2020-01-02", got.GetBirthDate())
	assert.True(t, got.GetBirthDateEstimated())
	assert.Equal(t, petv2.PetStatus_PET_STATUS_AVAILABLE, got.GetStatus())
	assert.Equal(t, []string{"good-boy"}, got.GetTags())
	assert.Equal(t, "alice@example.com", got.GetCreatedBy())
	assert.Equal(t, "bob@example.com", got.GetModifiedBy())
	assert.Equal(t, created, got.GetCreatedAt().AsTime())
	assert.Nil(t, got.GetModifiedAt())
	assert.Equal(t, []string{"https://example.com/rex.jpg"}, got.GetPhotoUrls())
}

func TestToProtoPetCarriesModifiedAt(t *testing.T) {
	t.Parallel()

	modified := time.Date(2025, 6, 2, 9, 30, 0, 0, time.UTC)
	got := toProtoPet(db.Pet{
		ID:         pgUUID(t, "123e4567-e89b-12d3-a456-426614174000"),
		ModifiedAt: pgtype.Timestamptz{Time: modified, Valid: true},
	})

	require.NotNil(t, got.GetModifiedAt())
	assert.Equal(t, modified, got.GetModifiedAt().AsTime())
}

func TestToProtoPetOmitsAnUnsetCreatedAt(t *testing.T) {
	t.Parallel()

	got := toProtoPet(db.Pet{ID: pgUUID(t, "123e4567-e89b-12d3-a456-426614174000")})

	assert.Nil(t, got.GetCreatedAt())
	assert.Nil(t, got.GetModifiedAt())
}

func TestToProtoPetOmitsAnInvalidBirthDate(t *testing.T) {
	t.Parallel()

	got := toProtoPet(db.Pet{ID: pgUUID(t, "123e4567-e89b-12d3-a456-426614174000")})

	assert.Empty(t, got.GetBirthDate())
}

// pgUUID parses a UUID string into its Postgres form, failing the test if it cannot.
func pgUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()

	var id pgtype.UUID
	require.NoError(t, id.Scan(s))
	return id
}
