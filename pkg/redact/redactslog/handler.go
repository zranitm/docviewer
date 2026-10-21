// Package redactslog is a redact.Redactor as an slog.Handler: it wraps the
// next handler the way HTTP middleware wraps an http.Handler and rewrites
// each record before passing it on.
package redactslog

import (
	"context"
	"log/slog"

	"docviewer/pkg/redact"
)

// Handler redacts every record, and attributes added with With, before
// passing them on.
type Handler struct {
	next slog.Handler
	red  *redact.Redactor
}

// NewHandler wraps next.
func NewHandler(next slog.Handler, red *redact.Redactor) *Handler {
	return &Handler{next: next, red: red}
}

// Enabled defers to next: redaction never changes what gets logged.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

// WithGroup passes the group on; attributes inside it are still redacted by
// their own key.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{next: h.next.WithGroup(name), red: h.red}
}

// Handle rewrites the message and every attribute of r.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.red.Text(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if a, keep := h.attr(a); keep {
			out.AddAttrs(a)
		}
		return true
	})
	return h.next.Handle(ctx, out)
}

// WithAttrs must redact too: attributes added with logger.With(...) never
// pass through Handle's record.
func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	kept := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		if a, keep := h.attr(a); keep {
			kept = append(kept, a)
		}
	}
	return &Handler{next: h.next.WithAttrs(kept), red: h.red}
}

// attr returns the redacted attribute, or keep=false to drop it.
func (h *Handler) attr(a slog.Attr) (slog.Attr, bool) {
	a.Value = a.Value.Resolve() // a LogValuer becomes its value first
	if a.Value.Kind() == slog.KindGroup {
		var kept []slog.Attr
		for _, g := range a.Value.Group() {
			if g, keep := h.attr(g); keep {
				kept = append(kept, g)
			}
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(kept...)}, true
	}
	if !textual(a.Value) && h.red.Rule(a.Key).Action == redact.Keep {
		return a, true // a number, bool, duration or time can't carry text
	}
	value, keep := h.red.Field(a.Key, a.Value.String())
	if !keep {
		return slog.Attr{}, false
	}
	return slog.String(a.Key, value), true
}

// textual reports whether v can carry free text: strings, and anything that
// isn't a plain scalar (errors, Stringers, structs), which is rendered as text
// and scrubbed. An error's text often holds a full upstream URL, VIN included.
func textual(v slog.Value) bool {
	switch v.Kind() {
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration, slog.KindTime:
		return false
	case slog.KindString, slog.KindAny, slog.KindGroup, slog.KindLogValuer:
	}
	return true
}
