package aggregator

import (
	"errors"
	"slices"
)

// The service layer's sentinel errors. The handler maps these, with model's
// and dto's, to HTTP.
var (
	ErrSourcesUnavailable = errors.New("no source answered")
	ErrInvalidCursor      = errors.New("invalid cursor")
	ErrCursorOutdated     = errors.New("cursor outdated") // a source was added since it was issued
)

// errPanic marks a page whose fan-out panicked; the handler maps it, like
// any unknown error, to a fixed 500.
var errPanic = errors.New("aggregator: panic during page fan-out")

// SourcesUnavailableError carries the per-source statuses behind
// ErrSourcesUnavailable, so the 502 response can name them.
type SourcesUnavailableError struct{ Sources []SourceResult }

func (e *SourcesUnavailableError) Error() string { return ErrSourcesUnavailable.Error() }

// Unwrap makes errors.Is(err, ErrSourcesUnavailable) true.
func (e *SourcesUnavailableError) Unwrap() error { return ErrSourcesUnavailable }

// Retryable reports whether any source failed transiently, so asking again
// with the same cursor may succeed.
func (e *SourcesUnavailableError) Retryable() bool {
	return slices.ContainsFunc(e.Sources, func(r SourceResult) bool {
		return r.Status == StatusTimeout || r.Status == StatusError
	})
}
