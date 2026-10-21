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

const (
	vin         model.VIN    = "1M8GDM9AXKP042788"
	salesName   model.Source = "sales"
	serviceName model.Source = "service"
	requestTO                = 3 * time.Second
	sourceTO                 = 2 * time.Second
	walkBound                = 1000 // pages; a paging bug must not loop forever
)

func build(t *testing.T, srcs ...source.Source) *aggregator.Aggregator {
	t.Helper()
	b := aggregator.NewBuilder(aggregator.Config{RequestTimeout: requestTO}, slog.New(slog.DiscardHandler))
	for _, s := range srcs {
		b.AddSource(s, sourceTO)
	}
	agg, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	return agg
}

// walk pages through every page for vin and returns them all.
func walk(t *testing.T, agg *aggregator.Aggregator, limit int) []aggregator.Result {
	t.Helper()
	var pages []aggregator.Result
	cursor := ""
	for range walkBound {
		res, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: cursor, Limit: limit})
		if err != nil {
			t.Fatalf("List() page %d error = %v", len(pages)+1, err)
		}
		pages = append(pages, res)
		if res.NextCursor == "" {
			return pages
		}
		cursor = res.NextCursor
	}
	t.Fatalf("walk did not end within %d pages", walkBound)
	return nil
}

func allEntries(pages []aggregator.Result) []model.Entry {
	var out []model.Entry
	for _, p := range pages {
		out = append(out, p.Entries...)
	}
	return out
}

func statusOf(res aggregator.Result, name model.Source) aggregator.Status {
	for _, s := range res.Sources {
		if s.Source == name {
			return s.Status
		}
	}
	return 0
}

// Fixture lists, each already in its source's own order.
var (
	// Sales: string IDs, one same-date tie (S-3, S-2 on day 2), the newest
	// document of all on day 9.
	salesDocs = []model.Document{
		doc(salesName, "S-5", day(9)), doc(salesName, "S-4", day(5)),
		doc(salesName, "S-3", day(2)), doc(salesName, "S-2", day(2)), doc(salesName, "S-1", day(1)),
	}
	// Service: numeric IDs with the 9999/10000 trap on the same day (numeric
	// order, which is what the upstream sends), plus a cross-source tie on
	// day 5 and day 2.
	serviceDocs = []model.Document{
		doc(serviceName, "10002", day(8)), doc(serviceName, "10001", day(5)),
		doc(serviceName, "10000", day(3)), doc(serviceName, "9999", day(3)),
		doc(serviceName, "9998", day(2)),
	}
)

func TestListWalkReturnsEverythingOnce(t *testing.T) {
	tests := []struct {
		name  string
		limit int
	}{
		{name: "page_size_1", limit: 1},
		{name: "page_size_2", limit: 2},
		{name: "page_size_3", limit: 3},
		{name: "page_size_20_fits_in_one_page", limit: 20},
	}
	want := docIDs(merged(salesDocs, serviceDocs))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sales := &fakeSource{name: salesName, docs: salesDocs}
			service := &fakeSource{name: serviceName, docs: serviceDocs}
			pages := walk(t, build(t, sales, service), tt.limit)
			if got := entryIDs(allEntries(pages)); !reflect.DeepEqual(got, want) {
				t.Errorf("walk = %v\nwant %v", got, want)
			}
			for i, p := range pages {
				if p.VIN != vin {
					t.Errorf("page %d VIN = %q, want %q", i+1, p.VIN, vin)
				}
				if len(p.Entries) > tt.limit {
					t.Errorf("page %d has %d entries, limit %d", i+1, len(p.Entries), tt.limit)
				}
				if i < len(pages)-1 && p.NextCursor == "" {
					t.Errorf("page %d has no next cursor but more pages followed", i+1)
				}
			}
			if last := pages[len(pages)-1]; last.NextCursor != "" {
				t.Error("last page still has a next cursor")
			}
		})
	}
}

