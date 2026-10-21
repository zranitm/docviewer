package telemetry

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"docviewer/internal/config"
	"docviewer/pkg/otelsetup"
	"docviewer/pkg/redact/redactotel"
	"docviewer/pkg/redact/redactslog"
)

// Telemetry is the service's logger, providers and instruments.
type Telemetry struct {
	otel    *otelsetup.Telemetry
	metrics *Metrics
}

// New sets telemetry up from the service's configuration on top of base
// (service name, version, sinks). With redaction enabled, the PII policy
// wraps the root log handler once and the span exporter once, so both log
// sinks and every exported span see redacted values; when it is off nothing
// is wrapped and a warning is logged at start so it can't go unnoticed.
func New(ctx context.Context, cfg config.Telemetry, base otelsetup.Options) (*Telemetry, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("telemetry: log level: %w", err)
	}
	base.LogLevel = level
	if cfg.Redact.Enabled {
		red := NewRedactor()
		base.WrapLog = func(next slog.Handler) slog.Handler { return redactslog.NewHandler(next, red) }
		base.WrapSpans = func(next sdktrace.SpanExporter) sdktrace.SpanExporter { return redactotel.NewExporter(next, red) }
	}
	ot, err := otelsetup.Setup(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	m, err := NewMetrics(ot.MeterProvider())
	if err != nil {
		return nil, fmt.Errorf("telemetry: metrics: %w", err)
	}
	if !cfg.Redact.Enabled {
		ot.Logger().WarnContext(ctx, "PII redaction is disabled: logs and traces carry raw VINs, paths and document fields")
	}
	return &Telemetry{otel: ot, metrics: m}, nil
}

// Logger is the root logger.
func (t *Telemetry) Logger() *slog.Logger { return t.otel.Logger() }

// Metrics are the service's instruments.
func (t *Telemetry) Metrics() *Metrics { return t.metrics }

// TracerProvider is for instrumentation (otelgin, otelhttp).
func (t *Telemetry) TracerProvider() trace.TracerProvider { return t.otel.TracerProvider() }

// MeterProvider is for instrumentation (otelgin, otelhttp).
func (t *Telemetry) MeterProvider() metric.MeterProvider { return t.otel.MeterProvider() }

// Shutdown flushes spans, metrics and logs.
func (t *Telemetry) Shutdown(ctx context.Context) error { return t.otel.Shutdown(ctx) }
