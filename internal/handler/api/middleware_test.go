package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"

	"docviewer/internal/aggregator"
	"docviewer/internal/config"
	"docviewer/internal/handler/api"
)

const (
	testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testSpanID  = "00f067aa0ba902b7"
)

// TestRememberSpanHeader: the header names a trace only when that trace is
// exported, so a quoted ID always finds something. An unsampled span
// context gets no header.
func TestRememberSpanHeader(t *testing.T) {
	tests := []struct {
		name       string
		flags      trace.TraceFlags
		wantHeader string
	}{
		{name: "sampled_span_names_its_trace", flags: trace.FlagsSampled, wantHeader: testTraceID},
		{name: "unsampled_span_gets_no_header", flags: 0, wantHeader: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			e := gin.New()
			e.Use(api.RememberSpan())
			e.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			tid, _ := trace.TraceIDFromHex(testTraceID)
			sid, _ := trace.SpanIDFromHex(testSpanID)
			sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: tt.flags})
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(trace.ContextWithSpanContext(req.Context(), sc))
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if got := rec.Header().Get(api.HeaderTraceID); got != tt.wantHeader {
				t.Errorf("%s = %q, want %q", api.HeaderTraceID, got, tt.wantHeader)
			}
		})
	}
}

// TestTraceIDHeaderSampling: the service is the root of its traces, so an
// inbound traceparent with the sampled flag off must not switch exporting
// off; the trace ID is still adopted, the span is exported and the header
// names it (HLD decision D-14).
func TestTraceIDHeaderSampling(t *testing.T) {
	tests := []struct {
		name        string
		traceparent string
		wantTraceID string // "" means any valid ID
	}{
		{name: "no_traceparent_starts_an_exported_trace", traceparent: ""},
		{name: "unsampled_traceparent_is_adopted_and_still_exported", traceparent: "00-" + testTraceID + "-" + testSpanID + "-00", wantTraceID: testTraceID},
		{name: "sampled_traceparent_is_adopted", traceparent: "00-" + testTraceID + "-" + testSpanID + "-01", wantTraceID: testTraceID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tel, spans := newTelemetryWithSpans(t)
			srv := api.NewServer(config.Server{Addr: ":0", ShutdownGrace: testGrace}, &api.Dependencies{Aggregator: &fakeLister{res: aggregator.Result{VIN: vin}}, Log: tel.Logger(), Telemetry: tel})
			req := httptest.NewRequest(http.MethodGet, docsPath, nil)
			if tt.traceparent != "" {
				req.Header.Set("traceparent", tt.traceparent)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			got := rec.Header().Get(api.HeaderTraceID)
			id, err := trace.TraceIDFromHex(got)
			if err != nil || !id.IsValid() || (tt.wantTraceID != "" && got != tt.wantTraceID) {
				t.Fatalf("%s = %q, want a valid trace ID %q", api.HeaderTraceID, got, tt.wantTraceID)
			}
			flusher, ok := tel.TracerProvider().(interface{ ForceFlush(context.Context) error })
			if !ok {
				t.Fatal("tracer provider cannot flush")
			}
			if err := flusher.ForceFlush(t.Context()); err != nil {
				t.Fatal(err)
			}
			exported := false
			for _, s := range spans.GetSpans() {
				if s.SpanContext.TraceID() == id {
					exported = true
				}
			}
			if !exported {
				t.Errorf("no exported span carries trace %s: the header names a trace Tempo will never have", got)
			}
		})
	}
}
