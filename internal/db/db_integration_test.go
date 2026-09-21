//go:build integration

package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/suite"

	"github.com/example/pets/internal/db"
	"github.com/example/pets/internal/testutil"
)

type DBTestSuite struct {
	suite.Suite
	ctx    context.Context
	cancel context.CancelFunc
	testDB *testutil.TestDB
}

func (s *DBTestSuite) SetupSuite() {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	testDB, err := testutil.StartTestDB(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping DB tests: postgres testcontainer unavailable: %v", err)
		return
	}
	s.testDB = testDB
}

func (s *DBTestSuite) TearDownSuite() {
	if s.testDB != nil {
		s.testDB.Close()
	}
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *DBTestSuite) SetupTest() {
	s.Require().NoError(s.testDB.TruncateTables(s.ctx), "failed to truncate tables before test")
}

func (s *DBTestSuite) TestMigrationLifecycle() {
	// Check migration status (all should be applied)
	statuses, err := db.MigrationStatus(s.ctx, s.testDB.Pool)
	s.Require().NoError(err)
	s.NotEmpty(statuses)
	for _, st := range statuses {
		s.Equal(goose.StateApplied, st.State)
	}

	// Roll back 1 migration
	err = db.MigrateDown(s.ctx, s.testDB.Pool)
	s.Require().NoError(err)

	// Re-apply migration
	err = db.Migrate(s.ctx, s.testDB.Pool)
	s.Require().NoError(err)
}

func (s *DBTestSuite) TestMigrationCycleAndSchemaSymmetry() {
	provider, cleanup, err := db.NewProvider(s.testDB.Pool)
	s.Require().NoError(err)
	defer cleanup()

	sources := provider.ListSources()
	s.Require().NotEmpty(sources, "expected at least one migration source")

	// Ensure all migrations are re-applied at the end so subsequent tests have the full schema
	defer func() {
		_, _ = provider.Up(s.ctx)
	}()

	// 1. Roll back everything to version 0 to verify clean state
	_, err = provider.DownTo(s.ctx, 0)
	s.Require().NoError(err)

	ver, err := provider.GetDBVersion(s.ctx)
	s.Require().NoError(err)
	s.Equal(int64(0), ver)

	dumpAt0, err := s.testDB.DumpSchema(s.ctx)
	s.Require().NoError(err)
	s.Empty(dumpAt0, "expected clean database at version 0, got: %v", dumpAt0)

	// 2. Migrate UP step-by-step and record schema dump at each version
	upDumps := make(map[int64][]string)
	for _, src := range sources {
		_, err := provider.UpTo(s.ctx, src.Version)
		s.Require().NoError(err, "failed migrating up to version %d", src.Version)

		curVer, err := provider.GetDBVersion(s.ctx)
		s.Require().NoError(err)
		s.Equal(src.Version, curVer)

		dump, err := s.testDB.DumpSchema(s.ctx)
		s.Require().NoError(err)
		s.NotEmpty(dump, "expected schema to exist at version %d", src.Version)
		upDumps[src.Version] = dump
	}

	// 3. Migrate DOWN step-by-step and verify schema matches UP dump at every step
	for i := len(sources) - 1; i >= 0; i-- {
		targetVersion := int64(0)
		if i > 0 {
			targetVersion = sources[i-1].Version
		}

		_, err := provider.DownTo(s.ctx, targetVersion)
		s.Require().NoError(err, "failed migrating down to version %d", targetVersion)

		curVer, err := provider.GetDBVersion(s.ctx)
		s.Require().NoError(err)
		s.Equal(targetVersion, curVer)

		downDump, err := s.testDB.DumpSchema(s.ctx)
		s.Require().NoError(err)

		if targetVersion == 0 {
			s.Empty(downDump, "expected clean database at version 0, got: %v", downDump)
		} else {
			s.Equal(upDumps[targetVersion], downDump, "schema mismatch at version %d after down migration", targetVersion)
		}
	}

	// 4. Re-apply all migrations to the latest version and verify final schema
	_, err = provider.Up(s.ctx)
	s.Require().NoError(err)

	latestVersion := sources[len(sources)-1].Version
	finalDump, err := s.testDB.DumpSchema(s.ctx)
	s.Require().NoError(err)
	s.Equal(upDumps[latestVersion], finalDump)
}

