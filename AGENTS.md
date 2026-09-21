# Agent guide — petstore-reference

Read this before changing Go code in this repository.

## The authoritative style guide

`~/Documents/agent-orange/go-advice/summary_rules.md` is the style guide for this
repository. Follow it. The sections that bite most often here are §3 (errors), §4
(interface design), §5 (functional core / imperative shell), §7 (HTTP services), §9
and §10 (testing), §11 (concurrency), and §14 (observability). §19 is a checklist
worth re-reading before opening a pull request.

Where this file and `summary_rules.md` disagree, this file wins — but only for the
carve-outs written down below, each with its reason.

### Carve-outs from summary_rules

- **testify is allowed**, and is the house assertion library, despite §10's "do not
  use third-party testing frameworks" and "no assertion library imported". The repo
  migrated to testify deliberately (`523e212 switch to testify`). Use
  `require` for preconditions that make the rest of the test meaningless, and
  `assert` for independent checks. Do not reintroduce hand-rolled `assert`/`ok`/
  `equals` helpers alongside it — one idiom, consistently.
- **`t.Parallel()` is not universal.** §10's resolution applies: parallel only when a
  test owns its world. Tests that install a global (OTel providers, `slog.SetDefault`)
  or share the package's Postgres container must stay serial and carry a
  `//nolint:paralleltest` with the reason.

## Non-negotiables

1. **`golangci-lint run ./...` and `golangci-lint run --build-tags=integration ./...`
   must both be clean.** Never relax a rule to make code pass: do not disable a
   linter, widen an exclusion path, or add a bare `//nolint`. `nolintlint` is on and
   requires both a specific linter name and an explanation, so a suppression has to
   argue for itself. Fixing the code is the default; suppressing is the exception.
2. **`go test -race ./...` must pass.** The race detector is on in `just test` and in
   CI; a data race is a failure, not a flake.
3. **`go mod tidy` must be a no-op.** CI fails a dirty `go.mod`/`go.sum`.
4. **`just generate` must be a no-op.** Generated code is committed; CI fails a diff.
5. **Run `just check` before proposing a change.**

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

- `main()` does nothing but translate `run()`'s error into an exit code. `run()`
  takes `ctx, args, getenv, stdin, stdout, stderr` and returns `error`. It never
  calls `os.Exit`. This exists so tests can drive the whole wired service in-process
  — see `cmd/server/integration_test.go`.
- **Read the environment through the injected `getenv`**, never `os.Getenv`, outside
  `main`. Tests then pass a map instead of calling `t.Setenv`, which is what lets
  them run in parallel.
- **Never mutate process-global state from a library function.** No `os.Setenv` in a
  config loader, no `init()` that writes globals.

## Functional core, imperative shell

`internal/pet/core.go` is pure: validation, parsing, wire↔storage translation,
pagination arithmetic. No I/O, no clock, no randomness. `handler.go` opens
transactions and runs queries and otherwise delegates every decision to the core.

When you add behaviour, ask which half it belongs to. If a new function needs a
database to test, it probably belongs in the core with the data passed in.

Put a `// Requires:` / `// Ensures:` contract comment on anything non-trivial.
If `Requires` needs more than a sentence, the function takes too many entry
conditions; if `Ensures` enumerates cases, it does too much.

## Errors

- Nothing from `pgx` or `pgconn` may escape `internal/pet`. `translate(ctx, op, err)`
  in `errors.go` is the boundary: it maps SQLSTATEs and sentinels onto Connect codes,
  logs the cause with its `op`, and returns a message safe to show a caller.
- Wrap with `%w` and a lowercase, punctuation-free context string:
  `fmt.Errorf("connecting to database: %w", err)`.
- The core's only error class is `errInvalid`; the shell maps it to
  `CodeInvalidArgument`.

## Observability

- `log/slog` only. The stdlib `log` package is banned by `forbidigo`.
- **Use the `*Context` variants** — `slog.InfoContext`, `ErrorContext` — so lines
  carry `trace_id`/`span_id`. A plain `slog.Info` silently loses correlation.
- Keys are `snake_case` (enforced by `sloglint`).
- Never log a secret or a bearer token.
- `/healthz` is liveness and must touch no dependency. `/readyz` is readiness and
  checks the database. Do not merge them: a dependency blip must not get healthy
  processes killed.
- Metrics, pprof, and trace snapshots live on the **admin listener only**
  (`ADMIN_ADDR`, loopback by default). Never route them on the public mux.

## Database

- Queries are declared in `sql/queries/*.sql` and generated by sqlc. Add a query
  there and run `just generate`; do not hand-write SQL in Go.
- Schema changes are goose migrations in `sql/schema/`. After changing them, refresh
  the snapshot and read the diff:
  `go test -tags=integration -run TestSchemaGolden ./internal/db/ -update`
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
