package serviceapi_test

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
	"docviewer/internal/client/serviceapi"
	"docviewer/pkg/httpjson"
)

const (
	vin          = "1M8GDM9AXKP042788"
	contractPage = "../../../testdata/contracts/service_page.json"
)

// contractPageValues is what testdata/contracts/service_page.json decodes to.
// The mock's tests encode their response for the same VIN to that file, so
// the two sides can't drift apart silently.
func contractPageValues() serviceapi.RecordPage {
	return serviceapi.RecordPage{
		HasMore: false,
		Records: []serviceapi.Record{
			{RecordID: 9003, Category: "service_invoice", Description: "Invoice for 10,000 km service", CreatedAt: 1780300800, DownloadLink: "http://localhost:8082/files/9003.pdf", WorkNotes: "Labour and parts, paid by card"},
			{RecordID: 9001, Category: "repair_order", Description: "10,000 km service", CreatedAt: 1780300800, DownloadLink: "http://localhost:8082/files/9001.pdf", WorkNotes: "Oil and filter changed, brake pads replaced"},
			{RecordID: 9002, Category: "recall_notice", Description: "Airbag inflator recall", CreatedAt: 1762939800, DownloadLink: "http://localhost:8082/files/9002.pdf", WorkNotes: "Driver airbag replaced under recall campaign"},
			{RecordID: 9004, Category: "inspection_report", Description: "Annual inspection", CreatedAt: 1747749600, DownloadLink: "http://localhost:8082/files/9004.pdf", WorkNotes: "Passed; rear tyres near the wear limit"},
			{RecordID: 9010, Category: "pre_delivery_inspection", Description: "PDI checklist, signed", CreatedAt: 1709337600, DownloadLink: "http://localhost:8082/files/9010.pdf", FileSHA256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
		},
	}
}

func TestListRecords(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string // path prefix on the base URL
		params    serviceapi.ListParams
		status    int    // response status; 0 means 200
		body      string // response body; empty means the contract page
		wantPath  string
		wantQuery url.Values
		want      serviceapi.RecordPage
		errIs     error
	}{
		{
			name:      "first_page_sends_vin_and_limit_only",
			params:    serviceapi.ListParams{Limit: 20},
			wantPath:  "/v2/records",
			wantQuery: url.Values{"vin": {vin}, "limit": {"20"}},
			want:      contractPageValues(),
		},
		{
			name:      "after_a_position_sends_before_ts_and_before_id",
			params:    serviceapi.ListParams{Limit: 2, BeforeTS: 1709337600, BeforeID: "9010"},
			wantPath:  "/v2/records",
			wantQuery: url.Values{"vin": {vin}, "limit": {"2"}, "before_ts": {"1709337600"}, "before_id": {"9010"}},
			want:      contractPageValues(),
		},
		{
			name:      "base_url_path_prefix_is_kept",
			prefix:    "/service",
			params:    serviceapi.ListParams{Limit: 20},
			wantPath:  "/service/v2/records",
			wantQuery: url.Values{"vin": {vin}, "limit": {"20"}},
			want:      contractPageValues(),
		},
		{
			name:      "decodes_an_empty_page",
			params:    serviceapi.ListParams{Limit: 20},
			body:      `{"has_more":false,"records":[]}`,
			wantPath:  "/v2/records",
			wantQuery: url.Values{"vin": {vin}, "limit": {"20"}},
			want:      serviceapi.RecordPage{Records: []serviceapi.Record{}},
		},
		{
			name:   "not_found_is_the_client_sentinel",
			params: serviceapi.ListParams{Limit: 20},
			status: http.StatusNotFound,
			body:   `{"error":"no such vehicle"}`,
			errIs:  client.ErrNotFound,
		},
		{
			name:   "rejected_is_the_client_sentinel",
			params: serviceapi.ListParams{Limit: 20},
			status: http.StatusForbidden,
			body:   `{"error":"forbidden"}`,
			errIs:  client.ErrRejected,
		},
		{
			name:   "malformed_body_is_the_client_sentinel",
			params: serviceapi.ListParams{Limit: 20},
			body:   `{"records":[{"record_id":"nine"}]}`,
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
			c, err := serviceapi.New(srv.URL+tt.prefix, httpjson.Client{HTTP: srv.Client(), MaxBytes: 1 << 20})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			got, err := c.ListRecords(t.Context(), vin, tt.params)
			if tt.errIs != nil {
				if !errors.Is(err, tt.errIs) {
					t.Fatalf("ListRecords() error = %v, want errors.Is %v", err, tt.errIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListRecords() error = %v", err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if !reflect.DeepEqual(gotQuery, tt.wantQuery) {
				t.Errorf("query = %v, want %v", gotQuery, tt.wantQuery)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ListRecords() = %+v\nwant %+v", got, tt.want)
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
	var page serviceapi.RecordPage
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
		{name: "valid_url", baseURL: "http://mock-service:8082"},
		{name: "unparseable_url", baseURL: "://mock-service", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := serviceapi.New(tt.baseURL, httpjson.Client{HTTP: &http.Client{}, MaxBytes: 1})
			if (err != nil) != tt.wantErr {
				t.Errorf("New(%q) error = %v, wantErr %v", tt.baseURL, err, tt.wantErr)
			}
		})
	}
}
