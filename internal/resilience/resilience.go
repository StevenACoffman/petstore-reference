// Package resilience holds the database-facing failure policies: retries, a
// circuit breaker, an admission rate limit, and a request deadline.
//
// The retry predicate names specific SQLSTATEs rather than retrying everything,
// because a replay is only safe for a failure known not to have applied. The
// breaker exists so an outage fails fast instead of parking every request on a
// connection-pool wait.
package resilience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATEs that are transient and safe to replay: serialization failures and
// deadlocks rolled the transaction back, and 08xxx never reached a backend.
const (
	sqlStateSerializationFailure = "40001"
	sqlStateDeadlockDetected     = "40P01"
	sqlStateConnectionException  = "08000"
	sqlStateConnectionFailure    = "08006"
	sqlStateCannotConnectNow     = "57P03"
)

// Config tunes the database-facing policies.
type Config struct {
	// MaxRetries replays of a transient failure; zero disables retrying.
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	// JitterFactor keeps a recovering fleet from retrying in lockstep.
	JitterFactor float64
	// FailureThreshold consecutive failures open the breaker, SuccessThreshold
	// consecutive successes close it, and OpenDelay is how long it stays open.
	FailureThreshold uint
	SuccessThreshold uint
	OpenDelay        time.Duration
}

// DefaultConfig returns policy settings suited to a local PostgreSQL dependency.
func DefaultConfig() Config {
	return Config{
		MaxRetries:       3,
		BaseDelay:        20 * time.Millisecond,
		MaxDelay:         500 * time.Millisecond,
		JitterFactor:     0.3,
		FailureThreshold: 5,
		SuccessThreshold: 2,
		OpenDelay:        5 * time.Second,
	}
}

// ErrUnavailable reports that the circuit breaker is open, so the call was rejected
// without being attempted.
var ErrUnavailable = errors.New("dependency unavailable")

// DB applies the database policies. Reads and writes differ but share one
// breaker: a failing write must help open it, and once open it must reject both.
type DB struct {
	query   failsafe.Executor[any] // retry, then breaker
	exec    failsafe.Executor[any] // breaker only
	breaker circuitbreaker.CircuitBreaker[any]
}

// NewDB builds the policies. Retry is outermost, so the breaker counts one logical
// call once rather than counting every retry of it and opening far too eagerly.
//
// The returned DB is safe for concurrent use.
func NewDB(cfg Config, logger *slog.Logger) *DB {
	breaker := circuitbreaker.NewBuilder[any]().
		HandleIf(func(_ any, err error) bool {
			// Validation failures and missing rows are normal outcomes, not evidence
			// that the database is unhealthy; they must not trip the breaker.
			return err != nil && isInfrastructureFailure(err)
		}).
		WithFailureThreshold(cfg.FailureThreshold).
		WithSuccessThreshold(cfg.SuccessThreshold).
		WithDelay(cfg.OpenDelay).
		OnStateChanged(func(event circuitbreaker.StateChangedEvent) {
			logger.Warn("database circuit breaker changed state",
				"from", event.OldState.String(),
				"to", event.NewState.String(),
			)
		}).
		Build()

	retry := retrypolicy.NewBuilder[any]().
		HandleIf(func(_ any, err error) bool {
			return err != nil && IsRetryable(err)
		}).
		WithMaxRetries(cfg.MaxRetries).
		WithBackoff(cfg.BaseDelay, cfg.MaxDelay).
		WithJitterFactor(cfg.JitterFactor).
		Build()

	return &DB{
		query: failsafe.With[any](retry, breaker),
		// Writes deliberately get the breaker WITHOUT the retry policy. Replaying a
		// write that may already have committed is worse than surfacing the error,
		// and this service has no idempotency key to make a replay safe
		// (summary_rules §12: "make every mutating operation idempotent — assign a
		// client-generated idempotency key"). Add one and writes could join the
		// retry path; until then they only fail fast.
		exec:    failsafe.With[any](breaker),
		breaker: breaker,
	}
}

// State reports the circuit breaker's current state, for the readiness endpoint.
func (d *DB) State() string { return d.breaker.State().String() }

// Read runs an idempotent operation under retry and the breaker.
//
// Requires: op is idempotent; a transient failure replays it. A nil db runs op
// directly. Returns ErrUnavailable when the breaker is open.
func Read[T any](ctx context.Context, db *DB, op func(context.Context) (T, error)) (T, error) {
	if db == nil {
		return op(ctx)
	}
	return runUnder(ctx, db.query, op)
}

// Write runs a non-idempotent operation under the breaker alone, never retried:
// replaying a write that may already have committed is worse than surfacing the
// error, and there is no idempotency key here to make a replay safe.
//
// op runs at most once. Returns ErrUnavailable, without invoking op, when the
// breaker is open.
func Write[T any](ctx context.Context, db *DB, op func(context.Context) (T, error)) (T, error) {
	if db == nil {
		return op(ctx)
	}
	return runUnder(ctx, db.exec, op)
}

// runUnder unboxes the any-typed result failsafe works in.
func runUnder[T any](
	ctx context.Context, executor failsafe.Executor[any], op func(context.Context) (T, error),
) (T, error) {
	var zero T

	// No timeout policy is configured, so the execution context is this one;
	// forwarding ctx directly keeps the data flow obvious.
	result, err := executor.WithContext(ctx).Get(func() (any, error) {
		return op(ctx)
	})
	if err != nil {
		if errors.Is(err, circuitbreaker.ErrOpen) {
			return zero, fmt.Errorf("%w: database circuit breaker is open", ErrUnavailable)
		}
		return zero, err
	}
	if result == nil {
		return zero, nil
	}
	typed, ok := result.(T)
	if !ok {
		return zero, fmt.Errorf("resilience: expected %T from the execution, got %T", zero, result)
	}
	return typed, nil
}

// IsRetryable reports whether err is a transient failure that is safe to replay.
// It defaults to false: retrying an unknown failure risks applying a write twice.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		// No server-side error means the statement never reached a backend. pgx's own
		// SafeToRetry knows which transport failures guarantee that.
		return pgconn.SafeToRetry(err) || errors.Is(err, pgconn.ErrConnClosed) || isNetworkFailure(err)
	}
	switch pgErr.Code {
	case sqlStateSerializationFailure,
		sqlStateDeadlockDetected,
		sqlStateConnectionException,
		sqlStateConnectionFailure,
		sqlStateCannotConnectNow:
		return true
	default:
		return false
	}
}

// isInfrastructureFailure distinguishes an unhealthy database from a bad request.
func isInfrastructureFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if IsRetryable(err) {
		return true
	}
	// A server-side error with a SQLSTATE means the database answered, so it is up.
	_, isPgError := errors.AsType[*pgconn.PgError](err)
	return !isPgError && isNetworkFailure(err)
}

// isNetworkFailure reports a transport problem rather than a server response.
func isNetworkFailure(err error) bool {
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}
