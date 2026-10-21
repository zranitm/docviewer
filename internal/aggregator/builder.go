package aggregator

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/model"
)

// Builder assembles an Aggregator one source at a time, so wiring adds
// sources in a loop instead of filling a constructor by hand.
type Builder struct {
	cfg      Config
	log      *slog.Logger
	observer Observer
	sources  []source.Source
	timeouts map[model.Source]time.Duration
	err      error
}

// NewBuilder starts an empty Builder.
func NewBuilder(cfg Config, log *slog.Logger) *Builder {
	return &Builder{cfg: cfg, log: log, observer: nopObserver{}, timeouts: map[model.Source]time.Duration{}}
}

// Observe installs the Observer that sees source calls and coalesced requests.
func (b *Builder) Observe(o Observer) *Builder {
	b.observer = o
	return b
}

// AddSource registers src with its own deadline. A duplicate name or a
// non-positive timeout is reported by Build; the first error wins.
func (b *Builder) AddSource(src source.Source, timeout time.Duration) *Builder {
	_, dup := b.timeouts[src.Name()]
	switch {
	case b.err != nil:
	case dup:
		b.err = fmt.Errorf("source %q added twice", src.Name())
	case timeout <= 0:
		b.err = fmt.Errorf("source %q: timeout must be positive", src.Name())
	default:
		b.sources = append(b.sources, src)
		b.timeouts[src.Name()] = timeout
	}
	return b
}

// Build returns the Aggregator, with sources in name order so responses list
// them the same way every time, whatever order they were added in.
func (b *Builder) Build() (*Aggregator, error) {
	if b.err != nil {
		return nil, b.err
	}
	if len(b.sources) == 0 {
		return nil, errors.New("aggregator: no sources")
	}
	slices.SortFunc(b.sources, func(x, y source.Source) int { return cmp.Compare(x.Name(), y.Name()) })
	return &Aggregator{sources: b.sources, timeouts: b.timeouts, cfg: b.cfg, log: b.log, observer: b.observer}, nil
}