// TestListFirstPageAndResume checks what each source is asked for: from the
// newest on the first page, after its own last returned document on the next.
func TestListFirstPageAndResume(t *testing.T) {
	sales := &fakeSource{name: salesName, docs: salesDocs}
	service := &fakeSource{name: serviceName, docs: serviceDocs}
	agg := build(t, sales, service)
	first, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Page 1 is S-5 (day 9) and 10002 (day 8): one from each source.
	second, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		got  []source.Request
		want []source.Request
	}{
		{
			name: "sales_from_the_newest_then_after_its_last",
			got:  sales.requests(),
			want: []source.Request{
				{VIN: vin, Limit: 2},
				{VIN: vin, Limit: 2, After: &source.Position{IssuedAt: day(9), ID: "sales:S-5"}},
			},
		},
		{
			name: "service_from_the_newest_then_after_its_last",
			got:  service.requests(),
			want: []source.Request{
				{VIN: vin, Limit: 2},
				{VIN: vin, Limit: 2, After: &source.Position{IssuedAt: day(8), ID: "service:10002"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("requests = %+v\nwant %+v", tt.got, tt.want)
			}
		})
	}
	if got := entryIDs(second.Entries); !reflect.DeepEqual(got, []string{"sales:S-4", "service:10001"}) {
		t.Errorf("page 2 = %v, want the two day-5 documents, sales first", got)
	}
}

// TestListCutOffDocumentsComeBack: every source is asked for the full
// limit; what the cut drops is fetched again and the position does not move.
func TestListCutOffDocumentsComeBack(t *testing.T) {
	tests := []struct {
		name          string
		limit         int
		wantPage1     []string
		wantServiceAt *source.Position // service's position in page 1's cursor, nil means still at the start
	}{
		{
			name:      "service_has_nothing_on_page_1_and_stays_at_start",
			limit:     1,
			wantPage1: []string{"sales:S-5"},
		},
		{
			name:          "service_moves_only_to_what_was_returned",
			limit:         3,
			wantPage1:     []string{"sales:S-5", "service:10002", "sales:S-4"},
			wantServiceAt: &source.Position{IssuedAt: day(8), ID: "service:10002"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sales := &fakeSource{name: salesName, docs: salesDocs}
			service := &fakeSource{name: serviceName, docs: serviceDocs}
			agg := build(t, sales, service)
			first, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: tt.limit})
			if err != nil {
				t.Fatal(err)
			}
			if got := entryIDs(first.Entries); !reflect.DeepEqual(got, tt.wantPage1) {
				t.Errorf("page 1 = %v, want %v", got, tt.wantPage1)
			}
			if _, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: first.NextCursor, Limit: tt.limit}); err != nil {
				t.Fatalf("page 2 error = %v (the cursor must not be outdated)", err)
			}
			reqs := service.requests()
			if len(reqs) != 2 {
				t.Fatalf("service was called %d times, want 2", len(reqs))
			}
			if !reflect.DeepEqual(reqs[1].After, tt.wantServiceAt) {
				t.Errorf("service page-2 position = %+v, want %+v", reqs[1].After, tt.wantServiceAt)
			}
		})
	}
}