func (s *DBTestSuite) TestPetQueries() {
	queries := db.New(s.testDB.Pool)

	// 1. CreatePet
	created, err := queries.CreatePet(s.ctx, db.CreatePetParams{
		Name:               "Buddy",
		Species:            "Dog",
		BirthDate:          pgtype.Date{Time: time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		BirthDateEstimated: false,
		Status:             "PET_STATUS_AVAILABLE",
		PhotoUrls:          []string{"https://example.com/buddy.jpg"},
		Tags:               []string{"friendly", "trained"},
		CreatedBy:          "test@example.com",
		ModifiedBy:         "test@example.com",
	})
	s.Require().NoError(err)
	s.Equal("Buddy", created.Name)
	s.Equal("Dog", created.Species)
	s.Equal("PET_STATUS_AVAILABLE", created.Status)
	s.Equal([]string{"https://example.com/buddy.jpg"}, created.PhotoUrls)
	s.True(created.ID.Valid)

	// CreatePet with NULL birth_date
	nullBirthPet, err := queries.CreatePet(s.ctx, db.CreatePetParams{
		Name:       "NoBirthPet",
		Species:    "Cat",
		BirthDate:  pgtype.Date{Valid: false},
		Status:     "PET_STATUS_AVAILABLE",
		PhotoUrls:  []string{},
		Tags:       []string{},
		CreatedBy:  "test@example.com",
		ModifiedBy: "test@example.com",
	})
	s.Require().NoError(err)
	s.False(nullBirthPet.BirthDate.Valid)

	// 2. GetPet
	fetched, err := queries.GetPet(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Equal(created.ID, fetched.ID)
	s.Equal("Buddy", fetched.Name)
	s.Equal([]string{"https://example.com/buddy.jpg"}, fetched.PhotoUrls)

	// 3. ListPets carries its own total, so no second count query is needed.
	pets, err := queries.ListPets(s.ctx, db.ListPetsParams{
		PageSize: 10,
		Species:  pgtype.Text{String: "Dog", Valid: true},
	})
	s.Require().NoError(err)
	s.Len(pets, 1)
	s.Equal(created.ID, pets[0].ID)
	s.Equal([]string{"https://example.com/buddy.jpg"}, pets[0].PhotoUrls)
	s.Equal(int64(1), pets[0].TotalCount, "the window total counts the filter")

	// 4. UpdatePet writes every column when every parameter is supplied.
	updated, err := queries.UpdatePet(s.ctx, db.UpdatePetParams{
		ID:                 created.ID,
		Name:               pgtype.Text{String: "Buddy The Best", Valid: true},
		Species:            pgtype.Text{String: "Dog", Valid: true},
		SetBirthDate:       true,
		BirthDate:          created.BirthDate,
		BirthDateEstimated: pgtype.Bool{Bool: true, Valid: true},
		Status:             pgtype.Text{String: "PET_STATUS_ADOPTED", Valid: true},
		PhotoUrls:          []string{"https://example.com/buddy2.jpg"},
		Tags:               []string{"adopted"},
		ModifiedBy:         "admin@example.com",
	})
	s.Require().NoError(err)
	s.Equal("Buddy The Best", updated.Name)
	s.Equal("PET_STATUS_ADOPTED", updated.Status)
	s.Equal([]string{"https://example.com/buddy2.jpg"}, updated.PhotoUrls)
	s.True(updated.BirthDateEstimated)

	// 4b. A partial update: only the name parameter is supplied, so every other
	// column must survive untouched. This is the SQL-level guarantee that the
	// handler's update_mask support is built on.
	renamed, err := queries.UpdatePet(s.ctx, db.UpdatePetParams{
		ID:         created.ID,
		Name:       pgtype.Text{String: "Buddy Renamed", Valid: true},
		ModifiedBy: "renamer@example.com",
	})
	s.Require().NoError(err)
	s.Equal("Buddy Renamed", renamed.Name)
	s.Equal("Dog", renamed.Species, "species must survive a name-only update")
	s.Equal("PET_STATUS_ADOPTED", renamed.Status, "status must survive a name-only update")
	s.Equal([]string{"adopted"}, renamed.Tags, "tags must survive a name-only update")
	s.Equal([]string{"https://example.com/buddy2.jpg"}, renamed.PhotoUrls)
	s.True(renamed.BirthDateEstimated)
	s.Equal(created.BirthDate, renamed.BirthDate, "birth_date must survive a name-only update")

	// 5. DeletePet
	rowsAffected, err := queries.DeletePet(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Equal(int64(1), rowsAffected)

	// Deleting again should affect 0 rows
	rowsAffected, err = queries.DeletePet(s.ctx, created.ID)
	s.Require().NoError(err)
	s.Equal(int64(0), rowsAffected)
}

func (s *DBTestSuite) TestWithTx() {
	tx, err := s.testDB.Pool.Begin(s.ctx)
	s.Require().NoError(err)
	defer func() { _ = tx.Rollback(s.ctx) }()

	txQueries := db.New(s.testDB.Pool).WithTx(tx)
	created, err := txQueries.CreatePet(s.ctx, db.CreatePetParams{
		Name:       "TxPet",
		Species:    "Hamster",
		BirthDate:  pgtype.Date{Time: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		Status:     "PET_STATUS_AVAILABLE",
		PhotoUrls:  []string{},
		Tags:       []string{},
		CreatedBy:  "tx@example.com",
		ModifiedBy: "tx@example.com",
	})
	s.Require().NoError(err)
	s.Equal("TxPet", created.Name)

	// Rollback transaction
	err = tx.Rollback(s.ctx)
	s.Require().NoError(err)

	// Verify pet does not exist outside transaction
	queries := db.New(s.testDB.Pool)
	_, err = queries.GetPet(s.ctx, created.ID)
	s.Require().Error(err)
}

func TestDBTestSuite(t *testing.T) {
	suite.Run(t, new(DBTestSuite))
}
