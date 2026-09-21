# Agent guide — petstore-reference

Read this before changing Go code in this repository.

## The authoritative style guide

`~/Documents/agent-orange/go-advice/summary_rules.md` is the style guide here.
Follow it. The sections that bite most often: §3 errors, §4 interfaces, §5
functional core, §7 HTTP, §9–10 testing, §11 concurrency, §14 observability. §19 is
a pre-PR checklist.

Where this file disagrees, this file wins — but only for the carve-outs below.

### Carve-outs from summary_rules

- **testify is the house assertion library**, despite §10 banning them. The repo
  moved to it deliberately (`523e212`). `require` for preconditions, `assert` for
  independent checks; no hand-rolled helpers alongside.
- **`t.Parallel()` only when a test owns its world** (§10's resolution). Tests
  installing a global (OTel, `slog.SetDefault`) or sharing the Postgres container
  stay serial with a `//nolint:paralleltest` and a reason.

## Non-negotiables

1. **Lint clean in both build configurations.** Never relax a rule: no disabling a
   linter, widening an exclusion, or bare `//nolint`. `nolintlint` requires a
   specific linter and a reason, so a suppression must argue for itself.
2. **`go test -race ./...` passes.** A data race is a failure, not a flake.
3. **`go mod tidy` and `just generate` are both no-ops.** CI fails a diff.
4. **Run `just check` before proposing a change.**

## Architecture

```
cmd/server/     main.go → run.go → server.go (+ admin.go)   the composition root
cmd/migrate/    main.go → run.go                            the migration CLI
internal/pet/   core.go (pure) + handler.go (I/O) + errors.go
internal/db/    sqlc-generated; do not hand-edit *.sql.go or models.go
internal/…      auth, config, logging, resilience, telemetry, testutil
sql/            schema/ (goose migrations) and queries/ (sqlc sources)
proto/          the API's source of truth
gen/            generated; never hand-edit
```

- `main()` only turns `run()`'s error into an exit code. `run(ctx, args, getenv,
  stdin, stdout, stderr) error` never calls `os.Exit`, so tests can drive the wired
  service in-process — see `cmd/server/integration_test.go`.
- **Read the environment through the injected `getenv`** outside `main`, so tests
  pass a map instead of `t.Setenv` and can run in parallel.
- **Never mutate process-global state from a library function.** No `os.Setenv` in a
  config loader, no `init()` writing globals.
- **Deviation from §1:** there is no root domain package, because the generated
  protobuf is this service's shared language. `internal/pet` therefore plays the
  root's role and may import `auth`, `db` and `resilience`; those must not import
  `pet` or each other. Keep the arrows pointing one way.

## Functional core, imperative shell

`internal/pet/core.go` is pure: validation, parsing, wire↔storage translation,
pagination. No I/O, no clock, no randomness. `handler.go` runs the queries and
delegates every decision to the core.

When adding behaviour, ask which half it belongs to. If a new function needs a
database to test, it probably belongs in the core with the data passed in.

Contract comments on anything non-trivial, but keep them to a line each: if
`Requires` needs more than a sentence the function has too many entry conditions,
and if `Ensures` enumerates cases it does too much.

## Authorization

- Action authorization lives in `internal/authz`: a declarative role × action
  matrix, deny by default, `admin` bypasses. Adding an RPC does **not** open it —
  name it in `AUTHZ_POLICY` or only admins can call it.
- Never hardcode a role check in a handler. A hardcoded check is invisible to
  whoever operates the service and needs a code change to adjust.
- §6 says authorization belongs in SQL `WHERE` clauses. That governs *row*
  filtering; this is *action* permission, which has no row-leak failure mode. Add
  row rules to the query when the domain needs them; the two compose.
- No ownership check by design: shelter staff edit each other's records.

## Errors

- Nothing from `pgx` or `pgconn` escapes `internal/pet`. `translate(ctx, op, err)`
  is the boundary: SQLSTATEs and sentinels to Connect codes, cause to the log, safe
  message to the caller.
- Wrap with `%w` and a lowercase, unpunctuated context: `fmt.Errorf("connecting to
  database: %w", err)`.
- The core's only error class is `errInvalid` → `CodeInvalidArgument`.

## Observability

- `log/slog` only; `forbidigo` bans stdlib `log`.
- **Use the `*Context` variants** so lines carry `trace_id`/`span_id`. A plain
  `slog.Info` silently loses correlation.
- Keys are `snake_case` (`sloglint`). Never log a secret or token.
- `/healthz` is liveness and touches no dependency; `/readyz` checks the database.
  Never merge them — a dependency blip must not kill healthy processes.
- Metrics, pprof and snapshots live on the **admin listener only**. Never the public
  mux.
- Continuous profiling is off unless `PYROSCOPE_ENDPOINT` is set. If you add a
  profile type, set its runtime sampling rate too — mutex and block profiles are
  empty otherwise, which looks like "no contention" rather than "not measured".

## Database

- Queries are declared in `sql/queries/*.sql` and generated by sqlc. Add a query
  there and run `just generate`; do not hand-write SQL in Go.
- Schema changes are goose migrations in `sql/schema/`. After changing them, refresh
  the snapshot and read the diff:
  `go test -tags=integration -run TestSchemaGolden ./internal/db/ -update`
- `ListPets` is cursor-paged on `(created_at, id)`. Do not reintroduce OFFSET: it
  serves rows twice under concurrent inserts. Row-value comparison
  `(created_at, id) < ($1, $2)`, not `created_at <= $1 AND id < $2`, which is a
  different predicate.
- A list query returns its own total via `COUNT(*) OVER()` inside a CTE holding the
  filters. Outside the CTE it would count the post-cursor remainder instead.
- Transactions never appear in a service method's signature.
  `defer tx.Rollback(ctx)` immediately after a successful `Begin`.
- Every database call goes through one of two helpers in `internal/pet/handler.go`:
  - `query(...)` for reads — retry **and** circuit breaker.
  - `exec(...)` for writes — circuit breaker **only**. A retry replays the call,
    which is unsafe for a write that may already have committed, and there is no
    idempotency key here to make a replay safe. Give the service one and writes
    could join the retry path.
  Both share a single breaker, so a failing write helps open it and an open
  breaker rejects reads and writes alike. Never call `h.queries.*` directly —
  that bypasses both policies and the no-database guard.

## Testing

- Table-driven, `t.Run` per case, cases named as sentences describing the behaviour.
- Container-backed tests go behind `//go:build integration` and live in a
  `*_integration_test.go` file. `go test ./...` must stay fast and Docker-free.
- A test that cannot run must `t.Skip` loudly. Never write
  `if err == nil { ...assertions... }` — that passes while asserting nothing.
- Pure core additions want a fuzz target in `internal/pet/fuzz_test.go`. Assert an
  invariant, not a fixed output.
- No `time.Sleep` with a fixed duration. Poll against a deadline.
- Anything added to `internal/pet/core.go` must survive `just mutate` (90% MSI).
  Two rules that mutation testing keeps catching here:
    - **Never assert against the constant under test.** Writing
      `assert.Equal(t, defaultPageSize, limit)` makes the expectation move with the
      constant, so the test cannot fail. Use the literal.
    - **Assert every field a translation function sets.** An unasserted field is
      exactly what a field-clearing mutant walks through.
  A surviving mutant that is genuinely equivalent (same observable behaviour) is
  fine — 100% is not the goal. Say so in review rather than contorting a test.

## Commits

Scope-first: `scope: imperative description`. No Conventional Commits type prefixes.

## Common commands

```
just check              every gate CI runs
just test               fast unit suite, race on, no Docker
just test-integration   container-backed suites
just lint / lint-fix    both build configurations
just fuzz-all 20s       a short burst per fuzz target
just generate           protobuf, Connect, OpenAPI, sqlc
```
