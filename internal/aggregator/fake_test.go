package aggregator_test

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/model"
)

// fakeSource pages through an in-memory list that is already in the
// source's own order (newest first). It records every request, can delay,
// and can fail or override a given call, so the aggregator's behaviour is
// observable without a network.
type fakeSource struct {
	name  model.Source
	docs  []model.Document
	delay time.Duration
	// delayFor, when set, gives the delay of call n (1-based) instead of delay.
	delayFor func(n int) time.Duration
	// fail returns the error for call n; nil means the call succeeds.
	fail func(n int, req source.Request) error
	// page overrides call n when it returns true; false falls through to
	// normal paging.
	page func(n int, req source.Request) (source.Page, bool)

	mu    sync.Mutex
	reqs  []source.Request
	calls int
}

func (f *fakeSource) Name() model.Source { return f.name }

func (f *fakeSource) FetchPage(ctx context.Context, req source.Request) (source.Page, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	d := f.delay
	if f.delayFor != nil {
		d = f.delayFor(n)
	}
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return source.Page{}, ctx.Err()
		}
	}
	if f.fail != nil {
		if err := f.fail(n, req); err != nil {
			return source.Page{}, err
		}
	}
	if f.page != nil {
		if p, ok := f.page(n, req); ok {
			return p, nil
		}
	}
	return f.normal(req), nil
}

// normal is keyset paging over docs: after the anchor when it is found by
// ID, else after everything dated at or after the anchor's date.
func (f *fakeSource) normal(req source.Request) source.Page {
	start := 0
	if req.After != nil {
		if i := slices.IndexFunc(f.docs, func(d model.Document) bool { return d.ID == req.After.ID }); i >= 0 {
			start = i + 1
		} else {
			start = slices.IndexFunc(f.docs, func(d model.Document) bool { return d.IssuedAt.Before(req.After.IssuedAt) })
			if start < 0 {
				start = len(f.docs)
			}
		}
	}
	end := min(start+req.Limit, len(f.docs))
	return source.Page{Docs: slices.Clone(f.docs[start:end]), HasMore: end < len(f.docs)}
}

func (f *fakeSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeSource) requests() []source.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// doc makes a document for src.
func doc(src model.Source, id string, at time.Time) model.Document {
	return model.Document{
		ID: model.DocumentID(src, id), Source: src, Type: "repair_order", Title: "Doc " + id,
		IssuedAt: at, URL: "http://" + string(src) + "/files/" + id + ".pdf",
	}
}

func day(d int) time.Time { return time.Date(2024, time.March, d, 0, 0, 0, 0, time.UTC) }

// merged is the expected consolidated order: date descending, then source
// name, and within one source the order its list already had (stable).
func merged(lists ...[]model.Document) []model.Document {
	all := slices.Concat(lists...)
	slices.SortStableFunc(all, func(x, y model.Document) int {
		if c := y.IssuedAt.Compare(x.IssuedAt); c != 0 {
			return c
		}
		return cmp.Compare(x.Source, y.Source)
	})
	return all
}

func entryIDs(es []model.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Sources[0].ID
	}
	return out
}

func docIDs(ds []model.Document) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}
	return out
}