func TestListPartialFailure(t *testing.T) {
	transient := errors.New("connection reset")
	failOn := func(call int, err error) func(n int, req source.Request) error {
		return func(n int, _ source.Request) error {
			if n == call {
				return err
			}
			return nil
		}
	}
	tests := []struct {
		name            string
		serviceFail     func(n int, req source.Request) error
		serviceDelayFor func(n int) time.Duration
		limit           int
		wantPage1Status aggregator.Status
		wantPage2Status aggregator.Status
		wantAll         bool // the walk still returns every document of both sources
		wantServiceCall int  // service calls over the whole walk; 0 means don't check
	}{
		{
			name:            "transient_error_on_page_2_keeps_the_position_and_catches_up",
			serviceFail:     failOn(2, transient),
			limit:           2,
			wantPage1Status: aggregator.StatusOK,
			wantPage2Status: aggregator.StatusError,
			wantAll:         true,
		},
		{
			name: "timeout_on_page_1_is_retried_on_page_2",
			serviceDelayFor: func(n int) time.Duration {
				if n == 1 {
					return sourceTO + time.Second // only the first call is slow
				}
				return 0
			},
			limit:           2,
			wantPage1Status: aggregator.StatusTimeout,
			wantPage2Status: aggregator.StatusOK,
			wantAll:         true,
		},
		{
			name:            "permanent_failure_marks_the_source_unavailable_for_good",
			serviceFail:     func(int, source.Request) error { return source.ErrPermanent },
			limit:           2,
			wantPage1Status: aggregator.StatusUnavailable,
			wantPage2Status: aggregator.StatusUnavailable,
			wantServiceCall: 1,
		},
		{
			name:            "unsupported_request_is_permanent_too",
			serviceFail:     func(int, source.Request) error { return source.ErrUnsupported },
			limit:           2,
			wantPage1Status: aggregator.StatusUnavailable,
			wantPage2Status: aggregator.StatusUnavailable,
			wantServiceCall: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sales := &fakeSource{name: salesName, docs: salesDocs}
				service := &fakeSource{name: serviceName, docs: serviceDocs, fail: tt.serviceFail, delayFor: tt.serviceDelayFor}
				pages := walk(t, build(t, sales, service), tt.limit)
				if got := statusOf(pages[0], serviceName); got != tt.wantPage1Status {
					t.Errorf("page 1 service status = %v, want %v", got, tt.wantPage1Status)
				}
				if got := statusOf(pages[1], serviceName); got != tt.wantPage2Status {
					t.Errorf("page 2 service status = %v, want %v", got, tt.wantPage2Status)
				}
				got := entryIDs(allEntries(pages))
				if tt.wantAll {
					want := docIDs(merged(salesDocs, serviceDocs))
					if len(got) != len(want) || !sameSet(got, want) {
						t.Errorf("walk = %v\nwant every document once: %v", got, want)
					}
				} else if want := docIDs(salesDocs); !reflect.DeepEqual(got, want) {
					t.Errorf("walk = %v, want Sales only %v", got, want)
				}
				if tt.wantServiceCall > 0 && service.callCount() != tt.wantServiceCall {
					t.Errorf("service called %d times, want %d", service.callCount(), tt.wantServiceCall)
				}
				if last := pages[len(pages)-1]; last.NextCursor != "" {
					t.Error("the walk did not end")
				}
			})
		})
	}
}

func sameSet(a, b []string) bool {
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// TestListTransientKeepsPosition pins the mechanism behind the catch-up: the
// failed source is asked for the same position again on the next page.
func TestListTransientKeepsPosition(t *testing.T) {
	sales := &fakeSource{name: salesName, docs: salesDocs}
	service := &fakeSource{name: serviceName, docs: serviceDocs, fail: func(n int, _ source.Request) error {
		if n == 2 {
			return errors.New("boom")
		}
		return nil
	}}
	agg := build(t, sales, service)
	p1, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: p1.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: p2.NextCursor, Limit: 2}); err != nil {
		t.Fatal(err)
	}
	reqs := service.requests()
	tests := []struct {
		name string
		got  any
		want any
	}{
		{name: "page_2_has_only_sales", got: entryIDs(p2.Entries), want: []string{"sales:S-4", "sales:S-3"}},
		{name: "page_2_names_the_failed_source", got: statusOf(p2, serviceName), want: aggregator.StatusError},
		{name: "page_3_asks_service_for_the_same_position_as_page_2", got: reqs[2].After, want: reqs[1].After},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("got %+v, want %+v", tt.got, tt.want)
			}
		})
	}
}

