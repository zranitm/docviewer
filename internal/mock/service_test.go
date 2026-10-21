package mock_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"testing"

	"docviewer/internal/mock"
)

const serviceContract = "../../testdata/contracts/service_page.json"

func newServiceServer(t *testing.T, fault mock.Fault) *httptest.Server {
	t.Helper()
	_, store := loadFixtures(t)
	srv := httptest.NewServer(mock.NewServiceHandler(store, mock.NewFaults(fault), slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	return srv
}

func TestServiceRecords(t *testing.T) {
	contract, err := os.ReadFile(serviceContract)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		path       string
		fault      mock.Fault
		wantStatus int
		wantJSON   []byte  // the exact body, compared as JSON values
		wantIDs    []int64 // else: the record IDs on the page, in order
		wantMore   *bool
	}{
		{
			name:       "first_page_matches_the_contract_golden",
			path:       "/v2/records?vin=" + vinBoth + "&limit=20",
			wantStatus: http.StatusOK,
			wantJSON:   contract,
		},
		{
			name:       "limit_defaults_to_20",
			path:       "/v2/records?vin=" + vinBoth,
			wantStatus: http.StatusOK,
			wantJSON:   contract,
		},
		{
			name:       "limit_cuts_the_page_and_sets_has_more",
			path:       "/v2/records?vin=" + vinBoth + "&limit=2",
			wantStatus: http.StatusOK,
			wantIDs:    []int64{9003, 9001},
			wantMore:   ptr(true),
		},
		{
			name:       "before_pages_by_timestamp_then_numeric_id",
			path:       "/v2/records?vin=" + vinBoth + "&limit=2&before_ts=1780300800&before_id=9003",
			wantStatus: http.StatusOK,
			wantIDs:    []int64{9001, 9002},
			wantMore:   ptr(true),
		},
		{
			name:       "before_at_the_numeric_trap_orders_as_numbers",
			path:       "/v2/records?vin=" + vinMany + "&limit=3&before_ts=" + strconv.FormatInt(tieTS(t), 10) + "&before_id=10001",
			wantStatus: http.StatusOK,
			wantIDs:    []int64{10000, 9999, 9998},
			wantMore:   ptr(true),
		},
		{
			name:       "missing_vin_is_400",
			path:       "/v2/records?limit=2",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown_vin_is_404",
			path:       "/v2/records?vin=" + vinNone,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "vin_only_in_sales_is_404_here",
			path:       "/v2/records?vin=" + vinSales,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "before_id_without_before_ts_is_400",
			path:       "/v2/records?vin=" + vinBoth + "&before_id=9003",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "before_ts_without_before_id_is_400",
			path:       "/v2/records?vin=" + vinBoth + "&before_ts=1780300800",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "before_ts_not_a_number_is_400",
			path:       "/v2/records?vin=" + vinBoth + "&before_ts=june&before_id=9003",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "limit_over_100_is_400",
			path:       "/v2/records?vin=" + vinBoth + "&limit=101",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "fixed_status_fault_applies_to_the_api",
			path:       "/v2/records?vin=" + vinBoth,
			fault:      mock.Fault{Status: http.StatusUnauthorized},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "file_is_served",
			path:       "/files/9001.pdf",
			wantStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServiceServer(t, tt.fault)
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
				var page mock.ServicePage
				if err := json.Unmarshal(body, &page); err != nil {
					t.Fatal(err)
				}
				ids := make([]int64, len(page.Records))
				for i, r := range page.Records {
					ids[i] = r.RecordID
				}
				if !reflect.DeepEqual(ids, tt.wantIDs) {
					t.Errorf("ids = %v, want %v", ids, tt.wantIDs)
				}
				if page.HasMore != *tt.wantMore {
					t.Errorf("has_more = %v, want %v", page.HasMore, *tt.wantMore)
				}
			}
		})
	}
}

// tieTS is the timestamp shared by records 9998..10001 of vinMany.
func tieTS(t *testing.T) int64 {
	t.Helper()
	_, store := loadFixtures(t)
	recs, _ := store.Page(vinMany, nil, 1000)
	for _, r := range recs {
		if r.RecordID == 10001 {
			return r.CreatedAt
		}
	}
	t.Fatal("record 10001 not in the fixture")
	return 0
}

// TestServiceWalk pages through a VIN with before_ts and before_id until
// has_more is false: every record once, in the store's order.
func TestServiceWalk(t *testing.T) {
	_, store := loadFixtures(t)
	all := func(vin string) []int64 {
		recs, _ := store.Page(vin, nil, 1000)
		ids := make([]int64, len(recs))
		for i, r := range recs {
			ids[i] = r.RecordID
		}
		return ids
	}
	tests := []struct {
		name  string
		vin   string
		limit string
		want  []int64
	}{
		{name: "page_size_1", vin: vinBoth, limit: "1", want: all(vinBoth)},
		{name: "page_size_2_over_a_tie", vin: vinBoth, limit: "2", want: all(vinBoth)},
		{name: "page_size_7_over_120_records_and_the_numeric_trap", vin: vinMany, limit: "7", want: all(vinMany)},
		{name: "page_size_100", vin: vinMany, limit: "100", want: all(vinMany)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServiceServer(t, mock.Fault{})
			var got []int64
			before := ""
			for range 200 { // bounded, so a paging bug can't loop forever
				path := "/v2/records?vin=" + tt.vin + "&limit=" + tt.limit + before
				status, body, _ := get(t, srv, path)
				if status != http.StatusOK {
					t.Fatalf("status = %d (body %s)", status, body)
				}
				var page mock.ServicePage
				if err := json.Unmarshal(body, &page); err != nil {
					t.Fatal(err)
				}
				for _, r := range page.Records {
					got = append(got, r.RecordID)
				}
				if !page.HasMore {
					break
				}
				last := page.Records[len(page.Records)-1]
				before = "&before_ts=" + strconv.FormatInt(last.CreatedAt, 10) + "&before_id=" + strconv.FormatInt(last.RecordID, 10)
			}
			if len(got) != len(tt.want) || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("walk returned %d records, want %d; first difference at %d", len(got), len(tt.want), firstDiff(got, tt.want))
			}
		})
	}
}

func firstDiff(a, b []int64) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}
