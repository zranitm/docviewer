package mock_test

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"docviewer/internal/mock"
)

// rec is a minimal record: its key only.
type rec struct {
	at time.Time
	id string
}

func day(d int) time.Time { return time.Date(2024, time.March, d, 0, 0, 0, 0, time.UTC) }

func key(r rec) mock.Key { return mock.Key{At: r.at, ID: r.id} }

func ids(rs []rec) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.id
	}
	return out
}

func TestStorePage(t *testing.T) {
	const vin = "V"
	// Added out of order on purpose; the store sorts newest first.
	strings := []rec{{day(1), "A"}, {day(3), "C"}, {day(2), "B1"}, {day(2), "B2"}, {day(5), "E"}}
	numbers := []rec{{day(1), "9998"}, {day(1), "10000"}, {day(1), "9999"}, {day(1), "10001"}, {day(2), "10002"}}
	tests := []struct {
		name     string
		recs     []rec
		cmpID    func(a, b string) int
		after    *mock.Key
		limit    int
		want     []string
		wantMore bool
	}{
		{
			name:     "first_page_newest_first",
			recs:     strings,
			cmpID:    mock.CompareStringIDs,
			limit:    2,
			want:     []string{"E", "C"},
			wantMore: true,
		},
		{
			name:     "same_date_ties_order_by_id_descending",
			recs:     strings,
			cmpID:    mock.CompareStringIDs,
			after:    &mock.Key{At: day(3), ID: "C"},
			limit:    2,
			want:     []string{"B2", "B1"},
			wantMore: true,
		},
		{
			name:     "after_a_tie_continues_within_the_date",
			recs:     strings,
			cmpID:    mock.CompareStringIDs,
			after:    &mock.Key{At: day(2), ID: "B2"},
			limit:    10,
			want:     []string{"B1", "A"},
			wantMore: false,
		},
		{
			name:     "last_page_reports_no_more",
			recs:     strings,
			cmpID:    mock.CompareStringIDs,
			after:    &mock.Key{At: day(2), ID: "B1"},
			limit:    1,
			want:     []string{"A"},
			wantMore: false,
		},
		{
			name:  "after_the_last_record_is_empty",
			recs:  strings,
			cmpID: mock.CompareStringIDs,
			after: &mock.Key{At: day(1), ID: "A"},
			limit: 5,
			want:  []string{},
		},
		{
			name:     "after_a_deleted_anchor_continues_from_its_place",
			recs:     strings,
			cmpID:    mock.CompareStringIDs,
			after:    &mock.Key{At: day(4), ID: "D"}, // never existed
			limit:    1,
			want:     []string{"C"},
			wantMore: true,
		},
		{
			name:  "limit_beyond_the_end_returns_the_rest",
			recs:  strings,
			cmpID: mock.CompareStringIDs,
			limit: 50,
			want:  []string{"E", "C", "B2", "B1", "A"},
		},
		{
			name:  "unknown_vin_is_empty",
			recs:  nil,
			cmpID: mock.CompareStringIDs,
			limit: 5,
			want:  []string{},
		},
		{
			name:     "numeric_ids_order_as_numbers_not_text",
			recs:     numbers,
			cmpID:    mock.CompareNumericIDs,
			limit:    3,
			want:     []string{"10002", "10001", "10000"},
			wantMore: true,
		},
		{
			name:  "numeric_ids_after_a_key_compare_as_numbers",
			recs:  numbers,
			cmpID: mock.CompareNumericIDs,
			after: &mock.Key{At: day(1), ID: "10000"},
			limit: 5,
			want:  []string{"9999", "9998"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mock.NewStore(key, tt.cmpID)
			if tt.recs != nil {
				s.Add(vin, tt.recs...)
			}
			got, more := s.Page(vin, tt.after, tt.limit)
			if gotIDs := ids(got); !reflect.DeepEqual(gotIDs, tt.want) {
				t.Errorf("Page() ids = %v, want %v", gotIDs, tt.want)
			}
			if more != tt.wantMore {
				t.Errorf("Page() more = %v, want %v", more, tt.wantMore)
			}
		})
	}
}

func TestStoreHas(t *testing.T) {
	s := mock.NewStore(key, mock.CompareStringIDs)
	s.Add("KNOWN", rec{day(1), "A"})
	tests := []struct {
		name string
		vin  string
		want bool
	}{
		{name: "known_vin", vin: "KNOWN", want: true},
		{name: "unknown_vin", vin: "OTHER", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Has(tt.vin); got != tt.want {
				t.Errorf("Has(%q) = %v, want %v", tt.vin, got, tt.want)
			}
		})
	}
}

func TestCompareNumericIDs(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{name: "fewer_digits_is_smaller", a: "9999", b: "10000", want: -1},
		{name: "more_digits_is_larger", a: "10000", b: "9999", want: 1},
		{name: "equal_numbers", a: "42", b: "42", want: 0},
		{name: "non_numeric_falls_back_to_text", a: "b", b: "a", want: 1},
		{name: "number_before_text", a: "7", b: "x", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mock.CompareNumericIDs(tt.a, tt.b); got != tt.want {
				t.Errorf("CompareNumericIDs(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			// Agrees with the integer order whenever both parse.
			if ai, errA := strconv.Atoi(tt.a); errA == nil {
				if bi, errB := strconv.Atoi(tt.b); errB == nil {
					want := 0
					switch {
					case ai < bi:
						want = -1
					case ai > bi:
						want = 1
					}
					if got := mock.CompareNumericIDs(tt.a, tt.b); got != want {
						t.Errorf("CompareNumericIDs(%q, %q) = %d, integer order says %d", tt.a, tt.b, got, want)
					}
				}
			}
		})
	}
}