func TestListDoneSources(t *testing.T) {
	tests := []struct {
		name              string
		service           *fakeSource
		limit             int
		wantPage1Status   aggregator.Status
		wantPage2Status   aggregator.Status
		wantServiceCalls  int
		wantPage1NextNone bool
	}{
		{
			name:             "not_found_is_an_answer_then_done_and_not_called_again",
			service:          &fakeSource{name: serviceName}, // empty, final page, like a 404
			limit:            2,
			wantPage1Status:  aggregator.StatusOK,
			wantPage2Status:  aggregator.StatusDone,
			wantServiceCalls: 1,
		},
		{
			name:             "a_source_whose_last_document_made_the_page_is_done",
			service:          &fakeSource{name: serviceName, docs: serviceDocs[:1]}, // 10002 on day 8
			limit:            2,
			wantPage1Status:  aggregator.StatusOK,
			wantPage2Status:  aggregator.StatusDone,
			wantServiceCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sales := &fakeSource{name: salesName, docs: salesDocs}
			pages := walk(t, build(t, sales, tt.service), tt.limit)
			if got := statusOf(pages[0], serviceName); got != tt.wantPage1Status {
				t.Errorf("page 1 service status = %v, want %v", got, tt.wantPage1Status)
			}
			if got := statusOf(pages[1], serviceName); got != tt.wantPage2Status {
				t.Errorf("page 2 service status = %v, want %v", got, tt.wantPage2Status)
			}
			if got := tt.service.callCount(); got != tt.wantServiceCalls {
				t.Errorf("service called %d times, want %d", got, tt.wantServiceCalls)
			}
		})
	}
}

func TestListEverythingDoneEndsWithoutError(t *testing.T) {
	tests := []struct {
		name    string
		sales   *fakeSource
		service *fakeSource
	}{
		{name: "both_empty_is_an_empty_last_page", sales: &fakeSource{name: salesName}, service: &fakeSource{name: serviceName}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := build(t, tt.sales, tt.service).List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(res.Entries) != 0 || res.NextCursor != "" {
				t.Errorf("List() = %+v, want no entries and no next cursor", res)
			}
			for _, s := range res.Sources {
				if s.Status != aggregator.StatusOK {
					t.Errorf("%s status = %v, want ok (an empty answer is an answer)", s.Source, s.Status)
				}
			}
		})
	}
}

func TestListNothingAnswered(t *testing.T) {
	transient := errors.New("boom")
	tests := []struct {
		name          string
		salesErr      error
		serviceErr    error
		wantErr       bool
		wantRetryable bool
		wantStatuses  map[model.Source]aggregator.Status
	}{
		{
			name: "both_transient_is_retryable", salesErr: transient, serviceErr: transient,
			wantErr: true, wantRetryable: true,
			wantStatuses: map[model.Source]aggregator.Status{salesName: aggregator.StatusError, serviceName: aggregator.StatusError},
		},
		{
			name: "both_permanent_is_not_retryable", salesErr: source.ErrPermanent, serviceErr: source.ErrPermanent,
			wantErr: true, wantRetryable: false,
			wantStatuses: map[model.Source]aggregator.Status{salesName: aggregator.StatusUnavailable, serviceName: aggregator.StatusUnavailable},
		},
		{
			name: "one_permanent_one_transient_is_retryable", salesErr: source.ErrPermanent, serviceErr: transient,
			wantErr: true, wantRetryable: true,
			wantStatuses: map[model.Source]aggregator.Status{salesName: aggregator.StatusUnavailable, serviceName: aggregator.StatusError},
		},
		{
			name: "one_answer_is_enough_even_an_empty_one", salesErr: nil, serviceErr: transient,
			wantErr:      false,
			wantStatuses: map[model.Source]aggregator.Status{salesName: aggregator.StatusOK, serviceName: aggregator.StatusError},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fail := func(err error) func(int, source.Request) error {
				return func(int, source.Request) error { return err }
			}
			sales := &fakeSource{name: salesName, fail: fail(tt.salesErr)} // no documents: an empty answer
			service := &fakeSource{name: serviceName, docs: serviceDocs, fail: fail(tt.serviceErr)}
			res, err := build(t, sales, service).List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20})
			for _, s := range res.Sources {
				if want := tt.wantStatuses[s.Source]; s.Status != want {
					t.Errorf("%s status = %v, want %v", s.Source, s.Status, want)
				}
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("List() error = %v", err)
				}
				if res.NextCursor == "" {
					t.Error("next cursor is empty, want one so the failed source is retried")
				}
				return
			}
			var su *aggregator.SourcesUnavailableError
			if !errors.Is(err, aggregator.ErrSourcesUnavailable) || !errors.As(err, &su) {
				t.Fatalf("List() error = %v, want ErrSourcesUnavailable carrying the statuses", err)
			}
			if su.Retryable() != tt.wantRetryable {
				t.Errorf("Retryable() = %v, want %v", su.Retryable(), tt.wantRetryable)
			}
			if res.NextCursor != "" {
				t.Error("next cursor set on a failed page, want none")
			}
			if len(su.Sources) != len(tt.wantStatuses) {
				t.Errorf("error carries %d statuses, want %d", len(su.Sources), len(tt.wantStatuses))
			}
		})
	}
}

