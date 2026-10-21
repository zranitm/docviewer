package resilience_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sony/gobreaker/v2"

	"docviewer/pkg/resilience"
)

var (
	errTransient = errors.New("transient")
	errFatal     = errors.New("fatal")
)

// script is a call that returns the scripted outcomes in order, then the
// last one forever, counting attempts.
type script struct {
	outcomes []error
	calls    int
}

func (s *script) call(context.Context) (int, error) {
	i := min(s.calls, len(s.outcomes)-1)
	s.calls++
	if err := s.outcomes[i]; err != nil {
		return 0, err
	}
	return s.calls, nil
}

func isTransient(err error) bool { return errors.Is(err, errTransient) }

func TestGuardRetry(t *testing.T) {
	tests := []struct {
		name      string
		policy    resilience.Policy
		outcomes  []error
		deadline  time.Duration // 0 means none
		cancelled bool          // the context is cancelled before the call
		wantCalls int
		wantErr   error // nil means success
	}{
		{
			name:      "retryable_error_then_success",
			policy:    resilience.Policy{MaxRetries: 1, Retryable: isTransient, TripAfter: 10},
			outcomes:  []error{errTransient, nil},
			wantCalls: 2,
		},
		{
			name:      "non_retryable_error_is_not_retried",
			policy:    resilience.Policy{MaxRetries: 1, Retryable: isTransient, TripAfter: 10},
			outcomes:  []error{errFatal, nil},
			wantCalls: 1,
			wantErr:   errFatal,
		},
		{
			name:      "max_retries_bounds_the_attempts",
			policy:    resilience.Policy{MaxRetries: 2, Retryable: isTransient, TripAfter: 10},
			outcomes:  []error{errTransient},
			wantCalls: 3,
			wantErr:   errTransient,
		},
		{
			name:      "nil_retryable_means_nothing_is_retried",
			policy:    resilience.Policy{MaxRetries: 3, TripAfter: 10},
			outcomes:  []error{errTransient, nil},
			wantCalls: 1,
			wantErr:   errTransient,
		},
		{
			name:      "no_retry_without_enough_budget",
			policy:    resilience.Policy{MaxRetries: 1, MinBudget: 500 * time.Millisecond, Retryable: isTransient, TripAfter: 10},
			outcomes:  []error{errTransient, nil},
			deadline:  100 * time.Millisecond,
			wantCalls: 1,
			wantErr:   errTransient,
		},
		{
			name:      "retry_when_the_budget_allows",
			policy:    resilience.Policy{MaxRetries: 1, MinBudget: 500 * time.Millisecond, Retryable: isTransient, TripAfter: 10},
			outcomes:  []error{errTransient, nil},
			deadline:  10 * time.Second,
			wantCalls: 2,
		},
		{
			name:      "no_deadline_means_budget_is_always_there",
			policy:    resilience.Policy{MaxRetries: 1, MinBudget: time.Hour, Retryable: isTransient, TripAfter: 10},
			outcomes:  []error{errTransient, nil},
			wantCalls: 2,
		},
		{
			name:      "cancelled_while_waiting_returns_the_context_error",
			policy:    resilience.Policy{MaxRetries: 1, MaxJitter: time.Hour, Retryable: isTransient, TripAfter: 10},
			outcomes:  []error{errTransient, nil},
			cancelled: true,
			wantCalls: 1,
			wantErr:   context.Canceled,
		},
		{
			name:      "zero_jitter_retries_at_once",
			policy:    resilience.Policy{MaxRetries: 1, MaxJitter: 0, Retryable: isTransient, TripAfter: 10},
			outcomes:  []error{errTransient, nil},
			wantCalls: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				if tt.deadline > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tt.deadline)
					defer cancel()
				}
				if tt.cancelled {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				s := &script{outcomes: tt.outcomes}
				g := resilience.NewGuard[int](tt.name, tt.policy)
				_, err := g.Do(ctx, s.call)
				if s.calls != tt.wantCalls {
					t.Errorf("calls = %d, want %d", s.calls, tt.wantCalls)
				}
				if tt.wantErr == nil && err != nil {
					t.Errorf("Do() error = %v, want nil", err)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("Do() error = %v, want %v", err, tt.wantErr)
				}
			})
		})
	}
}

