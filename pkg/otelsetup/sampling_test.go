package otelsetup_test

import (
	"bytes"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"docviewer/pkg/otelsetup"
)

// TestSamplingIsAlways: the service is the root of its own traces, so a
// remote parent with the sampled flag off must not switch exporting off.
// The remote trace ID is still adopted, as W3C trace context requires.
func TestSamplingIsAlways(t *testing.T) {
	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	tests := []struct {
		name   string
		parent *trace.SpanContextConfig // nil: no parent
	}{
		{name: "no_parent_is_exported"},
		{name: "remote_unsampled_parent_is_still_exported", parent: &trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: 0, Remote: true}},
		{name: "remote_sampled_parent_is_exported", parent: &trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem, exps := newInMemory()
			tel, err := otelsetup.Setup(t.Context(), otelsetup.Options{ServiceName: "svc", LogWriter: &bytes.Buffer{}, Exporters: exps})
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if tt.parent != nil {
				ctx = trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(*tt.parent))
			}
			_, span := tel.TracerProvider().Tracer("t").Start(ctx, "work")
			if !span.SpanContext().IsSampled() {
				t.Error("span is not sampled")
			}
			span.End()
			if err := tel.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			spans := mem.traces.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("%d spans exported, want 1", len(spans))
			}
			if tt.parent != nil && spans[0].SpanContext.TraceID() != tid {
				t.Errorf("trace ID = %s, want the remote parent's %s", spans[0].SpanContext.TraceID(), tid)
			}
		})
	}
}
