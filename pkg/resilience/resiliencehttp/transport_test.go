package resiliencehttp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sony/gobreaker/v2"

	"docviewer/pkg/resilience"
	"docviewer/pkg/resilience/resiliencehttp"
)

var errConn = errors.New("connection refused")

// body tracks whether it was closed.
type body struct {
	io.Reader
	closed atomic.Bool
}

func (b *body) Close() error { b.closed.Store(true); return nil }

// attempt is one scripted answer from the next RoundTripper.
type attempt struct {
	status int
	err    error
}

// fakeNext answers each attempt from its script (the last one repeats) and
// keeps the bodies it handed out, so the test can see which were closed.
type fakeNext struct {
	script []attempt
	calls  int
	bodies []*body
	ctxs   []context.Context
}

func (f *fakeNext) RoundTrip(req *http.Request) (*http.Response, error) {
	a := f.script[min(f.calls, len(f.script)-1)]
	f.calls++
	f.ctxs = append(f.ctxs, req.Context())
	if a.err != nil {
		return nil, a.err
	}
	b := &body{Reader: strings.NewReader("payload")}
	f.bodies = append(f.bodies, b)
	return &http.Response{StatusCode: a.status, Body: b, Request: req, Header: http.Header{}}, nil
}

func failed(r *http.Response) bool {
	return r.StatusCode >= http.StatusInternalServerError || r.StatusCode == http.StatusTooManyRequests
}

func isConn(err error) bool { return errors.Is(err, errConn) }

func policy() resilience.Policy {
	return resilience.Policy{MaxRetries: 1, Retryable: isConn, TripAfter: 10}
}

func TestTransport(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		script      []attempt
		policy      resilience.Policy
		wantCalls   int
		wantStatus  int   // 0 with wantErr
		wantErr     error // the error the caller sees
		wantClosed  []bool
		wantOpenEnd bool // the returned body is still open for the caller
	}{
		{
			name: "503_then_200_is_retried_once", method: http.MethodGet,
			script: []attempt{{status: 503}, {status: 200}}, policy: policy(),
			wantCalls: 2, wantStatus: 200, wantClosed: []bool{true, false}, wantOpenEnd: true,
		},
		{
			name: "429_counts_as_a_failure_and_is_retried", method: http.MethodGet,
			script: []attempt{{status: 429}, {status: 200}}, policy: policy(),
			wantCalls: 2, wantStatus: 200, wantClosed: []bool{true, false}, wantOpenEnd: true,
		},
		{
			name: "after_the_retries_the_failed_response_reaches_the_caller", method: http.MethodGet,
			script: []attempt{{status: 503}, {status: 502}}, policy: policy(),
			wantCalls: 2, wantStatus: 502, wantClosed: []bool{true, false}, wantOpenEnd: true,
		},
		{
			name: "4xx_is_not_a_failure_and_not_retried", method: http.MethodGet,
			script: []attempt{{status: 404}, {status: 200}}, policy: policy(),
			wantCalls: 1, wantStatus: 404, wantClosed: []bool{false}, wantOpenEnd: true,
		},
		{
			name: "connection_error_is_retried_when_the_policy_says_so", method: http.MethodGet,
			script: []attempt{{err: errConn}, {status: 200}}, policy: policy(),
			wantCalls: 2, wantStatus: 200, wantClosed: []bool{false}, wantOpenEnd: true,
		},
		{
			name: "connection_error_is_returned_when_retries_are_spent", method: http.MethodGet,
			script: []attempt{{err: errConn}}, policy: policy(),
			wantCalls: 2, wantErr: errConn,
		},
		{
			name: "context_error_is_not_retried", method: http.MethodGet,
			script: []attempt{{err: context.DeadlineExceeded}, {status: 200}}, policy: policy(),
			wantCalls: 1, wantErr: context.DeadlineExceeded,
		},
		{
			name: "post_is_never_retried", method: http.MethodPost,
			script: []attempt{{status: 503}, {status: 200}}, policy: policy(),
			wantCalls: 1, wantStatus: 503, wantClosed: []bool{false}, wantOpenEnd: true,
		},
		{
			name: "head_is_retried_like_get", method: http.MethodHead,
			script: []attempt{{status: 503}, {status: 200}}, policy: policy(),
			wantCalls: 2, wantStatus: 200, wantClosed: []bool{true, false}, wantOpenEnd: true,
		},
		{
			name: "no_retries_configured_hands_the_first_failure_back", method: http.MethodGet,
			script: []attempt{{status: 503}, {status: 200}}, policy: resilience.Policy{TripAfter: 10},
			wantCalls: 1, wantStatus: 503, wantClosed: []bool{false}, wantOpenEnd: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := &fakeNext{script: tt.script}
			rt := resiliencehttp.NewTransport(next, tt.name, tt.policy, failed)
			req, err := http.NewRequestWithContext(t.Context(), tt.method, "http://upstream/x", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := rt.RoundTrip(req)
			if next.calls != tt.wantCalls {
				t.Errorf("attempts = %d, want %d", next.calls, tt.wantCalls)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("RoundTrip() error = %v, want %v", err, tt.wantErr)
				}
				if resp != nil {
					t.Error("RoundTrip() returned a response with the error")
				}
				return
			}
			if err != nil {
				t.Fatalf("RoundTrip() error = %v", err)
			}
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			for i, want := range tt.wantClosed {
				if got := next.bodies[i].closed.Load(); got != want {
					t.Errorf("body %d closed = %v, want %v", i, got, want)
				}
			}
			if tt.wantOpenEnd {
				if b, err := io.ReadAll(resp.Body); err != nil || string(b) != "payload" {
					t.Errorf("returned body = %q, %v; want it readable", b, err)
				}
				resp.Body.Close()
			}
			for i, ctx := range next.ctxs {
				if ctx != req.Context() {
					t.Errorf("attempt %d did not carry the request's context", i)
				}
			}
		})
	}
}

