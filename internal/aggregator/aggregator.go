// Package aggregator pages across sources: it fans one page request out to
// every unfinished source in parallel, interleaves the answers newest first,
// cuts them to the page size and carries each source's position in an
// opaque cursor. It knows nothing about HTTP or upstream formats.
package aggregator

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/model"
)

// Status is how one source's call for this page went. The zero value is not a
// valid status, so a SourceResult that was never filled in can't pass as ok.
// Its wire names live in dto, not here.
//
//go:generate go tool stringer -type=Status -trimprefix=Status
type Status int

// The statuses, in the order they are listed in an error.
const (
	StatusOK Status = iota + 1
	StatusTimeout
	StatusError
	StatusDone        // nothing left; not called
	StatusUnavailable // permanent failure; not called again
)

// SourceResult is one source's part of a page.
type SourceResult struct {
	Source model.Source
	Status Status
	Page   source.Page // empty unless Status is ok
}

// Result is one page of the consolidated list.
type Result struct {
	VIN        model.VIN
	Entries    []model.Entry // one page, newest first
	Sources    []SourceResult
	NextCursor string // opaque token for the next page; empty on the last page
}

// Config is the aggregator's own setting; per-source deadlines come with
// Builder.AddSource.
type Config struct {
	RequestTimeout time.Duration // the whole fan-out (PRD A-07)
}

// Aggregator pages across its sources. Build one with a Builder.
type Aggregator struct {
	sources  []source.Source
	timeouts map[model.Source]time.Duration
	cfg      Config
	log      *slog.Logger
	observer Observer
	flights  singleflight.Group
}

// Observer sees what the aggregator does, for metrics: each source call's
// outcome with how long it took, and each request that joined another
// request's fan-out instead of starting its own. Builder.Observe installs one;
// without it nothing is recorded.
type Observer interface {
	SourceCall(ctx context.Context, src model.Source, status Status, took time.Duration)
	Coalesced(ctx context.Context)
}

// nopObserver records nothing.
type nopObserver struct{}

// SourceCall records nothing.
func (nopObserver) SourceCall(context.Context, model.Source, Status, time.Duration) {}

// Coalesced records nothing.
func (nopObserver) Coalesced(context.Context) {}

// ListRequest is one call to List: the list to page through, the opaque
// cursor from a previous Result (empty for the first page), and the limit.
type ListRequest struct {
	Query  Query
	Cursor string
	Limit  int // 1 to the API's maximum; the handler validates it
}

// List returns one page: from the beginning without a cursor, from where the
// cursor left off with one. It is the aggregator's only entry point, so callers
// pass opaque cursors in and out and never see paging state. Its errors are
// ErrInvalidCursor, ErrCursorOutdated, ErrSourcesUnavailable (as a
// *SourcesUnavailableError, with the page's statuses) and the caller's own
// context error.
func (a *Aggregator) List(ctx context.Context, req ListRequest) (Result, error) {
	st := a.firstPage(req.Query)
	if req.Cursor != "" {
		var err error
		if st, err = decodeCursor(req.Cursor, req.Query, a.sourceNames()); err != nil {
			return Result{}, err
		}
	}
	return a.coalesced(ctx, st, req.Limit)
}

// firstPage is the State of a first page: every source from the beginning. With
// decodeCursor checking that all sources are present, every source always has
// an entry, so a missing one can only mean a source added since.
func (a *Aggregator) firstPage(q Query) State {
	st := State{Query: q, Sources: make(map[model.Source]SourceState, len(a.sources))}
	for _, src := range a.sources {
		st.Sources[src.Name()] = SourceState{Phase: PhaseStart}
	}
	return st
}

// sourceNames lists the configured sources, in order, for decodeCursor's
// outdated check.
func (a *Aggregator) sourceNames() []model.Source {
	names := make([]model.Source, len(a.sources))
	for i, src := range a.sources {
		names[i] = src.Name()
	}
	return names
}

