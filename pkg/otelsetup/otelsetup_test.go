package otelsetup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"docviewer/pkg/otelsetup"
)

// inMemory is one set of in-memory exporters, so a test can see what left
// the SDK.
type inMemory struct {
	traces  *tracetest.InMemoryExporter
	metrics *sdkmetric.ManualReader
	logs    *memLogs
}

func newInMemory() (*inMemory, *otelsetup.Exporters) {
	m := &inMemory{traces: tracetest.NewInMemoryExporter(), metrics: sdkmetric.NewManualReader(), logs: &memLogs{}}
	return m, &otelsetup.Exporters{Traces: keepOnShutdown{m.traces}, Metrics: m.metrics, Logs: m.logs}
}

// counting wraps the root slog handler and counts the records it sees.
type counting struct {
	slog.Handler
	n *atomic.Int32
}

func (c counting) Handle(ctx context.Context, r slog.Record) error {
	c.n.Add(1)
	return c.Handler.Handle(ctx, r)
}

func TestSetup(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{name: "spans_arrive_after_shutdown", run: func(t *testing.T) {
			mem, exps := newInMemory()
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{ServiceName: "svc", Version: "test", LogWriter: &bytes.Buffer{}, Exporters: exps})
			if err != nil {
				t.Fatal(err)
			}
			_, span := tel.TracerProvider().Tracer("t").Start(t.Context(), "work")
			span.End()
			if err := tel.Shutdown(t.Context()); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
			spans := mem.traces.GetSpans()
			if len(spans) != 1 || spans[0].Name != "work" {
				t.Errorf("spans = %+v, want one named work", spans)
			}
			if got := spans[0].Resource.String(); !strings.Contains(got, "service.name=svc") || !strings.Contains(got, "service.version=test") {
				t.Errorf("resource = %s, want service.name and service.version", got)
			}
		}},
		{name: "metrics_arrive_on_collect", run: func(t *testing.T) {
			mem, exps := newInMemory()
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{ServiceName: "svc", LogWriter: &bytes.Buffer{}, Exporters: exps})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
			c, err := tel.MeterProvider().Meter("m").Int64Counter("hits")
			if err != nil {
				t.Fatal(err)
			}
			c.Add(t.Context(), 3)
			var rm metricdata.ResourceMetrics
			if err := mem.metrics.Collect(t.Context(), &rm); err != nil {
				t.Fatal(err)
			}
			if len(rm.ScopeMetrics) != 1 || len(rm.ScopeMetrics[0].Metrics) != 1 || rm.ScopeMetrics[0].Metrics[0].Name != "hits" {
				t.Errorf("metrics = %+v, want one named hits", rm.ScopeMetrics)
			}
		}},
		{name: "logs_go_to_stdout_json_and_to_the_otel_bridge", run: func(t *testing.T) {
			mem, exps := newInMemory()
			var out bytes.Buffer
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{ServiceName: "svc", LogWriter: &out, Exporters: exps})
			if err != nil {
				t.Fatal(err)
			}
			tel.Logger().Info("hello", "n", 1)
			if err := tel.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			var rec map[string]any
			if err := json.Unmarshal(out.Bytes(), &rec); err != nil || rec["msg"] != "hello" || rec["n"] != float64(1) {
				t.Errorf("stdout = %q, want a JSON record with msg hello and n 1 (%v)", out.String(), err)
			}
			if n := countRecords(mem.logs); n != 1 {
				t.Errorf("otel log records = %d, want 1", n)
			}
		}},
		{name: "log_level_filters_below_it", run: func(t *testing.T) {
			mem, exps := newInMemory()
			var out bytes.Buffer
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{ServiceName: "svc", LogLevel: slog.LevelWarn, LogWriter: &out, Exporters: exps})
			if err != nil {
				t.Fatal(err)
			}
			tel.Logger().Info("quiet")
			tel.Logger().Warn("loud")
			_ = tel.Shutdown(t.Context())
			if out.Len() == 0 || strings.Contains(out.String(), "quiet") || !strings.Contains(out.String(), "loud") {
				t.Errorf("stdout = %q, want only the warning", out.String())
			}
			if n := countRecords(mem.logs); n != 1 {
				t.Errorf("otel log records = %d, want 1", n)
			}
		}},
		{name: "wrap_log_sees_every_record_before_both_sinks", run: func(t *testing.T) {
			mem, exps := newInMemory()
			var seen atomic.Int32
			var out bytes.Buffer
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{
				ServiceName: "svc", LogWriter: &out, Exporters: exps,
				WrapLog: func(next slog.Handler) slog.Handler { return counting{Handler: next, n: &seen} },
			})
			if err != nil {
				t.Fatal(err)
			}
			tel.Logger().Info("one")
			tel.Logger().With("k", "v").Info("two")
			_ = tel.Shutdown(t.Context())
			if seen.Load() != 2 {
				t.Errorf("wrapper saw %d records, want 2", seen.Load())
			}
			if n := countRecords(mem.logs); n != 2 || strings.Count(out.String(), "\n") != 2 {
				t.Errorf("sinks got otel %d, stdout %d lines; want 2 and 2", n, strings.Count(out.String(), "\n"))
			}
		}},
		{name: "wrap_spans_wraps_the_exporter", run: func(t *testing.T) {
			mem, exps := newInMemory()
			var wrapped atomic.Int32
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{
				ServiceName: "svc", LogWriter: &bytes.Buffer{}, Exporters: exps,
				WrapSpans: func(next sdktrace.SpanExporter) sdktrace.SpanExporter {
					return countingExporter{SpanExporter: next, n: &wrapped}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, span := tel.TracerProvider().Tracer("t").Start(t.Context(), "work")
			span.End()
			_ = tel.Shutdown(t.Context())
			if wrapped.Load() != 1 || len(mem.traces.GetSpans()) != 1 {
				t.Errorf("wrapper exported %d spans, memory has %d; want 1 and 1", wrapped.Load(), len(mem.traces.GetSpans()))
			}
		}},
		{name: "shutdown_twice_is_harmless", run: func(t *testing.T) {
			_, exps := newInMemory()
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{ServiceName: "svc", LogWriter: &bytes.Buffer{}, Exporters: exps})
			if err != nil {
				t.Fatal(err)
			}
			if err := tel.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := tel.Shutdown(t.Context()); err != nil {
				t.Errorf("second Shutdown() error = %v", err)
			}
		}},
		{name: "default_exporters_are_otlp_and_construct_without_a_collector", run: func(t *testing.T) {
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{ServiceName: "svc", LogWriter: &bytes.Buffer{}})
			if err != nil {
				t.Fatalf("Setup() error = %v", err)
			}
			// No collector listens; shutdown must still return (the exporters
			// time out or fail to connect) rather than hang.
			ctx, cancel := context.WithTimeout(t.Context(), 5e9)
			defer cancel()
			_ = tel.Shutdown(ctx)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

type countingExporter struct {
	sdktrace.SpanExporter
	n *atomic.Int32
}

func (c countingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.n.Add(int32(len(spans))) //nolint:gosec // a handful of test spans
	return c.SpanExporter.ExportSpans(ctx, spans)
}

// memLogs is an in-memory log exporter.
type memLogs struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (m *memLogs) Export(_ context.Context, recs []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, recs...)
	return nil
}

func (m *memLogs) Shutdown(context.Context) error   { return nil }
func (m *memLogs) ForceFlush(context.Context) error { return nil }

func countRecords(m *memLogs) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

var _ sdklog.Exporter = (*memLogs)(nil)

// keepOnShutdown stops tracetest's exporter from clearing its spans when the
// provider shuts down, so a test can read what was flushed.
type keepOnShutdown struct{ sdktrace.SpanExporter }

func (keepOnShutdown) Shutdown(context.Context) error { return nil }

// WithAttrs keeps counting on the derived handler.
func (c counting) WithAttrs(attrs []slog.Attr) slog.Handler {
	return counting{Handler: c.Handler.WithAttrs(attrs), n: c.n}
}

// WithGroup keeps counting on the derived handler.
func (c counting) WithGroup(name string) slog.Handler {
	return counting{Handler: c.Handler.WithGroup(name), n: c.n}
}
