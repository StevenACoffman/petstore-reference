package resilience

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/failsafe-go/failsafe-go/ratelimiter"
)

// RateLimitConfig tunes server-side admission control.
type RateLimitConfig struct {
	// RequestsPerSecond is the sustained rate the service will admit. Zero or less
	// disables rate limiting entirely.
	RequestsPerSecond uint
	// MaxWait is how long a request may wait for a permit before being rejected.
	// Keeping this short is the point: a caller would rather be told to back off than
	// sit in a queue that is already longer than their own timeout.
	MaxWait time.Duration
}

// DefaultRateLimitConfig returns a rate limit suited to a single service instance.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		RequestsPerSecond: 200,
		MaxWait:           250 * time.Millisecond,
	}
}

// Enabled reports whether a rate limit should be installed.
func (c RateLimitConfig) Enabled() bool { return c.RequestsPerSecond > 0 }

// NewRateLimitInterceptor returns a Connect interceptor that admits at most the
// configured rate, rejecting the excess with CodeResourceExhausted.
//
// This is admission control, not client throttling: it protects the service and its
// database from more concurrent work than they can absorb, which is what keeps an
// overload from becoming an outage. It is per-instance — a fleet-wide limit needs a
// shared counter, which is a different mechanism.
//
// A smooth limiter is used rather than a bursty one so that permits are spaced
// evenly; a bursty limiter would let a full second's allowance arrive at once and
// hand the database a thundering herd.
//
// Requires: cfg.Enabled() is true; the caller checks this.
// Ensures:  the interceptor never blocks longer than cfg.MaxWait, and honours
//
//	cancellation of the request context while waiting.
func NewRateLimitInterceptor(cfg RateLimitConfig) connect.UnaryInterceptorFunc {
	limiter := ratelimiter.NewSmoothBuilder[any](cfg.RequestsPerSecond, time.Second).
		WithMaxWaitTime(cfg.MaxWait).
		Build()

	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if err := limiter.AcquirePermitWithMaxWait(ctx, cfg.MaxWait); err != nil {
				if errors.Is(err, ratelimiter.ErrExceeded) {
					return nil, connect.NewError(
						connect.CodeResourceExhausted,
						fmt.Errorf("rate limit of %d requests per second exceeded", cfg.RequestsPerSecond),
					)
				}
				// The only other outcome is the caller's context ending.
				return nil, connect.NewError(connect.CodeCanceled, err)
			}
			return next(ctx, req)
		}
	}
}
