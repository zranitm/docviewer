// Package redactotel is a redact.Redactor as an OpenTelemetry span
// exporter: it redacts the attributes of every span, and of its events,
// just before export, so attributes added after a span started are covered.
package redactotel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"docviewer/pkg/redact"
)

type exporter struct {
	next sdktrace.SpanExporter
	red  *redact.Redactor
}

// NewExporter wraps next. Spans are wrapped, not copied: only Attributes,
// Events and the Status description are overridden.
func NewExporter(next sdktrace.SpanExporter, r *redact.Redactor) sdktrace.SpanExporter {
	return &exporter{next: next, red: r}
}

// ExportSpans hands redacted views of spans to the next exporter.
func (e *exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		status := s.Status()
		status.Description = e.red.Text(status.Description)
		out[i] = &span{ReadOnlySpan: s, attrs: e.attrs(s.Attributes()), events: e.events(s.Events()), status: status}
	}
	return e.next.ExportSpans(ctx, out)
}

// Shutdown shuts the next exporter down.
func (e *exporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }

// attrs applies the rules by key: strings are redacted or scrubbed, string
// slices element by element, and any value under a Drop rule is removed.
func (e *exporter) attrs(in []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(in))
	for _, kv := range in {
		key := string(kv.Key)
		switch kv.Value.Type() {
		case attribute.STRING:
			if v, keep := e.red.Field(key, kv.Value.AsString()); keep {
				out = append(out, attribute.String(key, v))
			}
		case attribute.STRINGSLICE:
			if e.red.Rule(key).Action == redact.Drop {
				continue
			}
			vals := kv.Value.AsStringSlice()
			for i := range vals {
				vals[i], _ = e.red.Field(key, vals[i])
			}
			out = append(out, attribute.StringSlice(key, vals))
		default:
			if e.red.Rule(key).Action == redact.Drop {
				continue
			}
			out = append(out, kv)
		}
	}
	return out
}

func (e *exporter) events(in []sdktrace.Event) []sdktrace.Event {
	out := make([]sdktrace.Event, len(in))
	for i, ev := range in {
		ev.Attributes = e.attrs(ev.Attributes)
		out[i] = ev
	}
	return out
}

// span is a ReadOnlySpan whose attributes, events and status description
// were redacted; everything else comes from the wrapped span.
type span struct {
	sdktrace.ReadOnlySpan
	attrs  []attribute.KeyValue
	events []sdktrace.Event
	status sdktrace.Status
}

// Status is the status with its description scrubbed: an error description
// often carries a full upstream URL.
func (s *span) Status() sdktrace.Status { return s.status }

// Attributes are the redacted attributes.
func (s *span) Attributes() []attribute.KeyValue { return s.attrs }

// Events are the events with redacted attributes.
func (s *span) Events() []sdktrace.Event { return s.events }
