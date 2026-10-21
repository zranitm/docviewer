package service_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/aggregator/source/service"
	"docviewer/internal/client"
	"docviewer/internal/client/serviceapi"
	"docviewer/internal/model"
	"docviewer/pkg/httpjson"
)

const vin model.VIN = "1M8GDM9AXKP042788"

// fakeLister is a Service client that returns what it is told and records
// the call, so the adapter's paging parameters and context can be checked.
type fakeLister struct {
	page   serviceapi.RecordPage
	err    error
	gotCtx context.Context
	gotVIN string
	gotP   serviceapi.ListParams
}

func (f *fakeLister) ListRecords(ctx context.Context, vin string, p serviceapi.ListParams) (serviceapi.RecordPage, error) {
	f.gotCtx, f.gotVIN, f.gotP = ctx, vin, p
	return f.page, f.err
}

const (
	tsJune = 1780300800 // 2026-06-01T08:00:00Z
	tsPDI  = 1709337600 // 2024-03-02T00:00:00Z
)

func at(ts int64) time.Time { return time.Unix(ts, 0).UTC() }

func wireRec(id int64, category string, created int64) serviceapi.Record {
	return serviceapi.Record{RecordID: id, Category: category, Description: fmt.Sprintf("Record %d", id), CreatedAt: created, DownloadLink: fmt.Sprintf("http://service/files/%d.pdf", id)}
}

func page(hasMore bool, recs ...serviceapi.Record) serviceapi.RecordPage {
	return serviceapi.RecordPage{HasMore: hasMore, Records: recs}
}

