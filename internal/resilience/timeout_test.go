package resilience_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	petv2 "github.com/example/pets/gen/go/pet/v2"
	"github.com/example/pets/internal/resilience"
)

// callWithTimeout runs a no-op RPC through the timeout interceptor and reports the
// deadline the handler observed.
func callWithTimeout(
	ctx context.Context, t *testing.T, timeout time.Duration,
) (deadline time.Time, hasDeadline bool) {
	t.Helper()

	interceptor := resilience.NewTimeoutInterceptor(timeout)
	handler := interceptor(func(innerCtx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		deadline, hasDeadline = innerCtx.Deadline()
		return connect.NewResponse(&petv2.ListPetsResponse{}), nil
	})

	_, err := handler(ctx, connect.NewRequest(&petv2.ListPetsRequest{}))
	require.NoError(t, err)
	return deadline, hasDeadline
}

func TestTimeoutInterceptorAppliesADeadline(t *testing.T) {
	t.Parallel()

	deadline, ok := callWithTimeout(t.Context(), t, 250*time.Millisecond)

	require.True(t, ok, "the handler must receive a deadline")
	assert.WithinDuration(t, time.Now().Add(250*time.Millisecond), deadline, 100*time.Millisecond)
}

// TestTimeoutInterceptorHonoursAStricterCaller pins the rule that a client that has
// already committed to a shorter wait keeps it: the server must not extend it.
func TestTimeoutInterceptorHonoursAStricterCaller(t *testing.T) {
	t.Parallel()

	callerCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	t.Cleanup(cancel)

	deadline, ok := callWithTimeout(callerCtx, t, time.Hour)

	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), deadline, 40*time.Millisecond)
}

// TestTimeoutInterceptorClampsAGenerousCaller is the other half: a client willing to
// wait an hour does not get to hold a server connection for one.
func TestTimeoutInterceptorClampsAGenerousCaller(t *testing.T) {
	t.Parallel()

	callerCtx, cancel := context.WithTimeout(t.Context(), time.Hour)
	t.Cleanup(cancel)

	deadline, ok := callWithTimeout(callerCtx, t, 200*time.Millisecond)

	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(200*time.Millisecond), deadline, 100*time.Millisecond)
}

func TestRateLimitConfigEnabled(t *testing.T) {
	t.Parallel()

	assert.False(t, resilience.RateLimitConfig{RequestsPerSecond: 0}.Enabled())
	assert.True(t, resilience.RateLimitConfig{RequestsPerSecond: 1}.Enabled())
	assert.True(t, resilience.DefaultRateLimitConfig().Enabled())
}

// TestRateLimitInterceptorShedsExcess confirms that traffic beyond the configured
// rate is refused with ResourceExhausted rather than queued indefinitely.
func TestRateLimitInterceptorShedsExcess(t *testing.T) {
	t.Parallel()

	interceptor := resilience.NewRateLimitInterceptor(resilience.RateLimitConfig{
		RequestsPerSecond: 1,
		MaxWait:           time.Millisecond,
	})
	var admitted int
	handler := interceptor(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		admitted++
		return connect.NewResponse(&petv2.ListPetsResponse{}), nil
	})

	var rejected int
	for range 20 {
		_, err := handler(t.Context(), connect.NewRequest(&petv2.ListPetsRequest{}))
		if err != nil {
			assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
			rejected++
		}
	}

	assert.Positive(t, admitted, "the limiter must admit some traffic")
	assert.Positive(t, rejected, "the limiter must shed the excess")
	assert.Equal(t, 20, admitted+rejected)
}
