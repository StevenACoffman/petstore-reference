package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/example/pets/internal/db"
)

// configureDockerHost points testcontainers at a Colima socket on macOS, unless
// the caller already chose an endpoint or no socket exists.
//
// Called from StartTestDB rather than init(): init() runs in every binary linking
// this package and a test cannot suppress its effect on the environment.
func configureDockerHost() {
	dockerHostOnce.Do(func() {
		if os.Getenv("DOCKER_HOST") != "" {
			return
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		colimaSock := filepath.Join(home, ".colima", "default", "docker.sock")
		if _, err := os.Stat(colimaSock); err != nil {
			return
		}
		_ = os.Setenv("DOCKER_HOST", "unix://"+colimaSock)
		if os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
			_ = os.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock")
		}
	})
}

var dockerHostOnce sync.Once

// TestDB wraps a PostgreSQL connection pool and optional Testcontainer instance.
type TestDB struct {
	Pool      *pgxpool.Pool
	Container *pgmodule.PostgresContainer
}

// StartTestDB spins up a PostgreSQL testcontainer (or connects to DATABASE_URL if set),
// applies all migrations, and returns the TestDB instance.
func StartTestDB(ctx context.Context) (*TestDB, error) {
	configureDockerHost()

	dbURL := os.Getenv("DATABASE_URL")
	var pgContainer *pgmodule.PostgresContainer

	if dbURL == "" {
		var err error
		pgContainer, err = pgmodule.Run(ctx,
			"postgres:17-alpine",
			pgmodule.WithDatabase("pets_test_db"),
			pgmodule.WithUsername("postgres"),
			pgmodule.WithPassword("password"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to start postgres testcontainer: %w", err)
		}

		dbURL, err = pgContainer.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			_ = testcontainers.TerminateContainer(pgContainer)
			return nil, fmt.Errorf("failed to get testcontainer connection string: %w", err)
		}
	}

	pool, err := db.NewPool(ctx, dbURL)
	if err != nil {
		if pgContainer != nil {
			_ = testcontainers.TerminateContainer(pgContainer)
		}
		return nil, fmt.Errorf("failed to connect to test database at %s: %w", dbURL, err)
	}

	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		if pgContainer != nil {
			_ = testcontainers.TerminateContainer(pgContainer)
		}
		return nil, fmt.Errorf("failed to apply migrations to test database: %w", err)
	}

	return &TestDB{
		Pool:      pool,
		Container: pgContainer,
	}, nil
}

// TruncateTables truncates all application tables between tests to ensure test isolation.
func (tdb *TestDB) TruncateTables(ctx context.Context) error {
	_, err := tdb.Pool.Exec(ctx, "TRUNCATE TABLE pets RESTART IDENTITY CASCADE;")
	if err != nil {
		return fmt.Errorf("failed to truncate tables: %w", err)
	}
	return nil
}

// DumpSchema returns a sorted, normalized DDL representation of all tables,
// views, sequences, constraints, and indexes in the public schema.
func (tdb *TestDB) DumpSchema(ctx context.Context) ([]string, error) {
	query := `
		SELECT 'TABLE ' || table_name || ' (' || 
		       string_agg(column_name || ' ' || data_type || 
		         CASE WHEN is_nullable = 'NO' THEN ' NOT NULL' ELSE '' END || 
		         CASE WHEN column_default IS NOT NULL THEN ' DEFAULT ' || column_default ELSE '' END, 
		         ', ' ORDER BY column_name) || ')' AS ddl
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name NOT LIKE 'goose_%'
		GROUP BY table_name
		UNION ALL
		SELECT 'VIEW ' || table_name AS ddl
		FROM information_schema.views
		WHERE table_schema = 'public' AND table_name NOT LIKE 'goose_%'
		UNION ALL
		SELECT 'SEQUENCE ' || sequence_name AS ddl
		FROM information_schema.sequences
		WHERE sequence_schema = 'public' AND sequence_name NOT LIKE 'goose_%'
		UNION ALL
		SELECT 'CONSTRAINT ' || table_name || ' ' || constraint_name || ' ' || constraint_type AS ddl
		FROM information_schema.table_constraints
		WHERE table_schema = 'public' AND table_name NOT LIKE 'goose_%' AND constraint_name NOT LIKE '%_not_null'
		UNION ALL
		SELECT 'INDEX ' || indexdef AS ddl
		FROM pg_indexes
		WHERE schemaname = 'public' AND tablename NOT LIKE 'goose_%'
		ORDER BY ddl;
	`
	rows, err := tdb.Pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to dump schema: %w", err)
	}
	defer rows.Close()

	var ddl []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("failed to scan schema DDL: %w", err)
		}
		ddl = append(ddl, s)
	}
	return ddl, rows.Err()
}

// Close closes the database pool and terminates the container if one was started.
func (tdb *TestDB) Close() {
	if tdb.Pool != nil {
		tdb.Pool.Close()
	}
	if tdb.Container != nil {
		_ = testcontainers.TerminateContainer(tdb.Container)
	}
}
