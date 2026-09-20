package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/example/pets/internal/config"
	"github.com/example/pets/internal/db"
	"github.com/example/pets/internal/logging"
	"github.com/example/pets/internal/resilience"
	"github.com/example/pets/internal/telemetry"
)

const (
	// shutdownTimeout bounds how long in-flight requests may take to drain once a
	// termination signal arrives.
	shutdownTimeout = 10 * time.Second
	// otelShutdownTimeout bounds the final trace flush.
	otelShutdownTimeout = 5 * time.Second
	// readHeaderTimeout bounds how long a client may take to send request headers,
	// which is the cheap defence against Slowloris.
	readHeaderTimeout = 5 * time.Second
)

// run wires the service and serves until ctx is cancelled.
//
// Every OS facility the service touches arrives as a parameter, so a test can call
// run directly with a fake environment and captured output instead of starting a
// process. run never calls os.Exit; it returns an error and lets main decide.
//
// Requires: args is non-empty (args[0] is the program name); getenv behaves like
//
//	os.Getenv; stdout and stderr are non-nil.
//
// Ensures: the HTTP listener and database pool are closed before returning, whether
//
//	the exit is clean or an error.
func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	_ io.Reader,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	addrFlag := flags.String("addr", "", "listen address; overrides PORT (example: 127.0.0.1:8080)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	cfg := config.Load(getenv)
	logger := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)

	if err := cfg.Validate(); err != nil {
		return err
	}

	logger.Info("starting pet microservice", "port", cfg.Port, "dev_mode", cfg.DevMode)

	otelCfg, err := telemetry.LoadConfig(getenv)
	if err != nil {
		logger.Warn("telemetry config file unusable, continuing with environment settings", "error", err)
	}

	// Metrics come first: otelconnect and otelhttp resolve the global MeterProvider
	// when their interceptors are built, so it must exist before the handler is wired.
	var metricsHandler http.Handler
	if res, resErr := telemetry.NewResource(ctx, otelCfg); resErr != nil {
		logger.Warn("building telemetry resource", "error", resErr)
	} else if metrics, metricsErr := telemetry.InitMetrics(res); metricsErr != nil {
		logger.Warn("metrics init failed; continuing without metrics", "error", metricsErr)
	} else {
		metricsHandler = metrics.Handler
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), otelShutdownTimeout)
			defer cancel()
			if flushErr := metrics.Shutdown(shutdownCtx); flushErr != nil {
				logger.Error("shutting down metrics", "error", flushErr)
			}
		}()
	}

	if shutdownOTel, otelErr := telemetry.Init(ctx, otelCfg); otelErr != nil {
		logger.Warn("opentelemetry init failed", "error", otelErr)
	} else {
		defer func() {
			// WithoutCancel, not Background: the parent context is already cancelled by
			// the time this runs, but the final trace flush should still carry the
			// service's trace context.
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), otelShutdownTimeout)
			defer cancel()
			if flushErr := shutdownOTel(shutdownCtx); flushErr != nil {
				logger.Error("shutting down opentelemetry", "error", flushErr)
			}
		}()
	}

	pool, poolErr := db.NewPool(ctx, cfg.DatabaseURL)
	if poolErr != nil {
		return fmt.Errorf("connecting to database: %w", poolErr)
	}
	defer pool.Close()
	logger.Info("connected to postgresql")

	if cfg.AutoMigrate {
		logger.Info("applying database migrations", "auto_migrate", true)
		if migrateErr := db.Migrate(ctx, pool); migrateErr != nil {
			return fmt.Errorf("applying database migrations: %w", migrateErr)
		}
	} else {
		logger.Info("skipping automatic database migrations", "auto_migrate", false)
	}

	resilientDB := resilience.NewDB(resilience.DefaultConfig(), logger)

	handler, handlerErr := newServerHandler(cfg, pool, resilientDB)
	if handlerErr != nil {
		return fmt.Errorf("building server handler: %w", handlerErr)
	}

	// The admin surface is a separate listener: metrics and pprof must not be
	// reachable on the public port. See admin.go.
	adminErrCh := make(chan error, 1)
	if cfg.AdminEnabled() {
		admin, adminErr := newAdminServer(ctx, logger, cfg.AdminAddr, cfg.TraceSnapshotDir, metricsHandler)
		if adminErr != nil {
			return fmt.Errorf("starting admin listener: %w", adminErr)
		}
		logger.Info("admin listener started",
			"addr", admin.Addr().String(),
			"metrics", metricsHandler != nil,
			"flight_recorder", cfg.TraceSnapshotDir != "",
		)
		go func() {
			serveAdminErr := admin.Serve()
			if errors.Is(serveAdminErr, http.ErrServerClosed) {
				serveAdminErr = nil
			}
			adminErrCh <- serveAdminErr
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
			if closeErr := admin.Close(shutdownCtx); closeErr != nil {
				logger.Error("shutting down admin listener", "error", closeErr)
			}
		}()
	}

	// Support HTTP/1.1 and HTTP/2 (TLS and h2c) natively via http.Protocols.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	srv := &http.Server{
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	addr := *addrFlag
	if addr == "" {
		addr = net.JoinHostPort("", cfg.Port)
	}
	var listenCfg net.ListenConfig
	listener, listenErr := listenCfg.Listen(ctx, "tcp", addr)
	if listenErr != nil {
		return fmt.Errorf("listening on %s: %w", addr, listenErr)
	}
	defer func() { _ = listener.Close() }()

	certFile, keyFile, useTLS := tlsFiles(cfg)
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	logger.Info("pet microservice listening",
		"url", fmt.Sprintf("%s://%s", scheme, listener.Addr()),
		"tls", useTLS,
	)
	// The resolved address goes to stdout so a supervising test can discover the
	// port when it asked for :0.
	fmt.Fprintf(stdout, "listening on %s://%s\n", scheme, listener.Addr())

	serveErr := make(chan error, 1)
	go func() {
		var err error
		if useTLS {
			err = srv.ServeTLS(listener, certFile, keyFile)
		} else {
			err = srv.Serve(listener)
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serving http: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("shutting down pet microservice")
	// WithoutCancel: ctx is already cancelled, but in-flight requests should still be
	// given shutdownTimeout to drain, and their spans should stay attached.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; err != nil {
		return fmt.Errorf("serving http: %w", err)
	}

	logger.Info("server exited cleanly")
	return nil
}

// tlsFiles reports the certificate pair to serve with, and whether both are present.
// A missing pair is not an error: local development without mkcert serves cleartext.
func tlsFiles(cfg *config.Config) (certFile, keyFile string, ok bool) {
	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return "", "", false
	}
	if _, err := os.Stat(cfg.CertFile); err != nil {
		return "", "", false
	}
	if _, err := os.Stat(cfg.KeyFile); err != nil {
		return "", "", false
	}
	return cfg.CertFile, cfg.KeyFile, true
}
