package telemetry

import (
	"context"
	"strings"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"docviewer/internal/aggregator"
	"docviewer/internal/model"
)

// Instrument names (HLD section 12). In Prometheus they appear with the unit
// and counter suffixes: docviewer_http_request_duration_seconds,
// docviewer_source_results_total, and so on.
const (
	meterName = "docviewer"

	metricHTTPDuration   = "docviewer.http.request.duration"
	metricSourceDuration = "docviewer.source.request.duration"
	metricSourceResults  = "docviewer.source.results"
	metricBreakerState   = "docviewer.breaker.state"
	metricCoalesced      = "docviewer.coalesced.requests"

	attrRoute   = "route"
	attrCode    = "code"
	attrSource  = "source"
	attrOutcome = "outcome"
	attrStatus  = "status"
)

// breakerStates are the gauge values: 0 closed, 1 half-open, 2 open.
var breakerStates = map[gobreaker.State]int64{
	gobreaker.StateClosed:   0,
	gobreaker.StateHalfOpen: 1,
	gobreaker.StateOpen:     2,
}

// latencyBuckets suit a 3s request deadline.
var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5}

// Metrics are the docviewer_* instruments. It implements
// aggregator.Observer and provides the breaker's OnState hook.
type Metrics struct {
	httpDuration   metric.Float64Histogram
	sourceDuration metric.Float64Histogram
	sourceResults  metric.Int64Counter
	breakerState   metric.Int64Gauge
	coalesced      metric.Int64Counter
}

// NewMetrics registers the instruments on mp.
func NewMetrics(mp metric.MeterProvider) (*Metrics, error) {
	meter := mp.Meter(meterName)
	var m Metrics
	var err error
	if m.httpDuration, err = meter.Float64Histogram(metricHTTPDuration,
		metric.WithUnit("s"), metric.WithDescription("HTTP request duration by route and status code"),
		metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return nil, err
	}
	if m.sourceDuration, err = meter.Float64Histogram(metricSourceDuration,
		metric.WithUnit("s"), metric.WithDescription("Upstream source call duration by source and outcome"),
		metric.WithExplicitBucketBoundaries(latencyBuckets...)); err != nil {
		return nil, err
	}
	if m.sourceResults, err = meter.Int64Counter(metricSourceResults,
		metric.WithUnit("{result}"), metric.WithDescription("Upstream source calls by source and status")); err != nil {
		return nil, err
	}
	if m.breakerState, err = meter.Int64Gauge(metricBreakerState,
		metric.WithUnit("{state}"), metric.WithDescription("Circuit breaker state by source: 0 closed, 1 half-open, 2 open")); err != nil {
		return nil, err
	}
	if m.coalesced, err = meter.Int64Counter(metricCoalesced,
		metric.WithUnit("{request}"), metric.WithDescription("Requests that joined another request's fan-out")); err != nil {
		return nil, err
	}
	return &m, nil
}

// HTTPRequest records one served request.
func (m *Metrics) HTTPRequest(ctx context.Context, route string, code int, took time.Duration) {
	m.httpDuration.Record(ctx, took.Seconds(), metric.WithAttributes(attribute.String(attrRoute, route), attribute.Int(attrCode, code)))
}

// SourceCall records one upstream call's duration and outcome
// (aggregator.Observer).
func (m *Metrics) SourceCall(ctx context.Context, src model.Source, status aggregator.Status, took time.Duration) {
	name := strings.ToLower(status.String())
	m.sourceDuration.Record(ctx, took.Seconds(), metric.WithAttributes(attribute.String(attrSource, string(src)), attribute.String(attrOutcome, name)))
	m.sourceResults.Add(ctx, 1, metric.WithAttributes(attribute.String(attrSource, string(src)), attribute.String(attrStatus, name)))
}

// Coalesced counts a request that shared another request's fan-out
// (aggregator.Observer).
func (m *Metrics) Coalesced(ctx context.Context) { m.coalesced.Add(ctx, 1) }

// BreakerState records a breaker's new state; it is the resilience
// Policy's OnState hook, so it has no context of its own.
func (m *Metrics) BreakerState(name string, _, to gobreaker.State) {
	m.breakerState.Record(context.Background(), breakerStates[to], metric.WithAttributes(attribute.String(attrSource, name)))
}
