package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"docviewer/internal/aggregator"
	"docviewer/internal/config"
	"docviewer/internal/telemetry"
	"docviewer/pkg/otelsetup"
)

const vin = "1M8GDM9AXKP042788"

func TestPIIPolicyFields(t *testing.T) {
	r := telemetry.NewRedactor()
	tests := []struct {
		name     string
		key      string
		value    string
		want     string
		wantKeep bool
	}{
		{name: "vin_is_masked_to_its_last_4", key: "vin", value: vin, want: "*************2788", wantKeep: true},
		{name: "path_is_dropped", key: "path", value: "/v1/vehicles/" + vin + "/documents", wantKeep: false},
		{name: "url_is_dropped", key: "url", value: "http://sales/api/v1/vehicles/" + vin, wantKeep: false},
		{name: "url_path_is_dropped", key: "url.path", value: "/x", wantKeep: false},
		{name: "url_full_is_dropped", key: "url.full", value: "http://x", wantKeep: false},
		{name: "query_is_dropped", key: "query", value: "cursor=abc", wantKeep: false},
		{name: "url_query_is_dropped", key: "url.query", value: "cursor=abc", wantKeep: false},
		{name: "cursor_is_dropped", key: "cursor", value: "eyJ2IjoxfQ", wantKeep: false},
		{name: "title_is_dropped", key: "title", value: "Purchase agreement for J. Doe", wantKeep: false},
		{name: "summary_is_dropped", key: "summary", value: "x", wantKeep: false},
		{name: "notes_is_dropped", key: "notes", value: "x", wantKeep: false},
		{name: "work_notes_is_dropped", key: "work_notes", value: "x", wantKeep: false},
		{name: "body_is_dropped", key: "body", value: "x", wantKeep: false},
		{name: "route_template_is_kept", key: "route", value: "/v1/vehicles/:vin/documents", want: "/v1/vehicles/:vin/documents", wantKeep: true},
		{name: "source_is_kept", key: "source", value: "sales", want: "sales", wantKeep: true},
		{name: "a_vin_inside_another_value_is_masked", key: "err", value: `Get "http://sales/api/v1/vehicles/` + vin + `/documents": refused`, want: `Get "http://sales/api/v1/vehicles/*************2788/documents": refused`, wantKeep: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, keep := r.Field(tt.key, tt.value)
			if got != tt.want || keep != tt.wantKeep {
				t.Errorf("Field(%q) = %q, %v; want %q, %v", tt.key, got, keep, tt.want, tt.wantKeep)
			}
		})
	}
}

