package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"runtime/trace"
	"time"
)

// The admin surface — metrics, pprof, and trace snapshots — is served on its own
// listener, never on the public mux.
//
// pprof in particular must not be publicly reachable: the handlers expose heap
// contents and goroutine stacks, and /debug/pprof/profile will happily burn CPU for
// as long as a caller asks. Binding it to a separate, operator-chosen address means
// exposing it is a deliberate act rather than an accident of routing.

const (
	// flightRecorderMinAge is how much history the circular trace buffer keeps.
	flightRecorderMinAge = 10 * time.Second
	// adminReadHeaderTimeout bounds header reads on the admin listener.
	adminReadHeaderTimeout = 5 * time.Second
	// snapshotDirPerm is the mode for the directory holding trace snapshots.
	snapshotDirPerm = 0o750
)

// adminServer bundles the admin listener and the resources it owns.
type adminServer struct {
	server   *http.Server
	listener net.Listener
	recorder *trace.FlightRecorder
	logger   *slog.Logger
}

// newAdminServer builds the admin listener.
//
// A flight recorder is started when snapshotDir is set: it keeps a rolling in-memory
// execution trace of roughly the last flightRecorderMinAge, costing a few percent of
// CPU, and /debug/trace/snapshot writes the buffer to a file. That turns "the p99
// spiked twenty minutes ago and we cannot reproduce it" into a trace you can open in
// `go tool trace` — you capture the window after noticing it, not before.
//
// Requires: metricsHandler may be nil, in which case /metrics is not registered.
// Ensures:  returns a server that owns its listener; Close releases both it and the
//
//	flight recorder.
func newAdminServer(
	ctx context.Context,
	logger *slog.Logger,
	addr, snapshotDir string,
	metricsHandler http.Handler,
) (*adminServer, error) {
	mux := http.NewServeMux()

	if metricsHandler != nil {
		mux.Handle("GET /metrics", metricsHandler)
	}

	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	admin := &adminServer{logger: logger}

	if snapshotDir != "" {
		recorder := trace.NewFlightRecorder(trace.FlightRecorderConfig{MinAge: flightRecorderMinAge})
		if err := recorder.Start(); err != nil {
			return nil, fmt.Errorf("starting flight recorder: %w", err)
		}
		admin.recorder = recorder
		mux.Handle("POST /debug/trace/snapshot", handleTraceSnapshot(logger, recorder, snapshotDir))
	}

	mux.Handle("/", http.NotFoundHandler())

	var listenCfg net.ListenConfig
	listener, err := listenCfg.Listen(ctx, "tcp", addr)
	if err != nil {
		if admin.recorder != nil {
			admin.recorder.Stop()
		}
		return nil, fmt.Errorf("listening on admin address %s: %w", addr, err)
	}

	admin.listener = listener
	admin.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: adminReadHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	return admin, nil
}

// Addr reports the address the admin listener bound to.
func (a *adminServer) Addr() net.Addr { return a.listener.Addr() }

// Serve runs the admin listener until it is closed.
func (a *adminServer) Serve() error {
	return a.server.Serve(a.listener)
}

// Close shuts the admin listener down and stops the flight recorder.
func (a *adminServer) Close(ctx context.Context) error {
	var shutdownErr error
	if a.server != nil {
		shutdownErr = a.server.Shutdown(ctx)
	}
	if a.recorder != nil {
		a.recorder.Stop()
	}
	return shutdownErr
}

// handleTraceSnapshot writes the flight recorder's buffer to a file and replies with
// its path. It is a POST because it has a side effect and is not free.
func handleTraceSnapshot(logger *slog.Logger, recorder *trace.FlightRecorder, dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.MkdirAll(dir, snapshotDirPerm); err != nil {
			logger.ErrorContext(r.Context(), "creating trace snapshot directory", "dir", dir, "error", err)
			http.Error(w, "could not create snapshot directory", http.StatusInternalServerError)
			return
		}

		path := filepath.Join(dir, fmt.Sprintf("trace-%d.out", time.Now().UnixNano()))
		// dir is operator-supplied at startup; the basename is generated here, so the
		// path is not attacker-controlled.
		file, err := os.Create(path)
		if err != nil {
			logger.ErrorContext(r.Context(), "creating trace snapshot file", "path", path, "error", err)
			http.Error(w, "could not create snapshot file", http.StatusInternalServerError)
			return
		}
		defer func() { _ = file.Close() }()

		written, err := recorder.WriteTo(file)
		if err != nil {
			logger.ErrorContext(r.Context(), "writing trace snapshot", "path", path, "error", err)
			http.Error(w, "could not write snapshot", http.StatusInternalServerError)
			return
		}

		logger.InfoContext(r.Context(), "wrote execution trace snapshot", "path", path, "bytes", written)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"path":%q,"bytes":%d}`+"\n", path, written)
	})
}
