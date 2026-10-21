package dto_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"docviewer/internal/aggregator"
	"docviewer/internal/handler/api/dto"
	"docviewer/internal/model"
)

const specPath = "../../../../api/openapi.yaml"

func TestStatuses(t *testing.T) {
	tests := []struct {
		name string
		in   []aggregator.SourceResult
		want []dto.SourceStatus
	}{
		{
			name: "every_status_has_its_wire_name",
			in: []aggregator.SourceResult{
				{Source: "a", Status: aggregator.StatusOK}, {Source: "b", Status: aggregator.StatusTimeout},
				{Source: "c", Status: aggregator.StatusError}, {Source: "d", Status: aggregator.StatusDone},
				{Source: "e", Status: aggregator.StatusUnavailable},
			},
			want: []dto.SourceStatus{
				{Source: "a", Status: "ok"}, {Source: "b", Status: "timeout"},
				{Source: "c", Status: "error"}, {Source: "d", Status: "done"}, {Source: "e", Status: "unavailable"},
			},
		},
		{
			name: "order_is_kept",
			in:   []aggregator.SourceResult{{Source: "service", Status: aggregator.StatusOK}, {Source: "sales", Status: aggregator.StatusOK}},
			want: []dto.SourceStatus{{Source: "service", Status: "ok"}, {Source: "sales", Status: "ok"}},
		},
		{
			name: "an_unfilled_status_is_reported_as_error_never_empty",
			in:   []aggregator.SourceResult{{Source: "a"}},
			want: []dto.SourceStatus{{Source: "a", Status: "error"}},
		},
		{
			name: "empty_in_is_an_empty_list_not_null",
			in:   nil,
			want: []dto.SourceStatus{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dto.Statuses(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Statuses() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNewDocumentPage(t *testing.T) {
	at := time.Date(2026, time.June, 1, 8, 0, 0, 0, time.UTC)
	entry := model.Entry{
		Type: "repair_order", Title: "10,000 km service", Summary: "Oil changed", IssuedAt: at,
		Sources: []model.SourceRef{{Source: "service", ID: "service:9001", URL: "http://localhost:8082/files/9001.pdf"}},
	}
	noSummary := model.Entry{
		Type: "purchase_agreement", Title: "Purchase agreement", IssuedAt: at,
		Sources: []model.SourceRef{{Source: "sales", ID: "sales:S-1001", URL: "http://localhost:8081/files/S-1001.pdf"}},
	}
	tests := []struct {
		name     string
		res      aggregator.Result
		wantJSON string
	}{
		{
			name: "full_page_with_a_next_cursor",
			res: aggregator.Result{
				VIN: "1M8GDM9AXKP042788", Entries: []model.Entry{entry, noSummary}, NextCursor: "abc",
				Sources: []aggregator.SourceResult{{Source: "sales", Status: aggregator.StatusOK}, {Source: "service", Status: aggregator.StatusTimeout}},
			},
			wantJSON: `{"vin":"1M8GDM9AXKP042788","documents":[` +
				`{"type":"repair_order","title":"10,000 km service","summary":"Oil changed","issued_at":"2026-06-01T08:00:00Z","sources":[{"source":"service","id":"service:9001","url":"http://localhost:8082/files/9001.pdf"}]},` +
				`{"type":"purchase_agreement","title":"Purchase agreement","issued_at":"2026-06-01T08:00:00Z","sources":[{"source":"sales","id":"sales:S-1001","url":"http://localhost:8081/files/S-1001.pdf"}]}],` +
				`"next_cursor":"abc","sources":[{"source":"sales","status":"ok"},{"source":"service","status":"timeout"}]}`,
		},
		{
			name:     "last_page_has_null_cursor_and_empty_arrays_not_null",
			res:      aggregator.Result{VIN: "1M8GDM9AXKP042788"},
			wantJSON: `{"vin":"1M8GDM9AXKP042788","documents":[],"next_cursor":null,"sources":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(dto.NewDocumentPage(tt.res))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.wantJSON {
				t.Errorf("JSON = %s\nwant %s", got, tt.wantJSON)
			}
		})
	}
}

func TestListQueryBinding(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		want       dto.ListQuery
		wantErr    bool
		wantErrHas string
	}{
		{name: "absent_limit_defaults_to_20", query: "", want: dto.ListQuery{Limit: 20}},
		{name: "limit_within_range", query: "limit=5", want: dto.ListQuery{Limit: 5}},
		{name: "limit_at_maximum", query: "limit=100", want: dto.ListQuery{Limit: 100}},
		{name: "cursor_passes_through", query: "limit=1&cursor=abc", want: dto.ListQuery{Limit: 1, Cursor: "abc"}},
		{name: "limit_zero_is_rejected", query: "limit=0", wantErr: true},
		{name: "limit_over_maximum_is_rejected", query: "limit=101", wantErr: true},
		{name: "limit_negative_is_rejected", query: "limit=-1", wantErr: true},
		{name: "limit_not_an_integer_is_rejected", query: "limit=many", wantErr: true},
		{name: "empty_limit_takes_the_default", query: "limit=", want: dto.ListQuery{Limit: 20}},
	}
	gin.SetMode(gin.TestMode)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/x?"+tt.query, nil)
			var q dto.ListQuery
			err := c.ShouldBindQuery(&q)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ShouldBindQuery() = %+v, want an error", q)
				}
				return
			}
			if err != nil {
				t.Fatalf("ShouldBindQuery() error = %v", err)
			}
			if q != tt.want {
				t.Errorf("ShouldBindQuery() = %+v, want %+v", q, tt.want)
			}
		})
	}
}

func TestErrorResponseShape(t *testing.T) {
	tests := []struct {
		name     string
		in       dto.ErrorResponse
		wantJSON string
	}{
		{
			name:     "code_and_message_only",
			in:       dto.ErrorResponse{Error: dto.ErrorDetail{Code: dto.CodeInvalidVIN, Message: "invalid VIN: must be 17 characters, got 3"}},
			wantJSON: `{"error":{"code":"invalid_vin","message":"invalid VIN: must be 17 characters, got 3"}}`,
		},
		{
			name: "sources_only_for_sources_unavailable",
			in: dto.ErrorResponse{
				Error:   dto.ErrorDetail{Code: dto.CodeSourcesUnavailable, Message: "no source answered; retry with the same cursor"},
				Sources: []dto.SourceStatus{{Source: "sales", Status: "timeout"}},
			},
			wantJSON: `{"error":{"code":"sources_unavailable","message":"no source answered; retry with the same cursor"},"sources":[{"source":"sales","status":"timeout"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.wantJSON {
				t.Errorf("JSON = %s\nwant %s", got, tt.wantJSON)
			}
		})
	}
}

func TestErrInvalidLimitIsASentinel(t *testing.T) {
	wrapped := errors.Join(dto.ErrInvalidLimit, errors.New("validator text"))
	if !errors.Is(wrapped, dto.ErrInvalidLimit) {
		t.Error("ErrInvalidLimit does not survive wrapping")
	}
}

// spec is the part of api/openapi.yaml these tests compare against.
type spec struct {
	Components struct {
		Schemas map[string]struct {
			Properties map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func loadSpec(t *testing.T) spec {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var s spec
	if err := yaml.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// jsonKeys lists a struct's JSON field names.
func jsonKeys(v any) []string {
	var keys []string
	rt := reflect.TypeOf(v)
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		keys = append(keys, name)
	}
	slices.Sort(keys)
	return keys
}

// TestMatchesOpenAPI keeps the Go types and api/openapi.yaml from drifting:
// same property names per schema, same status and error-code enums.
func TestMatchesOpenAPI(t *testing.T) {
	s := loadSpec(t)
	tests := []struct {
		name   string
		schema string
		value  any
	}{
		{name: "DocumentPage", schema: "DocumentPage", value: dto.DocumentPage{}},
		{name: "Document", schema: "Document", value: dto.Document{}},
		{name: "SourceRef", schema: "SourceRef", value: dto.SourceRef{}},
		{name: "SourceStatus", schema: "SourceStatus", value: dto.SourceStatus{}},
		{name: "ErrorResponse", schema: "ErrorResponse", value: dto.ErrorResponse{}},
		{name: "ErrorDetail", schema: "ErrorDetail", value: dto.ErrorDetail{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sch, ok := s.Components.Schemas[tt.schema]
			if !ok {
				t.Fatalf("schema %s is not in the spec", tt.schema)
			}
			want := slices.Sorted(func(yield func(string) bool) {
				for k := range sch.Properties {
					if !yield(k) {
						return
					}
				}
			})
			if got := jsonKeys(tt.value); !reflect.DeepEqual(got, want) {
				t.Errorf("JSON keys = %v, spec properties %v", got, want)
			}
		})
	}
	enums := []struct {
		name   string
		schema string
		prop   string
		want   []string
	}{
		{
			name: "status_names", schema: "SourceStatus", prop: "status",
			want: func() []string {
				var names []string
				for _, st := range []aggregator.Status{aggregator.StatusOK, aggregator.StatusTimeout, aggregator.StatusError, aggregator.StatusDone, aggregator.StatusUnavailable} {
					names = append(names, dto.Statuses([]aggregator.SourceResult{{Status: st}})[0].Status)
				}
				return names
			}(),
		},
		{
			name: "error_codes", schema: "ErrorDetail", prop: "code",
			want: []string{
				string(dto.CodeInvalidVIN), string(dto.CodeInvalidLimit), string(dto.CodeInvalidCursor), string(dto.CodeCursorOutdated),
				string(dto.CodeSourcesUnavailable), string(dto.CodeNotFound), string(dto.CodeInternal),
			},
		},
	}
	for _, tt := range enums {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Components.Schemas[tt.schema].Properties[tt.prop].Enum
			slices.Sort(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("spec enum = %v, code has %v", got, want)
			}
		})
	}
}
