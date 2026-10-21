package mock_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"docviewer/internal/mock"
)

const salesContract = "../../testdata/contracts/sales_page.json"

func newSalesServer(t *testing.T, fault mock.Fault) *httptest.Server {
	t.Helper()
	store, _ := loadFixtures(t)
	srv := httptest.NewServer(mock.NewSalesHandler(store, mock.NewFaults(fault), slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string) (int, []byte, http.Header) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body, resp.Header
}

// jsonValue decodes JSON into a generic value, so two documents compare
// equal whatever their formatting.
func jsonValue(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	return v
}

func TestSalesDocuments(t *testing.T) {
	contract, err := os.ReadFile(salesContract)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		path       string
		fault      mock.Fault
		wantStatus int
		wantJSON   []byte   // the exact body, compared as JSON values
		wantIDs    []string // else: the document IDs on the page, in order
		wantMore   *bool    // and hasMore
		wantVIN    string   // and vehicleVin
	}{
		{
			name:       "first_page_matches_the_contract_golden",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?limit=20",
			wantStatus: http.StatusOK,
			wantJSON:   contract,
		},
		{
			name:       "limit_defaults_to_20",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents",
			wantStatus: http.StatusOK,
			wantJSON:   contract,
		},
		{
			name:       "limit_cuts_the_page_and_sets_has_more",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?limit=2",
			wantStatus: http.StatusOK,
			wantIDs:    []string{"S-1005", "S-1004"},
			wantMore:   ptr(true),
			wantVIN:    vinBoth,
		},
		{
			name:       "before_pages_by_date_then_id",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?limit=2&before=2024-03-05|S-1004",
			wantStatus: http.StatusOK,
			wantIDs:    []string{"S-1003", "S-1002"},
			wantMore:   ptr(true),
			wantVIN:    vinBoth,
		},
		{
			name:       "before_within_a_same_date_tie",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?limit=2&before=2024-03-02|S-1002",
			wantStatus: http.StatusOK,
			wantIDs:    []string{"S-1001"},
			wantMore:   ptr(false),
			wantVIN:    vinBoth,
		},
		{
			name:       "before_url_encoded_separator",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?limit=1&before=2024-03-02%7CS-1002",
			wantStatus: http.StatusOK,
			wantIDs:    []string{"S-1001"},
			wantMore:   ptr(false),
			wantVIN:    vinBoth,
		},
		{
			name:       "unknown_vin_is_404",
			path:       "/api/v1/vehicles/" + vinNone + "/documents",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "vin_only_in_service_is_404_here",
			path:       "/api/v1/vehicles/" + vinService + "/documents",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "limit_not_a_number_is_400",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?limit=many",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "limit_zero_is_400",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?limit=0",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "limit_over_100_is_400",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?limit=101",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "before_without_separator_is_400",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?before=2024-03-02",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "before_with_bad_date_is_400",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents?before=March|S-1002",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "fixed_status_fault_applies_to_the_api",
			path:       "/api/v1/vehicles/" + vinBoth + "/documents",
			fault:      mock.Fault{Status: http.StatusForbidden},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "unknown_route_is_404",
			path:       "/api/v1/vehicles",
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newSalesServer(t, tt.fault)
			status, body, _ := get(t, srv, tt.path)
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", status, tt.wantStatus, body)
			}
			if tt.wantJSON != nil {
				if got, want := jsonValue(t, body), jsonValue(t, tt.wantJSON); !reflect.DeepEqual(got, want) {
					t.Errorf("body = %s\nwant %s", body, tt.wantJSON)
				}
			}
			if tt.wantIDs != nil {
				var page mock.SalesPage
				if err := json.Unmarshal(body, &page); err != nil {
					t.Fatal(err)
				}
				ids := make([]string, len(page.Documents))
				for i, d := range page.Documents {
					ids[i] = d.DocID
				}
				if !reflect.DeepEqual(ids, tt.wantIDs) {
					t.Errorf("ids = %v, want %v", ids, tt.wantIDs)
				}
				if page.HasMore != *tt.wantMore {
					t.Errorf("hasMore = %v, want %v", page.HasMore, *tt.wantMore)
				}
				if page.VehicleVIN != tt.wantVIN {
					t.Errorf("vehicleVin = %q, want %q", page.VehicleVIN, tt.wantVIN)
				}
			}
		})
	}
}

// TestSalesWalk pages through a VIN with before until hasMore is false and
// checks nothing is skipped or repeated.
func TestSalesWalk(t *testing.T) {
	tests := []struct {
		name  string
		vin   string
		limit string
		want  []string
	}{
		{name: "page_size_1", vin: vinBoth, limit: "1", want: []string{"S-1005", "S-1004", "S-1003", "S-1002", "S-1001"}},
		{name: "page_size_2", vin: vinBoth, limit: "2", want: []string{"S-1005", "S-1004", "S-1003", "S-1002", "S-1001"}},
		{name: "page_size_3_over_a_tie", vin: vinMany, limit: "3", want: []string{"S-4008", "S-4007", "S-4006", "S-4005", "S-4004", "S-4003", "S-4002", "S-4001"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newSalesServer(t, mock.Fault{})
			var got []string
			before := ""
			for range 100 { // bounded, so a paging bug can't loop forever
				path := "/api/v1/vehicles/" + tt.vin + "/documents?limit=" + tt.limit
				if before != "" {
					path += "&before=" + before
				}
				status, body, _ := get(t, srv, path)
				if status != http.StatusOK {
					t.Fatalf("status = %d (body %s)", status, body)
				}
				var page mock.SalesPage
				if err := json.Unmarshal(body, &page); err != nil {
					t.Fatal(err)
				}
				for _, d := range page.Documents {
					got = append(got, d.DocID)
				}
				if !page.HasMore {
					break
				}
				last := page.Documents[len(page.Documents)-1]
				before = last.CreatedDate + "%7C" + last.DocID
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("walk = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSalesFiles(t *testing.T) {
	tests := []struct {
		name            string
		path            string
		wantStatus      int
		wantContentType string
	}{
		{name: "any_document_file_is_the_placeholder_pdf", path: "/files/S-1001.pdf", wantStatus: http.StatusOK, wantContentType: "application/pdf"},
		{name: "another_id_gets_the_same_pdf", path: "/files/does-not-matter.pdf", wantStatus: http.StatusOK, wantContentType: "application/pdf"},
		{name: "health", path: "/healthz", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newSalesServer(t, mock.Fault{})
			status, body, header := get(t, srv, tt.path)
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if tt.wantContentType != "" {
				if got := header.Get("Content-Type"); got != tt.wantContentType {
					t.Errorf("Content-Type = %q, want %q", got, tt.wantContentType)
				}
				if !bytes.HasPrefix(body, []byte("%PDF-")) {
					t.Errorf("body does not start with a PDF header: %q", body[:min(len(body), 8)])
				}
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
