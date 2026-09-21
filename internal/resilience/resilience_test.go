package resilience_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/resilience"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func pgError(code string) error {
	return &pgconn.PgError{Code: code, Message: "synthetic " + code}
}

// fastConfig keeps the retry delays negligible so the tests stay quick.
func fastConfig() resilience.Config {
	cfg := resilience.DefaultConfig()
	cfg.BaseDelay = time.Millisecond
	cfg.MaxDelay = 2 * time.Millisecond
	return cfg
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil is not retryable":                   {nil, false},
		"serialization failure is retryable":     {pgError("40001"), true},
		"deadlock is retryable":                  {pgError("40P01"), true},
		"connection exception is retryable":      {pgError("08000"), true},
		"connection failure is retryable":        {pgError("08006"), true},
		"cannot connect now is retryable":        {pgError("57P03"), true},
		"unique violation is NOT retryable":      {pgError("23505"), false},
		"foreign key violation is NOT retryable": {pgError("23503"), false},
		"check violation is NOT retryable":       {pgError("23514"), false},
		"syntax error is NOT retryable":          {pgError("42601"), false},
		"no rows is NOT retryable":               {pgx.ErrNoRows, false},
		"a cancelled context is NOT retryable":   {context.Canceled, false},
		"an expired deadline is NOT retryable":   {context.DeadlineExceeded, false},
		"an unrecognised error is NOT retryable": {errors.New("who knows"), false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, resilience.IsRetryable(tc.err))
		})
	}
}

// TestRetriesTransientFailures is the point of the retry policy: a serialization
// failure is replayed and the caller never sees it.
func TestRetriesTransientFailures(t *testing.T) {
	t.Parallel()

	db := resilience.NewDB(fastConfig(), discardLogger())
	var attempts atomic.Int32

	got, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
		if attempts.Add(1) < 3 {
			return "", pgError("40001")
		}
		return "succeeded", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "succeeded", got)
	assert.Equal(t, int32(3), attempts.Load(), "expected two retries before success")
}

// TestDoesNotRetryDeterministicFailures is the more important half: replaying a
// constraint violation would be pointless, and replaying an unknown failure could
// apply a write twice.
func TestDoesNotRetryDeterministicFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]error{
		"unique violation": pgError("23505"),
		"no rows":          pgx.ErrNoRows,
		"unknown error":    errors.New("something else"),
	}

	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := resilience.NewDB(fastConfig(), discardLogger())
			var attempts atomic.Int32

			_, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
				attempts.Add(1)
				return "", failure
			})

			require.Error(t, err)
			require.ErrorIs(t, err, failure)
			assert.Equal(t, int32(1), attempts.Load(), "the operation must be attempted exactly once")
		})
	}
}

func TestRetriesAreBounded(t *testing.T) {
	t.Parallel()

	cfg := fastConfig()
	cfg.MaxRetries = 2
	db := resilience.NewDB(cfg, discardLogger())
	var attempts atomic.Int32

	_, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
		attempts.Add(1)
		return "", pgError("40001")
	})

	require.Error(t, err)
	assert.Equal(t, int32(3), attempts.Load(), "one initial attempt plus MaxRetries")
}

// TestCircuitBreakerOpensAndRejects covers the behaviour that matters during an
// outage: once the database is clearly down, calls fail immediately instead of
// queueing, and the operation stops being invoked at all.
func TestCircuitBreakerOpensAndRejects(t *testing.T) {
	t.Parallel()

	cfg := fastConfig()
	cfg.MaxRetries = 0
	cfg.FailureThreshold = 3
	cfg.OpenDelay = time.Hour // long enough that it cannot close during the test
	db := resilience.NewDB(cfg, discardLogger())

	var attempts atomic.Int32
	call := func() error {
		_, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
			attempts.Add(1)
			return "", pgError("08006")
		})
		return err
	}

	for range int(cfg.FailureThreshold) {
		require.Error(t, call())
	}
	attemptsWhenOpen := attempts.Load()
	assert.Equal(t, int(cfg.FailureThreshold), int(attemptsWhenOpen))

	err := call()

	require.Error(t, err)
	require.ErrorIs(t, err, resilience.ErrUnavailable)
	assert.Equal(t, attemptsWhenOpen, attempts.Load(),
		"an open breaker must reject without invoking the operation")
	assert.Equal(t, "open", db.State())
}

