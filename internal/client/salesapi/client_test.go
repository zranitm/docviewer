package salesapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"

	"docviewer/internal/client"
	"docviewer/internal/client/salesapi"
	"docviewer/pkg/httpjson"
)

const (
	vin          = "1M8GDM9AXKP042788"
	contractPage = "../../../testdata/contracts/sales_page.json"
)

// contractPageValues is what testdata/contracts/sales_page.json decodes to.
// The mock's tests encode their response for the same VIN to that file, so
// the two sides can't drift apart silently.
func contractPageValues() salesapi.DocumentPage {
	return salesapi.DocumentPage{
		VehicleVIN: vin,
		HasMore:    false,
		Documents: []salesapi.Document{
			{DocID: "S-1005", DocType: "REGISTRATION", Title: "Vehicle registration", CreatedDate: "2024-03-10", FileURL: "http://localhost:8081/files/S-1005.pdf"},
			{DocID: "S-1004", DocType: "SALES_INVOICE", Title: "Sales invoice", CreatedDate: "2024-03-05", FileURL: "http://localhost:8081/files/S-1004.pdf", Notes: "Paid in full", FileSHA256: "3b7c1f0a9d2e4c6b8a0f1e2d3c4b5a69788796a5b4c3d2e1f0a9b8c7d6e5f4a3"},
			{DocID: "S-1003", DocType: "FINANCE_CONTRACT", Title: "36-month finance plan", CreatedDate: "2024-03-02", FileURL: "http://localhost:8081/files/S-1003.pdf"},
			{DocID: "S-1002", DocType: "PRE_DELIVERY_INSPECTION", Title: "PDI checklist, signed", CreatedDate: "2024-03-02", FileURL: "http://localhost:8081/files/S-1002.pdf", FileSHA256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
			{DocID: "S-1001", DocType: "PURCHASE_AGREEMENT", Title: "Purchase agreement", CreatedDate: "2024-03-02", FileURL: "http://localhost:8081/files/S-1001.pdf"},
		},
	}
}

func TestListDocuments(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string // path prefix on the base URL
		params    salesapi.ListParams
		status    int    // response status; 0 means 200
		body      string // response body; empty means the contract page
		wantPath  string
		wantQuery url.Values
		want      salesapi.DocumentPage
		errIs     error
	}{
		{
			name:      "first_page_sends_limit_only",
			params:    salesapi.ListParams{Limit: 20},
			wantPath:  "/api/v1/vehicles/" + vin + "/documents",
			wantQuery: url.Values{"limit": {"20"}},
			want:      contractPageValues(),
		},
		{
			name:      "after_a_position_sends_before_as_date_and_id",
			params:    salesapi.ListParams{Limit: 2, BeforeDate: "2024-03-02", BeforeID: "S-1002"},
			wantPath:  "/api/v1/vehicles/" + vin + "/documents",
			wantQuery: url.Values{"limit": {"2"}, "before": {"2024-03-02|S-1002"}},
			want:      contractPageValues(),
		},
		{
			name:      "base_url_path_prefix_is_kept",
			prefix:    "/sales",
			params:    salesapi.ListParams{Limit: 20},
			wantPath:  "/sales/api/v1/vehicles/" + vin + "/documents",
			wantQuery: url.Values{"limit": {"20"}},
			want:      contractPageValues(),
		},
		{
			name:      "decodes_an_empty_page",
			params:    salesapi.ListParams{Limit: 20},
			body:      `{"vehicleVin":"` + vin + `","hasMore":false,"documents":[]}`,
			wantPath:  "/api/v1/vehicles/" + vin + "/documents",
			wantQuery: url.Values{"limit": {"20"}},
			want:      salesapi.DocumentPage{VehicleVIN: vin, Documents: []salesapi.Document{}},
		},
		{
			name:   "not_found_is_the_client_sentinel",
			params: salesapi.ListParams{Limit: 20},
			status: http.StatusNotFound,
			body:   `{"error":"unknown vehicle"}`,
			errIs:  client.ErrNotFound,
		},
		{
			name:   "server_error_is_the_client_sentinel",
			params: salesapi.ListParams{Limit: 20},
			status: http.StatusBadGateway,
			body:   `oops`,
			errIs:  client.ErrUnavailable,
		},
		{
			name:   "malformed_body_is_the_client_sentinel",
			params: salesapi.ListParams{Limit: 20},
			body:   `{"vehicleVin":`,
			errIs:  client.ErrMalformed,
		},
	}
	contract, err := os.ReadFile(contractPage)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotQuery url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotQuery = r.URL.Path, r.URL.Query()
				status, body := tt.status, []byte(tt.body)
				if status == 0 {
					status = http.StatusOK
				}
				if tt.body == "" {
					body = contract
				}
				w.WriteHeader(status)
				_, _ = w.Write(body)
			}))
			t.Cleanup(srv.Close)
			c, err := salesapi.New(srv.URL+tt.prefix, httpjson.Client{HTTP: srv.Client(), MaxBytes: 1 << 20})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			got, err := c.ListDocuments(t.Context(), vin, tt.params)
			if tt.errIs != nil {
				if !errors.Is(err, tt.errIs) {
					t.Fatalf("ListDocuments() error = %v, want errors.Is %v", err, tt.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListDocuments() error = %v", err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if !reflect.DeepEqual(gotQuery, tt.wantQuery) {
				t.Errorf("query = %v, want %v", gotQuery, tt.wantQuery)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ListDocuments() = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// TestContractRoundTrip pins the wire model to the golden file both ways:
// decoding then encoding gives the same JSON, so no field or tag is missing.
func TestContractRoundTrip(t *testing.T) {
	raw, err := os.ReadFile(contractPage)
	if err != nil {
		t.Fatal(err)
	}
	var page salesapi.DocumentPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the JSON:\n got %s\nwant %s", encoded, raw)
	}
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "valid_url", baseURL: "http://mock-sales:8081"},
		{name: "unparseable_url", baseURL: "://mock-sales", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := salesapi.New(tt.baseURL, httpjson.Client{HTTP: &http.Client{}, MaxBytes: 1})
			if (err != nil) != tt.wantErr {
				t.Errorf("New(%q) error = %v, wantErr %v", tt.baseURL, err, tt.wantErr)
			}
		})
	}
}