// TestListSkipsFullyMalformedPage: a page with nothing usable but a SkipTo
// moves the position past it instead of asking for it forever.
func TestListSkipsFullyMalformedPage(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "skip_to_moves_the_cursor_past_the_malformed_page"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skip := &source.Position{IssuedAt: day(7), ID: "service:777"}
			service := &fakeSource{name: serviceName, docs: serviceDocs[1:]} // from 10001 on day 5
			service.page = func(n int, req source.Request) (source.Page, bool) {
				if n == 1 {
					return source.Page{HasMore: true, SkipTo: skip}, true // the page before 10001 was all malformed
				}
				if n == 2 && !reflect.DeepEqual(req.After, skip) {
					t.Errorf("call %d after = %+v, want the skip position %+v", n, req.After, skip)
				}
				return source.Page{}, false
			}
			sales := &fakeSource{name: salesName, docs: salesDocs[:1]}
			pages := walk(t, build(t, sales, service), 2)
			want := docIDs(merged(salesDocs[:1], serviceDocs[1:]))
			if got := entryIDs(allEntries(pages)); !reflect.DeepEqual(got, want) {
				t.Errorf("walk = %v, want %v", got, want)
			}
			if service.callCount() != 3 { // the malformed page, then two pages of 2
				t.Errorf("service called %d times, want 3", service.callCount())
			}
		})
	}
}

// TestListParallelism: sources are fetched at the same time, so two 300ms
// sources take 300ms, not 600ms. synctest's virtual clock makes it exact.
func TestListParallelism(t *testing.T) {
	tests := []struct {
		name  string
		delay time.Duration
		want  time.Duration
	}{
		{name: "two_sources_of_300ms_take_300ms", delay: 300 * time.Millisecond, want: 300 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sales := &fakeSource{name: salesName, docs: salesDocs, delay: tt.delay}
				service := &fakeSource{name: serviceName, docs: serviceDocs, delay: tt.delay}
				agg := build(t, sales, service)
				start := time.Now()
				if _, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20}); err != nil {
					t.Fatal(err)
				}
				if got := time.Since(start); got != tt.want {
					t.Errorf("List() took %v, want exactly %v", got, tt.want)
				}
			})
		})
	}
}

// TestListTimeoutBudget: a source slower than its own deadline is a timeout
// while the other source's answer is kept, and the page takes no longer than
// the source deadline.
func TestListTimeoutBudget(t *testing.T) {
	tests := []struct {
		name         string
		serviceDelay time.Duration
		wantStatus   aggregator.Status
		wantTook     time.Duration
	}{
		{name: "slower_than_the_source_deadline_times_out", serviceDelay: sourceTO + time.Second, wantStatus: aggregator.StatusTimeout, wantTook: sourceTO},
		{name: "within_the_deadline_answers", serviceDelay: sourceTO - time.Second, wantStatus: aggregator.StatusOK, wantTook: sourceTO - time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sales := &fakeSource{name: salesName, docs: salesDocs}
				service := &fakeSource{name: serviceName, docs: serviceDocs, delay: tt.serviceDelay}
				agg := build(t, sales, service)
				start := time.Now()
				res, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20})
				if err != nil {
					t.Fatal(err)
				}
				if got := time.Since(start); got != tt.wantTook {
					t.Errorf("List() took %v, want %v", got, tt.wantTook)
				}
				if got := statusOf(res, serviceName); got != tt.wantStatus {
					t.Errorf("service status = %v, want %v", got, tt.wantStatus)
				}
				if got := statusOf(res, salesName); got != aggregator.StatusOK {
					t.Errorf("sales status = %v, want ok: a slow source never cancels a healthy one", got)
				}
			})
		})
	}
}