func TestTransportBreaker(t *testing.T) {
	tests := []struct {
		name       string
		script     []attempt
		policy     resilience.Policy
		calls      int
		wantCalls  int // attempts that reached next over all calls
		wantLast   error
		wantStatus int
	}{
		{
			name:   "an_open_breaker_fails_fast",
			script: []attempt{{status: 503}}, policy: resilience.Policy{TripAfter: 2},
			calls: 3, wantCalls: 2, wantLast: gobreaker.ErrOpenState,
		},
		{
			name:   "4xx_responses_never_open_it",
			script: []attempt{{status: 403}}, policy: resilience.Policy{TripAfter: 2},
			calls: 5, wantCalls: 5, wantStatus: 403,
		},
		{
			name:   "an_ignored_error_never_opens_it",
			script: []attempt{{err: context.Canceled}}, policy: resilience.Policy{TripAfter: 2, Ignore: func(err error) bool { return errors.Is(err, context.Canceled) }},
			calls: 5, wantCalls: 5, wantLast: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := &fakeNext{script: tt.script}
			rt := resiliencehttp.NewTransport(next, tt.name, tt.policy, failed)
			var resp *http.Response
			var err error
			for range tt.calls {
				req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://upstream/x", nil)
				if reqErr != nil {
					t.Fatal(reqErr)
				}
				resp, err = rt.RoundTrip(req)
				if resp != nil {
					resp.Body.Close()
				}
			}
			if next.calls != tt.wantCalls {
				t.Errorf("attempts = %d, want %d", next.calls, tt.wantCalls)
			}
			if tt.wantLast != nil && !errors.Is(err, tt.wantLast) {
				t.Errorf("last error = %v, want %v", err, tt.wantLast)
			}
			if tt.wantStatus != 0 && (err != nil || resp.StatusCode != tt.wantStatus) {
				t.Errorf("last = %v, %v; want status %d", resp, err, tt.wantStatus)
			}
		})
	}
}

// TestTransportAgainstAServer: the transport inside a real http.Client,
// against an httptest server that fails once.
func TestTransportAgainstAServer(t *testing.T) {
	tests := []struct {
		name       string
		statuses   []int
		wantStatus int
		wantHits   int32
	}{
		{name: "503_then_200", statuses: []int{503, 200}, wantStatus: 200, wantHits: 2},
		{name: "200_first_time", statuses: []int{200}, wantStatus: 200, wantHits: 1},
		{name: "two_503s_reach_the_caller", statuses: []int{503, 503}, wantStatus: 503, wantHits: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := int(hits.Add(1)) - 1
				w.WriteHeader(tt.statuses[min(n, len(tt.statuses)-1)])
				_, _ = w.Write([]byte("hello"))
			}))
			t.Cleanup(srv.Close)
			client := &http.Client{Transport: resiliencehttp.NewTransport(http.DefaultTransport.(*http.Transport).Clone(), tt.name, policy(), failed)}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus || hits.Load() != tt.wantHits {
				t.Errorf("status %d after %d hits, want %d after %d", resp.StatusCode, hits.Load(), tt.wantStatus, tt.wantHits)
			}
			if b, _ := io.ReadAll(resp.Body); string(b) != "hello" {
				t.Errorf("body = %q", b)
			}
		})
	}
}
