package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/example/pets/sql/schema"
)

// Migrate applies all pending database migrations against the provided connection pool.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() {
		_ = sqlDB.Close()
	}()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, schema.FS)
	if err != nil {
		return fmt.Errorf("failed to create goose provider: %w", err)
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	for _, res := range results {
		slog.Info("Applied migration",
			"version", res.Source.Version,
			"path", res.Source.Path,
			"duration", res.Duration,
		)
	}

	return nil
}

// MigrateDown rolls back the most recent database migration.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() {
		_ = sqlDB.Close()
	}()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, schema.FS)
	if err != nil {
		return fmt.Errorf("failed to create goose provider: %w", err)
	}

	res, err := provider.Down(ctx)
	if err != nil {
		return fmt.Errorf("failed to rollback migration: %w", err)
	}

	if res != nil {
		slog.Info("Rolled back migration",
			"version", res.Source.Version,
			"path", res.Source.Path,
			"duration", res.Duration,
		)
	}

	return nil
}

// MigrationStatus returns the status of all available migrations.
func MigrationStatus(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationStatus, error) {
	provider, cleanup, err := NewProvider(pool)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return provider.Status(ctx)
}

// NewProvider creates a new goose.Provider for the schema migrations.
// The caller is responsible for calling the returned cleanup function when done.
func NewProvider(pool *pgxpool.Pool) (*goose.Provider, func(), error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	cleanup := func() {
		_ = sqlDB.Close()
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, schema.FS)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("failed to create goose provider: %w", err)
	}

	return provider, cleanup, nil
}

// GetDBVersion returns the current migration version of the database.
func GetDBVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	provider, cleanup, err := NewProvider(pool)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	return provider.GetDBVersion(ctx)
}