// coalesced returns one page for st. Identical concurrent requests (same
// query, positions and limit) share one fan-out.
func (a *Aggregator) coalesced(ctx context.Context, st State, limit int) (Result, error) {
	started := false // set by the flight function, which only the first caller runs
	ch := a.flights.DoChan(st.flightKey(limit), func() (res any, err error) {
		started = true
		// singleflight re-raises a panic from this function on a new
		// goroutine, where no handler could recover it; keep it here.
		defer func() {
			if r := recover(); r != nil {
				a.log.ErrorContext(ctx, "page fan-out panicked", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				err = fmt.Errorf("%w: %v", errPanic, r)
			}
		}()
		// Detached from the first caller's cancellation (trace values kept),
		// with its own deadline, so one client hanging up doesn't fail
		// every caller sharing this flight.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.RequestTimeout)
		defer cancel()
		return a.page(fctx, st, limit), nil
	})
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case r := <-ch:
		if !started {
			a.observer.Coalesced(ctx)
		}
		if r.Err != nil {
			return Result{}, r.Err
		}
		res := r.Val.(Result)
		if nothingAnswered(st, res.Sources) {
			failure := &SourcesUnavailableError{Sources: res.Sources}
			if failure.Retryable() || st.nothingShown() {
				// No page and no cursor (HLD section 5): the client retries with
				// the cursor it already holds, since no position moved, or
				// starts again if nothing was ever shown.
				return Result{VIN: res.VIN, Sources: res.Sources}, failure
			}
			// Every live source failed permanently after documents were shown:
			// a terminal page (nothing more, sources named, no next cursor),
			// so "load more" ends instead of failing for ever.
		}
		return res, nil
	}
}

// page fans st out to every unfinished source, waits for all of them, and
// builds the page and the next state.
func (a *Aggregator) page(ctx context.Context, st State, limit int) Result {
	results := make([]SourceResult, len(a.sources))
	var wg sync.WaitGroup
	for i, src := range a.sources {
		ss := st.Sources[src.Name()]
		switch ss.Phase {
		case PhaseDone:
			results[i] = SourceResult{Source: src.Name(), Status: StatusDone}
			continue
		case PhaseUnavailable:
			results[i] = SourceResult{Source: src.Name(), Status: StatusUnavailable}
			continue
		}
		req := st.Query.request(ss.At, limit)
		wg.Go(func() { results[i] = a.fetchOne(ctx, src, req) }) // own slot, no lock
	}
	wg.Wait()
	docs, next := cut(st, results, limit)
	res := Result{VIN: st.Query.VIN, Entries: toEntries(docs), Sources: results}
	if next != nil {
		res.NextCursor = encodeCursor(*next)
	}
	return res
}

// fetchOne calls one source within its own deadline and classifies the
// outcome. A panic in the source (adapter or mapping code) is contained here:
// it counts as a transient error for this page and the source is retried on
// the next, while the other sources' answers are kept.
func (a *Aggregator) fetchOne(ctx context.Context, src source.Source, req source.Request) (res SourceResult) {
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			a.log.ErrorContext(ctx, "source panicked", "source", src.Name(), "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			res = SourceResult{Source: src.Name(), Status: StatusError}
		}
		a.observer.SourceCall(ctx, src.Name(), res.Status, time.Since(start))
	}()
	return a.fetch(ctx, src, req)
}

// fetch is the call itself, within the source's own deadline.
func (a *Aggregator) fetch(ctx context.Context, src source.Source, req source.Request) SourceResult {
	fctx, cancel := context.WithTimeout(ctx, a.timeouts[src.Name()])
	defer cancel()
	page, err := src.FetchPage(fctx, req)
	switch {
	case err == nil:
		return SourceResult{Source: src.Name(), Status: StatusOK, Page: page}
	case errors.Is(err, source.ErrPermanent):
		a.log.ErrorContext(ctx, "source rejected the request", "source", src.Name(), "err", err)
		return SourceResult{Source: src.Name(), Status: StatusUnavailable}
	case errors.Is(err, context.DeadlineExceeded):
		a.log.WarnContext(ctx, "source timed out", "source", src.Name())
		return SourceResult{Source: src.Name(), Status: StatusTimeout}
	default:
		a.log.WarnContext(ctx, "source failed", "source", src.Name(), "err", err)
		return SourceResult{Source: src.Name(), Status: StatusError}
	}
}

