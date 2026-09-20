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
	// os.Exit is called from a frame that holds no defers, so every cleanup in
	// serve() — including releasing the signal handler — runs first.
	os.Exit(serve())
}

// serve owns the process lifecycle and translates run's error into an exit code.
//
// signal.NotifyContext belongs here rather than inside run so that a test can drive
// run with its own cancellable context and never install a process-wide handler.
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
