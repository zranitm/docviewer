package redactotel_test

import (
	"context"
	"reflect"
	"regexp"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"docviewer/pkg/redact"
	"docviewer/pkg/redact/redactotel"
)

func newRedactor() *redact.Redactor {
	return redact.New(redact.Config{
		Rules: map[string]redact.Rule{
			"card": {Action: redact.Mask, KeepLast: 4},
			"body": {Action: redact.Drop},
		},
		Patterns:        []*regexp.Regexp{regexp.MustCompile(`\b\d{3}-\d{4}\b`)},
		PatternKeepLast: 2,
	})
}

// exported runs span inside a provider whose exporter is wrapped by the
// redactor and returns the one span that came out.
func exported(t *testing.T, span func(ctx context.Context, tr trace.Tracer)) tracetest.SpanStub {
	t.Helper()
	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(redactotel.NewExporter(mem, newRedactor())))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	span(context.Background(), tp.Tracer("test"))
	spans := mem.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	return spans[0]
}

func attrs(kvs ...attribute.KeyValue) []attribute.KeyValue { return kvs }

func TestExportSpansAttributes(t *testing.T) {
	tests := []struct {
		name string
		span func(ctx context.Context, tr trace.Tracer)
		want []attribute.KeyValue
	}{
		{
			name: "start_attributes_are_masked_and_dropped",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op", trace.WithAttributes(attribute.String("card", "4111222233334444"), attribute.String("body", "<xml>"), attribute.Int("n", 1)))
				s.End()
			},
			want: attrs(attribute.String("card", "************4444"), attribute.Int("n", 1)),
		},
		{
			name: "attributes_set_after_start_are_covered",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op")
				s.SetAttributes(attribute.String("card", "4111222233334444"))
				s.End()
			},
			want: attrs(attribute.String("card", "************4444")),
		},
		{
			name: "kept_strings_are_scrubbed_of_patterns",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op", trace.WithAttributes(attribute.String("note", "ring 555-1234")))
				s.End()
			},
			want: attrs(attribute.String("note", "ring ******34")),
		},
		{
			name: "string_slices_are_scrubbed_element_by_element",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op", trace.WithAttributes(attribute.StringSlice("notes", []string{"555-1234", "fine"})))
				s.End()
			},
			want: attrs(attribute.StringSlice("notes", []string{"******34", "fine"})),
		},
		{
			name: "non_string_values_under_a_dropped_key_are_dropped",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op", trace.WithAttributes(attribute.Int("body", 7), attribute.Bool("ok", true)))
				s.End()
			},
			want: attrs(attribute.Bool("ok", true)),
		},
		{
			name: "no_attributes",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op")
				s.End()
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exported(t, tt.span)
			if len(got.Attributes) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got.Attributes, tt.want) {
				t.Errorf("attributes = %v\nwant %v", got.Attributes, tt.want)
			}
		})
	}
}

func TestExportSpansEvents(t *testing.T) {
	tests := []struct {
		name string
		span func(ctx context.Context, tr trace.Tracer)
		want []attribute.KeyValue // the first event's attributes
	}{
		{
			name: "event_attributes_are_redacted",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op")
				s.AddEvent("paid", trace.WithAttributes(attribute.String("card", "4111222233334444"), attribute.String("body", "x")))
				s.End()
			},
			want: attrs(attribute.String("card", "************4444")),
		},
		{
			name: "recorded_error_text_is_scrubbed",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op")
				s.RecordError(errText("dial tel 555-1234 refused"))
				s.End()
			},
			want: attrs(attribute.String("exception.type", "docviewer/pkg/redact/redactotel_test.errText"), attribute.String("exception.message", "dial tel ******34 refused")),
		},
		{
			name: "status_description_is_scrubbed",
			span: func(ctx context.Context, tr trace.Tracer) {
				_, s := tr.Start(ctx, "op")
				s.SetStatus(codes.Error, "Get \"http://sales/tel/555-1234\": refused")
				s.AddEvent("x")
				s.End()
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exported(t, tt.span)
			if len(got.Events) != 1 {
				t.Fatalf("events = %d, want 1", len(got.Events))
			}
			if got.Status.Code == codes.Error && got.Status.Description != `Get "http://sales/tel/******34": refused` {
				t.Errorf("status description = %q, want it scrubbed", got.Status.Description)
			}
			if tt.want == nil {
				return
			}
			if !reflect.DeepEqual(got.Events[0].Attributes, tt.want) {
				t.Errorf("event attributes = %v\nwant %v", got.Events[0].Attributes, tt.want)
			}
		})
	}
}

type errText string

func (e errText) Error() string { return string(e) }

// TestWrappedNotCopied: everything but attributes and events reaches the
// next exporter as the SDK produced it.
func TestWrappedNotCopied(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "name_kind_and_span_context_are_kept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ctx trace.SpanContext
			got := exported(t, func(c context.Context, tr trace.Tracer) {
				_, s := tr.Start(c, "lookup", trace.WithSpanKind(trace.SpanKindClient))
				ctx = s.SpanContext()
				s.End()
			})
			if got.Name != "lookup" || got.SpanKind != trace.SpanKindClient || !got.SpanContext.Equal(ctx) {
				t.Errorf("span = %+v, want name lookup, kind client, the same span context", got)
			}
		})
	}
}

func TestShutdownDelegates(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "shutdown_reaches_the_next_exporter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := &shutdownRecorder{SpanExporter: tracetest.NewInMemoryExporter()}
			exp := redactotel.NewExporter(next, newRedactor())
			if err := exp.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
			if !next.shutdown {
				t.Error("the next exporter was not shut down")
			}
		})
	}
}

// shutdownRecorder notes whether Shutdown reached it.
type shutdownRecorder struct {
	sdktrace.SpanExporter
	shutdown bool
}

func (s *shutdownRecorder) Shutdown(ctx context.Context) error {
	s.shutdown = true
	return s.SpanExporter.Shutdown(ctx)
}
