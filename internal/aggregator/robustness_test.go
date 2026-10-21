package aggregator_test

import (
	"errors"
	"reflect"
	"testing"

	"docviewer/internal/aggregator"
	"docviewer/internal/aggregator/source"
	"docviewer/internal/model"
)

// TestListSourcePanics: a panic inside a source call (adapter or mapping
// code) is contained to that source. It counts as an error for the page,
// the other source's answer is kept, and the next page retries it.
func TestListSourcePanics(t *testing.T) {
	tests := []struct {
		name      string
		panicOn   int // which service call panics
		wantPage1 aggregator.Status
		wantPage2 aggregator.Status
	}{
		{name: "panic_on_page_1_is_an_error_then_retried", panicOn: 1, wantPage1: aggregator.StatusError, wantPage2: aggregator.StatusOK},
		{name: "panic_on_page_2_keeps_page_1", panicOn: 2, wantPage1: aggregator.StatusOK, wantPage2: aggregator.StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sales := &fakeSource{name: salesName, docs: salesDocs}
			service := &fakeSource{name: serviceName, docs: serviceDocs, fail: func(n int, _ source.Request) error {
				if n == tt.panicOn {
					panic("mapping bug")
				}
				return nil
			}}
			pages := walk(t, build(t, sales, service), 2)
			if got := statusOf(pages[0], serviceName); got != tt.wantPage1 {
				t.Errorf("page 1 service status = %v, want %v", got, tt.wantPage1)
			}
			if got := statusOf(pages[1], serviceName); got != tt.wantPage2 {
				t.Errorf("page 2 service status = %v, want %v", got, tt.wantPage2)
			}
			if got := statusOf(pages[0], salesName); got != aggregator.StatusOK {
				t.Errorf("page 1 sales status = %v, want ok: a panic in one source never touches the other", got)
			}
			want := docIDs(merged(salesDocs, serviceDocs))
			if got := entryIDs(allEntries(pages)); !sameSet(got, want) {
				t.Errorf("walk = %v, want every document once", got)
			}
		})
	}
}

// TestListStalledSourceEnds: a source that answers ok with nothing usable,
// says there is more, and gives no position to skip to can never move the
// cursor. It is marked unavailable, so paging ends instead of looping.
func TestListStalledSourceEnds(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "empty_has_more_without_skip_to_becomes_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sales := &fakeSource{name: salesName, docs: salesDocs}
			service := &fakeSource{name: serviceName, page: func(int, source.Request) (source.Page, bool) {
				return source.Page{HasMore: true}, true
			}}
			pages := walk(t, build(t, sales, service), 2)
			if got := statusOf(pages[0], serviceName); got != aggregator.StatusOK {
				t.Errorf("page 1 service status = %v, want ok (it did answer)", got)
			}
			if got := statusOf(pages[1], serviceName); got != aggregator.StatusUnavailable {
				t.Errorf("page 2 service status = %v, want unavailable", got)
			}
			if service.callCount() != 1 {
				t.Errorf("service called %d times, want 1", service.callCount())
			}
			if got, want := entryIDs(allEntries(pages)), docIDs(salesDocs); !reflect.DeepEqual(got, want) {
				t.Errorf("walk = %v, want Sales only %v", got, want)
			}
		})
	}
}

// TestListLaterPermanentFailureIsTerminal: a 502 is for a page that can
// still be helped (a retry may work, or nothing has been shown yet). When
// every live source fails permanently on a later page, the client gets a
// terminal page instead: no documents, the sources named unavailable, and
// no next cursor, so "load more" ends rather than failing forever.
func TestListLaterPermanentFailureIsTerminal(t *testing.T) {
	transient := errors.New("boom")
	failFrom := func(call int, err error) func(n int, req source.Request) error {
		return func(n int, _ source.Request) error {
			if n >= call {
				return err
			}
			return nil
		}
	}
	tests := []struct {
		name         string
		sales        *fakeSource
		service      *fakeSource
		wantErr      bool
		wantRetry    bool
		wantStatuses map[model.Source]aggregator.Status
	}{
		{
			name:         "both_permanent_on_page_2_is_a_terminal_200",
			sales:        &fakeSource{name: salesName, docs: salesDocs, fail: failFrom(2, source.ErrPermanent)},
			service:      &fakeSource{name: serviceName, docs: serviceDocs, fail: failFrom(2, source.ErrPermanent)},
			wantStatuses: map[model.Source]aggregator.Status{salesName: aggregator.StatusUnavailable, serviceName: aggregator.StatusUnavailable},
		},
		{
			name:         "last_live_source_permanent_while_the_other_is_done_is_terminal",
			sales:        &fakeSource{name: salesName, docs: salesDocs[:1]}, // done after page 1
			service:      &fakeSource{name: serviceName, docs: serviceDocs, fail: failFrom(2, source.ErrPermanent)},
			wantStatuses: map[model.Source]aggregator.Status{salesName: aggregator.StatusDone, serviceName: aggregator.StatusUnavailable},
		},
		{
			name:         "transient_on_page_2_is_still_a_retryable_502",
			sales:        &fakeSource{name: salesName, docs: salesDocs, fail: failFrom(2, transient)},
			service:      &fakeSource{name: serviceName, docs: serviceDocs, fail: failFrom(2, source.ErrPermanent)},
			wantErr:      true,
			wantRetry:    true,
			wantStatuses: map[model.Source]aggregator.Status{salesName: aggregator.StatusError, serviceName: aggregator.StatusUnavailable},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agg := build(t, tt.sales, tt.service)
			p1, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 2})
			if err != nil || p1.NextCursor == "" {
				t.Fatalf("page 1 = %+v, %v; want a page with a cursor", p1, err)
			}
			p2, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: p1.NextCursor, Limit: 2})
			for _, s := range p2.Sources {
				if want := tt.wantStatuses[s.Source]; s.Status != want {
					t.Errorf("%s status = %v, want %v", s.Source, s.Status, want)
				}
			}
			if tt.wantErr {
				var su *aggregator.SourcesUnavailableError
				if !errors.As(err, &su) || su.Retryable() != tt.wantRetry {
					t.Fatalf("page 2 error = %v, want ErrSourcesUnavailable retryable=%v", err, tt.wantRetry)
				}
				return
			}
			if err != nil {
				t.Fatalf("page 2 error = %v, want a terminal page", err)
			}
			if len(p2.Entries) != 0 || p2.NextCursor != "" {
				t.Errorf("page 2 = %d entries, cursor %q; want none and no cursor", len(p2.Entries), p2.NextCursor)
			}
		})
	}
}