// cut interleaves the sources' pages newest first, keeps the first limit
// documents, and moves each source's position to the last document it had on
// the page. Documents fetched but cut off come back next time. A transiently
// failed source keeps its old position, so the next page retries it; a
// permanently failed one is marked unavailable. The next state is nil when
// every source has finished.
func cut(st State, results []SourceResult, limit int) ([]model.Document, *State) {
	var docs []model.Document
	for _, r := range results {
		docs = append(docs, r.Page.Docs...)
	}
	// Stable sort on date, then source, only: within one source the upstream's
	// own order (including its ID tie-break) is kept, because that order is
	// what its position means.
	slices.SortStableFunc(docs, func(x, y model.Document) int {
		if c := y.IssuedAt.Compare(x.IssuedAt); c != 0 {
			return c
		}
		return cmp.Compare(x.Source, y.Source)
	})
	if len(docs) > limit {
		docs = docs[:limit]
	}

	next := State{Query: st.Query, Sources: maps.Clone(st.Sources)}
	returned := map[model.Source]int{}
	for _, d := range docs {
		returned[d.Source]++
		next.Sources[d.Source] = SourceState{Phase: PhaseAfter, At: &source.Position{IssuedAt: d.IssuedAt, ID: d.ID}}
	}
	for _, r := range results {
		switch {
		case r.Status == StatusUnavailable:
			next.Sources[r.Source] = SourceState{Phase: PhaseUnavailable}
		case r.Status != StatusOK:
			// transient failure, or already done: position unchanged
		case !r.Page.HasMore && returned[r.Source] == len(r.Page.Docs):
			next.Sources[r.Source] = SourceState{Phase: PhaseDone}
		case returned[r.Source] == 0 && r.Page.SkipTo != nil:
			next.Sources[r.Source] = SourceState{Phase: PhaseAfter, At: r.Page.SkipTo} // step past a fully malformed page
		case len(r.Page.Docs) == 0 && r.Page.HasMore:
			// Nothing usable, more promised, nowhere to skip to: the cursor could
			// never move. Ending the source beats asking for the same page forever.
			next.Sources[r.Source] = SourceState{Phase: PhaseUnavailable}
		}
	}
	for _, ss := range next.Sources {
		if ss.Phase != PhaseDone && ss.Phase != PhaseUnavailable {
			return docs, &next
		}
	}
	return docs, nil // every source finished
}

// toEntries maps each document to one entry. Today every entry has one source;
// the planned checksum index (HLD section 14) would add the other systems
// holding the same file.
func toEntries(docs []model.Document) []model.Entry {
	entries := make([]model.Entry, len(docs))
	for i, d := range docs {
		entries[i] = model.Entry{
			Type: d.Type, Title: d.Title, Summary: d.Summary, IssuedAt: d.IssuedAt,
			Sources: []model.SourceRef{{Source: d.Source, ID: d.ID, URL: d.URL}},
		}
	}
	return entries
}

// nothingAnswered is true when no source answered and at least one of the
// sources asked this page failed, transiently or permanently (PRD A-05: if
// every system fails, show an error). A source answering with nothing (404, an
// empty page) still counts as an answer. A source finished on an earlier page
// was not asked, so it neither answered nor failed: a cursor in which every
// source has finished gives an empty last page, not an error.
func nothingAnswered(st State, results []SourceResult) bool {
	failed := false
	for _, r := range results {
		if prev := st.Sources[r.Source].Phase; prev == PhaseDone || prev == PhaseUnavailable {
			continue // not asked this page
		}
		switch r.Status {
		case StatusOK:
			return false
		case StatusTimeout, StatusError, StatusUnavailable:
			failed = true
		case StatusDone:
		}
	}
	return failed
}
