package resilience

import (
	"context"
	"time"

	"connectrpc.com/connect"
)

// DefaultRequestTimeout bounds how long any single RPC may run.
//
// Without a server-side deadline, one slow query holds a pool connection for as long
// as the client is willing to wait — and a client that has already given up does not
// release it. An explicit ceiling means work is abandoned at a known point.
const DefaultRequestTimeout = 30 * time.Second

// NewTimeoutInterceptor bounds every RPC at the given duration.
//
// A deadline the client already set is honoured when it is shorter: the client knows
// its own patience better than the server does, and shortening is always safe.
// A longer client deadline is clamped, because the server's limit exists to protect
// the server.
//
// Requires: timeout is positive.
// Ensures:  the context handed to the handler always carries a deadline no later
//
//	than now+timeout.
func NewTimeoutInterceptor(timeout time.Duration) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
				// The caller's deadline is already at least as strict; leave it alone.
				return next(ctx, req)
			}

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return next(ctx, req)
		}
	}
}
