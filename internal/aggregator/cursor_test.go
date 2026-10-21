package aggregator_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"docviewer/internal/aggregator"
	"docviewer/internal/aggregator/source"
)

// token encodes a JSON document the way the service does, so the strict
// decoder can be probed with hand-made cursors.
func token(jsonDoc string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(jsonDoc))
}

// decodeToken reads a cursor back into a generic JSON value.
func decodeToken(t *testing.T, cursor string) any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("cursor is not base64url: %v", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("cursor is not JSON: %v", err)
	}
	return v
}

// TestCursorFormat pins the token to HLD section 7: v, query.vin and one
// stream per source with its state and, for "after", a [date, id] key.
func TestCursorFormat(t *testing.T) {
	tests := []struct {
		name    string
		service *fakeSource
		limit   int
		want    map[string]any
	}{
		{
			name:    "after_and_start",
			service: &fakeSource{name: serviceName, docs: serviceDocs},
			limit:   1, // only sales:S-5 (day 9) fits; service returned 10002 but it was cut
			want: map[string]any{
				"v":     float64(1),
				"query": map[string]any{"vin": string(vin)},
				"streams": map[string]any{
					"sales":   map[string]any{"state": "after", "key": []any{"2024-03-09T00:00:00Z", "sales:S-5"}},
					"service": map[string]any{"state": "start"},
				},
			},
		},
		{
			name:    "after_and_done",
			service: &fakeSource{name: serviceName}, // nothing: done after the first page
			limit:   2,
			want: map[string]any{
				"v":     float64(1),
				"query": map[string]any{"vin": string(vin)},
				"streams": map[string]any{
					"sales":   map[string]any{"state": "after", "key": []any{"2024-03-05T00:00:00Z", "sales:S-4"}},
					"service": map[string]any{"state": "done"},
				},
			},
		},
		{
			name:    "after_and_unavailable",
			service: &fakeSource{name: serviceName, fail: func(int, source.Request) error { return source.ErrPermanent }},
			limit:   2,
			want: map[string]any{
				"v":     float64(1),
				"query": map[string]any{"vin": string(vin)},
				"streams": map[string]any{
					"sales":   map[string]any{"state": "after", "key": []any{"2024-03-05T00:00:00Z", "sales:S-4"}},
					"service": map[string]any{"state": "unavailable"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sales := &fakeSource{name: salesName, docs: salesDocs}
			res, err := build(t, sales, tt.service).List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: tt.limit})
			if err != nil {
				t.Fatal(err)
			}
			if got := decodeToken(t, res.NextCursor); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("cursor = %v\nwant %v", got, tt.want)
			}
		})
	}
}

func TestCursorDecoding(t *testing.T) {
	const good = `{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"after","key":["2024-03-09T00:00:00Z","sales:S-5"]},"service":{"state":"start"}}}`
	tests := []struct {
		name     string
		cursor   string
		errIs    error    // nil means the cursor is accepted
		wantNext []string // the entries of the page it resumes, when accepted
	}{
		{name: "round_trip_resumes", cursor: token(good), wantNext: []string{"service:10002", "sales:S-4"}},
		{name: "not_base64", cursor: "%%%not-base64%%%", errIs: aggregator.ErrInvalidCursor},
		{name: "padded_base64_is_refused", cursor: token(good) + "==", errIs: aggregator.ErrInvalidCursor},
		{name: "not_json", cursor: token("v=1"), errIs: aggregator.ErrInvalidCursor},
		{name: "trailing_data", cursor: token(good + "{}"), errIs: aggregator.ErrInvalidCursor},
		{name: "unknown_version", cursor: token(`{"v":2,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"start"},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "missing_version", cursor: token(`{"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"start"},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "unknown_top_level_field", cursor: token(`{"v":1,"sig":"x","query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"start"},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "unknown_query_field", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788","order":"oldest"},"streams":{"sales":{"state":"start"},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "unknown_stream_field", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"start","page":3},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "unknown_state", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"paused"},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "empty_state", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "after_without_key", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"after"},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "key_with_one_part", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"after","key":["2024-03-09T00:00:00Z"]},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "key_with_a_bad_date", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"after","key":["yesterday","sales:S-5"]},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "key_with_an_empty_id", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"after","key":["2024-03-09T00:00:00Z",""]},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "key_on_a_done_stream", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"done","key":["2024-03-09T00:00:00Z","sales:S-5"]},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "another_vin_is_invalid", cursor: token(`{"v":1,"query":{"vin":"WVWZZZ1JZXW000002"},"streams":{"sales":{"state":"start"},"service":{"state":"start"}}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "missing_source_is_outdated", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"start"}}}`), errIs: aggregator.ErrCursorOutdated},
		{name: "missing_streams_object_is_invalid", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"}}`), errIs: aggregator.ErrInvalidCursor},
		{name: "unknown_source_is_dropped", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"after","key":["2024-03-09T00:00:00Z","sales:S-5"]},"service":{"state":"start"},"crm":{"state":"after","key":["2024-03-09T00:00:00Z","crm:1"]}}}`), wantNext: []string{"service:10002", "sales:S-4"}},
		{name: "done_and_unavailable_are_not_called", cursor: token(`{"v":1,"query":{"vin":"1M8GDM9AXKP042788"},"streams":{"sales":{"state":"done"},"service":{"state":"unavailable"}}}`), wantNext: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sales := &fakeSource{name: salesName, docs: salesDocs}
			service := &fakeSource{name: serviceName, docs: serviceDocs}
			res, err := build(t, sales, service).List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: tt.cursor, Limit: 2})
			if tt.errIs != nil {
				if !errors.Is(err, tt.errIs) {
					t.Fatalf("List() error = %v, want %v", err, tt.errIs)
				}
				if sales.callCount()+service.callCount() != 0 {
					t.Error("a rejected cursor must not reach the sources")
				}
				return
			}
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			got := entryIDs(res.Entries)
			if len(got) == 0 && len(tt.wantNext) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.wantNext) {
				t.Errorf("resumed page = %v, want %v", got, tt.wantNext)
			}
			if len(res.Sources) != 2 {
				t.Errorf("statuses name %d sources, want the 2 configured (unknown ones dropped)", len(res.Sources))
			}
		})
	}
}

// TestCursorIndependentOfLimit: the page size can change between pages.
func TestCursorIndependentOfLimit(t *testing.T) {
	tests := []struct {
		name   string
		limit1 int
		limit2 int
		want   []string // page 2
	}{
		{name: "grow_from_1_to_3", limit1: 1, limit2: 3, want: []string{"service:10002", "sales:S-4", "service:10001"}},
		{name: "shrink_from_3_to_1", limit1: 3, limit2: 1, want: []string{"service:10001"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agg := build(t, &fakeSource{name: salesName, docs: salesDocs}, &fakeSource{name: serviceName, docs: serviceDocs})
			p1, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: tt.limit1})
			if err != nil {
				t.Fatal(err)
			}
			p2, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: p1.NextCursor, Limit: tt.limit2})
			if err != nil {
				t.Fatal(err)
			}
			if got := entryIDs(p2.Entries); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("page 2 = %v, want %v", got, tt.want)
			}
		})
	}
}