func TestPIIPolicyText(t *testing.T) {
	r := telemetry.NewRedactor()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "vin_shaped_token_is_masked", in: "lookup " + vin + " failed", want: "lookup *************2788 failed"},
		{name: "lower_case_vin_is_masked_too", in: strings.ToLower(vin), want: "*************2788"},
		{name: "sixteen_characters_are_left_alone", in: "1M8GDM9AXKP04278", want: "1M8GDM9AXKP04278"},
		{name: "eighteen_characters_are_left_alone", in: "1M8GDM9AXKP0427888", want: "1M8GDM9AXKP0427888"},
		{name: "token_with_a_forbidden_letter_is_left_alone", in: "1M8GDM9AXKP04278O", want: "1M8GDM9AXKP04278O"},
		{name: "over_redaction_of_a_vin_shaped_word_is_accepted", in: "ABCDEFGHJKLMNPRST", want: "*************PRST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Text(tt.in); got != tt.want {
				t.Errorf("Text(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// harness is a Telemetry over in-memory exporters and a stdout buffer.
type harness struct {
	tel     *telemetry.Telemetry
	out     *bytes.Buffer
	spans   *tracetest.InMemoryExporter
	metrics *sdkmetric.ManualReader
}

func newHarness(t *testing.T, cfg config.Telemetry) *harness {
	t.Helper()
	h := &harness{out: &bytes.Buffer{}, spans: tracetest.NewInMemoryExporter(), metrics: sdkmetric.NewManualReader()}
	tel, err := telemetry.New(t.Context(), cfg, otelsetup.Options{
		ServiceName: "docviewer", Version: "test", LogWriter: h.out,
		Exporters: &otelsetup.Exporters{Traces: keepOnShutdown{h.spans}, Metrics: h.metrics},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	h.tel = tel
	return h
}

func (h *harness) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(h.out.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestRedactSwitch(t *testing.T) {
	tests := []struct {
		name        string
		enabled     bool
		wantVIN     string // the logged vin attribute
		wantSpanVIN string // the exported span's vin attribute
		wantWarning bool   // "redaction disabled" logged at start
	}{
		{name: "enabled_redacts_logs_and_spans", enabled: true, wantVIN: "*************2788", wantSpanVIN: "*************2788"},
		{name: "disabled_logs_a_warning_and_wraps_nothing", enabled: false, wantVIN: vin, wantSpanVIN: vin, wantWarning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, config.Telemetry{LogLevel: "info", Redact: config.Redact{Enabled: tt.enabled}})
			h.tel.Logger().Info("lookup", "vin", vin)
			_, span := h.tel.TracerProvider().Tracer("t").Start(t.Context(), "lookup")
			span.SetAttributes(attribute.String("vin", vin))
			span.End()
			if err := h.tel.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			recs := h.records(t)
			var warned bool
			var gotVIN string
			for _, r := range recs {
				if r["level"] == "WARN" && strings.Contains(r["msg"].(string), "redaction") {
					warned = true
				}
				if r["msg"] == "lookup" {
					gotVIN, _ = r["vin"].(string)
				}
			}
			if warned != tt.wantWarning {
				t.Errorf("warning logged = %v, want %v (records %v)", warned, tt.wantWarning, recs)
			}
			if gotVIN != tt.wantVIN {
				t.Errorf("logged vin = %q, want %q", gotVIN, tt.wantVIN)
			}
			spans := h.spans.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("spans = %d, want 1", len(spans))
			}
			var spanVIN string
			for _, a := range spans[0].Attributes {
				if a.Key == "vin" {
					spanVIN = a.Value.AsString()
				}
			}
			if spanVIN != tt.wantSpanVIN {
				t.Errorf("span vin = %q, want %q", spanVIN, tt.wantSpanVIN)
			}
		})
	}
}

func TestLogLevel(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		wantMsg []string
	}{
		{name: "info_drops_debug", level: "info", wantMsg: []string{"i", "w"}},
		{name: "debug_keeps_everything", level: "debug", wantMsg: []string{"d", "i", "w"}},
		{name: "error_keeps_errors_only", level: "error", wantMsg: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, config.Telemetry{LogLevel: tt.level, Redact: config.Redact{Enabled: true}})
			h.tel.Logger().Debug("d")
			h.tel.Logger().Info("i")
			h.tel.Logger().Warn("w")
			var got []string
			for _, r := range h.records(t) {
				got = append(got, r["msg"].(string))
			}
			if strings.Join(got, ",") != strings.Join(tt.wantMsg, ",") {
				t.Errorf("messages = %v, want %v", got, tt.wantMsg)
			}
		})
	}
}

