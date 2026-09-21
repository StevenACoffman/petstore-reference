//go:build integration

package pet

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/suite"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	petv1 "github.com/example/pets/gen/go/pet/v1"
	"github.com/example/pets/internal/auth"
	"github.com/example/pets/internal/testutil"
)

type PetHandlerTestSuite struct {
	suite.Suite
	ctx     context.Context
	cancel  context.CancelFunc
	testDB  *testutil.TestDB
	handler *Handler
}

func (s *PetHandlerTestSuite) SetupSuite() {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	testDB, err := testutil.StartTestDB(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping PetHandler tests: postgres testcontainer unavailable: %v", err)
		return
	}
	s.testDB = testDB
	s.handler = NewHandler(testDB.Pool)
}

func (s *PetHandlerTestSuite) TearDownSuite() {
	if s.testDB != nil {
		s.testDB.Close()
	}
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *PetHandlerTestSuite) SetupTest() {
	s.Require().NoError(s.testDB.TruncateTables(s.ctx), "failed to truncate tables before test")
}

func (s *PetHandlerTestSuite) authContext(email string) context.Context {
	claims := &auth.Claims{
		Email:    email,
		Subject:  "test-user",
		Provider: "test",
		Roles:    []string{"user"},
	}
	return auth.WithClaims(s.ctx, claims)
}

func (s *PetHandlerTestSuite) TestCreatePet() {
	ctx := s.authContext("creator@example.com")

	req := connect.NewRequest(&petv1.CreatePetRequest{
		Name:               "  Luna  ",
		Species:            "  Cat  ",
		BirthDate:          "2023-06-15",
		BirthDateEstimated: true,
		Status:             petv1.PetStatus_PET_STATUS_UNSPECIFIED, // Should default to AVAILABLE
		Tags:               []string{"calico", "friendly"},
		PhotoUrls:          []string{"https://example.com/luna1.jpg", "https://example.com/luna2.jpg"},
	})

	resp, err := s.handler.CreatePet(ctx, req)
	s.Require().NoError(err)
	s.Require().NotNil(resp.Msg.GetPet())

	pet := resp.Msg.GetPet()
	s.NotEmpty(pet.GetId())
	s.Equal("Luna", pet.GetName())   // Trimmed
	s.Equal("Cat", pet.GetSpecies()) // Trimmed
	s.Equal("2023-06-15", pet.GetBirthDate())
	s.True(pet.GetBirthDateEstimated())
	s.Equal(petv1.PetStatus_PET_STATUS_AVAILABLE, pet.GetStatus()) // Defaulted
	s.Equal([]string{"calico", "friendly"}, pet.GetTags())
	s.Equal([]string{"https://example.com/luna1.jpg", "https://example.com/luna2.jpg"}, pet.GetPhotoUrls())
	s.Equal("creator@example.com", pet.GetCreatedBy())
	s.Equal("creator@example.com", pet.GetModifiedBy())

	// Create pet without birth date (optional)
	reqNoBirth := connect.NewRequest(&petv1.CreatePetRequest{
		Name:    "Mochi",
		Species: "Cat",
	})
	respNoBirth, err := s.handler.CreatePet(ctx, reqNoBirth)
	s.Require().NoError(err)
	s.Require().NotNil(respNoBirth.Msg.GetPet())
	s.Empty(respNoBirth.Msg.GetPet().GetBirthDate())
}

func (s *PetHandlerTestSuite) TestCreatePetValidation() {
	ctx := s.authContext("creator@example.com")

	// Blank name
	_, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name:      "   ",
		Species:   "Dog",
		BirthDate: "2023-01-01",
	}))
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))

	// Invalid birth date format
	_, err = s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name:      "Fido",
		Species:   "Dog",
		BirthDate: "not-a-date",
	}))
	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))
}

