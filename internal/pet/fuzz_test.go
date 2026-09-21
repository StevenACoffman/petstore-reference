package pet

import (
	"strings"
	"testing"
	"unicode/utf8"

	petv2 "github.com/example/pets/gen/go/pet/v2"
)

// Fuzz targets for the pure core. Each asserts an invariant rather than a specific
// output, because the point is to find the input that breaks the rule — a panic, a
// value that escapes its documented range, or a validator that lets something
// through it promised to reject.
//
// Run locally with: just fuzz   (or go test -run=X -fuzz=FuzzName ./internal/pet/)

// FuzzParseDate asserts that parseDate never panics and that a date it accepts
// round-trips back to the exact string it was given.
func FuzzParseDate(f *testing.F) {
	for _, seed := range []string{
		"2023-05-10", "2024-02-29", "0001-01-01", "9999-12-31",
		"", "invalid-date", "10/05/2023", "2023-05-10T00:00:00Z",
		"2023-13-01", "2023-00-10", "-2023-05-10", "２０２３-０５-１０",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		got, err := parseDate(input)
		if err != nil {
			// Every rejection must be classified as a validation failure, so the
			// transport layer can map it to InvalidArgument rather than Internal.
			if !strings.Contains(err.Error(), errInvalid.Error()) {
				t.Fatalf("parseDate(%q) rejected with a non-validation error: %v", input, err)
			}
			return
		}
		if !got.Valid {
			// A birth date is optional, so an accepted-but-null result is correct
			// exactly when the input held no date at all.
			if strings.TrimSpace(input) != "" {
				t.Fatalf("parseDate(%q) accepted a non-empty input but produced no date", input)
			}
			return
		}
		if round := got.Time.Format(birthDateLayout); round != strings.TrimSpace(input) {
			t.Fatalf("parseDate(%q) round-tripped to %q", input, round)
		}
	})
}

// FuzzParseUUID asserts that parseUUID never panics and never reports success for a
// value it could not actually parse.
func FuzzParseUUID(f *testing.F) {
	for _, seed := range []string{
		"123e4567-e89b-12d3-a456-426614174000",
		"123E4567-E89B-12D3-A456-426614174000",
		"", "not-a-uuid", "123e4567e89b12d3a456426614174000",
		"123e4567-e89b-12d3-a456-42661417400", "../../etc/passwd",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		got, err := parseUUID("id", input)
		if err != nil {
			if !strings.Contains(err.Error(), errInvalid.Error()) {
				t.Fatalf("parseUUID(%q) rejected with a non-validation error: %v", input, err)
			}
			return
		}
		if !got.Valid {
			t.Fatalf("parseUUID(%q) returned no error but an invalid UUID", input)
		}
	})
}

// FuzzStatusFromDB asserts that no stored string can make the mapping panic, and
// that anything unrecognised lands on UNSPECIFIED rather than an invented enum value.
func FuzzStatusFromDB(f *testing.F) {
	for _, seed := range []string{
		"AVAILABLE", "PET_STATUS_AVAILABLE", "PENDING", "ADOPTED",
		"", "UNKNOWN", "PET_STATUS_", "PET_STATUS_PET_STATUS_AVAILABLE", "\x00",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, stored string) {
		got := statusFromDB(stored)
		if _, known := petv2.PetStatus_name[int32(got)]; !known {
			t.Fatalf("statusFromDB(%q) produced %d, which is not a declared enum value", stored, got)
		}
	})
}

// FuzzNewPetInput asserts the normalization contract that the rest of the service
// relies on: an accepted input has non-blank trimmed fields, non-nil tags, and a
// concrete status.
func FuzzNewPetInput(f *testing.F) {
	f.Add("Rex", "dog", "2020-01-02", int32(0))
	f.Add("  Rex  ", "  dog  ", "2020-01-02", int32(1))
	f.Add("", "dog", "2020-01-02", int32(0))
	f.Add("Rex", "", "bad-date", int32(99))
	f.Add("Rex", "dog", "", int32(0))

	f.Fuzz(func(t *testing.T, name, species, birthDate string, status int32) {
		msg := &petv2.CreatePetRequest{
			Name:      name,
			Species:   species,
			BirthDate: birthDate,
			Status:    petv2.PetStatus(status),
		}

		in, err := newPetInput(msg)
		if err != nil {
			return
		}
		if in.Name == "" || in.Species == "" {
			t.Fatalf("newPetInput accepted a blank name or species: %+v", in)
		}
		if strings.TrimSpace(in.Name) != in.Name || strings.TrimSpace(in.Species) != in.Species {
			t.Fatalf("newPetInput left untrimmed whitespace: %q / %q", in.Name, in.Species)
		}
		if in.Tags == nil {
			t.Fatal("newPetInput left Tags nil, which would encode as JSON null")
		}
		if in.PhotoUrls == nil {
			t.Fatal("newPetInput left PhotoUrls nil, which would encode as JSON null")
		}
		// The birth date is optional, but an absent one is only legitimate when the
		// caller supplied nothing.
		if !in.BirthDate.Valid && strings.TrimSpace(birthDate) != "" {
			t.Fatalf("newPetInput dropped a supplied birth date %q", birthDate)
		}
		if in.Status == petv2.PetStatus_PET_STATUS_UNSPECIFIED.String() {
			t.Fatal("newPetInput left the status unspecified")
		}
		if !utf8.ValidString(in.Name) && utf8.ValidString(name) {
			t.Fatalf("newPetInput corrupted a valid UTF-8 name: %q", in.Name)
		}
	})
}
