package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"docviewer/internal/config"
	"docviewer/internal/handler/api"
	"docviewer/internal/telemetry"
	"docviewer/pkg/otelsetup"
)

// newTelemetry is the service's telemetry over in-memory exporters and a
// discarded log, built through the same constructor production uses.
func newTelemetry(t *testing.T) *telemetry.Telemetry {
	t.Helper()
	tel, _ := newTelemetryWithLog(t)
	return tel
}

// newTelemetryWithLog also returns the buffer the JSON log lines go to.
func newTelemetryWithLog(t *testing.T) (*telemetry.Telemetry, *bytes.Buffer) {
	t.Helper()
	tel, out, _ := newTelemetryWithSinks(t)
	return tel, out
}

// newTelemetryWithSinks also returns the metric reader, to collect from.
func newTelemetryWithSinks(t *testing.T) (*telemetry.Telemetry, *bytes.Buffer, *sdkmetric.ManualReader) {
	t.Helper()
	out := &bytes.Buffer{}
	reader := sdkmetric.NewManualReader()
	tel, err := telemetry.New(t.Context(), config.Telemetry{LogLevel: "debug", Redact: config.Redact{Enabled: true}}, otelsetup.Options{
		ServiceName: "docviewer-test", LogWriter: out,
		Exporters: &otelsetup.Exporters{Traces: tracetest.NewInMemoryExporter(), Metrics: reader},
	})
	if err != nil {
		t.Fatalf("telemetry.New() error = %v", err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel, out, reader
}

// newDeps wires a fake aggregator with real telemetry.
func newDeps(t *testing.T, lister api.Lister) *api.Dependencies {
	t.Helper()
	tel := newTelemetry(t)
	return &api.Dependencies{Aggregator: lister, Log: tel.Logger(), Telemetry: tel}
}

// logLines decodes the JSON records written so far.
func logLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		recs = append(recs, rec)
	}
	return recs
}

// newTelemetryWithSpans also returns the span exporter, to see what was
// exported after a flush.
func newTelemetryWithSpans(t *testing.T) (*telemetry.Telemetry, *tracetest.InMemoryExporter) {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	tel, err := telemetry.New(t.Context(), config.Telemetry{LogLevel: "debug", Redact: config.Redact{Enabled: true}}, otelsetup.Options{
		ServiceName: "docviewer-test", LogWriter: &bytes.Buffer{},
		Exporters: &otelsetup.Exporters{Traces: spans, Metrics: sdkmetric.NewManualReader()},
	})
	if err != nil {
		t.Fatalf("telemetry.New() error = %v", err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel, spans
}
