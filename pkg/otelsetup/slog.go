package otelsetup

import (
	"context"
	"errors"
	"log/slog"
)

// fanout sends each record to every handler that wants it, below one level
// for all of them, so the level is decided once at the root.
type fanout struct {
	level    slog.Leveler
	handlers []slog.Handler
}

func newFanout(level slog.Leveler, handlers ...slog.Handler) *fanout {
	return &fanout{level: level, handlers: handlers}
}

// Enabled applies the root level.
func (f *fanout) Enabled(_ context.Context, l slog.Level) bool { return l >= f.level.Level() }

// Handle passes a copy of r to every handler enabled for its level.
func (f *fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f.handlers {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

// WithAttrs applies the attributes to every handler.
func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return &fanout{level: f.level, handlers: hs}
}

// WithGroup applies the group to every handler.
func (f *fanout) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		hs[i] = h.WithGroup(name)
	}
	return &fanout{level: f.level, handlers: hs}
}
