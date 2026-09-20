# Petstore Reference Architecture (`petstore-reference`)

A modern, production-grade reference microservice modeled after the classic Petstore domain, built with **[Go 1.27](https://go.dev)**, **[ConnectRPC](https://connectrpc.com)**, **[OpenTelemetry](https://opentelemetry.io)**, **[Buf](https://buf.build)**, **[protovalidate](https://github.com/bufbuild/protovalidate)**, **[FauxRPC](https://github.com/sudorandom/fauxrpc)**, **[sqlc](https://sqlc.dev)**, **[PostgreSQL](https://www.postgresql.org)**, and a **[React](https://react.dev)** + **[Vite](https://vite.dev)** frontend using **[TanStack Query](https://tanstack.com/query)** and **[Connect-Web](https://connectrpc.com/docs/web/getting-started)**.

This was put together [by request](https://github.com/sudorandom/kmcd.dev/issues/11). This shows how you can have static typing and validation for your APIs, your code (because Go) and database queries via SQLc.

---

## 🛠️ Tech Stack & Tooling

| Component | Tool / Library | Description |
| :--- | :--- | :--- |
| **Tooling Manager** | [mise](https://mise.jdx.dev) | Installs and manages `go`, `buf`, `sqlc`, `node`, `pnpm`, `fauxrpc`, etc. |
| **Language** | Go 1.27 | High-performance backend runtime |
| **RPC & API** | [ConnectRPC](https://connectrpc.com) | Multi-protocol RPC (Connect, gRPC, gRPC-Web) over HTTP/1.1 and HTTP/2 |
| **Observability** | [OpenTelemetry](https://opentelemetry.io) | Distributed tracing with W3C `traceparent` adoption via `otelconnect` and `otelpgx` |
| **Protobuf Management** | [Buf CLI](https://buf.build) | Linting, breaking change detection, and multi-language code generation |
| **Validation** | [protovalidate](https://buf.build/bufbuild/protovalidate) | Schema-level validation rules compiled into Protobuf definitions |
| **OpenAPI Generation** | [protoc-gen-connect-openapi](https://github.com/sudorandom/protoc-gen-connect-openapi) | Generates OpenAPI 3.1 specifications directly from Connect Protobuf definitions |
| **Testing & Mocking** | [FauxRPC](https://github.com/sudorandom/fauxrpc) | Fake Connect/gRPC/REST server with CEL dynamic stubs and failure simulation |
| **Integration Testing** | [Testcontainers for Go](https://golang.testcontainers.org) | Ephemeral PostgreSQL containers with automated Goose migrations and TRUNCATE isolation |
| **Database & ORM** | [sqlc](https://sqlc.dev) + [pgx/v5](https://github.com/jackc/pgx/v5) | Compile-time type-safe Go code generated from raw SQL queries |
| **Local Database** | Docker Compose | Local PostgreSQL container with automated schema migrations via Goose |
| **Linter & Security** | [golangci-lint](https://golangci-lint.run) + [gosec](https://github.com/securego/gosec) | Static analysis and security vulnerability scanner |
| **Vulnerability Scanner** | [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) | Official Go vulnerability scanner for known CVEs |
| **Frontend** | [React](https://react.dev) + [Vite](https://vite.dev) + [TanStack Query](https://tanstack.com/query) | Responsive SPA with Connect-Web, black/white dark mode toggle, and photo URLs |

---

## 📂 Project Structure

```
├── .mise.toml              # Toolchain versions (Go 1.27, Buf, sqlc, node, pnpm, fauxrpc)
├── .golangci.yml           # Linter configuration with gosec enabled
├── justfile                # Task runner commands (just)
├── docker-compose.yml      # Local PostgreSQL service
├── buf.yaml                # Buf module definition with protovalidate dependency
├── buf.gen.yaml            # Buf code generation for Go, OpenAPI, and TypeScript
├── sqlc.yaml               # SQLC configuration with pgx/v5 engine
├── cmd/
│   ├── migrate/            # Database migration CLI tool
│   └── server/             # Microservice entry point (CORS, h2c, interceptors, OTel, OpenAPI)
├── internal/
│   ├── auth/               # ConnectRPC Bearer Token authentication interceptor & context claims
│   ├── config/             # Environment variable configuration
│   ├── db/                 # SQLC generated database code & pgxpool with otelpgx
│   ├── pet/                # PetService: pure core (core.go) + I/O shell (handler.go)
│   ├── telemetry/          # OpenTelemetry TracerProvider & Connect interceptor setup
│   └── testutil/           # PostgreSQL Testcontainers helper with Goose migrations & TRUNCATE
├── proto/
│   └── pet/v1/pet.proto    # Protobuf schema with validation rules
├── gen/                    # Generated Go stubs, OpenAPI specs, and binary descriptor images
├── sql/
│   ├── schema/             # Versioned Goose migrations
│   └── queries/            # SQLC queries for pets
├── stubs/
│   ├── normal/             # FauxRPC stubs with CEL dynamic responses
│   └── failures/           # FauxRPC failure stubs for error testing
├── test/
│   └── integration_test.go # End-to-end ConnectRPC & PostgreSQL integration tests
└── web/                    # React + Vite frontend with TanStack Query and Connect-Web
    ├── src/
    │   ├── components/     # Layout, ThemeSwitcher, etc.
    │   ├── lib/            # Connect client, date utilities
    │   ├── pages/          # PetList, PetDetails, CreatePet, EditPet, Docs
    │   └── test/           # Vitest tests with ephemeral FauxRPC server
    └── package.json
```
---

## 🚀 Getting Started
 
### 1. Install Tooling with `mise`
```bash
just setup
```

### 2. Generate Code
Regenerate Protobuf, Connect stubs, OpenAPI specs, SQLC database code, and TypeScript types:
```bash
just generate
```

### 3. Start Local PostgreSQL with Docker Compose
```bash
just up
```

### 4. Code Quality, Security & Tests
```bash
# Run golangci-lint over both build configurations (default and integration)
just lint

# Apply every fix the linters can make automatically
just lint-fix

# Run Go vulnerability check
just vulncheck

# Fast unit suite: race detector on, no Docker required (~5s)
just test

# The same suite with a coverage summary
just test-cover

# Container-backed suites (needs a running Docker/Colima daemon)
just test-integration

# Fuzz the pure core; a short burst per target
just fuzz-all 20s
# ...or one target for longer
just fuzz FuzzNewPetInput 60s

# Verify go.mod/go.sum are tidy
just tidy-check

# Run frontend tests (Vitest + ephemeral FauxRPC mock server)
just test-web

# Every gate CI runs
just check

# Install the git hooks (pre-commit, pre-push, commit-msg)
just hooks
```

The same gates run on every push and pull request via
[`.github/workflows/ci.yml`](.github/workflows/ci.yml): lint, tidy, generated-code
freshness, `buf lint` plus breaking-change detection, unit tests, integration tests,
a fuzz smoke run, `govulncheck`, and the frontend build and tests.

### 5. Run the Go Microservice
```bash
just run
```
The service will be listening on `https://localhost:8080` (TLS enabled via `mkcert`).
- **Interactive OpenAPI Documentation:** `https://localhost:8080/docs`
- **OpenAPI 3.1 Spec (YAML):** `https://localhost:8080/openapi.yaml`
- **Liveness:** `https://localhost:8080/healthz` — the process is up. Touches no
  dependency, so a database blip never gets a healthy pod killed.
- **Readiness:** `https://localhost:8080/readyz` — the service can serve traffic,
  which means PostgreSQL is reachable. This is the one a load balancer should poll.
- **Connect Service:** `https://localhost:8080/pet.v1.PetService/`

Operational endpoints are served on a **separate admin listener**, bound to loopback
(`127.0.0.1:9090`) by default, so profiling data is never exposed publicly:
- **Prometheus metrics:** `http://127.0.0.1:9090/metrics` — RED metrics
  (rate, errors, duration as a histogram, so p50/p95/p99 are queryable) for every RPC
  plus Go runtime saturation signals.
- **pprof:** `http://127.0.0.1:9090/debug/pprof/` — CPU, heap, goroutine, mutex.
- **Execution trace snapshot:** `POST http://127.0.0.1:9090/debug/trace/snapshot`
  writes the flight recorder's rolling buffer to a file for `go tool trace`. Enable it
  by setting `TRACE_SNAPSHOT_DIR`.

### 6. Run the Web Frontend
```bash
just web-dev
```
Open `https://localhost:4321` in your browser (TLS enabled via `mkcert`).
- **Web Interface:** `https://localhost:4321/`
- **Embedded API Documentation (Scalar):** `https://localhost:4321/docs`
- **OpenAPI 3.1 Spec (YAML):** `https://localhost:4321/openapi.yaml`

### 7. Run FauxRPC Standalone Mock Server
To run a mock server with fake data without starting PostgreSQL:
```bash
# Run with normal dynamic stubs (celfakeit)
just fauxrpc

# Run with failure stubs (simulating errors across all RPC methods)
just fauxrpc-fail
```
- **Mock Documentation:** `https://127.0.0.1:8080/fauxrpc/docs/`
FauxRPC will be available over HTTPS at `https://127.0.0.1:8080` with built-in documentation at `/fauxrpc/docs/`.

### 8. Testing the Frontend with FauxRPC
You can test the frontend against FauxRPC both interactively in the browser and via automated tests:

#### Interactive Development / Manual Testing
1. In one terminal, start the FauxRPC mock server (listening on `:8080`, the same port as the Go server):
   ```bash
   just fauxrpc
   # or test failure states:
   just fauxrpc-fail
   ```
2. In another terminal, start the web dev server:
   ```bash
   just web-dev
   ```
   The frontend at `https://localhost:4321` proxies all requests to `https://localhost:8080` (FauxRPC).

#### Automated Frontend Tests
Run the Vitest test suite, which automatically spawns ephemeral FauxRPC mock servers and verifies frontend pages, components, and error states:
```bash
just test-web
```
Or from the `web` directory:
```bash
pnpm test
```

---

## 🧪 Testing

Tests that touch the database run against real PostgreSQL (`postgres:17-alpine`) using **[Testcontainers for Go](https://golang.testcontainers.org)** instead of mocks or SQLite. This even includes top-level handler code, which makes those tests very powerful because they will interact with all layers beneath it without any mocking code. Very often, unit tests end up testing mock assertions more than your actual code if you leverage interfaces and mocking too often.

- **Automated migrations**: Containers start with the full suite of Goose migrations applied via [`db.Migrate`](internal/db/migrate.go).
- **Fast isolation via `TRUNCATE`**: To keep test suites fast (<3s), suites reuse the container and run `TRUNCATE TABLE pets RESTART IDENTITY CASCADE;` between tests instead of recreating containers.
- **Docker & Colima**: Automatically detects Colima on macOS (`~/.colima/default/docker.sock`). Set `DATABASE_URL` to point tests at an existing database instead.
- **Pure unit tests**: Logic without database dependencies (config, CORS, auth headers, validation helpers) runs in-memory.
- **Build-tagged separation**: Container-backed suites sit behind `//go:build integration`
  in `*_integration_test.go` files, so `just test` stays fast and needs no Docker,
  while `just test-integration` runs everything.
- **Race detector everywhere**: both `just test` and `just test-integration` run `-race`.
- **Fuzzing**: the pure core in [`internal/pet/core.go`](internal/pet/core.go) has fuzz
  targets in [`fuzz_test.go`](internal/pet/fuzz_test.go) that assert invariants rather
  than fixed outputs — for example, that a pet accepted by `newPetInput` always has
  trimmed, non-blank fields and non-nil slices, whatever bytes arrived on the wire.
- **Golden schema snapshot**: [`internal/db/testdata/schema.golden`](internal/db/testdata/schema.golden)
  pins the schema the migrations produce, so a migration that drops a column or
  loosens a constraint shows up as a reviewable diff. Refresh it with
  `go test -tags=integration -run TestSchemaGolden ./internal/db/ -update`.
- **End-to-end in-process**: [`cmd/server/integration_test.go`](cmd/server/integration_test.go)
  drives the fully wired `run()` on an OS-assigned port as a real HTTP client.
- **Frontend mocks**: Web tests in `web/` use [FauxRPC](https://github.com/sudorandom/fauxrpc) stubs to test UI states without a running backend.

---

## ⚙️ Configuration

Every setting is read from the environment at startup. The service defaults to a
development posture so an unconfigured checkout runs; set `APP_ENV=production` (or
`DEV_MODE=false`) and no credential, token, or CORS origin is ever invented for you.

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | Public listen port |
| `DATABASE_URL` | local postgres | PostgreSQL connection string |
| `APP_ENV` | `development` | `production` switches to the strict posture |
| `DEV_MODE` | derived from `APP_ENV` | Explicit override |
| `AUTO_MIGRATE` | `true` in dev | Apply migrations on startup |
| `AUTH_ENABLED` | `true` | Enforce authentication |
| `AUTH_TOKENS` | dev token in dev | Comma-separated static bearer tokens |
| `TRUST_PROXY_HEADERS` | `false` | Accept upstream IAP / OAuth2-Proxy identity headers |
| `DEV_EMAIL` | `developer@local.test` | Identity injected in dev mode |
| `CORS_ALLOWED_ORIGINS` | localhost in dev | Comma-separated; `*` is dropped, as credentialed CORS forbids it |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | `.certs/*.pem` | Serve TLS when both exist, otherwise cleartext |
| `LOG_LEVEL` | `debug` in dev, `info` otherwise | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `text` in dev, `json` otherwise | `json` for production collectors |
| `ADMIN_ADDR` | `127.0.0.1:9090` | Admin listener; `off` disables it |
| `TRACE_SNAPSHOT_DIR` | unset | Enables the flight recorder and names the snapshot directory |
| `RATE_LIMIT_RPS` | `200` | Per-instance admission rate; `0` disables it |
| `OTEL_SERVICE_NAME` | `pets-service` | Resource attribute shared by traces and metrics |
| `OTEL_TRACES_EXPORTER` | `otlp` if an endpoint is set, else `none` | `otlp`, `stdout`, `none` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | OTLP gRPC collector address |
| `OTEL_SAMPLE_PERCENTAGE` | `100` | Root-span sampling; accepts a trailing `%` |
| `OTEL_CONFIG_FILE` | unset | Optional YAML/JSON/TOML telemetry config, overlaid by the environment |

Startup refuses to proceed if authentication is enabled in production with no
credential source configured — neither `TRUST_PROXY_HEADERS` nor `AUTH_TOKENS`.

---

## 🛡️ Resilience

Backed by [failsafe-go](https://failsafe-go.dev), scoped to failures this service
actually has:

- **Retry with exponential backoff and jitter** on read paths only, and only for
  SQLSTATEs known to be transient *and* known not to have applied — serialization
  failures, deadlocks, and connection-class errors. Writes are non-idempotent, so
  they deliberately stay outside the retry policy: replaying one that may already
  have committed is worse than surfacing the error.
- **Circuit breaker** on the database path, so an outage fails fast with
  `Unavailable` instead of parking every request on a connection-pool wait. Its
  predicate ignores caller errors — a stream of constraint violations means bad
  requests, not an unhealthy database, and must not trip it.
- **Rate limiting** as a Connect interceptor (`RATE_LIMIT_RPS`), using a smooth
  limiter so permits are spaced evenly rather than arriving as a burst.
- **Per-RPC deadlines**, so one slow query cannot hold a pool connection for as long
  as a client is willing to wait. A stricter client deadline is honoured; a more
  generous one is clamped.
- **Explicit connection pool bounds**, rather than pgx's CPU-derived default, which
  is also what makes the circuit breaker meaningful — without a ceiling an outage
  just grows the pool's wait queue.

---

## 🔐 Authentication

In production, user login is handled by an upstream reverse proxy (like Google Cloud IAP or OAuth2 Proxy), which forwards user identity via headers. The service also supports static bearer tokens for machine-to-machine calls.

- **Proxy headers**: Reads identity from IAP (`X-Goog-Authenticated-User-*`) or OAuth2 Proxy (`X-Forwarded-*`) when `TRUST_PROXY_HEADERS=true`. Only enable this behind a proxy that strips untrusted client headers.
- **Bearer tokens**: Set `AUTH_TOKENS=token1,token2` for service-to-service or CLI access (`Authorization: Bearer <token>`).
- **Local dev**: When `DEV_MODE=true` (default), requests without credentials automatically use a dummy developer identity (`developer@local.test`).
- **Context**: Parsed claims are accessible in Go handlers via [`auth.FromContext(ctx)`](internal/auth/auth.go).
