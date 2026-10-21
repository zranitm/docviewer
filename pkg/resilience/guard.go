// Package resilience puts any call behind a circuit breaker and a bounded
// retry. It knows nothing about HTTP, gRPC or any other transport; each
// transport gets a thin adapter package (resiliencehttp today).
package resilience

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/sony/gobreaker/v2"
)

// Policy decides what is worth retrying and what counts against the breaker.
type Policy struct {
	MaxRetries int              // extra attempts after the first
	MinBudget  time.Duration    // deadline that must remain before a retry
	MaxJitter  time.Duration    // upper bound of the wait before a retry
	Retryable  func(error) bool // errors worth another attempt; nil retries nothing
	Ignore     func(error) bool // errors that say nothing about the callee's health
	TripAfter  uint32           // consecutive failures that open the breaker
	Cooldown   time.Duration    // time open before a probe
	OnState    func(name string, from, to gobreaker.State)
}

// Guard puts calls returning T behind a circuit breaker and a bounded retry.
type Guard[T any] struct {
	cb     *gobreaker.CircuitBreaker[T]
	policy Policy
}

// NewGuard makes a Guard named name (the breaker's name in state changes).
func NewGuard[T any](name string, p Policy) *Guard[T] {
	if p.Retryable == nil {
		p.Retryable = func(error) bool { return false } // nothing is retried unless the policy says so
	}
	return &Guard[T]{policy: p, cb: gobreaker.NewCircuitBreaker[T](gobreaker.Settings{
		Name:          name,
		Timeout:       p.Cooldown,
		ReadyToTrip:   func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= p.TripAfter },
		IsSuccessful:  func(err error) bool { return err == nil || (p.Ignore != nil && p.Ignore(err)) },
		OnStateChange: p.OnState,
	})}
}

// Do runs call through the breaker, retrying inside it, so one logical call
// counts as one failure and an open breaker skips the retries entirely.
func (g *Guard[T]) Do(ctx context.Context, call func(context.Context) (T, error)) (T, error) {
	return g.cb.Execute(func() (T, error) { return retry(ctx, g.policy, call) })
}

// retry calls once, then again up to p.MaxRetries times while the error is
// retryable and the deadline leaves room, waiting a random jitter in between.
func retry[T any](ctx context.Context, p Policy, call func(context.Context) (T, error)) (T, error) {
	v, err := call(ctx)
	for i := 0; err != nil && i < p.MaxRetries && p.Retryable(err) && hasBudget(ctx, p.MinBudget); i++ {
		select {
		case <-time.After(jitter(p.MaxJitter)):
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
		v, err = call(ctx)
	}
	return v, err
}

// jitter is a random wait in [0, max].
func jitter(maxWait time.Duration) time.Duration {
	if maxWait <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(maxWait) + 1)) //nolint:gosec // backoff jitter, not a secret
}

// hasBudget reports whether at least minLeft remains before ctx's deadline,
// or that ctx has none.
func hasBudget(ctx context.Context, minLeft time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) > minLeft
}
