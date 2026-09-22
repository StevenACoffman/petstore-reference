// Command migrate applies, rolls back, and reports on the database schema
// migrations embedded in the service binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const (
	exitSuccess = 0
	exitFailure = 1
)

func main() {
	// This frame holds no defers, so migrate()'s cleanup all runs before the exit.
	os.Exit(migrate())
}

// migrate owns the process lifecycle and translates run's error into an exit code.
func migrate() int {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT,
	)
	defer stop()

	err := run(ctx, os.Args, os.Getenv, os.Stdout, os.Stderr)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return exitSuccess
	default:
		fmt.Fprintf(os.Stderr, "migrate: %s\n", err)
		return exitFailure
	}
}
