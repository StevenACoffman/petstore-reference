package pet

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/internal/db"
)

// dbUpdateParams is a local alias so the table below reads without repeating the
// generated package name on every row.
type dbUpdateParams = db.UpdatePetParams

func testUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	return pgUUID(t, "123e4567-e89b-12d3-a456-426614174000")
}

// TestNewUpdateParamsLeavesUnnamedFieldsAlone is the regression test for the bug
// this change exists to fix: renaming a pet used to clear its tags and photo urls,
// blank its birth date, and move it from adopted back to available.
func TestNewUpdateParamsLeavesUnnamedFieldsAlone(t *testing.T) {
	t.Parallel()

	params, err := newUpdateParams(&petv2.UpdatePetRequest{
		Id:         "123e4567-e89b-12d3-a456-426614174000",
		Name:       new("Luna II"),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
	}, testUUID(t), "caller@example.com")

	require.NoError(t, err)
	assert.Equal(t, "Luna II", params.Name.String)
	assert.True(t, params.Name.Valid)

	// Everything else must be "leave alone", which the SQL reads as NULL.
	assert.False(t, params.Species.Valid, "species must not be written")
	assert.False(t, params.Status.Valid, "status must not be written")
	assert.False(t, params.BirthDateEstimated.Valid, "birth_date_estimated must not be written")
	assert.False(t, params.SetBirthDate, "birth_date must not be written")
	assert.Nil(t, params.Tags, "tags must not be written")
	assert.Nil(t, params.PhotoUrls, "photo_urls must not be written")
}

// TestNewUpdateParamsNeverResetsStatus covers the deviation from strict full
// replacement: an unspecified status means "leave it", in masked and unmasked mode
// alike, because a zero enum cannot be told apart from an unsent one.
func TestNewUpdateParamsNeverResetsStatus(t *testing.T) {
	t.Parallel()

	cases := map[string]*petv2.UpdatePetRequest{
		"no mask at all": {
			Name:    new("Luna"),
			Species: new("Cat"),
		},
		"mask naming status but sending none": {
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"status"}},
		},
	}

	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			params, err := newUpdateParams(msg, testUUID(t), "caller@example.com")

			require.NoError(t, err)
			assert.False(t, params.Status.Valid,
				"an unspecified status must never overwrite the stored one")
		})
	}
}

func TestNewUpdateParamsWithoutAMaskWritesWhatWasSent(t *testing.T) {
	t.Parallel()

	params, err := newUpdateParams(&petv2.UpdatePetRequest{
		Name:               new("Luna"),
		Species:            new("Cat"),
		BirthDate:          new("2021-04-04"),
		BirthDateEstimated: new(true),
		Status:             petv2.PetStatus_PET_STATUS_ADOPTED.Enum(),
		Tags:               []string{"calico"},
		PhotoUrls:          []string{"https://example.com/luna.jpg"},
	}, testUUID(t), "caller@example.com")

	require.NoError(t, err)
	assert.Equal(t, "Luna", params.Name.String)
	assert.Equal(t, "Cat", params.Species.String)
	assert.True(t, params.SetBirthDate)
	assert.True(t, params.BirthDate.Valid)
	assert.True(t, params.BirthDateEstimated.Bool)
	assert.Equal(t, "PET_STATUS_ADOPTED", params.Status.String)
	assert.Equal(t, []string{"calico"}, params.Tags)
	assert.Equal(t, []string{"https://example.com/luna.jpg"}, params.PhotoUrls)
}

// TestNewUpdateParamsCanStillClear guards against over-correcting: "leave unchanged"
// must not make deliberate clearing impossible.
func TestNewUpdateParamsCanStillClear(t *testing.T) {
	t.Parallel()

	params, err := newUpdateParams(&petv2.UpdatePetRequest{
		BirthDate:  new(""),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"tags", "photo_urls", "birth_date"}},
	}, testUUID(t), "caller@example.com")

	require.NoError(t, err)
	assert.NotNil(t, params.Tags, "a masked write of tags must clear, not skip")
	assert.Empty(t, params.Tags)
	assert.NotNil(t, params.PhotoUrls)
	assert.Empty(t, params.PhotoUrls)
	assert.True(t, params.SetBirthDate, "a masked birth_date must be written")
	assert.False(t, params.BirthDate.Valid, "an empty birth date stores SQL NULL")
}

func TestNewUpdateParamsRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := map[string]*petv2.UpdatePetRequest{
		"an unknown mask path": {
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"created_by"}},
		},
		"a blank name": {
			Name:       new("   "),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		},
		"a blank species": {
			Species:    new(""),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"species"}},
		},
		"a malformed birth date": {
			BirthDate:  new("04/04/2021"),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"birth_date"}},
		},
	}

	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := newUpdateParams(msg, testUUID(t), "caller@example.com")

			require.ErrorIs(t, err, errInvalid)
		})
	}
}

// TestNewUpdateParamsWritesEachMaskedFieldExactly pins the value written for every
// path individually. Asserting only that a field "was written" leaves a mutant that
// writes the wrong value alive, which is what the field-clear mutants found.
func TestNewUpdateParamsWritesEachMaskedFieldExactly(t *testing.T) {
	t.Parallel()

	full := &petv2.UpdatePetRequest{
		Name:               new("Luna"),
		Species:            new("Cat"),
		BirthDate:          new("2021-04-04"),
		BirthDateEstimated: new(true),
		Status:             petv2.PetStatus_PET_STATUS_ADOPTED.Enum(),
		Tags:               []string{"calico"},
		PhotoUrls:          []string{"https://example.com/luna.jpg"},
	}

	for _, tc := range []struct {
		path   string
		verify func(t *testing.T, params dbUpdateParams)
	}{
		{"name", func(t *testing.T, p dbUpdateParams) {
			t.Helper()
			assert.Equal(t, "Luna", p.Name.String)
			assert.True(t, p.Name.Valid)
		}},
		{"species", func(t *testing.T, p dbUpdateParams) {
			t.Helper()
			assert.Equal(t, "Cat", p.Species.String)
			assert.True(t, p.Species.Valid)
		}},
		{"birth_date", func(t *testing.T, p dbUpdateParams) {
			t.Helper()
			assert.True(t, p.SetBirthDate)
			assert.True(t, p.BirthDate.Valid)
			assert.Equal(t, "2021-04-04", p.BirthDate.Time.Format("2006-01-02"))
		}},
		{"birth_date_estimated", func(t *testing.T, p dbUpdateParams) {
			t.Helper()
			assert.True(t, p.BirthDateEstimated.Valid)
			assert.True(t, p.BirthDateEstimated.Bool)
		}},
		{"status", func(t *testing.T, p dbUpdateParams) {
			t.Helper()
			assert.True(t, p.Status.Valid)
			assert.Equal(t, "PET_STATUS_ADOPTED", p.Status.String)
		}},
		{"tags", func(t *testing.T, p dbUpdateParams) {
			t.Helper()
			assert.Equal(t, []string{"calico"}, p.Tags)
		}},
		{"photo_urls", func(t *testing.T, p dbUpdateParams) {
			t.Helper()
			assert.Equal(t, []string{"https://example.com/luna.jpg"}, p.PhotoUrls)
		}},
	} {
		t.Run("writes "+tc.path, func(t *testing.T) {
			t.Parallel()

			msg, ok := proto.Clone(full).(*petv2.UpdatePetRequest)
			require.True(t, ok, "Clone must preserve the concrete type")
			msg.UpdateMask = &fieldmaskpb.FieldMask{Paths: []string{tc.path}}

			params, err := newUpdateParams(msg, testUUID(t), "caller@example.com")

			require.NoError(t, err)
			tc.verify(t, params)
		})
	}
}

// TestNewUpdateParamsEstimatedFlagIsCarriedFaithfully: a false value must be
// written as false, not skipped, when the mask names it.
func TestNewUpdateParamsEstimatedFlagIsCarriedFaithfully(t *testing.T) {
	t.Parallel()

	for _, want := range []bool{true, false} {
		t.Run(map[bool]string{true: "true", false: "false"}[want], func(t *testing.T) {
			t.Parallel()

			params, err := newUpdateParams(&petv2.UpdatePetRequest{
				BirthDateEstimated: new(want),
				UpdateMask:         &fieldmaskpb.FieldMask{Paths: []string{"birth_date_estimated"}},
			}, testUUID(t), "caller@example.com")

			require.NoError(t, err)
			require.True(t, params.BirthDateEstimated.Valid)
			assert.Equal(t, want, params.BirthDateEstimated.Bool)
		})
	}
}

func TestNewUpdateParamsAlwaysCarriesIdentity(t *testing.T) {
	t.Parallel()

	params, err := newUpdateParams(&petv2.UpdatePetRequest{
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{}},
	}, testUUID(t), "caller@example.com")

	require.NoError(t, err)
	assert.Equal(t, "caller@example.com", params.ModifiedBy)
	assert.Equal(t, testUUID(t), params.ID)
}
