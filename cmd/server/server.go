package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/cors"
	"github.com/sudorandom/protojsonx/protojsonxconnect"

	"github.com/example/pets/gen/go/pet/v2/petv2connect"
	"github.com/example/pets/internal/auth"
	"github.com/example/pets/internal/authz"
	"github.com/example/pets/internal/config"
	"github.com/example/pets/internal/pet"
	"github.com/example/pets/internal/profiling"
	"github.com/example/pets/internal/resilience"
	"github.com/example/pets/internal/telemetry"
)

// newServerHandler wires every route and the global middleware chain, returning the
// handler the listener will serve.
//
// A nil pool is accepted: the health endpoints then report the database as
// unreachable rather than panicking, which is also what makes the routing testable
// without a container.
//
// Requires: cfg is non-nil.
// Ensures:  the returned handler answers every registered route plus an explicit 404.
func newServerHandler(cfg *config.Config, pool *pgxpool.Pool, resilientDB *resilience.DB) (http.Handler, error) {
	otelInterceptor, err := telemetry.NewConnectInterceptor()
	if err != nil {
		return nil, fmt.Errorf("initialising opentelemetry connect interceptor: %w", err)
	}

	// A malformed policy fails startup. Falling back to a default would either
	// open procedures the operator meant to close or close ones they meant to
	// open, and neither shows up until someone is affected.
	rules, err := authz.ParseRules(cfg.AuthzPolicy)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", config.EnvAuthzPolicy, err)
	}
	policy := authz.NewPolicy(rules)
	// With deny-by-default, an operator needs to see at boot what the policy opened.
	slog.Info("authorization policy loaded",
		"procedures", policy.Procedures(),
		"note", "unlisted procedures are admin-only",
	)

	authCfg := auth.Config{
		Enabled:           cfg.AuthEnabled,
		DevMode:           cfg.DevMode,
		StaticTokens:      cfg.AuthTokens,
		TrustProxyHeaders: cfg.TrustProxyHeaders || cfg.DevMode,
		SkipProcedures:    map[string]bool{},
	}

	mux := http.NewServeMux()
	addRoutes(mux, cfg, pool, resilientDB, authCfg, policy, otelInterceptor)

	// Global middleware is applied here, once, rather than repeated per route.
	var handler http.Handler = mux
	// Outermost, so the labels cover the whole request. Applied unconditionally:
	// it costs ~13ns when no Baggage header is present, and the labels are useful
	// to anything reading pprof on the admin listener, not only to Pyroscope.
	handler = profiling.K6LabelsMiddleware()(handler)
	if cfg.DevMode {
		handler = auth.DevIdentityMiddleware(cfg.DevEmail, auth.DefaultDevSubject, cfg.DevRoles)(handler)
	}
	return cors.New(corsOptions(cfg)).Handler(handler), nil
}

// addRoutes registers every route in one place. It never fails: anything that can
// error is resolved in newServerHandler before this is called.
func addRoutes(
	mux *http.ServeMux,
	cfg *config.Config,
	pool *pgxpool.Pool,
	resilientDB *resilience.DB,
	authCfg auth.Config,
	policy *authz.Policy,
	otelInterceptor connect.Interceptor,
) {
	// Interceptor order is the request's path inwards: tracing outermost so a
	// rejected request still produces a span, then the deadline so it covers
	// everything after it, then load shedding, then authentication, then
	// authorization, and validation last — there is no point parsing a body the
	// caller was never allowed to send.
	interceptors := []connect.Interceptor{
		otelInterceptor,
		resilience.NewTimeoutInterceptor(resilience.DefaultRequestTimeout),
	}
	rateLimit := resilience.RateLimitConfig{
		RequestsPerSecond: cfg.RateLimitRPS,
		MaxWait:           resilience.DefaultRateLimitConfig().MaxWait,
	}
	if rateLimit.Enabled() {
		interceptors = append(interceptors, resilience.NewRateLimitInterceptor(rateLimit))
	}
	interceptors = append(interceptors,
		auth.NewInterceptor(authCfg),
		authz.NewInterceptor(policy),
		validate.NewInterceptor(),
	)

	petPath, connectHandler := petv2connect.NewPetServiceHandler(
		pet.NewHandler(pool).WithResilience(resilientDB),
		connect.WithCodec(&protojsonxconnect.Codec{}),
		connect.WithInterceptors(interceptors...),
	)
	mux.Handle(petPath, connectHandler)

	mux.Handle("GET /openapi.yaml", handleOpenAPISpec())
	mux.Handle("GET /docs", handleDocs())
	mux.Handle("GET /healthz", handleLiveness())
	mux.Handle("GET /readyz", handleReadiness(pool))

	mux.Handle("/", http.NotFoundHandler())
}

// handleLiveness answers whether the process itself is running. It deliberately
// touches no dependency: an orchestrator uses liveness to decide whether to kill the
// container, and a database blip must not cause healthy processes to be restarted.
// Dependency health belongs on /readyz.
func handleLiveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"status":"ok"}`)
	})
}

// handleReadiness answers whether the service can serve traffic right now, which
// means the database must be reachable. An orchestrator uses this to decide whether
// to route requests, and taking an instance out of rotation is safely reversible.
func handleReadiness(pool *pgxpool.Pool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pool != nil && pool.Ping(r.Context()) == nil {
			writeJSON(w, http.StatusOK, `{"status":"ok","database":"connected"}`)
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, `{"status":"unavailable","database":"disconnected"}`)
	})
}

// handleOpenAPISpec serves the generated specification. The candidate paths let the
// binary find the spec whether it is run from the repository root or from its own
// package directory under `go test`.
func handleOpenAPISpec() http.Handler {
	candidates := []string{
		"gen/openapi/pet/v2/pet.openapi.yaml",
		"../../gen/openapi/pet/v2/pet.openapi.yaml",
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				http.ServeFile(w, r, candidate)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// handleDocs serves the Scalar API reference page, which loads the spec from
// /openapi.yaml in the browser.
func handleDocs() http.Handler {
	const page = `<!doctype html>
<html>
  <head>
    <title>Petstore API Reference - OpenAPI</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="/openapi.yaml"></script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
}

// writeJSON writes a pre-rendered JSON body with the given status.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func corsOptions(cfg *config.Config) cors.Options {
	return cors.Options{
		AllowedOrigins:   cfg.AllowedOrigins,
		AllowCredentials: true,
		AllowedMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodDelete,
			http.MethodOptions,
		},
		AllowedHeaders: []string{
			"Accept",
			"Authorization",
			"Content-Type",
			"Connect-Protocol-Version",
			"Connect-Timeout-Ms",
			"Grpc-Timeout",
			"Traceparent",
			"Tracestate",
		},
		ExposedHeaders: []string{
			"Content-Encoding",
			"Connect-Content-Encoding",
			"Grpc-Status",
			"Grpc-Message",
			"traceparent",
			"tracestate",
		},
		MaxAge: 300,
	}
}
