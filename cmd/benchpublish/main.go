// Command benchpublish reads `go test -bench` output and records one point per
// benchmark in InfluxDB, so performance is a trend across commits rather than a
// number in a CI log that scrolls away.
//
// The endpoint is configuration, so it works against any self-hosted InfluxDB:
//
//	INFLUXDB_URL=http://localhost:8086 INFLUXDB_TOKEN=... \
//	  benchpublish reports/bench.txt
//
// With INFLUXDB_URL unset it prints the line protocol and exits zero, which is
// what lets a fork's pull request run benchmarks without holding the secret.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	exitSuccess = 0
	exitFailure = 1
)

func main() {
	// This frame holds no defers, so publish()'s cleanup all runs before the exit.
	os.Exit(publish())
}

// publish owns the process lifecycle and translates run's error into an exit code.
func publish() int {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT,
	)
	defer stop()

	client := &http.Client{Timeout: time.Minute}
	err := run(ctx, os.Args, os.Getenv, os.Stdout, os.Stderr, client)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return exitSuccess
	default:
		fmt.Fprintf(os.Stderr, "benchpublish: %s\n", err)
		return exitFailure
	}
}
