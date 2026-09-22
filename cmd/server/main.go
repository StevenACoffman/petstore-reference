// Command server runs the Pet microservice: a ConnectRPC API, its generated OpenAPI
// documentation, over HTTP/1.1 and HTTP/2.
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
	// This frame holds no defers, so serve()'s cleanup all runs before the exit.
	os.Exit(serve())
}

// serve owns the process lifecycle and turns run's error into an exit code.
// signal.NotifyContext lives here so a test can drive run with its own context
// and never install a process-wide handler.
func serve() int {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT,
	)
	defer stop()

	err := run(ctx, os.Args, os.Getenv, os.Stdin, os.Stdout, os.Stderr)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return exitSuccess
	default:
		fmt.Fprintf(os.Stderr, "server: %s\n", err)
		return exitFailure
	}
}
