package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"docviewer/internal/config"
	"docviewer/internal/telemetry"
	"docviewer/pkg/resilience"
	"docviewer/pkg/resilience/resiliencehttp"
)

// upstreamPolicy is the retry and breaker policy every upstream client gets
// (LLD section 5): one retry with jitter within the deadline for connection
// errors and failed responses; a client hanging up says nothing about the
// upstream, so it is ignored; context errors and an open breaker are never
// retried.
func upstreamPolicy(r config.Resilience) resilience.Policy {
	return resilience.Policy{
		MaxRetries: r.MaxRetries,
		MinBudget:  r.MinBudget,
		MaxJitter:  r.MaxJitter,
		Retryable:  isConnectionError,
		Ignore:     func(err error) bool { return errors.Is(err, context.Canceled) },
		TripAfter:  r.TripAfter,
		Cooldown:   r.Cooldown,
	}
}

// isConnectionError is a transport error that another attempt may get past:
// anything but a context error or the breaker's own errors.
func isConnectionError(err error) bool {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, gobreaker.ErrOpenState), errors.Is(err, gobreaker.ErrTooManyRequests):
		return false
	}
	return err != nil
}

// failedResponse counts 5xx and 429 as failures. Other 4xx don't count, since
// an upstream rejecting a request says nothing about its health.
func failedResponse(resp *http.Response) bool {
	return resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests
}

// upstreamHTTP is one upstream's client stack, outside in: tracing, then
// resilience with that upstream's own breaker (its state changes feed the
// breaker metric), then a pooled transport of its own. With the breaker
// outside the retry, one logical call counts as one failure.
func upstreamHTTP(name string, r config.Resilience, tel *telemetry.Telemetry) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	policy := upstreamPolicy(r)
	policy.OnState = tel.Metrics().BreakerState
	guarded := resiliencehttp.NewTransport(base, name, policy, failedResponse)
	traced := otelhttp.NewTransport(guarded, otelhttp.WithTracerProvider(tel.TracerProvider()), otelhttp.WithMeterProvider(tel.MeterProvider()))
	return &http.Client{Transport: traced}
}
