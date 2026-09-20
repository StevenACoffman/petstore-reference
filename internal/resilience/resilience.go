// Package resilience holds the failure-handling policies that sit between the
// service and the resources it depends on: retries, a circuit breaker, and an
// admission rate limit.
//
// The policies are deliberately narrow. Retrying is only safe for failures that are
// known to be transient and known not to have applied, so the retry predicate names
// specific PostgreSQL SQLSTATEs rather than retrying every error. The circuit breaker
// exists so that a database outage fails fast with Unavailable instead of parking
// every request on a connection-pool wait, which is how one slow dependency turns
// into a service-wide stall.
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

// PostgreSQL SQLSTATEs that describe a transient, safely retryable failure.
//
// Serialization failures and deadlocks mean the transaction was rolled back and
// never applied, so replaying it cannot double-apply anything. Connection-class
// errors (08xxx) mean the statement never reached a live backend.
const (
	sqlStateSerializationFailure = "40001"
	sqlStateDeadlockDetected     = "40P01"
	sqlStateConnectionException  = "08000"
	sqlStateConnectionFailure    = "08006"
	sqlStateCannotConnectNow     = "57P03"
)

// Config tunes the database-facing policies.
type Config struct {
	// MaxRetries is how many times a transient failure is replayed. Zero disables
	// retrying.
	MaxRetries int
	// BaseDelay is the first backoff interval; each retry multiplies it.
	BaseDelay time.Duration
	// MaxDelay caps the backoff interval.
	MaxDelay time.Duration
	// JitterFactor randomises each delay by this fraction, so that a fleet of
	// instances recovering from the same outage does not retry in lockstep.
	JitterFactor float64
	// FailureThreshold is how many consecutive failures open the breaker.
	FailureThreshold uint
	// SuccessThreshold is how many consecutive successes close it again.
	SuccessThreshold uint
	// OpenDelay is how long the breaker stays open before probing with a trial call.
	OpenDelay time.Duration
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

// DB applies the database-facing policies to an operation.
type DB struct {
	executor failsafe.Executor[any]
	breaker  circuitbreaker.CircuitBreaker[any]
}

// NewDB builds the retry and circuit-breaker pair for database work.
//
// Composition order matters: the executor is built retry-outermost, so a transient
// failure is retried, and only a run of genuine failures trips the breaker. Building
// it the other way would let the breaker count each retry of a single logical call
// as a separate failure and open far too eagerly.
//
// Requires: cfg fields are non-negative; a zero MaxRetries disables retrying.
// Ensures:  the returned DB is safe for concurrent use.
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
		executor: failsafe.With[any](retry, breaker),
		breaker:  breaker,
	}
}

// State reports the circuit breaker's current state, for the readiness endpoint.
func (d *DB) State() string { return d.breaker.State().String() }

// Do runs op under the retry and circuit-breaker policies.
//
// Requires: op is idempotent, because it may be invoked more than once.
// Ensures:  returns ErrUnavailable when the breaker rejected the call outright;
//
//	otherwise returns op's own error unchanged, so callers can still match
//	on pgx sentinels.
func (d *DB) Do(ctx context.Context, op func(context.Context) error) error {
	_, err := Get(ctx, d, func(innerCtx context.Context) (any, error) {
		return nil, op(innerCtx)
	})
	return err
}

// Get runs a value-returning operation under db's policies.
//
// It is a function rather than a method because Go does not allow methods to
// introduce new type parameters.
//
// Requires: op is idempotent. A nil db runs op directly, which keeps resilience
//
//	optional for tests and for callers that have not opted in.
//
// Ensures: returns ErrUnavailable when the breaker is open; otherwise op's result.
func Get[T any](ctx context.Context, db *DB, op func(context.Context) (T, error)) (T, error) {
	var zero T
	if db == nil {
		return op(ctx)
	}

	// ctx is passed straight through rather than read back from the execution: the
	// executor carries no timeout policy, so exec.Context() is this same context, and
	// forwarding it directly keeps the data flow obvious.
	result, err := db.executor.WithContext(ctx).Get(func() (any, error) {
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

// IsRetryable reports whether err describes a transient database failure that is
// safe to replay.
//
// Requires: nothing.
// Ensures:  returns false for a nil error, for a cancelled context (the caller has
//
//	already given up), and for anything not on the known-transient list.
//	Defaulting to false is the safe direction: retrying an unknown failure
//	risks applying a write twice.
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

// isInfrastructureFailure reports whether err suggests the database itself is
// unhealthy, as opposed to the request being bad.
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

// isNetworkFailure reports whether err looks like a transport problem rather than a
// response from the server.
func isNetworkFailure(err error) bool {
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}
