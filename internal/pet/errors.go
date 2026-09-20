package pet

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL SQLSTATE codes this service distinguishes.
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
	sqlStateNotNullViolation    = "23502"
	sqlStateSerializationFail   = "40001"
	sqlStateDeadlockDetected    = "40P01"
)

// Messages sent to clients. They deliberately say less than the underlying error:
// the detail goes to the log, where operators can see it, rather than to a caller
// who may be untrusted.
var (
	errNotFound      = errors.New("not found")
	errAlreadyExists = errors.New("already exists")
	errConflict      = errors.New("conflicting concurrent update, retry the request")
	errConstraint    = errors.New("request violates a data constraint")
	errInternal      = errors.New("internal error")
	errUnauthClaims  = errors.New("authenticated identity has no email")
)

// translate converts an error from the core or the database into the Connect error a
// client should see.
//
// This is the implementation boundary: no pgx or pgconn error escapes past it, and
// no raw database text reaches a caller. Anything unrecognised becomes Internal and
// is logged with its op so the cause is still recoverable from the logs.
//
// Requires: op names the operation in "Type.Method" form, for the log line.
// Ensures:  returns nil when err is nil; otherwise a *connect.Error.
func translate(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}

	// A validation failure from the core is the caller's fault and safe to echo.
	if errors.Is(err, errInvalid) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, errNotFound)
	}

	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		if code := connectCodeForSQLState(pgErr.Code); code != connect.CodeInternal {
			slog.DebugContext(ctx, "database constraint rejected the request",
				"op", op, "sqlstate", pgErr.Code, "error", err)
			return connect.NewError(code, messageForSQLState(pgErr.Code))
		}
	}

	slog.ErrorContext(ctx, "database operation failed", "op", op, "error", err)
	return connect.NewError(connect.CodeInternal, errInternal)
}

// connectCodeForSQLState maps a SQLSTATE onto an RPC code, returning CodeInternal
// for anything the service does not model.
func connectCodeForSQLState(sqlState string) connect.Code {
	switch sqlState {
	case sqlStateUniqueViolation:
		return connect.CodeAlreadyExists
	case sqlStateForeignKeyViolation, sqlStateCheckViolation, sqlStateNotNullViolation:
		return connect.CodeFailedPrecondition
	case sqlStateSerializationFail, sqlStateDeadlockDetected:
		// Aborted tells a well-behaved client this is worth retrying.
		return connect.CodeAborted
	default:
		return connect.CodeInternal
	}
}

// messageForSQLState returns the client-facing message for a modelled SQLSTATE.
func messageForSQLState(sqlState string) error {
	switch sqlState {
	case sqlStateUniqueViolation:
		return errAlreadyExists
	case sqlStateSerializationFail, sqlStateDeadlockDetected:
		return errConflict
	default:
		return errConstraint
	}
}
