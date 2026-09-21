//go:build integration

package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/suite"

	"github.com/sudorandom/protojsonx/protojsonxconnect"

	petv1 "github.com/example/pets/gen/go/pet/v1"
	"github.com/example/pets/gen/go/pet/v1/petv1connect"
	"github.com/example/pets/internal/auth"
	"github.com/example/pets/internal/pet"
	"github.com/example/pets/internal/telemetry"
	"github.com/example/pets/internal/testutil"
)

type PetServiceIntegrationTestSuite struct {
	suite.Suite

	ctx          context.Context
	cancel       context.CancelFunc
	testDB       *testutil.TestDB
	pool         *pgxpool.Pool
	server       *httptest.Server
	client       petv1connect.PetServiceClient
	createdPetID string
}

func (s *PetServiceIntegrationTestSuite) SetupSuite() {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	testDB, err := testutil.StartTestDB(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping integration test: Docker/Testcontainers not available: %v", err)
		return
	}
	s.testDB = testDB
	s.pool = testDB.Pool

	petHandler := pet.NewHandler(s.pool)

	otelInterceptor, err := telemetry.NewConnectInterceptor()
	s.Require().NoError(err, "failed to create otel interceptor")

	valInterceptor := validate.NewInterceptor()
	authCfg := auth.Config{
		Enabled:      true,
		StaticTokens: []string{"test-token"},
	}
	authInterceptor := auth.NewInterceptor(authCfg)

	path, handler := petv1connect.NewPetServiceHandler(
		petHandler,
		connect.WithCodec(&protojsonxconnect.Codec{}),
		connect.WithInterceptors(otelInterceptor, valInterceptor, authInterceptor),
	)

	mux := http.NewServeMux()
	mux.Handle(path, handler)
	s.server = httptest.NewServer(mux)

	s.client = petv1connect.NewPetServiceClient(
		s.server.Client(),
		s.server.URL,
		connect.WithCodec(&protojsonxconnect.Codec{}),
	)
}

func (s *PetServiceIntegrationTestSuite) TearDownSuite() {
	if s.server != nil {
		s.server.Close()
	}
	if s.testDB != nil {
		s.testDB.Close()
	}
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *PetServiceIntegrationTestSuite) SetupTest() {
	if s.testDB != nil {
		s.Require().NoError(s.testDB.TruncateTables(s.ctx))
	}
}

func (s *PetServiceIntegrationTestSuite) TestPetLifecycle() {
	s.Run("CreatePet", func() {
		req := connect.NewRequest(&petv1.CreatePetRequest{
			Name:               "Milo",
			Species:            "Dog",
			BirthDate:          "2023-05-10",
			BirthDateEstimated: false,
			Status:             petv1.PetStatus_PET_STATUS_AVAILABLE,
			Tags:               []string{"friendly", "playful"},
			PhotoUrls:          []string{"https://example.com/milo1.jpg"},
		})
		req.Header().Set("Authorization", "Bearer test-token")

		resp, err := s.client.CreatePet(s.ctx, req)
		s.Require().NoError(err, "CreatePet failed")
		s.Require().NotNil(resp.Msg)
		s.Require().NotNil(resp.Msg.GetPet())

		s.Equal("Milo", resp.Msg.GetPet().GetName())
		s.Equal("2023-05-10", resp.Msg.GetPet().GetBirthDate())
		s.False(resp.Msg.GetPet().GetBirthDateEstimated())
		s.Equal([]string{"https://example.com/milo1.jpg"}, resp.Msg.GetPet().GetPhotoUrls())
		s.NotEmpty(resp.Msg.GetPet().GetId())
		s.NotEmpty(resp.Msg.GetPet().GetCreatedBy())
		s.NotEmpty(resp.Msg.GetPet().GetModifiedBy())
		s.NotNil(resp.Msg.GetPet().GetCreatedAt())
		s.NotNil(resp.Msg.GetPet().GetModifiedAt())

		s.createdPetID = resp.Msg.GetPet().GetId()
	})

	s.Run("GetPet", func() {
		s.Require().NotEmpty(s.createdPetID, "skipping GetPet because CreatePet did not succeed")

		req := connect.NewRequest(&petv1.GetPetRequest{Id: s.createdPetID})
		req.Header().Set("Authorization", "Bearer test-token")

		resp, err := s.client.GetPet(s.ctx, req)
		s.Require().NoError(err, "GetPet failed")
		s.Require().NotNil(resp.Msg)
		s.Require().NotNil(resp.Msg.GetPet())
		s.Equal(s.createdPetID, resp.Msg.GetPet().GetId())
		s.Equal([]string{"https://example.com/milo1.jpg"}, resp.Msg.GetPet().GetPhotoUrls())
	})

	s.Run("ListPets", func() {
		req := connect.NewRequest(&petv1.ListPetsRequest{
			Species:  "Dog",
			PageSize: 10,
		})
		req.Header().Set("Authorization", "Bearer test-token")

		resp, err := s.client.ListPets(s.ctx, req)
		s.Require().NoError(err, "ListPets failed")
		s.Require().NotNil(resp.Msg)
		s.NotEmpty(resp.Msg.GetPets())
	})

	s.Run("UpdatePet", func() {
		s.Require().NotEmpty(s.createdPetID, "skipping UpdatePet")

		req := connect.NewRequest(&petv1.UpdatePetRequest{
			Id:                 s.createdPetID,
			Name:               new("Milo The Great"),
			Species:            new("Dog"),
			BirthDate:          new("2022-04-12"),
			BirthDateEstimated: new(true),
			Status:             petv1.PetStatus_PET_STATUS_ADOPTED.Enum(),
			Tags:               []string{"adopted", "happy"},
			PhotoUrls:          []string{"https://example.com/milo-updated.jpg"},
		})
		req.Header().Set("Authorization", "Bearer test-token")

		resp, err := s.client.UpdatePet(s.ctx, req)
		s.Require().NoError(err, "UpdatePet failed")
		s.Require().NotNil(resp.Msg)
		s.Require().NotNil(resp.Msg.GetPet())
		s.Equal("Milo The Great", resp.Msg.GetPet().GetName())
		s.Equal("2022-04-12", resp.Msg.GetPet().GetBirthDate())
		s.True(resp.Msg.GetPet().GetBirthDateEstimated())
		s.Equal(petv1.PetStatus_PET_STATUS_ADOPTED, resp.Msg.GetPet().GetStatus())
		s.Equal([]string{"https://example.com/milo-updated.jpg"}, resp.Msg.GetPet().GetPhotoUrls())
	})

	s.Run("DeletePet", func() {
		s.Require().NotEmpty(s.createdPetID, "skipping DeletePet")

		req := connect.NewRequest(&petv1.DeletePetRequest{Id: s.createdPetID})
		req.Header().Set("Authorization", "Bearer test-token")

		resp, err := s.client.DeletePet(s.ctx, req)
		s.Require().NoError(err, "DeletePet failed")
		s.Require().NotNil(resp.Msg)
		s.True(resp.Msg.GetSuccess())

		// Verify pet is gone
		getReq := connect.NewRequest(&petv1.GetPetRequest{Id: s.createdPetID})
		getReq.Header().Set("Authorization", "Bearer test-token")
		_, err = s.client.GetPet(s.ctx, getReq)
		s.Require().Error(err, "expected pet to be not found")
		s.Equal(connect.CodeNotFound, connect.CodeOf(err))
	})
}

