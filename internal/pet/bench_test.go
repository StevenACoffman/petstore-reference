package pet

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/internal/db"
)

// Benchmarks over the functional core. The core is the only part worth measuring
// in CI: it is deterministic, allocation-visible, and needs no database, so a
// change in ns/op is a change in the code rather than in the runner's mood.
//
// Everything below uses b.Loop, which keeps arguments and results alive so the
// compiler cannot delete the call being measured.

const benchPetID = "123e4567-e89b-12d3-a456-426614174000"

// benchPet is a fully populated row: every optional column set, so the
// translation does its maximum work rather than skipping branches.
func benchPet(tb testing.TB) db.Pet {
	tb.Helper()
	now := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	return db.Pet{
		ID:                 pgtype.UUID{Bytes: uuid.MustParse(benchPetID), Valid: true},
		Name:               "Rex",
		Species:            "dog",
		BirthDate:          pgtype.Date{Time: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), Valid: true},
		BirthDateEstimated: true,
		Status:             "AVAILABLE",
		Tags:               []string{"good-boy", "house-trained"},
		PhotoUrls:          []string{"https://example.com/rex.jpg"},
		CreatedBy:          "alice@example.com",
		ModifiedBy:         "bob@example.com",
		CreatedAt:          pgtype.Timestamptz{Time: now, Valid: true},
		ModifiedAt:         pgtype.Timestamptz{Time: now, Valid: true},
	}
}

// BenchmarkNewPetInput covers validation and normalization, which runs once per
// CreatePet.
func BenchmarkNewPetInput(b *testing.B) {
	msg := &petv2.CreatePetRequest{
		Name:      "  Rex  ",
		Species:   "  dog  ",
		BirthDate: "2020-01-02",
		Status:    petv2.PetStatus_PET_STATUS_AVAILABLE,
		Tags:      []string{"good-boy", "house-trained"},
		PhotoUrls: []string{"https://example.com/rex.jpg"},
	}

	for b.Loop() {
		if _, err := newPetInput(msg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkToProtoPet is the hottest translation in the service: it runs once per
// row, so a page of 20 pays it 20 times.
func BenchmarkToProtoPet(b *testing.B) {
	row := benchPet(b)

	for b.Loop() {
		_ = toProtoPet(row)
	}
}

// BenchmarkNewUpdateParams measures the field-mask merge. The masked case is the
// one that matters, because it walks the mask for every writable field.
func BenchmarkNewUpdateParams(b *testing.B) {
	id := pgtype.UUID{Bytes: uuid.MustParse(benchPetID), Valid: true}
	name := "Rex"
	species := "dog"
	msg := &petv2.UpdatePetRequest{
		Name:    &name,
		Species: &species,
		Status:  petv2.PetStatus_PET_STATUS_ADOPTED.Enum(),
		Tags:    []string{"good-boy"},
		UpdateMask: &fieldmaskpb.FieldMask{
			Paths: []string{"name", "species", "status", "tags"},
		},
	}

	for b.Loop() {
		if _, err := newUpdateParams(msg, id, "bob@example.com"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeCursor(b *testing.B) {
	id := pgtype.UUID{Bytes: uuid.MustParse(benchPetID), Valid: true}
	createdAt := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)

	for b.Loop() {
		_ = encodeCursor(createdAt, id)
	}
}

func BenchmarkDecodeCursor(b *testing.B) {
	id := pgtype.UUID{Bytes: uuid.MustParse(benchPetID), Valid: true}
	token := encodeCursor(time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), id)

	for b.Loop() {
		if _, _, err := decodeCursor(token); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSplitPage measures a full page plus the lookahead row, which is the
// case that has to encode a next-page token.
func BenchmarkSplitPage(b *testing.B) {
	const limit int32 = 20
	rows := make([]db.ListPetsRow, 0, limit+1)
	base := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := range int(limit) + 1 {
		rows = append(rows, db.ListPetsRow{
			ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
			Name:      "Rex",
			Species:   "dog",
			Status:    "AVAILABLE",
			CreatedAt: pgtype.Timestamptz{Time: base.Add(time.Duration(i) * time.Second), Valid: true},
		})
	}

	for b.Loop() {
		_, _ = splitPage(rows, limit)
	}
}

// BenchmarkListBounds covers the paging arguments, including decoding the token
// the previous page handed back.
func BenchmarkListBounds(b *testing.B) {
	id := pgtype.UUID{Bytes: uuid.MustParse(benchPetID), Valid: true}
	token := encodeCursor(time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC), id)

	for b.Loop() {
		if _, _, _, err := listBounds(20, 0, token); err != nil {
			b.Fatal(err)
		}
	}
}