func TestFetchPageMapping(t *testing.T) {
	full := wireRec(9001, "repair_order", tsJune)
	full.WorkNotes = " Oil and filter changed, brake pads replaced\n"
	full.FileSHA256 = "9F86D081884C7D659A2FEAA0C55AD015A3BF4F1B2B0B822CD15D6C15B0F00A08"
	tests := []struct {
		name string
		page serviceapi.RecordPage
		want source.Page
	}{
		{
			name: "maps_every_field",
			page: page(false, full),
			want: source.Page{Docs: []model.Document{{
				ID: "service:9001", Source: service.Name, Type: "repair_order", Title: "Record 9001",
				Summary: "Oil and filter changed, brake pads replaced", IssuedAt: at(tsJune), URL: "http://service/files/9001.pdf",
				Checksum: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			}}},
		},
		{
			name: "unknown_category_becomes_other",
			page: page(false, wireRec(9104, "warranty_claim", tsJune)),
			want: source.Page{Docs: []model.Document{{
				ID: "service:9104", Source: service.Name, Type: model.TypeOther, Title: "Record 9104",
				IssuedAt: at(tsJune), URL: "http://service/files/9104.pdf",
			}}},
		},
		{
			name: "no_work_notes_means_no_summary",
			page: page(false, wireRec(9010, "pre_delivery_inspection", tsPDI)),
			want: source.Page{Docs: []model.Document{{
				ID: "service:9010", Source: service.Name, Type: "pre_delivery_inspection", Title: "Record 9010",
				IssuedAt: at(tsPDI), URL: "http://service/files/9010.pdf",
			}}},
		},
		{
			name: "epoch_seconds_become_utc_time",
			page: page(false, wireRec(9002, "recall_notice", 1762939800)),
			want: source.Page{Docs: []model.Document{{
				ID: "service:9002", Source: service.Name, Type: "recall_notice", Title: "Record 9002",
				IssuedAt: time.Date(2025, time.November, 12, 9, 30, 0, 0, time.UTC), URL: "http://service/files/9002.pdf",
			}}},
		},
		{
			name: "has_more_is_passed_through",
			page: page(true, wireRec(9003, "service_invoice", tsJune)),
			want: source.Page{HasMore: true, Docs: []model.Document{{
				ID: "service:9003", Source: service.Name, Type: "service_invoice", Title: "Record 9003",
				IssuedAt: at(tsJune), URL: "http://service/files/9003.pdf",
			}}},
		},
		{
			name: "upstream_order_is_kept_including_numeric_ties",
			page: page(false, wireRec(10000, "repair_order", tsJune), wireRec(9999, "repair_order", tsJune)),
			want: source.Page{Docs: []model.Document{
				{ID: "service:10000", Source: service.Name, Type: "repair_order", Title: "Record 10000", IssuedAt: at(tsJune), URL: "http://service/files/10000.pdf"},
				{ID: "service:9999", Source: service.Name, Type: "repair_order", Title: "Record 9999", IssuedAt: at(tsJune), URL: "http://service/files/9999.pdf"},
			}},
		},
		{
			name: "empty_page_has_no_docs",
			page: page(false),
			want: source.Page{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.New(&fakeLister{page: tt.page}).FetchPage(t.Context(), source.Request{VIN: vin, Limit: 20})
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
	noID := wireRec(0, "repair_order", tsJune)
	noDate := wireRec(2, "repair_order", 0)
	noURL := wireRec(4, "repair_order", tsPDI)
	noURL.DownloadLink = ""
	good := wireRec(5, "repair_order", tsJune)
	wantGood := model.Document{ID: "service:5", Source: service.Name, Type: "repair_order", Title: "Record 5", IssuedAt: at(tsJune), URL: "http://service/files/5.pdf"}
	tests := []struct {
		name string
		page serviceapi.RecordPage
		want source.Page
	}{
		{name: "missing_id_is_skipped", page: page(false, noID, good), want: source.Page{Docs: []model.Document{wantGood}}},
		{name: "missing_timestamp_is_skipped", page: page(false, noDate, good), want: source.Page{Docs: []model.Document{wantGood}}},
		{name: "missing_link_is_skipped", page: page(false, noURL, good), want: source.Page{Docs: []model.Document{wantGood}}},
		{
			name: "fully_malformed_page_skips_to_the_last_placeable_item",
			page: page(true, noURL, noDate),
			want: source.Page{HasMore: true, SkipTo: &source.Position{IssuedAt: at(tsPDI), ID: "service:4"}},
		},
		{
			name: "fully_malformed_page_without_a_placeable_item_has_no_skip",
			page: page(true, noID, noDate),
			want: source.Page{HasMore: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.New(&fakeLister{page: tt.page}).FetchPage(t.Context(), source.Request{VIN: vin, Limit: 20})
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
		want serviceapi.ListParams
	}{
		{
			name: "first_page_asks_from_the_newest",
			req:  source.Request{VIN: vin, Limit: 20},
			want: serviceapi.ListParams{Limit: 20},
		},
		{
			name: "position_becomes_before_ts_and_upstream_id",
			req:  source.Request{VIN: vin, Limit: 2, After: &source.Position{IssuedAt: at(tsPDI), ID: "service:9010"}},
			want: serviceapi.ListParams{Limit: 2, BeforeTS: tsPDI, BeforeID: "9010"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeLister{page: page(false)}
			ctx := t.Context()
			if _, err := service.New(f).FetchPage(ctx, tt.req); err != nil {
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
		wantDone      bool
		wantPermanent bool
		wantIs        error
	}{
		{name: "not_found_is_done", err: fmt.Errorf("%w: 404", client.ErrNotFound), wantDone: true},
		{name: "rejected_is_permanent", err: fmt.Errorf("%w: 401", client.ErrRejected), wantPermanent: true},
		{name: "too_large_is_permanent", err: client.ErrTooLarge, wantPermanent: true},
		{name: "malformed_is_permanent", err: client.ErrMalformed, wantPermanent: true},
		{name: "unavailable_is_transient", err: fmt.Errorf("%w: 429", client.ErrUnavailable), wantIs: client.ErrUnavailable},
		{name: "cancelled_is_transient", err: context.Canceled, wantIs: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.New(&fakeLister{err: tt.err}).FetchPage(t.Context(), source.Request{VIN: vin, Limit: 20})
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
		{name: "builds_from_a_valid_url", baseURL: "http://mock-service:8082"},
		{name: "rejects_a_bad_url", baseURL: "://bad", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := service.Build(tt.baseURL, httpjson.Client{HTTP: &http.Client{}, MaxBytes: 1})
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && src.Name() != service.Name {
				t.Errorf("Name() = %q, want %q", src.Name(), service.Name)
			}
		})
	}
}