// TestGuardJitterWaits: the wait before a retry is at most MaxJitter, and at
// least something happens on the virtual clock only when MaxJitter > 0.
func TestGuardJitterWaits(t *testing.T) {
	tests := []struct {
		name      string
		maxJitter time.Duration
	}{
		{name: "100ms_jitter", maxJitter: 100 * time.Millisecond},
		{name: "no_jitter", maxJitter: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := &script{outcomes: []error{errTransient, nil}}
				g := resilience.NewGuard[int](tt.name, resilience.Policy{MaxRetries: 1, MaxJitter: tt.maxJitter, Retryable: isTransient, TripAfter: 10})
				start := time.Now()
				if _, err := g.Do(t.Context(), s.call); err != nil {
					t.Fatal(err)
				}
				if waited := time.Since(start); waited > tt.maxJitter {
					t.Errorf("waited %v, more than MaxJitter %v", waited, tt.maxJitter)
				}
			})
		})
	}
}

func TestGuardBreaker(t *testing.T) {
	ignoreFatal := func(err error) bool { return errors.Is(err, errFatal) }
	tests := []struct {
		name       string
		policy     resilience.Policy
		outcomes   []error
		calls      int           // calls to make before the probe
		cooldown   time.Duration // time to let pass before the probe; 0 means none
		wantProbed bool          // the probe reaches the call
		wantErr    error         // the probe's error; nil means success
		wantStates []gobreaker.State
	}{
		{
			name:       "opens_after_trip_after_consecutive_failures",
			policy:     resilience.Policy{TripAfter: 2, Cooldown: time.Minute},
			outcomes:   []error{errTransient},
			calls:      2,
			wantProbed: false,
			wantErr:    gobreaker.ErrOpenState,
			wantStates: []gobreaker.State{gobreaker.StateOpen},
		},
		{
			name:       "stays_closed_below_the_threshold",
			policy:     resilience.Policy{TripAfter: 3, Cooldown: time.Minute},
			outcomes:   []error{errTransient},
			calls:      2,
			wantProbed: true,
			wantErr:    errTransient,
		},
		{
			name:       "ignored_errors_do_not_count",
			policy:     resilience.Policy{TripAfter: 2, Cooldown: time.Minute, Ignore: ignoreFatal},
			outcomes:   []error{errFatal},
			calls:      5,
			wantProbed: true,
			wantErr:    errFatal,
		},
		{
			name:       "a_success_resets_the_count",
			policy:     resilience.Policy{TripAfter: 2, Cooldown: time.Minute},
			outcomes:   []error{errTransient, nil, errTransient},
			calls:      3,
			wantProbed: true,
			wantErr:    errTransient,
		},
		{
			name:       "half_open_after_the_cooldown_lets_a_probe_through",
			policy:     resilience.Policy{TripAfter: 1, Cooldown: time.Minute},
			outcomes:   []error{errTransient, nil},
			calls:      1,
			cooldown:   time.Minute + time.Millisecond, // the probe goes through strictly after the cooldown
			wantProbed: true,
			wantStates: []gobreaker.State{gobreaker.StateOpen, gobreaker.StateHalfOpen, gobreaker.StateClosed},
		},
		{
			name:       "retries_count_as_one_failure",
			policy:     resilience.Policy{MaxRetries: 3, Retryable: isTransient, TripAfter: 2, Cooldown: time.Minute},
			outcomes:   []error{errTransient},
			calls:      1, // one logical call, four attempts
			wantProbed: true,
			wantErr:    errTransient,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var states []gobreaker.State
				tt.policy.OnState = func(_ string, _, to gobreaker.State) { states = append(states, to) }
				s := &script{outcomes: tt.outcomes}
				g := resilience.NewGuard[int](tt.name, tt.policy)
				for range tt.calls {
					_, _ = g.Do(t.Context(), s.call)
				}
				before := s.calls
				if tt.cooldown > 0 {
					time.Sleep(tt.cooldown)
				}
				_, err := g.Do(t.Context(), s.call)
				if probed := s.calls > before; probed != tt.wantProbed {
					t.Errorf("probe reached the call = %v, want %v", probed, tt.wantProbed)
				}
				if tt.wantErr == nil && err != nil {
					t.Errorf("probe error = %v, want nil", err)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("probe error = %v, want %v", err, tt.wantErr)
				}
				if tt.wantStates != nil && !equalStates(states, tt.wantStates) {
					t.Errorf("state changes = %v, want %v", states, tt.wantStates)
				}
			})
		})
	}
}

func equalStates(a, b []gobreaker.State) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