func (s *PetHandlerTestSuite) TestGetPet() {
	ctx := s.authContext("user@example.com")

	// 1. Not found
	_, err := s.handler.GetPet(ctx, connect.NewRequest(&petv1.GetPetRequest{
		Id: "00000000-0000-0000-0000-000000000000",
	}))
	s.Require().Error(err)
	s.Equal(connect.CodeNotFound, connect.CodeOf(err))

	// 2. Existing pet
	createResp, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name:      "Max",
		Species:   "Dog",
		BirthDate: "2021-04-10",
		Status:    petv1.PetStatus_PET_STATUS_AVAILABLE,
	}))
	s.Require().NoError(err)

	getResp, err := s.handler.GetPet(ctx, connect.NewRequest(&petv1.GetPetRequest{
		Id: createResp.Msg.GetPet().GetId(),
	}))
	s.Require().NoError(err)
	s.Equal(createResp.Msg.GetPet().GetId(), getResp.Msg.GetPet().GetId())
	s.Equal("Max", getResp.Msg.GetPet().GetName())
}

func (s *PetHandlerTestSuite) TestListPets() {
	ctx := s.authContext("user@example.com")

	// Seed 3 pets: 2 Dogs (1 AVAILABLE, 1 ADOPTED), 1 Cat (AVAILABLE)
	dog1Resp, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name:      "Dog1",
		Species:   "Dog",
		BirthDate: "2020-01-01",
		Status:    petv1.PetStatus_PET_STATUS_AVAILABLE,
		PhotoUrls: []string{"https://example.com/dog1.jpg"},
	}))
	s.Require().NoError(err)
	dog1ID := dog1Resp.Msg.GetPet().GetId()

	_, err = s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name: "Dog2", Species: "Dog", BirthDate: "2021-01-01", Status: petv1.PetStatus_PET_STATUS_ADOPTED,
	}))
	s.Require().NoError(err)

	_, err = s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name: "Cat1", Species: "Cat", BirthDate: "2022-01-01", Status: petv1.PetStatus_PET_STATUS_AVAILABLE,
	}))
	s.Require().NoError(err)

	// Filter by species "Dog"
	dogResp, err := s.handler.ListPets(ctx, connect.NewRequest(&petv1.ListPetsRequest{
		Species: "Dog",
	}))
	s.Require().NoError(err)
	s.Equal(int32(2), dogResp.Msg.GetTotalCount())
	s.Len(dogResp.Msg.GetPets(), 2)
	// One of the dogs should have photo_urls
	var foundPhoto bool
	for _, p := range dogResp.Msg.GetPets() {
		if len(p.GetPhotoUrls()) > 0 {
			foundPhoto = true
			s.Equal(dog1ID, p.GetId())
			s.Equal([]string{"https://example.com/dog1.jpg"}, p.GetPhotoUrls())
		}
	}
	s.True(foundPhoto)

	// Filter by status ADOPTED
	adoptedResp, err := s.handler.ListPets(ctx, connect.NewRequest(&petv1.ListPetsRequest{
		Status: petv1.PetStatus_PET_STATUS_ADOPTED,
	}))
	s.Require().NoError(err)
	s.Equal(int32(1), adoptedResp.Msg.GetTotalCount())
	s.Len(adoptedResp.Msg.GetPets(), 1)
	s.Equal("Dog2", adoptedResp.Msg.GetPets()[0].GetName())

	// Pagination: PageSize=1, Page=0 then Page=1
	page0, err := s.handler.ListPets(ctx, connect.NewRequest(&petv1.ListPetsRequest{
		PageSize: 1,
		Page:     0,
	}))
	s.Require().NoError(err)
	s.Len(page0.Msg.GetPets(), 1)

	page1, err := s.handler.ListPets(ctx, connect.NewRequest(&petv1.ListPetsRequest{
		PageSize: 1,
		Page:     1,
	}))
	s.Require().NoError(err)
	s.Len(page1.Msg.GetPets(), 1)
	s.NotEqual(page0.Msg.GetPets()[0].GetId(), page1.Msg.GetPets()[0].GetId())
}

