// Package otelsetup is generic OpenTelemetry setup: OTLP exporters and
// providers for traces, metrics and logs, an slog handler writing to stdout
// and to the OTel bridge, and one Shutdown. It knows nothing about the
// service that uses it.
package otelsetup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Options configures Setup.
type Options struct {
	ServiceName string
	Version     string
	LogLevel    slog.Leveler // nil means Info
	LogWriter   io.Writer    // the stdout JSON sink; nil means os.Stdout
	// WrapLog, when set, wraps the root slog handler once, before records fan
	// out to stdout and the OTel bridge. Redaction plugs in here.
	WrapLog func(next slog.Handler) slog.Handler
	// WrapSpans, when set, wraps the span exporter, so every span is seen on
	// export. Redaction plugs in here.
	WrapSpans func(next sdktrace.SpanExporter) sdktrace.SpanExporter
	// Exporters is where the three signals go. Nil means OTLP over HTTP for
	// all three, from the standard OTEL_EXPORTER_OTLP_* variables; a set value
	// injects any SDK exporter or reader (another protocol, stdout, memory),
	// and a nil field inside it discards that signal.
	Exporters *Exporters
}

// Exporters are the three signals' destinations.
type Exporters struct {
	Traces  sdktrace.SpanExporter
	Metrics sdkmetric.Reader
	Logs    sdklog.Exporter
}

// Telemetry is the running providers and the root logger.
type Telemetry struct {
	log *slog.Logger
	tp  *sdktrace.TracerProvider
	mp  *sdkmetric.MeterProvider
	lp  *sdklog.LoggerProvider

	once        sync.Once
	shutdownErr error
}

// Setup builds the providers (OTLP over HTTP, endpoint from the standard
// OTEL_* variables, unless opts.Exporters says otherwise) and the root slog
// handler, and installs the tracer and meter providers and the W3C
// propagator as the process defaults for instrumentation libraries.
func Setup(ctx context.Context, opts Options) (*Telemetry, error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(opts.ServiceName), semconv.ServiceVersion(opts.Version)))
	if err != nil {
		return nil, err
	}
	exps, err := exporters(ctx, opts.Exporters)
	if err != nil {
		return nil, err
	}

	// The service is the root of its own traces: always sample, so an inbound
	// traceparent with the sampled flag off cannot switch exporting off (its
	// trace ID is still adopted). The X-Trace-Id header relies on this.
	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.AlwaysSample())}
	if exps.Traces != nil {
		traceExp := exps.Traces
		if opts.WrapSpans != nil {
			traceExp = opts.WrapSpans(traceExp)
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(traceExp))
	}
	tp := sdktrace.NewTracerProvider(traceOpts...)

	metricOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if exps.Metrics != nil {
		metricOpts = append(metricOpts, sdkmetric.WithReader(exps.Metrics))
	}
	mp := sdkmetric.NewMeterProvider(metricOpts...)

	logOpts := []sdklog.LoggerProviderOption{sdklog.WithResource(res)}
	if exps.Logs != nil {
		logOpts = append(logOpts, sdklog.WithProcessor(sdklog.NewBatchProcessor(exps.Logs)))
	}
	lp := sdklog.NewLoggerProvider(logOpts...)

	w := opts.LogWriter
	if w == nil {
		w = os.Stdout
	}
	var level slog.Leveler = slog.LevelInfo
	if opts.LogLevel != nil {
		level = opts.LogLevel
	}
	var root slog.Handler = newFanout(level,
		slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}),
		otelslog.NewHandler(opts.ServiceName, otelslog.WithLoggerProvider(lp)),
	)
	if opts.WrapLog != nil {
		root = opts.WrapLog(root)
	}
	log := slog.New(root)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	// Export failures (no collector listening, say) are a telemetry problem,
	// not a service problem: debug level, through our own logger.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.DebugContext(context.Background(), "telemetry export failed", "err", err)
	}))
	return &Telemetry{log: log, tp: tp, mp: mp, lp: lp}, nil
}

// Logger is the root logger: JSON to the log writer and records to the OTel
// bridge, through WrapLog if set.
func (t *Telemetry) Logger() *slog.Logger { return t.log }

// TracerProvider is the SDK tracer provider.
func (t *Telemetry) TracerProvider() trace.TracerProvider { return t.tp }

// MeterProvider is the SDK meter provider.
func (t *Telemetry) MeterProvider() metric.MeterProvider { return t.mp }

// Shutdown flushes and stops all three providers. Calling it again returns
// the first call's result.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	t.once.Do(func() {
		t.shutdownErr = errors.Join(t.tp.Shutdown(ctx), t.mp.Shutdown(ctx), t.lp.Shutdown(ctx))
	})
	return t.shutdownErr
}

// exporters returns the given set, or the three OTLP over HTTP exporters,
// which read their endpoint from the standard OTEL_EXPORTER_OTLP_* variables.
func exporters(ctx context.Context, given *Exporters) (Exporters, error) {
	if given != nil {
		return *given, nil
	}
	traces, err := otlptracehttp.New(ctx)
	if err != nil {
		return Exporters{}, err
	}
	metrics, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return Exporters{}, err
	}
	logs, err := otlploghttp.New(ctx)
	if err != nil {
		return Exporters{}, err
	}
	return Exporters{Traces: traces, Metrics: sdkmetric.NewPeriodicReader(metrics), Logs: logs}, nil
}
