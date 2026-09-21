package pet

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/db"
)

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()

	cases := map[string]time.Time{
		"utc":              time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
		"with nanoseconds": time.Date(2025, 3, 1, 12, 0, 0, 123456789, time.UTC),
		"with an offset":   time.Date(2025, 3, 1, 12, 0, 0, 0, time.FixedZone("CET", 3600)),
		"zero time":        {},
	}

	for name, createdAt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			id := pgUUID(t, "123e4567-e89b-12d3-a456-426614174000")

			gotAt, gotID, err := decodeCursor(encodeCursor(createdAt, id))

			require.NoError(t, err)
			assert.True(t, createdAt.Equal(gotAt), "want %s, got %s", createdAt, gotAt)
			assert.Equal(t, id, gotID)
		})
	}
}

// Each rejection names its own cause, so a caller debugging a bad token learns
// which part is wrong. Asserting the message, not just the class, is what keeps
// the three branches distinguishable.
func TestDecodeCursorRejectsGarbage(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		token   string
		wantMsg string
	}{
		"not base64":        {"!!!not base64!!!", "base64"},
		"no separator":      {"MjAyNS0wMy0wMVQxMjowMDowMFo", "malformed"},
		"bad timestamp":     {encodeRaw("not-a-time|123e4567-e89b-12d3-a456-426614174000"), "timestamp"},
		"bad uuid":          {encodeRaw("2025-03-01T12:00:00Z|not-a-uuid"), "id"},
		"empty":             {encodeRaw(""), "malformed"},
		"separator only":    {encodeRaw("|"), "timestamp"},
		"timestamp missing": {encodeRaw("|123e4567-e89b-12d3-a456-426614174000"), "timestamp"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, err := decodeCursor(tc.token)

			require.ErrorIs(t, err, errInvalid, "a bad token is the caller's mistake")
			assert.Contains(t, err.Error(), tc.wantMsg, "the message must name the cause")
		})
	}
}

func TestListBounds(t *testing.T) {
	t.Parallel()

	validToken := encodeCursor(
		time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC),
		pgUUID(t, "123e4567-e89b-12d3-a456-426614174000"),
	)

	t.Run("defaults to 20 with no cursor", func(t *testing.T) {
		t.Parallel()
		limit, at, _, err := listBounds(0, 0, "")
		require.NoError(t, err)
		assert.Equal(t, int32(20), limit)
		assert.False(t, at.Valid, "no cursor means no predicate")
	})

	t.Run("caps the page size at 200", func(t *testing.T) {
		t.Parallel()
		limit, _, _, err := listBounds(10_000, 0, "")
		require.NoError(t, err)
		assert.Equal(t, int32(200), limit)
	})

	t.Run("honours a page size of one", func(t *testing.T) {
		t.Parallel()
		limit, _, _, err := listBounds(1, 0, "")
		require.NoError(t, err)
		assert.Equal(t, int32(1), limit, "1 is a size, not an absent value")
	})

	t.Run("a negative page size falls back to the default", func(t *testing.T) {
		t.Parallel()
		limit, _, _, err := listBounds(-5, 0, "")
		require.NoError(t, err)
		assert.Equal(t, int32(20), limit)
	})

	t.Run("decodes a cursor", func(t *testing.T) {
		t.Parallel()
		_, at, id, err := listBounds(10, 0, validToken)
		require.NoError(t, err)
		assert.True(t, at.Valid)
		assert.True(t, id.Valid)
	})

	// The deprecated offset field is refused rather than honoured: serving it is
	// what duplicated and skipped rows.
	t.Run("refuses the deprecated page field", func(t *testing.T) {
		t.Parallel()
		_, _, _, err := listBounds(10, 2, "")
		require.ErrorIs(t, err, errInvalid)
		assert.Contains(t, err.Error(), "page_token")
	})

	t.Run("refuses a malformed cursor", func(t *testing.T) {
		t.Parallel()
		_, _, _, err := listBounds(10, 0, "garbage")
		require.ErrorIs(t, err, errInvalid)
	})
}

func TestSplitPage(t *testing.T) {
	t.Parallel()

	row := func(n int) db.ListPetsRow {
		return db.ListPetsRow{
			ID:        pgUUID(t, fmt.Sprintf("123e4567-e89b-12d3-a456-42661417400%d", n)),
			CreatedAt: pgtype.Timestamptz{Time: time.Date(2025, 3, 1, 12, 0, n, 0, time.UTC), Valid: true},
		}
	}

	t.Run("a short page is the last one", func(t *testing.T) {
		t.Parallel()
		page, token := splitPage([]db.ListPetsRow{row(1), row(2)}, 5)
		assert.Len(t, page, 2)
		assert.Empty(t, token, "no further rows means no token")
	})

	t.Run("an empty page is the last one", func(t *testing.T) {
		t.Parallel()
		page, token := splitPage(nil, 5)
		assert.Empty(t, page)
		assert.Empty(t, token)
	})

	// The probe row is what distinguishes "exactly full, nothing after" from
	// "exactly full, more to come" without a second query.
	t.Run("an exactly full page with no probe is the last one", func(t *testing.T) {
		t.Parallel()
		page, token := splitPage([]db.ListPetsRow{row(1), row(2)}, 2)
		assert.Len(t, page, 2)
		assert.Empty(t, token)
	})

	t.Run("the probe row is trimmed and yields a token", func(t *testing.T) {
		t.Parallel()
		page, token := splitPage([]db.ListPetsRow{row(1), row(2), row(3)}, 2)
		require.Len(t, page, 2, "the probe row must not be served")
		require.NotEmpty(t, token)

		// The token points at the last served row, not the probe.
		gotAt, gotID, err := decodeCursor(token)
		require.NoError(t, err)
		assert.Equal(t, page[1].ID, gotID)
		assert.True(t, page[1].CreatedAt.Time.Equal(gotAt))
	})
}

