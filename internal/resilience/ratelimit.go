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
	// RequestsPerSecond admitted; zero disables rate limiting.
	RequestsPerSecond uint
	// MaxWait for a permit. Short on purpose: a caller would rather be told to back
	// off than sit in a queue longer than its own timeout.
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

// NewRateLimitInterceptor admits at most the configured rate, shedding the excess
// with CodeResourceExhausted.
//
// This is admission control, not client throttling: it keeps an overload from
// becoming an outage. It is per-instance; a fleet-wide limit needs a shared
// counter. The limiter is smooth rather than bursty so permits are spaced evenly
// instead of handing the database a full second's allowance at once.
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