// TestCircuitBreakerIgnoresCallerErrors pins the predicate: a stream of
// constraint violations means callers are sending bad requests, not that the
// database is unhealthy, so the breaker must stay closed.
func TestCircuitBreakerIgnoresCallerErrors(t *testing.T) {
	t.Parallel()

	cfg := fastConfig()
	cfg.MaxRetries = 0
	cfg.FailureThreshold = 2
	db := resilience.NewDB(cfg, discardLogger())

	for range 10 {
		_, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
			return "", pgError("23505")
		})
		require.Error(t, err)
	}

	assert.Equal(t, "closed", db.State())
}

func TestReadWithNilPoliciesRunsDirectly(t *testing.T) {
	t.Parallel()

	got, err := resilience.Read(t.Context(), nil, func(context.Context) (int, error) {
		return 42, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 42, got)
}

// TestReadReturnsZeroValueOnError confirms a failed call yields the zero value rather
// than a partially populated result.
func TestReadReturnsZeroValueOnError(t *testing.T) {
	t.Parallel()

	db := resilience.NewDB(fastConfig(), discardLogger())

	got, err := resilience.Read(t.Context(), db, func(context.Context) (*string, error) {
		return nil, pgError("23505")
	})

	require.Error(t, err)
	assert.Nil(t, got)
}

// TestWriteNeverRetries is the write-path contract. A transient failure that the
// read path would replay must be attempted exactly once here: replaying a write
// that may already have committed is worse than surfacing the error, and this
// service has no idempotency key to make a replay safe.
func TestWriteNeverRetries(t *testing.T) {
	t.Parallel()

	db := resilience.NewDB(fastConfig(), discardLogger())
	var attempts atomic.Int32

	_, err := resilience.Write(t.Context(), db, func(context.Context) (string, error) {
		attempts.Add(1)
		return "", pgError("40001") // serialization failure: the read path WOULD retry this
	})

	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load(), "a write must be attempted exactly once")
}

// TestWriteIsRejectedByAnOpenBreaker is the other half of the write contract: writes
// must fail fast during an outage rather than queueing on the connection pool.
func TestWriteIsRejectedByAnOpenBreaker(t *testing.T) {
	t.Parallel()

	cfg := fastConfig()
	cfg.MaxRetries = 0
	cfg.FailureThreshold = 2
	cfg.OpenDelay = time.Hour
	db := resilience.NewDB(cfg, discardLogger())

	// Trip the breaker through the read path.
	for range int(cfg.FailureThreshold) {
		_, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
			return "", pgError("08006")
		})
		require.Error(t, err)
	}
	require.Equal(t, "open", db.State())

	var attempts atomic.Int32
	_, err := resilience.Write(t.Context(), db, func(context.Context) (string, error) {
		attempts.Add(1)
		return "ok", nil
	})

	require.ErrorIs(t, err, resilience.ErrUnavailable)
	assert.Zero(t, attempts.Load(), "an open breaker must reject a write without running it")
}

// TestWritesTripTheSharedBreaker pins why reads and writes share one breaker: a
// database that is only being written to must still be able to open it.
func TestWritesTripTheSharedBreaker(t *testing.T) {
	t.Parallel()

	cfg := fastConfig()
	cfg.MaxRetries = 0
	cfg.FailureThreshold = 3
	cfg.OpenDelay = time.Hour
	db := resilience.NewDB(cfg, discardLogger())

	for range int(cfg.FailureThreshold) {
		_, err := resilience.Write(t.Context(), db, func(context.Context) (string, error) {
			return "", pgError("08006")
		})
		require.Error(t, err)
	}

	assert.Equal(t, "open", db.State(), "failing writes must open the shared breaker")

	// And the read path now fails fast too, because the breaker is shared.
	_, err := resilience.Read(t.Context(), db, func(context.Context) (string, error) {
		return "ok", nil
	})
	require.ErrorIs(t, err, resilience.ErrUnavailable)
}

func TestWriteWithNilPoliciesRunsDirectly(t *testing.T) {
	t.Parallel()

	got, err := resilience.Write(t.Context(), nil, func(context.Context) (int, error) {
		return 7, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 7, got)
}
