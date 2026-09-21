package pet

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/example/pets/internal/resilience"
)

// translate is the boundary that decides what a client learns about a failure, so
// every branch of it is pinned here. The two properties that matter: the right code
// reaches the caller, and no raw database text does.
func TestTranslate(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err      error
		wantCode connect.Code
		wantMsg  string
	}{
		"a core validation failure is the caller's fault": {
			err:      fmt.Errorf("%w: name and species cannot be blank", errInvalid),
			wantCode: connect.CodeInvalidArgument,
			wantMsg:  "name and species cannot be blank",
		},
		"an open circuit breaker is Unavailable, not Internal": {
			err:      fmt.Errorf("%w: database circuit breaker is open", resilience.ErrUnavailable),
			wantCode: connect.CodeUnavailable,
			wantMsg:  "retry later",
		},
		"a handler with no database is Unavailable": {
			err:      errNoDatabase,
			wantCode: connect.CodeUnavailable,
			wantMsg:  "retry later",
		},
		"a missing row is NotFound": {
			err:      pgx.ErrNoRows,
			wantCode: connect.CodeNotFound,
			wantMsg:  "not found",
		},
		"a unique violation is AlreadyExists": {
			err:      &pgconn.PgError{Code: sqlStateUniqueViolation, Message: "duplicate key value"},
			wantCode: connect.CodeAlreadyExists,
			wantMsg:  "already exists",
		},
		"a foreign key violation is FailedPrecondition": {
			err:      &pgconn.PgError{Code: sqlStateForeignKeyViolation},
			wantCode: connect.CodeFailedPrecondition,
			wantMsg:  "constraint",
		},
		"a check violation is FailedPrecondition": {
			err:      &pgconn.PgError{Code: sqlStateCheckViolation},
			wantCode: connect.CodeFailedPrecondition,
			wantMsg:  "constraint",
		},
		"a not-null violation is FailedPrecondition": {
			err:      &pgconn.PgError{Code: sqlStateNotNullViolation},
			wantCode: connect.CodeFailedPrecondition,
			wantMsg:  "constraint",
		},
		"a serialization failure is Aborted, which invites a retry": {
			err:      &pgconn.PgError{Code: sqlStateSerializationFail},
			wantCode: connect.CodeAborted,
			wantMsg:  "retry",
		},
		"a deadlock is Aborted": {
			err:      &pgconn.PgError{Code: sqlStateDeadlockDetected},
			wantCode: connect.CodeAborted,
			wantMsg:  "retry",
		},
		"an unmodelled SQLSTATE falls back to Internal": {
			err:      &pgconn.PgError{Code: "42601", Message: "syntax error at or near SELECT"},
			wantCode: connect.CodeInternal,
			wantMsg:  "internal error",
		},
		"an unrecognised error falls back to Internal": {
			err:      errors.New("something nobody modelled"),
			wantCode: connect.CodeInternal,
			wantMsg:  "internal error",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := translate(context.Background(), "Handler.Test", tc.err)

			require.Error(t, got)
			assert.Equal(t, tc.wantCode, connect.CodeOf(got))
			assert.Contains(t, got.Error(), tc.wantMsg)
		})
	}
}

func TestTranslatePassesNilThrough(t *testing.T) {
	t.Parallel()

	assert.NoError(t, translate(context.Background(), "Handler.Test", nil))
}

// TestTranslateLeaksNoDatabaseDetail is the security half of the boundary: an
// operator needs the cause in the log, a caller must not receive it in a response.
func TestTranslateLeaksNoDatabaseDetail(t *testing.T) {
	t.Parallel()

	const secret = "relation \"internal_billing\" does not exist"
	cases := map[string]error{
		"an unmodelled SQLSTATE": &pgconn.PgError{Code: "42P01", Message: secret},
		"a bare error":           errors.New(secret),
		"a wrapped error":        fmt.Errorf("querying pets: %w", errors.New(secret)),
	}

	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := translate(context.Background(), "Handler.Test", err)

			require.Error(t, got)
			assert.NotContains(t, got.Error(), "internal_billing",
				"the database's own message must not reach the caller")
			assert.Contains(t, got.Error(), "internal error")
		})
	}
}
