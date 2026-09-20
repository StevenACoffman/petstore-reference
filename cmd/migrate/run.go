package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/example/pets/internal/config"
	"github.com/example/pets/internal/db"
	"github.com/example/pets/internal/logging"
)

// defaultTimeout bounds a migration run. Schema changes take locks; an unbounded
// migration that blocks behind a long-running query would hold them indefinitely.
const defaultTimeout = 5 * time.Minute

// run executes one migration subcommand and returns its outcome.
//
// Requires: args is non-empty (args[0] is the program name); getenv behaves like
//
//	os.Getenv; stdout and stderr are non-nil.
//
// Ensures: the database pool is closed before returning; never calls os.Exit.
func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", defaultTimeout, "maximum time to wait for the migration to finish")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s [flags] [up|down|status]\n\nFlags:\n", args[0])
		flags.PrintDefaults()
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	command := flags.Arg(0)
	if command == "" {
		command = "up"
	}

	cfg := config.Load(getenv)
	logger := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	switch command {
	case "up":
		logger.Info("applying database migrations")
		if err := db.Migrate(ctx, pool); err != nil {
			return fmt.Errorf("applying migrations: %w", err)
		}
		logger.Info("migrations applied successfully")
	case "down":
		logger.Info("rolling back last migration")
		if err := db.MigrateDown(ctx, pool); err != nil {
			return fmt.Errorf("rolling back migration: %w", err)
		}
		logger.Info("rollback completed successfully")
	case "status":
		if err := writeStatus(ctx, stdout, pool); err != nil {
			return err
		}
	default:
		flags.Usage()
		return fmt.Errorf("unknown command %q", command)
	}

	return nil
}

// writeStatus prints one line per migration with its applied-at time, aligned into
// columns. Status is the one subcommand whose output is for a human to read, so it
// goes to stdout rather than the structured log.
func writeStatus(ctx context.Context, stdout io.Writer, pool *pgxpool.Pool) error {
	statuses, err := db.MigrationStatus(ctx, pool)
	if err != nil {
		return fmt.Errorf("reading migration status: %w", err)
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tMIGRATION\tSTATUS")
	for _, s := range statuses {
		state := "pending"
		if s.State == "applied" {
			state = "applied at " + s.AppliedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\n", s.Source.Version, s.Source.Path, state)
	}
	return tw.Flush()
}
