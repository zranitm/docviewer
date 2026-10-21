package api_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"

	"docviewer/internal/config"
	"docviewer/internal/handler/api"
)

func TestFailedResponse(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{name: "500_fails", status: http.StatusInternalServerError, want: true},
		{name: "503_fails", status: http.StatusServiceUnavailable, want: true},
		{name: "429_fails", status: http.StatusTooManyRequests, want: true},
		{name: "200_ok", status: http.StatusOK, want: false},
		{name: "404_says_nothing_about_health", status: http.StatusNotFound, want: false},
		{name: "403_says_nothing_about_health", status: http.StatusForbidden, want: false},
		{name: "400_says_nothing_about_health", status: http.StatusBadRequest, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := api.FailedResponse(&http.Response{StatusCode: tt.status}); got != tt.want {
				t.Errorf("failedResponse(%d) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestUpstreamPolicy(t *testing.T) {
	cfg := config.Resilience{MaxRetries: 1, MinBudget: 500 * time.Millisecond, MaxJitter: 100 * time.Millisecond, TripAfter: 5, Cooldown: 30 * time.Second}
	p := api.UpstreamPolicy(cfg)
	connErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	tests := []struct {
		name          string
		err           error
		wantRetryable bool
		wantIgnored   bool
	}{
		{name: "connection_error_is_retryable", err: connErr, wantRetryable: true},
		{name: "cancelled_is_ignored_and_not_retried", err: context.Canceled, wantIgnored: true},
		{name: "deadline_is_neither", err: context.DeadlineExceeded},
		{name: "open_breaker_is_not_retried", err: gobreaker.ErrOpenState},
		{name: "half_open_limit_is_not_retried", err: gobreaker.ErrTooManyRequests},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Retryable(tt.err); got != tt.wantRetryable {
				t.Errorf("Retryable(%v) = %v, want %v", tt.err, got, tt.wantRetryable)
			}
			if got := p.Ignore(tt.err); got != tt.wantIgnored {
				t.Errorf("Ignore(%v) = %v, want %v", tt.err, got, tt.wantIgnored)
			}
		})
	}
	settings := []struct {
		name string
		got  any
		want any
	}{
		{name: "max_retries", got: p.MaxRetries, want: cfg.MaxRetries},
		{name: "min_budget", got: p.MinBudget, want: cfg.MinBudget},
		{name: "max_jitter", got: p.MaxJitter, want: cfg.MaxJitter},
		{name: "trip_after", got: p.TripAfter, want: cfg.TripAfter},
		{name: "cooldown", got: p.Cooldown, want: cfg.Cooldown},
	}
	for _, tt := range settings {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}
