package sales_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/aggregator/source/sales"
	"docviewer/internal/client"
	"docviewer/internal/client/salesapi"
	"docviewer/internal/model"
	"docviewer/pkg/httpjson"
)

const vin model.VIN = "1M8GDM9AXKP042788"

// fakeLister is a Sales client that returns what it is told and records the
// call, so the adapter's paging parameters and context can be checked.
type fakeLister struct {
	page   salesapi.DocumentPage
	err    error
	gotCtx context.Context
	gotVIN string
	gotP   salesapi.ListParams
}

func (f *fakeLister) ListDocuments(ctx context.Context, vin string, p salesapi.ListParams) (salesapi.DocumentPage, error) {
	f.gotCtx, f.gotVIN, f.gotP = ctx, vin, p
	return f.page, f.err
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func wireDoc(id, docType, created string) salesapi.Document {
	return salesapi.Document{DocID: id, DocType: docType, Title: "Title " + id, CreatedDate: created, FileURL: "http://sales/files/" + id + ".pdf"}
}

func page(hasMore bool, docs ...salesapi.Document) salesapi.DocumentPage {
	return salesapi.DocumentPage{VehicleVIN: string(vin), HasMore: hasMore, Documents: docs}
}

func TestFetchPageMapping(t *testing.T) {
	full := wireDoc("S-1004", "SALES_INVOICE", "2024-03-05")
	full.Notes = "  Paid in full "
	full.FileSHA256 = "3B7C1F0A9D2E4C6B8A0F1E2D3C4B5A69788796A5B4C3D2E1F0A9B8C7D6E5F4A3"
	badSHA := wireDoc("S-1", "REGISTRATION", "2024-01-01")
	badSHA.FileSHA256 = "not-a-digest"
	tests := []struct {
		name          string
		page          salesapi.DocumentPage
		want          source.Page
		wantPermanent bool
	}{
		{
			name: "maps_every_field",
			page: page(false, full),
			want: source.Page{Docs: []model.Document{{
				ID: "sales:S-1004", Source: sales.Name, Type: "sales_invoice", Title: "Title S-1004",
				Summary: "Paid in full", IssuedAt: date(2024, time.March, 5), URL: "http://sales/files/S-1004.pdf",
				Checksum: "3b7c1f0a9d2e4c6b8a0f1e2d3c4b5a69788796a5b4c3d2e1f0a9b8c7d6e5f4a3",
			}}},
		},
		{
			name: "unknown_type_becomes_other",
			page: page(false, wireDoc("S-2", "WARRANTY_EXTENSION", "2024-01-01")),
			want: source.Page{Docs: []model.Document{{
				ID: "sales:S-2", Source: sales.Name, Type: model.TypeOther, Title: "Title S-2",
				IssuedAt: date(2024, time.January, 1), URL: "http://sales/files/S-2.pdf",
			}}},
		},
		{
			name: "type_is_lower_cased_whatever_the_upstream_case",
			page: page(false, wireDoc("S-3", "Purchase_Agreement", "2024-01-01")),
			want: source.Page{Docs: []model.Document{{
				ID: "sales:S-3", Source: sales.Name, Type: "purchase_agreement", Title: "Title S-3",
				IssuedAt: date(2024, time.January, 1), URL: "http://sales/files/S-3.pdf",
			}}},
		},
		{
			name: "no_notes_means_no_summary",
			page: page(false, wireDoc("S-4", "REGISTRATION", "2024-01-01")),
			want: source.Page{Docs: []model.Document{{
				ID: "sales:S-4", Source: sales.Name, Type: "registration", Title: "Title S-4",
				IssuedAt: date(2024, time.January, 1), URL: "http://sales/files/S-4.pdf",
			}}},
		},
		{
			name: "checksum_that_is_not_a_digest_is_dropped",
			page: page(false, badSHA),
			want: source.Page{Docs: []model.Document{{
				ID: "sales:S-1", Source: sales.Name, Type: "registration", Title: "Title S-1",
				IssuedAt: date(2024, time.January, 1), URL: "http://sales/files/S-1.pdf",
			}}},
		},
		{
			name: "has_more_is_passed_through",
			page: page(true, wireDoc("S-5", "REGISTRATION", "2024-01-01")),
			want: source.Page{HasMore: true, Docs: []model.Document{{
				ID: "sales:S-5", Source: sales.Name, Type: "registration", Title: "Title S-5",
				IssuedAt: date(2024, time.January, 1), URL: "http://sales/files/S-5.pdf",
			}}},
		},
		{
			name: "upstream_order_is_kept_not_resorted",
			page: page(false, wireDoc("S-10", "REGISTRATION", "2024-01-01"), wireDoc("S-9", "REGISTRATION", "2024-06-01")),
			want: source.Page{Docs: []model.Document{
				{ID: "sales:S-10", Source: sales.Name, Type: "registration", Title: "Title S-10", IssuedAt: date(2024, time.January, 1), URL: "http://sales/files/S-10.pdf"},
				{ID: "sales:S-9", Source: sales.Name, Type: "registration", Title: "Title S-9", IssuedAt: date(2024, time.June, 1), URL: "http://sales/files/S-9.pdf"},
			}},
		},
		{
			name: "empty_page_has_no_docs",
			page: page(false),
			want: source.Page{},
		},
		{
			name: "vin_echoed_in_lower_case_is_accepted",
			page: salesapi.DocumentPage{VehicleVIN: "1m8gdm9axkp042788", Documents: []salesapi.Document{wireDoc("S-6", "REGISTRATION", "2024-01-01")}},
			want: source.Page{Docs: []model.Document{{
				ID: "sales:S-6", Source: sales.Name, Type: "registration", Title: "Title S-6",
				IssuedAt: date(2024, time.January, 1), URL: "http://sales/files/S-6.pdf",
			}}},
		},
		{
			name:          "another_vehicles_page_is_permanent",
			page:          salesapi.DocumentPage{VehicleVIN: "WVWZZZ1JZXW000002", Documents: []salesapi.Document{wireDoc("S-7", "REGISTRATION", "2024-01-01")}},
			wantPermanent: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := sales.New(&fakeLister{page: tt.page})
			got, err := a.FetchPage(t.Context(), source.Request{VIN: vin, Limit: 20})
			if tt.wantPermanent {
				if !errors.Is(err, source.ErrPermanent) {
					t.Fatalf("FetchPage() error = %v, want ErrPermanent", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("FetchPage() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FetchPage() = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestFetchPageMalformedItems(t *testing.T) {
	noID := wireDoc("", "REGISTRATION", "2024-02-01")
	noDate := wireDoc("S-2", "REGISTRATION", "")
	badDate := wireDoc("S-3", "REGISTRATION", "03/02/2024")
	noURL := wireDoc("S-4", "REGISTRATION", "2024-04-01")
	noURL.FileURL = ""
	good := wireDoc("S-5", "REGISTRATION", "2024-05-01")
	wantGood := model.Document{ID: "sales:S-5", Source: sales.Name, Type: "registration", Title: "Title S-5", IssuedAt: date(2024, time.May, 1), URL: "http://sales/files/S-5.pdf"}
	tests := []struct {
		name string
		page salesapi.DocumentPage
		want source.Page
	}{
		{name: "missing_id_is_skipped", page: page(false, noID, good), want: source.Page{Docs: []model.Document{wantGood}}},
		{name: "missing_date_is_skipped", page: page(false, noDate, good), want: source.Page{Docs: []model.Document{wantGood}}},
		{name: "unparseable_date_is_skipped", page: page(false, badDate, good), want: source.Page{Docs: []model.Document{wantGood}}},
		{name: "missing_url_is_skipped", page: page(false, noURL, good), want: source.Page{Docs: []model.Document{wantGood}}},
		{
			name: "fully_malformed_page_skips_to_the_last_placeable_item",
			page: page(true, noURL, badDate),
			want: source.Page{HasMore: true, SkipTo: &source.Position{IssuedAt: date(2024, time.April, 1), ID: "sales:S-4"}},
		},
		{
			name: "fully_malformed_page_without_a_placeable_item_has_no_skip",
			page: page(true, noID, noDate),
			want: source.Page{HasMore: true},
		},
		{
			name: "partly_malformed_page_has_no_skip",
			page: page(true, noURL, good),
			want: source.Page{HasMore: true, Docs: []model.Document{wantGood}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := sales.New(&fakeLister{page: tt.page})
			got, err := a.FetchPage(t.Context(), source.Request{VIN: vin, Limit: 20})
			if err != nil {
				t.Fatalf("FetchPage() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FetchPage() = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestFetchPageParams(t *testing.T) {
	tests := []struct {
		name string
		req  source.Request
		want salesapi.ListParams
	}{
		{
			name: "first_page_asks_from_the_newest",
			req:  source.Request{VIN: vin, Limit: 20},
			want: salesapi.ListParams{Limit: 20},
		},
		{
			name: "position_becomes_before_date_and_upstream_id",
			req:  source.Request{VIN: vin, Limit: 2, After: &source.Position{IssuedAt: date(2024, time.March, 2), ID: "sales:S-1002"}},
			want: salesapi.ListParams{Limit: 2, BeforeDate: "2024-03-02", BeforeID: "S-1002"},
		},
		{
			name: "position_time_of_day_is_dropped_to_the_date",
			req:  source.Request{VIN: vin, Limit: 2, After: &source.Position{IssuedAt: time.Date(2024, time.March, 2, 23, 59, 0, 0, time.UTC), ID: "sales:S-1002"}},
			want: salesapi.ListParams{Limit: 2, BeforeDate: "2024-03-02", BeforeID: "S-1002"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeLister{page: page(false)}
			ctx := t.Context()
			if _, err := sales.New(f).FetchPage(ctx, tt.req); err != nil {
				t.Fatalf("FetchPage() error = %v", err)
			}
			if f.gotVIN != string(vin) {
				t.Errorf("vin = %q, want %q", f.gotVIN, vin)
			}
			if !reflect.DeepEqual(f.gotP, tt.want) {
				t.Errorf("params = %+v, want %+v", f.gotP, tt.want)
			}
			if f.gotCtx != ctx {
				t.Error("the client did not get the caller's context")
			}
		})
	}
}

func TestFetchPageErrors(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantDone      bool  // nil error, empty final page
		wantPermanent bool  // wraps source.ErrPermanent
		wantIs        error // else: the error passes through
	}{
		{name: "not_found_is_done", err: fmt.Errorf("%w: 404", client.ErrNotFound), wantDone: true},
		{name: "rejected_is_permanent", err: fmt.Errorf("%w: 403", client.ErrRejected), wantPermanent: true},
		{name: "too_large_is_permanent", err: client.ErrTooLarge, wantPermanent: true},
		{name: "malformed_is_permanent", err: client.ErrMalformed, wantPermanent: true},
		{name: "unavailable_is_transient", err: fmt.Errorf("%w: 503", client.ErrUnavailable), wantIs: client.ErrUnavailable},
		{name: "deadline_is_transient", err: context.DeadlineExceeded, wantIs: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sales.New(&fakeLister{err: tt.err}).FetchPage(t.Context(), source.Request{VIN: vin, Limit: 20})
			switch {
			case tt.wantDone:
				if err != nil || len(got.Docs) != 0 || got.HasMore {
					t.Fatalf("FetchPage() = %+v, %v; want an empty final page", got, err)
				}
			case tt.wantPermanent:
				if !errors.Is(err, source.ErrPermanent) {
					t.Fatalf("FetchPage() error = %v, want ErrPermanent", err)
				}
			default:
				if !errors.Is(err, tt.wantIs) || errors.Is(err, source.ErrPermanent) {
					t.Fatalf("FetchPage() error = %v, want transient %v", err, tt.wantIs)
				}
			}
		})
	}
}

func TestNameAndBuild(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "builds_from_a_valid_url", baseURL: "http://mock-sales:8081"},
		{name: "rejects_a_bad_url", baseURL: "://bad", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := sales.Build(tt.baseURL, httpjson.Client{HTTP: &http.Client{}, MaxBytes: 1})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && src.Name() != sales.Name {
				t.Errorf("Name() = %q, want %q", src.Name(), sales.Name)
			}
		})
	}
}