func (s *PetServiceIntegrationTestSuite) TestUnauthenticatedCallRejection() {
	req := connect.NewRequest(&petv1.CreatePetRequest{
		Name:      "Shadow",
		Species:   "Cat",
		BirthDate: "2023-01-01",
	})
	// No Authorization header
	_, err := s.client.CreatePet(s.ctx, req)
	s.Require().Error(err)
	s.Equal(connect.CodeUnauthenticated, connect.CodeOf(err))
}

func (s *PetServiceIntegrationTestSuite) TestFilteringAndPagination() {
	// Database is automatically truncated before this test starts
	petsToCreate := []struct {
		name    string
		species string
		status  petv1.PetStatus
	}{
		{"Bella", "Dog", petv1.PetStatus_PET_STATUS_AVAILABLE},
		{"Max", "Dog", petv1.PetStatus_PET_STATUS_ADOPTED},
		{"Luna", "Cat", petv1.PetStatus_PET_STATUS_AVAILABLE},
		{"Charlie", "Cat", petv1.PetStatus_PET_STATUS_PENDING},
		{"Lucy", "Bird", petv1.PetStatus_PET_STATUS_AVAILABLE},
	}

	for _, p := range petsToCreate {
		req := connect.NewRequest(&petv1.CreatePetRequest{
			Name:      p.name,
			Species:   p.species,
			Status:    p.status,
			BirthDate: "2023-01-01",
		})
		req.Header().Set("Authorization", "Bearer test-token")
		_, err := s.client.CreatePet(s.ctx, req)
		s.Require().NoError(err)
	}

	// Filter by species "Dog"
	dogReq := connect.NewRequest(&petv1.ListPetsRequest{
		Species: "Dog",
	})
	dogReq.Header().Set("Authorization", "Bearer test-token")
	dogResp, err := s.client.ListPets(s.ctx, dogReq)
	s.Require().NoError(err)
	s.Equal(int32(2), dogResp.Msg.GetTotalCount())
	s.Len(dogResp.Msg.GetPets(), 2)

	// Filter by status "AVAILABLE"
	availReq := connect.NewRequest(&petv1.ListPetsRequest{
		Status: petv1.PetStatus_PET_STATUS_AVAILABLE,
	})
	availReq.Header().Set("Authorization", "Bearer test-token")
	availResp, err := s.client.ListPets(s.ctx, availReq)
	s.Require().NoError(err)
	s.Equal(int32(3), availResp.Msg.GetTotalCount())
	s.Len(availResp.Msg.GetPets(), 3)

	// Pagination: pageSize=2, page=0
	page0Req := connect.NewRequest(&petv1.ListPetsRequest{
		PageSize: 2,
		Page:     0,
	})
	page0Req.Header().Set("Authorization", "Bearer test-token")
	page0Resp, err := s.client.ListPets(s.ctx, page0Req)
	s.Require().NoError(err)
	s.Equal(int32(5), page0Resp.Msg.GetTotalCount())
	s.Len(page0Resp.Msg.GetPets(), 2)

	// Pagination: pageSize=2, page=2 (should return 1 pet)
	page2Req := connect.NewRequest(&petv1.ListPetsRequest{
		PageSize: 2,
		Page:     2,
	})
	page2Req.Header().Set("Authorization", "Bearer test-token")
	page2Resp, err := s.client.ListPets(s.ctx, page2Req)
	s.Require().NoError(err)
	s.Equal(int32(5), page2Resp.Msg.GetTotalCount())
	s.Len(page2Resp.Msg.GetPets(), 1)
}

// TRUNCATE between them; running it in parallel would make failures ambiguous.
//
//nolint:paralleltest // the suite shares one Postgres container and isolates cases with
func TestPetServiceIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(PetServiceIntegrationTestSuite))
}
