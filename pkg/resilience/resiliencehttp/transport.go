// Package resiliencehttp is a resilience.Guard as an http.RoundTripper, so
// any http.Client gets a breaker and a bounded retry.
package resiliencehttp

import (
	"context"
	"errors"
	"net/http"

	"docviewer/pkg/resilience"
)

// ErrFailedResponse marks an attempt whose response counted as a failure. It
// never reaches the caller: after the last attempt the response itself is returned.
var ErrFailedResponse = errors.New("failed response")

// NewTransport puts requests through a breaker and a bounded retry. failed
// decides which responses count as failures (e.g. 5xx and 429). Only GET and
// HEAD are retried, so a retry never repeats a change.
func NewTransport(next http.RoundTripper, name string, p resilience.Policy, failed func(*http.Response) bool) http.RoundTripper {
	retryable := p.Retryable
	p.Retryable = func(err error) bool {
		return errors.Is(err, ErrFailedResponse) || (retryable != nil && retryable(err))
	}
	return &transport{next: next, failed: failed, guard: resilience.NewGuard[*http.Response](name, p)}
}

type transport struct {
	next   http.RoundTripper
	failed func(*http.Response) bool
	guard  *resilience.Guard[*http.Response]
}

// RoundTrip sends req through the guard. A failed response is closed before
// the retry; the last one, failed or not, goes to the caller unread.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return t.next.RoundTrip(req)
	}
	var last *http.Response // the latest failed response, closed if it is retried
	resp, err := t.guard.Do(req.Context(), func(ctx context.Context) (*http.Response, error) {
		if last != nil {
			last.Body.Close()
			last = nil
		}
		r, err := t.next.RoundTrip(req.Clone(ctx))
		if err == nil && t.failed(r) {
			last = r
			return r, ErrFailedResponse
		}
		return r, err
	})
	if errors.Is(err, ErrFailedResponse) {
		return resp, nil // retries spent: hand the failed response to the caller
	}
	if last != nil {
		last.Body.Close() // gave up while waiting to retry
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}