func (s *PetHandlerTestSuite) TestUpdatePet() {
	ctx := s.authContext("updater@example.com")

	createResp, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name:      "Rocky",
		Species:   "Dog",
		BirthDate: "2022-05-01",
		Status:    petv1.PetStatus_PET_STATUS_AVAILABLE,
	}))
	s.Require().NoError(err)

	updateResp, err := s.handler.UpdatePet(ctx, connect.NewRequest(&petv1.UpdatePetRequest{
		Id:                 createResp.Msg.GetPet().GetId(),
		Name:               new("Rocky Balboa"),
		Species:            new("Dog"),
		BirthDate:          new("2022-05-01"),
		BirthDateEstimated: new(true),
		Status:             petv1.PetStatus_PET_STATUS_ADOPTED.Enum(),
		Tags:               []string{"champion"},
		PhotoUrls:          []string{"https://example.com/rocky.jpg"},
	}))
	s.Require().NoError(err)
	s.Equal("Rocky Balboa", updateResp.Msg.GetPet().GetName())
	s.Equal(petv1.PetStatus_PET_STATUS_ADOPTED, updateResp.Msg.GetPet().GetStatus())
	s.Equal([]string{"https://example.com/rocky.jpg"}, updateResp.Msg.GetPet().GetPhotoUrls())
	s.Equal("updater@example.com", updateResp.Msg.GetPet().GetModifiedBy())

	// Update non-existent pet -> CodeNotFound
	_, err = s.handler.UpdatePet(ctx, connect.NewRequest(&petv1.UpdatePetRequest{
		Id:        "00000000-0000-0000-0000-000000000000",
		Name:      new("Ghost"),
		Species:   new("Wolf"),
		BirthDate: new("2020-01-01"),
	}))
	s.Require().Error(err)
	s.Equal(connect.CodeNotFound, connect.CodeOf(err))
}

func (s *PetHandlerTestSuite) TestDeletePet() {
	ctx := s.authContext("user@example.com")

	createResp, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name:      "Charlie",
		Species:   "Parrot",
		BirthDate: "2021-08-20",
		Status:    petv1.PetStatus_PET_STATUS_AVAILABLE,
	}))
	s.Require().NoError(err)
	petID := createResp.Msg.GetPet().GetId()

	// Delete existing pet
	delResp, err := s.handler.DeletePet(ctx, connect.NewRequest(&petv1.DeletePetRequest{Id: petID}))
	s.Require().NoError(err)
	s.True(delResp.Msg.GetSuccess())

	// Delete again -> CodeNotFound
	_, err = s.handler.DeletePet(ctx, connect.NewRequest(&petv1.DeletePetRequest{Id: petID}))
	s.Require().Error(err)
	s.Equal(connect.CodeNotFound, connect.CodeOf(err))
}

// TRUNCATE between them; running it in parallel would make failures ambiguous.
//
//nolint:paralleltest // the suite shares one Postgres container and isolates cases with
func TestPetHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(PetHandlerTestSuite))
}

// TestUpdatePetPreservesUnsentFields is the regression test for the data-loss bug:
// renaming a pet used to clear its tags, photo urls and birth date, and silently
// move it from adopted back to available.
func (s *PetHandlerTestSuite) TestUpdatePetPreservesUnsentFields() {
	ctx := s.authContext("owner@example.com")

	created, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name:      "Luna",
		Species:   "Cat",
		BirthDate: "2021-04-04",
		Status:    petv1.PetStatus_PET_STATUS_ADOPTED,
		Tags:      []string{"calico", "friendly"},
		PhotoUrls: []string{"https://example.com/luna.jpg"},
	}))
	s.Require().NoError(err)
	id := created.Msg.GetPet().GetId()

	// A caller who only wants to rename the pet.
	renamed, err := s.handler.UpdatePet(ctx, connect.NewRequest(&petv1.UpdatePetRequest{
		Id:         id,
		Name:       new("Luna II"),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
	}))
	s.Require().NoError(err)

	got := renamed.Msg.GetPet()
	s.Equal("Luna II", got.GetName())
	s.Equal("Cat", got.GetSpecies())
	s.Equal("2021-04-04", got.GetBirthDate(), "birth date must survive a rename")
	s.Equal([]string{"calico", "friendly"}, got.GetTags(), "tags must survive a rename")
	s.Equal([]string{"https://example.com/luna.jpg"}, got.GetPhotoUrls())
	s.Equal(petv1.PetStatus_PET_STATUS_ADOPTED, got.GetStatus(),
		"an adopted pet must not become available again because someone fixed a typo")
	s.Equal("owner@example.com", got.GetModifiedBy())
}

