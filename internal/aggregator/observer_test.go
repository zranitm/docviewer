package aggregator_test

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"docviewer/internal/aggregator"
	"docviewer/internal/aggregator/source"
	"docviewer/internal/model"
)

var errTransient = errors.New("transient")

// recorder is an Observer that keeps what it was told.
type recorder struct {
	mu        sync.Mutex
	calls     map[model.Source][]aggregator.Status
	tooks     map[model.Source][]time.Duration
	coalesced int
}

func newRecorder() *recorder {
	return &recorder{calls: map[model.Source][]aggregator.Status{}, tooks: map[model.Source][]time.Duration{}}
}

func (r *recorder) SourceCall(_ context.Context, src model.Source, status aggregator.Status, took time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[src] = append(r.calls[src], status)
	r.tooks[src] = append(r.tooks[src], took)
}

func (r *recorder) Coalesced(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.coalesced++
}

func buildObserved(t *testing.T, obs aggregator.Observer, srcs ...source.Source) *aggregator.Aggregator {
	t.Helper()
	b := aggregator.NewBuilder(aggregator.Config{RequestTimeout: requestTO}, slog.New(slog.DiscardHandler)).Observe(obs)
	for _, s := range srcs {
		b.AddSource(s, sourceTO)
	}
	agg, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return agg
}

func TestObserverSourceCalls(t *testing.T) {
	tests := []struct {
		name        string
		service     *fakeSource
		wantService []aggregator.Status
		wantTook    time.Duration // the service call's duration, exact under synctest
	}{
		{
			name:        "ok_with_its_duration",
			service:     &fakeSource{name: serviceName, docs: serviceDocs, delay: 150 * time.Millisecond},
			wantService: []aggregator.Status{aggregator.StatusOK},
			wantTook:    150 * time.Millisecond,
		},
		{
			name:        "timeout_at_the_source_deadline",
			service:     &fakeSource{name: serviceName, docs: serviceDocs, delay: sourceTO + time.Second},
			wantService: []aggregator.Status{aggregator.StatusTimeout},
			wantTook:    sourceTO,
		},
		{
			name:        "permanent_failure_is_unavailable",
			service:     &fakeSource{name: serviceName, fail: func(int, source.Request) error { return source.ErrPermanent }},
			wantService: []aggregator.Status{aggregator.StatusUnavailable},
		},
		{
			name:        "transient_failure_is_error",
			service:     &fakeSource{name: serviceName, fail: func(int, source.Request) error { return errTransient }},
			wantService: []aggregator.Status{aggregator.StatusError},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := newRecorder()
				agg := buildObserved(t, rec, &fakeSource{name: salesName, docs: salesDocs}, tt.service)
				if _, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20}); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(rec.calls[serviceName], tt.wantService) {
					t.Errorf("service calls = %v, want %v", rec.calls[serviceName], tt.wantService)
				}
				if !reflect.DeepEqual(rec.calls[salesName], []aggregator.Status{aggregator.StatusOK}) {
					t.Errorf("sales calls = %v, want one ok", rec.calls[salesName])
				}
				if tt.wantTook > 0 && rec.tooks[serviceName][0] != tt.wantTook {
					t.Errorf("service took %v, want %v", rec.tooks[serviceName][0], tt.wantTook)
				}
			})
		})
	}
}

// TestObserverNotCalledForFinishedSources: done and unavailable sources are
// not called, so nothing is observed for them.
func TestObserverNotCalledForFinishedSources(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "second_page_observes_only_the_live_source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder()
			agg := buildObserved(t, rec, &fakeSource{name: salesName, docs: salesDocs}, &fakeSource{name: serviceName})
			p1, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: p1.NextCursor, Limit: 2}); err != nil {
				t.Fatal(err)
			}
			if got := len(rec.calls[serviceName]); got != 1 {
				t.Errorf("service observed %d times, want 1 (done after page 1)", got)
			}
			if got := len(rec.calls[salesName]); got != 2 {
				t.Errorf("sales observed %d times, want 2", got)
			}
		})
	}
}

func TestObserverCoalesced(t *testing.T) {
	tests := []struct {
		name          string
		callers       int
		wantCoalesced int
	}{
		{name: "five_identical_callers_share_one_flight_four_coalesced", callers: 5, wantCoalesced: 4},
		{name: "one_caller_nothing_coalesced", callers: 1, wantCoalesced: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := newRecorder()
				agg := buildObserved(t, rec, &fakeSource{name: salesName, docs: salesDocs, delay: 100 * time.Millisecond})
				var wg sync.WaitGroup
				for range tt.callers {
					wg.Go(func() {
						if _, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20}); err != nil {
							t.Error(err)
						}
					})
				}
				wg.Wait()
				if rec.coalesced != tt.wantCoalesced {
					t.Errorf("coalesced = %d, want %d", rec.coalesced, tt.wantCoalesced)
				}
				if got := len(rec.calls[salesName]); got != 1 {
					t.Errorf("sales observed %d times, want 1", got)
				}
			})
		})
	}
}

func TestBuilderWithoutObserver(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "no_observer_is_fine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agg := build(t, &fakeSource{name: salesName, docs: salesDocs})
			if _, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
