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
	"github.com/example/pets/internal/profiling"
	"github.com/example/pets/internal/resilience"
	"github.com/example/pets/internal/telemetry"
)

const (
	// shutdownTimeout for in-flight requests to drain.
	shutdownTimeout = 10 * time.Second
	// otelShutdownTimeout for the final trace flush.
	otelShutdownTimeout = 5 * time.Second
	// readHeaderTimeout is the cheap defence against Slowloris.
	readHeaderTimeout = 5 * time.Second
)

// run wires the service and serves until ctx is cancelled.
//
// Every OS facility arrives as a parameter, so a test can drive run directly with a
// fake environment instead of starting a process. It never calls os.Exit, and
// closes the listener and pool on every path out.
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

	// Metrics first: otelconnect resolves the global MeterProvider when its
	// interceptor is built, so it must already exist.
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

	// Profiling starts before tracing, because whether it is running decides
	// whether spans should carry a profile id.
	profilingCfg := profiling.LoadConfig(getenv, otelCfg.ServiceName, otelCfg.ServiceVersion)
	stopProfiling, profErr := profiling.Start(profilingCfg)
	if profErr != nil {
		logger.Warn("continuous profiling unavailable", "error", profErr)
	} else if profilingCfg.Enabled() {
		logger.Info("pushing continuous profiles",
			"endpoint", profilingCfg.Endpoint, "environment", profilingCfg.Environment)
	}
	defer stopProfiling()

	if shutdownOTel, otelErr := telemetry.Init(ctx, otelCfg, profilingCfg.Enabled()); otelErr != nil {
		logger.Warn("opentelemetry init failed", "error", otelErr)
	} else {
		defer func() {
			// WithoutCancel, not Background: ctx is already cancelled, but the flush
			// should still carry the service's trace context.
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

	// Separate listener: metrics and pprof must not reach the public port.
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
	// Resolved address to stdout, so a test that asked for :0 can find the port.
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
	// WithoutCancel: ctx is cancelled, but in-flight requests still get
	// shutdownTimeout to drain.
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

// tlsFiles reports the certificate pair, if both exist. A missing pair is not an
// error: local development without mkcert serves cleartext.
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
