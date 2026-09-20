//go:build integration

package db_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/testutil"
)

// updateGolden rewrites the checked-in schema snapshot instead of asserting against
// it. Run `go test -tags=integration -run TestSchemaGolden -update ./internal/db/`,
// read the resulting diff, and commit it only if the change was intended.
var updateGolden = flag.Bool("update", false, "rewrite the golden schema snapshot")

// TestSchemaGoldenMatchesMigrations applies every migration to an empty database and
// compares the resulting schema against a committed snapshot.
//
// The value here is review pressure, not correctness: a migration that quietly drops
// a column, loosens a NOT NULL, or removes an index produces a visible diff in a file
// a human has to approve, rather than silently changing production's shape.
//
//nolint:paralleltest // starts its own container and writes a repository file under -update.
func TestSchemaGoldenMatchesMigrations(t *testing.T) {
	ctx := t.Context()

	testDB, err := testutil.StartTestDB(ctx)
	if err != nil {
		t.Skipf("skipping: postgres testcontainer unavailable: %v", err)
	}
	t.Cleanup(testDB.Close)

	ddl, err := testDB.DumpSchema(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, ddl, "migrations produced an empty schema")

	got := strings.Join(ddl, "\n") + "\n"
	golden := filepath.Join("testdata", "schema.golden")

	if *updateGolden {
		require.NoError(t, os.MkdirAll("testdata", 0o750))
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		t.Logf("wrote %s; review the diff before committing", golden)
		return
	}

	want, err := os.ReadFile(golden)
	if os.IsNotExist(err) {
		t.Fatalf("%s is missing; generate it with:\n"+
			"  go test -tags=integration -run TestSchemaGolden -update ./internal/db/", golden)
	}
	require.NoError(t, err)

	assert.Equal(t, string(want), got,
		"the schema produced by the migrations no longer matches %s.\n"+
			"If the change is intended, regenerate with -update and commit the diff.", golden)
}