// collect returns the metrics by name.
func (h *harness) collect(t *testing.T) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.metrics.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func TestMetrics(t *testing.T) {
	tests := []struct {
		name     string
		record   func(ctx context.Context, m *telemetry.Metrics)
		wantName string
		wantUnit string
		wantAttr []attribute.KeyValue // on the one data point
		wantSum  float64              // histogram sum or counter/gauge value
	}{
		{
			name: "http_request_duration",
			record: func(ctx context.Context, m *telemetry.Metrics) {
				m.HTTPRequest(ctx, "/v1/vehicles/:vin/documents", 200, 250*time.Millisecond)
			},
			wantName: "docviewer.http.request.duration", wantUnit: "s",
			wantAttr: []attribute.KeyValue{attribute.String("route", "/v1/vehicles/:vin/documents"), attribute.Int("code", 200)},
			wantSum:  0.25,
		},
		{
			name: "source_request_duration",
			record: func(ctx context.Context, m *telemetry.Metrics) {
				m.SourceCall(ctx, "sales", aggregator.StatusTimeout, 2*time.Second)
			},
			wantName: "docviewer.source.request.duration", wantUnit: "s",
			wantAttr: []attribute.KeyValue{attribute.String("source", "sales"), attribute.String("outcome", "timeout")},
			wantSum:  2,
		},
		{
			name: "source_results_total",
			record: func(ctx context.Context, m *telemetry.Metrics) {
				m.SourceCall(ctx, "service", aggregator.StatusOK, time.Second)
			},
			wantName: "docviewer.source.results", wantUnit: "{result}",
			wantAttr: []attribute.KeyValue{attribute.String("source", "service"), attribute.String("status", "ok")},
			wantSum:  1,
		},
		{
			name: "breaker_state_gauge",
			record: func(ctx context.Context, m *telemetry.Metrics) {
				m.BreakerState("sales", gobreaker.StateClosed, gobreaker.StateOpen)
			},
			wantName: "docviewer.breaker.state", wantUnit: "{state}",
			wantAttr: []attribute.KeyValue{attribute.String("source", "sales")},
			wantSum:  2,
		},
		{
			name:     "coalesced_requests_total",
			record:   func(ctx context.Context, m *telemetry.Metrics) { m.Coalesced(ctx); m.Coalesced(ctx) },
			wantName: "docviewer.coalesced.requests", wantUnit: "{request}",
			wantAttr: nil,
			wantSum:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, config.Telemetry{LogLevel: "info", Redact: config.Redact{Enabled: true}})
			tt.record(t.Context(), h.tel.Metrics())
			m, ok := h.collect(t)[tt.wantName]
			if !ok {
				t.Fatalf("metric %s not recorded", tt.wantName)
			}
			if m.Unit != tt.wantUnit {
				t.Errorf("unit = %q, want %q", m.Unit, tt.wantUnit)
			}
			attrs, sum := onePoint(t, m)
			if !attrs.Equals(ptr(attribute.NewSet(tt.wantAttr...))) {
				t.Errorf("attributes = %v, want %v", attrs.ToSlice(), tt.wantAttr)
			}
			if sum != tt.wantSum {
				t.Errorf("value = %v, want %v", sum, tt.wantSum)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// onePoint returns the attributes and value of a metric's single data point.
func onePoint(t *testing.T, m metricdata.Metrics) (attribute.Set, float64) {
	t.Helper()
	switch d := m.Data.(type) {
	case metricdata.Histogram[float64]:
		if len(d.DataPoints) != 1 {
			t.Fatalf("%s has %d points, want 1", m.Name, len(d.DataPoints))
		}
		return d.DataPoints[0].Attributes, d.DataPoints[0].Sum
	case metricdata.Sum[int64]:
		if len(d.DataPoints) != 1 {
			t.Fatalf("%s has %d points, want 1", m.Name, len(d.DataPoints))
		}
		return d.DataPoints[0].Attributes, float64(d.DataPoints[0].Value)
	case metricdata.Gauge[int64]:
		if len(d.DataPoints) != 1 {
			t.Fatalf("%s has %d points, want 1", m.Name, len(d.DataPoints))
		}
		return d.DataPoints[0].Attributes, float64(d.DataPoints[0].Value)
	}
	t.Fatalf("%s has unexpected data %T", m.Name, m.Data)
	return attribute.Set{}, 0
}

func TestBreakerStateValues(t *testing.T) {
	tests := []struct {
		name  string
		state gobreaker.State
		want  int64
	}{
		{name: "closed_is_0", state: gobreaker.StateClosed, want: 0},
		{name: "half_open_is_1", state: gobreaker.StateHalfOpen, want: 1},
		{name: "open_is_2", state: gobreaker.StateOpen, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, config.Telemetry{LogLevel: "info", Redact: config.Redact{Enabled: true}})
			h.tel.Metrics().BreakerState("sales", gobreaker.StateClosed, tt.state)
			_, got := onePoint(t, h.collect(t)["docviewer.breaker.state"])
			if int64(got) != tt.want {
				t.Errorf("gauge = %v, want %d", got, tt.want)
			}
		})
	}
}

func TestInvalidLogLevel(t *testing.T) {
	tests := []struct {
		name  string
		level string
	}{
		{name: "unknown_word", level: "verbose"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := telemetry.New(t.Context(), config.Telemetry{LogLevel: tt.level}, otelsetup.Options{LogWriter: &bytes.Buffer{}, Exporters: &otelsetup.Exporters{Traces: tracetest.NewInMemoryExporter(), Metrics: sdkmetric.NewManualReader()}})
			if err == nil {
				t.Fatal("New() error = nil, want an error for an unknown level")
			}
		})
	}
}

// keepOnShutdown stops tracetest's exporter from clearing its spans when the
// provider shuts down, so a test can read what was flushed.
type keepOnShutdown struct{ sdktrace.SpanExporter }

func (keepOnShutdown) Shutdown(context.Context) error { return nil }
