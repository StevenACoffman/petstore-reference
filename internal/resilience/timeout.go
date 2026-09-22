package resilience

import (
	"context"
	"time"

	"connectrpc.com/connect"
)

// DefaultRequestTimeout bounds any single RPC. Without it, one slow query holds a
// pool connection for as long as the client waits — and a client that has already
// given up does not release it.
const DefaultRequestTimeout = 30 * time.Second

// NewTimeoutInterceptor bounds every RPC at timeout. A shorter client deadline is
// honoured — the client knows its own patience; a longer one is clamped, because
// the limit exists to protect the server.
func NewTimeoutInterceptor(timeout time.Duration) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
				// Already at least as strict.
				return next(ctx, req)
			}

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return next(ctx, req)
		}
	}
}
