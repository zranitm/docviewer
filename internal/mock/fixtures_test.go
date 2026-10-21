package mock_test

import (
	"bytes"
	"slices"
	"strconv"
	"testing"

	"docviewer/internal/mock"
)

// The fixture VINs (LLD section 10): one in both systems, one only in
// Sales, one only in Service, one with 120 Service records over many pages,
// and one in neither.
const (
	vinBoth    = "1M8GDM9AXKP042788"
	vinSales   = "WVWZZZ1JZXW000002"
	vinService = "JH4KA7561PC000003"
	vinMany    = "5YJ3E1EA7KF000004"
	vinNone    = "1HGCM82633A000005"

	salesBase   = "http://localhost:8081"
	serviceBase = "http://localhost:8082"
)

func loadFixtures(t *testing.T) (*mock.Store[mock.SalesDocument], *mock.Store[mock.ServiceRecord]) {
	t.Helper()
	sales, err := mock.LoadSales(bytes.NewReader(mock.SalesFixture), salesBase)
	if err != nil {
		t.Fatalf("LoadSales() error = %v", err)
	}
	service, err := mock.LoadService(bytes.NewReader(mock.ServiceFixture), serviceBase)
	if err != nil {
		t.Fatalf("LoadService() error = %v", err)
	}
	return sales, service
}

func TestFixturesPerVIN(t *testing.T) {
	sales, service := loadFixtures(t)
	tests := []struct {
		name        string
		vin         string
		wantSales   int // documents; -1 means the VIN is unknown to Sales
		wantService int // records; -1 means the VIN is unknown to Service
	}{
		{name: "in_both_systems", vin: vinBoth, wantSales: 5, wantService: 5},
		{name: "only_in_sales", vin: vinSales, wantSales: 3, wantService: -1},
		{name: "only_in_service", vin: vinService, wantSales: -1, wantService: 4},
		{name: "many_service_records", vin: vinMany, wantSales: 8, wantService: 120},
		{name: "in_neither", vin: vinNone, wantSales: -1, wantService: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sales.Has(tt.vin); got != (tt.wantSales >= 0) {
				t.Errorf("sales.Has() = %v, want %v", got, tt.wantSales >= 0)
			}
			if got := service.Has(tt.vin); got != (tt.wantService >= 0) {
				t.Errorf("service.Has() = %v, want %v", got, tt.wantService >= 0)
			}
			docs, more := sales.Page(tt.vin, nil, 1000)
			if more || len(docs) != max(tt.wantSales, 0) {
				t.Errorf("sales.Page() = %d documents, more %v; want %d, false", len(docs), more, max(tt.wantSales, 0))
			}
			recs, more := service.Page(tt.vin, nil, 1000)
			if more || len(recs) != max(tt.wantService, 0) {
				t.Errorf("service.Page() = %d records, more %v; want %d, false", len(recs), more, max(tt.wantService, 0))
			}
		})
	}
}

// TestFixturesAreSortedNewestFirst walks every VIN and checks each store's
// own order: date descending, then ID descending, numerically for Service.
func TestFixturesAreSortedNewestFirst(t *testing.T) {
	sales, service := loadFixtures(t)
	tests := []struct {
		name string
		vin  string
	}{
		{name: "in_both_systems", vin: vinBoth},
		{name: "only_in_sales", vin: vinSales},
		{name: "only_in_service", vin: vinService},
		{name: "many_service_records", vin: vinMany},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docs, _ := sales.Page(tt.vin, nil, 1000)
			for i := 1; i < len(docs); i++ {
				prev, cur := mock.SalesKey(docs[i-1]), mock.SalesKey(docs[i])
				if cur.At.After(prev.At) || (cur.At.Equal(prev.At) && mock.CompareStringIDs(cur.ID, prev.ID) >= 0) {
					t.Errorf("sales out of order at %d: %v then %v", i, prev, cur)
				}
			}
			recs, _ := service.Page(tt.vin, nil, 1000)
			for i := 1; i < len(recs); i++ {
				prev, cur := mock.ServiceKey(recs[i-1]), mock.ServiceKey(recs[i])
				if cur.At.After(prev.At) || (cur.At.Equal(prev.At) && mock.CompareNumericIDs(cur.ID, prev.ID) >= 0) {
					t.Errorf("service out of order at %d: %v then %v", i, prev, cur)
				}
			}
		})
	}
}

// TestFixtureTieGroups pins the same-timestamp groups the paging tests rely
// on, including the numeric-ID trap around 9999/10000.
func TestFixtureTieGroups(t *testing.T) {
	_, service := loadFixtures(t)
	recs, _ := service.Page(vinMany, nil, 1000)
	tests := []struct {
		name string
		ids  []int64 // consecutive, in this order
	}{
		{name: "numeric_trap_across_four_and_five_digits", ids: []int64{10001, 10000, 9999, 9998}},
		{name: "three_way_tie", ids: []int64{10052, 10051, 10050}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := slices.IndexFunc(recs, func(r mock.ServiceRecord) bool { return r.RecordID == tt.ids[0] })
			if start < 0 {
				t.Fatalf("record %d not found", tt.ids[0])
			}
			for i, id := range tt.ids {
				r := recs[start+i]
				if r.RecordID != id {
					t.Errorf("position %d: record %d, want %d", start+i, r.RecordID, id)
				}
				if r.CreatedAt != recs[start].CreatedAt {
					t.Errorf("record %d: created_at %d, want the group's %d", id, r.CreatedAt, recs[start].CreatedAt)
				}
			}
		})
	}
}

func TestFixtureLinksUsePublicBaseURL(t *testing.T) {
	sales, service := loadFixtures(t)
	docs, _ := sales.Page(vinBoth, nil, 1)
	recs, _ := service.Page(vinBoth, nil, 1)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "sales_file_url", got: docs[0].FileURL, want: salesBase + "/files/" + docs[0].DocID + ".pdf"},
		{name: "service_download_link", got: recs[0].DownloadLink, want: serviceBase + "/files/" + strconv.FormatInt(recs[0].RecordID, 10) + ".pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("link = %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestLoadRejectsBadFixtures(t *testing.T) {
	tests := []struct {
		name  string
		sales string // JSON for LoadSales; empty means test LoadService instead
		svc   string
	}{
		{name: "sales_invalid_json", sales: `[{"vin":`},
		{name: "sales_bad_date", sales: `[{"vin":"V","documents":[{"docId":"S-1","docType":"X","title":"t","createdDate":"yesterday"}]}]`},
		{name: "service_invalid_json", svc: `{"records":`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.sales != "" {
				_, err = mock.LoadSales(bytes.NewReader([]byte(tt.sales)), salesBase)
			} else {
				_, err = mock.LoadService(bytes.NewReader([]byte(tt.svc)), serviceBase)
			}
			if err == nil {
				t.Fatal("Load error = nil, want an error")
			}
		})
	}
}