// encodeRaw base64-encodes a payload directly, so malformed cursors can be built.
func encodeRaw(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// TestPetFromListRowCopiesEveryField: the conversion exists only to drop
// total_count, so any field it silently fails to carry is a bug the rest of the
// service cannot see. Asserting each one is what kills a field-clearing mutant.
func TestPetFromListRowCopiesEveryField(t *testing.T) {
	t.Parallel()

	created := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	modified := time.Date(2025, 4, 2, 9, 30, 0, 0, time.UTC)
	row := db.ListPetsRow{
		ID:                 pgUUID(t, "123e4567-e89b-12d3-a456-426614174000"),
		Name:               "Rex",
		Species:            "dog",
		BirthDate:          pgtype.Date{Time: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), Valid: true},
		BirthDateEstimated: true,
		Status:             "PET_STATUS_ADOPTED",
		Tags:               []string{"good-boy"},
		CreatedAt:          pgtype.Timestamptz{Time: created, Valid: true},
		ModifiedAt:         pgtype.Timestamptz{Time: modified, Valid: true},
		CreatedBy:          "alice@example.com",
		ModifiedBy:         "bob@example.com",
		PhotoUrls:          []string{"https://example.com/rex.jpg"},
		TotalCount:         99,
	}

	got := petFromListRow(row)

	assert.Equal(t, row.ID, got.ID)
	assert.Equal(t, "Rex", got.Name)
	assert.Equal(t, "dog", got.Species)
	assert.Equal(t, row.BirthDate, got.BirthDate)
	assert.True(t, got.BirthDateEstimated)
	assert.Equal(t, "PET_STATUS_ADOPTED", got.Status)
	assert.Equal(t, []string{"good-boy"}, got.Tags)
	assert.Equal(t, row.CreatedAt, got.CreatedAt)
	assert.Equal(t, row.ModifiedAt, got.ModifiedAt)
	assert.Equal(t, "alice@example.com", got.CreatedBy)
	assert.Equal(t, "bob@example.com", got.ModifiedBy)
	assert.Equal(t, []string{"https://example.com/rex.jpg"}, got.PhotoUrls)
}

// TestDecodeCursorReturnsZeroesOnFailure: a caller that ignores the error must not
// receive a usable-looking cursor.
func TestDecodeCursorReturnsZeroesOnFailure(t *testing.T) {
	t.Parallel()

	for name, token := range map[string]string{
		"not base64":    "!!!",
		"no separator":  encodeRaw("no-separator-here"),
		"bad timestamp": encodeRaw("nope|123e4567-e89b-12d3-a456-426614174000"),
		"bad uuid":      encodeRaw("2025-03-01T12:00:00Z|nope"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			at, id, err := decodeCursor(token)

			require.Error(t, err)
			assert.True(t, at.IsZero(), "a failed decode must not yield a time")
			assert.False(t, id.Valid, "a failed decode must not yield an id")
		})
	}
}

// TestListBoundsReturnsZeroesOnFailure: same contract at the layer above.
func TestListBoundsReturnsZeroesOnFailure(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		page  int32
		token string
	}{
		"the deprecated page field": {page: 3},
		"a malformed token":         {token: "garbage"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			limit, at, id, err := listBounds(10, tc.page, tc.token)

			require.ErrorIs(t, err, errInvalid)
			assert.Zero(t, limit, "a rejected request must not yield a usable limit")
			assert.False(t, at.Valid)
			assert.False(t, id.Valid)
		})
	}
}

// TestListBoundsCursorValuesAreExact pins what the predicate actually receives.
func TestListBoundsCursorValuesAreExact(t *testing.T) {
	t.Parallel()

	want := time.Date(2025, 3, 1, 12, 34, 56, 789000000, time.UTC)
	wantID := pgUUID(t, "123e4567-e89b-12d3-a456-426614174000")

	limit, at, id, err := listBounds(7, 0, encodeCursor(want, wantID))

	require.NoError(t, err)
	assert.Equal(t, int32(7), limit)
	require.True(t, at.Valid)
	assert.True(t, want.Equal(at.Time), "want %s, got %s", want, at.Time)
	assert.Equal(t, wantID, id)
}