func TestListCoalescing(t *testing.T) {
	const callers = 5
	tests := []struct {
		name         string
		requests     func(cursor string) []aggregator.ListRequest // what each caller sends
		wantCalls    int                                          // per source
		cancelFirst  bool                                         // caller 0 hangs up mid-flight
		wantCanceled int                                          // callers that get context.Canceled
	}{
		{
			name: "identical_requests_share_one_fan_out",
			requests: func(string) []aggregator.ListRequest {
				out := make([]aggregator.ListRequest, callers)
				for i := range out {
					out[i] = aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20}
				}
				return out
			},
			wantCalls: 1,
		},
		{
			name: "a_different_limit_is_its_own_flight",
			requests: func(string) []aggregator.ListRequest {
				out := make([]aggregator.ListRequest, callers)
				for i := range out {
					out[i] = aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20 + i%2}
				}
				return out
			},
			wantCalls: 2,
		},
		{
			name: "a_different_position_is_its_own_flight",
			requests: func(cursor string) []aggregator.ListRequest {
				out := make([]aggregator.ListRequest, callers)
				for i := range out {
					out[i] = aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20}
					if i%2 == 1 {
						out[i].Cursor = cursor
					}
				}
				return out
			},
			wantCalls: 2,
		},
		{
			name: "one_caller_hanging_up_does_not_fail_the_others",
			requests: func(string) []aggregator.ListRequest {
				out := make([]aggregator.ListRequest, callers)
				for i := range out {
					out[i] = aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20}
				}
				return out
			},
			wantCalls:    1,
			cancelFirst:  true,
			wantCanceled: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// A cursor from a quiet aggregator, for the "different position" case.
				quiet := build(t, &fakeSource{name: salesName, docs: salesDocs}, &fakeSource{name: serviceName, docs: serviceDocs})
				first, err := quiet.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 1})
				if err != nil {
					t.Fatal(err)
				}
				sales := &fakeSource{name: salesName, docs: salesDocs, delay: 100 * time.Millisecond}
				service := &fakeSource{name: serviceName, docs: serviceDocs, delay: 100 * time.Millisecond}
				agg := build(t, sales, service)
				reqs := tt.requests(first.NextCursor)
				errs := make([]error, callers)
				results := make([]aggregator.Result, callers)
				var wg sync.WaitGroup
				for i, req := range reqs {
					ctx, cancel := context.WithCancel(t.Context())
					wg.Go(func() {
						results[i], errs[i] = agg.List(ctx, req)
					})
					if i == 0 && tt.cancelFirst {
						time.Sleep(10 * time.Millisecond) // the flight is in progress
						cancel()
					} else {
						t.Cleanup(cancel)
					}
				}
				wg.Wait()
				canceled := 0
				for i, err := range errs {
					switch {
					case errors.Is(err, context.Canceled):
						canceled++
					case err != nil:
						t.Errorf("caller %d error = %v", i, err)
					case len(results[i].Entries) == 0:
						t.Errorf("caller %d got no entries", i)
					}
				}
				if canceled != tt.wantCanceled {
					t.Errorf("%d callers got context.Canceled, want %d", canceled, tt.wantCanceled)
				}
				if sales.callCount() != tt.wantCalls || service.callCount() != tt.wantCalls {
					t.Errorf("calls = sales %d, service %d; want %d each", sales.callCount(), service.callCount(), tt.wantCalls)
				}
			})
		})
	}
}

func TestListCallerCancellation(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "a_cancelled_caller_gets_context_canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sales := &fakeSource{name: salesName, docs: salesDocs, delay: time.Second}
				agg := build(t, sales)
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				_, err := agg.List(ctx, aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20})
				if !errors.Is(err, context.Canceled) {
					t.Errorf("List() error = %v, want context.Canceled", err)
				}
				// The flight is detached from the caller and carries on; let it
				// finish inside the bubble. It must still have called the source.
				time.Sleep(sourceTO)
				if sales.callCount() != 1 {
					t.Errorf("source called %d times, want 1: the flight outlives the caller", sales.callCount())
				}
			})
		})
	}
}