// TestUpdatePetWithoutAMaskStillReplaces keeps the older client contract working:
// a caller that loads a pet, edits it and sends the whole thing back gets a full
// replacement, with the single exception of an unspecified status.
func (s *PetHandlerTestSuite) TestUpdatePetWithoutAMaskStillReplaces() {
	ctx := s.authContext("owner@example.com")

	created, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name: "Rex", Species: "Dog", Tags: []string{"old"},
		Status: petv1.PetStatus_PET_STATUS_ADOPTED,
	}))
	s.Require().NoError(err)

	updated, err := s.handler.UpdatePet(ctx, connect.NewRequest(&petv1.UpdatePetRequest{
		Id:      created.Msg.GetPet().GetId(),
		Name:    new("Rex II"),
		Species: new("Wolf"),
		Tags:    []string{"new"},
	}))
	s.Require().NoError(err)

	got := updated.Msg.GetPet()
	s.Equal("Rex II", got.GetName())
	s.Equal("Wolf", got.GetSpecies())
	s.Equal([]string{"new"}, got.GetTags())
	s.Equal(petv1.PetStatus_PET_STATUS_ADOPTED, got.GetStatus(),
		"an unspecified status leaves the stored one alone even without a mask")
}

// TestUpdatePetCanStillClearFields guards the other direction: preserving unsent
// fields must not make deliberate clearing impossible.
func (s *PetHandlerTestSuite) TestUpdatePetCanStillClearFields() {
	ctx := s.authContext("owner@example.com")

	created, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name: "Milo", Species: "Dog", BirthDate: "2020-01-01",
		Tags: []string{"tagged"}, PhotoUrls: []string{"https://example.com/milo.jpg"},
	}))
	s.Require().NoError(err)

	cleared, err := s.handler.UpdatePet(ctx, connect.NewRequest(&petv1.UpdatePetRequest{
		Id:         created.Msg.GetPet().GetId(),
		BirthDate:  new(""),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"tags", "photo_urls", "birth_date"}},
	}))
	s.Require().NoError(err)

	got := cleared.Msg.GetPet()
	s.Empty(got.GetTags())
	s.Empty(got.GetPhotoUrls())
	s.Empty(got.GetBirthDate())
	s.Equal("Milo", got.GetName(), "an unnamed field is still left alone")
}

// TestUpdatePetRejectsAnUnknownMaskPath: a client asking to change a field the
// server does not know about has misunderstood the API; half-applying would be
// worse than refusing.
func (s *PetHandlerTestSuite) TestUpdatePetRejectsAnUnknownMaskPath() {
	ctx := s.authContext("owner@example.com")

	created, err := s.handler.CreatePet(ctx, connect.NewRequest(&petv1.CreatePetRequest{
		Name: "Ghost", Species: "Wolf",
	}))
	s.Require().NoError(err)

	_, err = s.handler.UpdatePet(ctx, connect.NewRequest(&petv1.UpdatePetRequest{
		Id:         created.Msg.GetPet().GetId(),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"created_by"}},
	}))

	s.Require().Error(err)
	s.Equal(connect.CodeInvalidArgument, connect.CodeOf(err))
}
