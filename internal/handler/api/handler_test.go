package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"

	"docviewer/internal/aggregator"
	"docviewer/internal/config"
	"docviewer/internal/handler/api"
	"docviewer/internal/handler/api/dto"
	"docviewer/internal/model"
)

const (
	vin       = "1M8GDM9AXKP042788"
	docsPath  = "/v1/vehicles/" + vin + "/documents"
	testGrace = time.Second
)

// fakeLister stands in for the aggregator: it returns what it is told and
// records the request, or panics when asked to.
type fakeLister struct {
	res   aggregator.Result
	err   error
	panic bool
	got   *aggregator.ListRequest
}

func (f *fakeLister) List(_ context.Context, req aggregator.ListRequest) (aggregator.Result, error) {
	f.got = &req
	if f.panic {
		panic("boom")
	}
	return f.res, f.err
}

func newServer(t *testing.T, lister api.Lister, origins ...string) *httptest.Server {
	t.Helper()
	srv := api.NewServer(config.Server{Addr: ":0", CORSOrigins: origins, ShutdownGrace: testGrace}, newDeps(t, lister))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Sources []dto.SourceStatus `json:"sources"`
}

func get(t *testing.T, ts *httptest.Server, path string) (int, []byte) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestListDocumentsErrors(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		err         error
		panics      bool
		wantStatus  int
		wantCode    string
		wantMsg     string   // exact, when fixed
		wantMsgHas  string   // substring, when the error's own text is used
		wantMsgNot  []string // must never appear
		wantSources []dto.SourceStatus
	}{
		{
			name: "invalid_vin_names_the_rule", path: "/v1/vehicles/ABC/documents",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_vin", wantMsgHas: "must be 17 characters, got 3",
		},
		{
			name: "invalid_vin_forbidden_letter", path: "/v1/vehicles/1M8GDM9AXKP04278I/documents",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_vin", wantMsgHas: "'I' is not allowed (position 17)",
		},
		{
			name: "limit_zero_is_invalid_limit_with_the_fixed_message", path: docsPath + "?limit=0",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_limit", wantMsg: "limit must be an integer from 1 to 100",
		},
		{
			name: "limit_over_100", path: docsPath + "?limit=101",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_limit", wantMsg: "limit must be an integer from 1 to 100",
		},
		{
			name: "limit_not_a_number_never_echoes_the_validator", path: docsPath + "?limit=many",
			wantStatus: http.StatusBadRequest, wantCode: "invalid_limit", wantMsg: "limit must be an integer from 1 to 100",
			wantMsgNot: []string{"strconv", "Field"},
		},
		{
			name: "invalid_cursor_is_fixed_and_never_echoes_the_cursor", path: docsPath + "?cursor=SECRET-TOKEN", err: aggregator.ErrInvalidCursor,
			wantStatus: http.StatusBadRequest, wantCode: "invalid_cursor", wantMsg: "invalid cursor", wantMsgNot: []string{"SECRET"},
		},
		{
			name: "wrapped_invalid_cursor_is_still_mapped", path: docsPath, err: errors.Join(errors.New("ctx"), aggregator.ErrInvalidCursor),
			wantStatus: http.StatusBadRequest, wantCode: "invalid_cursor", wantMsg: "invalid cursor",
		},
		{
			name: "cursor_outdated", path: docsPath, err: aggregator.ErrCursorOutdated,
			wantStatus: http.StatusBadRequest, wantCode: "cursor_outdated", wantMsg: "a source was added; start again from the first page",
		},
		{
			name: "sources_unavailable_retryable", path: docsPath,
			err:        &aggregator.SourcesUnavailableError{Sources: []aggregator.SourceResult{{Source: "sales", Status: aggregator.StatusTimeout}, {Source: "service", Status: aggregator.StatusUnavailable}}},
			wantStatus: http.StatusBadGateway, wantCode: "sources_unavailable", wantMsg: "no source answered; retry with the same cursor",
			wantSources: []dto.SourceStatus{{Source: "sales", Status: "timeout"}, {Source: "service", Status: "unavailable"}},
		},
		{
			name: "sources_unavailable_permanent", path: docsPath,
			err:        &aggregator.SourcesUnavailableError{Sources: []aggregator.SourceResult{{Source: "sales", Status: aggregator.StatusUnavailable}, {Source: "service", Status: aggregator.StatusUnavailable}}},
			wantStatus: http.StatusBadGateway, wantCode: "sources_unavailable", wantMsg: "no source can answer; retrying won't help",
			wantSources: []dto.SourceStatus{{Source: "sales", Status: "unavailable"}, {Source: "service", Status: "unavailable"}},
		},
		{
			name: "unknown_error_is_a_fixed_500", path: docsPath, err: errors.New("database on fire at 10.0.0.7"),
			wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantMsg: "internal error", wantMsgNot: []string{"10.0.0.7"},
		},
		{
			name: "panic_is_a_fixed_500", path: docsPath, panics: true,
			wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantMsg: "internal error", wantMsgNot: []string{"boom"},
		},
		{
			name: "unknown_route_is_not_found", path: "/v1/vehicles",
			wantStatus: http.StatusNotFound, wantCode: "not_found", wantMsg: "not found",
		},
		{
			name: "wrong_method_is_not_found", path: "",
			wantStatus: http.StatusNotFound, wantCode: "not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lister := &fakeLister{err: tt.err, panic: tt.panics}
			ts := newServer(t, lister)
			var status int
			var body []byte
			if tt.path == "" { // the wrong-method row
				resp, err := ts.Client().Post(ts.URL+docsPath, "application/json", strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				body, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
				status = resp.StatusCode
			} else {
				status, body = get(t, ts, tt.path)
			}
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", status, tt.wantStatus, body)
			}
			var eb errorBody
			if err := json.Unmarshal(body, &eb); err != nil {
				t.Fatalf("body %s is not an error document: %v", body, err)
			}
			if eb.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", eb.Error.Code, tt.wantCode)
			}
			if tt.wantMsg != "" && eb.Error.Message != tt.wantMsg {
				t.Errorf("message = %q, want %q", eb.Error.Message, tt.wantMsg)
			}
			if tt.wantMsgHas != "" && !strings.Contains(eb.Error.Message, tt.wantMsgHas) {
				t.Errorf("message = %q, want it to mention %q", eb.Error.Message, tt.wantMsgHas)
			}
			for _, s := range tt.wantMsgNot {
				if strings.Contains(string(body), s) {
					t.Errorf("body %s echoes %q", body, s)
				}
			}
			if !reflect.DeepEqual(eb.Sources, tt.wantSources) {
				t.Errorf("sources = %+v, want %+v", eb.Sources, tt.wantSources)
			}
			if strings.Contains(string(body), vin) {
				t.Errorf("body echoes the full VIN: %s", body)
			}
		})
	}
}

func TestListDocumentsRequest(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantReq  *aggregator.ListRequest // nil means the aggregator must not be called
		wantBody string
	}{
		{
			name:    "defaults_limit_to_20_and_normalises_the_vin",
			path:    "/v1/vehicles/%201m8gdm9axkp042788%20/documents",
			wantReq: &aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20},
		},
		{
			name:    "passes_limit_and_cursor_through",
			path:    docsPath + "?limit=7&cursor=abc",
			wantReq: &aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Cursor: "abc", Limit: 7},
		},
		{
			name: "invalid_vin_never_reaches_the_aggregator",
			path: "/v1/vehicles/nope/documents",
		},
		{
			name: "invalid_limit_never_reaches_the_aggregator",
			path: docsPath + "?limit=0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lister := &fakeLister{res: aggregator.Result{VIN: vin}}
			ts := newServer(t, lister)
			get(t, ts, tt.path)
			if !reflect.DeepEqual(lister.got, tt.wantReq) {
				t.Errorf("aggregator got %+v, want %+v", lister.got, tt.wantReq)
			}
		})
	}
}

func TestListDocumentsSuccess(t *testing.T) {
	at := time.Date(2026, time.June, 1, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		res      aggregator.Result
		wantJSON string
	}{
		{
			name: "page_is_written_through_dto",
			res: aggregator.Result{
				VIN: vin, NextCursor: "next",
				Entries: []model.Entry{{Type: "repair_order", Title: "Service", Summary: "Oil", IssuedAt: at, Sources: []model.SourceRef{{Source: "service", ID: "service:1", URL: "http://s/1.pdf"}}}},
				Sources: []aggregator.SourceResult{{Source: "sales", Status: aggregator.StatusDone}, {Source: "service", Status: aggregator.StatusOK}},
			},
			wantJSON: `{"vin":"1M8GDM9AXKP042788","documents":[{"type":"repair_order","title":"Service","summary":"Oil","issued_at":"2026-06-01T08:00:00Z","sources":[{"source":"service","id":"service:1","url":"http://s/1.pdf"}]}],"next_cursor":"next","sources":[{"source":"sales","status":"done"},{"source":"service","status":"ok"}]}`,
		},
		{
			name:     "empty_last_page",
			res:      aggregator.Result{VIN: vin, Sources: []aggregator.SourceResult{{Source: "sales", Status: aggregator.StatusOK}}},
			wantJSON: `{"vin":"1M8GDM9AXKP042788","documents":[],"next_cursor":null,"sources":[{"source":"sales","status":"ok"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newServer(t, &fakeLister{res: tt.res})
			resp, err := ts.Client().Get(ts.URL + docsPath)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d (body %s)", resp.StatusCode, body)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q", ct)
			}
			if got := strings.TrimSpace(string(body)); got != tt.wantJSON {
				t.Errorf("body = %s\nwant %s", got, tt.wantJSON)
			}
		})
	}
}

// TestListDocumentsClientGone: a cancelled request writes nothing.
func TestListDocumentsClientGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "context_canceled_writes_nothing", err: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := api.NewServer(config.Server{Addr: ":0", ShutdownGrace: testGrace}, newDeps(t, &fakeLister{err: tt.err}))
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, docsPath, nil))
			if rec.Body.Len() != 0 {
				t.Errorf("body = %s, want nothing written", rec.Body.String())
			}
			if rec.Code != api.StatusClientClosedRequest {
				t.Errorf("status = %d, want %d so logs and metrics never count a hang-up as 200", rec.Code, api.StatusClientClosedRequest)
			}
		})
	}
}

// TestRequestLog checks the one line per request: its status is the one the
// client got (a panic is a 500, a hang-up a 499, never a default 200), a
// recovered panic logs its stack, and health probes stay out of the info log.
func TestRequestLog(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		lister     *fakeLister
		wantStatus float64
		wantLevel  string
		wantStack  bool
	}{
		{name: "success_is_200", path: docsPath, lister: &fakeLister{res: aggregator.Result{VIN: vin}}, wantStatus: 200, wantLevel: "INFO"},
		{name: "panic_is_500_with_a_stack", path: docsPath, lister: &fakeLister{panic: true}, wantStatus: 500, wantLevel: "INFO", wantStack: true},
		{name: "hang_up_is_499", path: docsPath, lister: &fakeLister{err: context.Canceled}, wantStatus: 499, wantLevel: "INFO"},
		{name: "unknown_route_is_404", path: "/nope", lister: &fakeLister{}, wantStatus: 404, wantLevel: "INFO"},
		{name: "health_probe_is_debug_only", path: "/healthz", lister: &fakeLister{}, wantStatus: 200, wantLevel: "DEBUG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tel, out := newTelemetryWithLog(t)
			srv := api.NewServer(config.Server{Addr: ":0", ShutdownGrace: testGrace}, &api.Dependencies{Aggregator: tt.lister, Log: tel.Logger(), Telemetry: tel})
			srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tt.path, nil))
			var request map[string]any
			stack := false
			for _, rec := range logLines(t, out) {
				if rec["msg"] == "request" {
					request = rec
				}
				if s, ok := rec["stack"].(string); ok && strings.Contains(s, "goroutine") {
					stack = true
				}
			}
			if request == nil {
				t.Fatal("no request log line")
			}
			if request["status"] != tt.wantStatus || request["level"] != tt.wantLevel {
				t.Errorf("request line = %v, want status %v at %s", request, tt.wantStatus, tt.wantLevel)
			}
			if stack != tt.wantStack {
				t.Errorf("stack logged = %v, want %v", stack, tt.wantStack)
			}
			if _, has := request["trace_id"]; !has && tt.path == docsPath {
				t.Error("request line has no trace_id")
			}
		})
	}
}

func TestHealthAndReadiness(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		running    bool // the server has started and not begun shutting down
		wantStatus int
	}{
		{name: "healthz_is_always_200", path: "/healthz", running: false, wantStatus: http.StatusOK},
		{name: "readyz_before_start_is_503", path: "/readyz", running: false, wantStatus: http.StatusServiceUnavailable},
		{name: "readyz_while_serving_is_200", path: "/readyz", running: true, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := api.NewServer(config.Server{Addr: "127.0.0.1:0", ShutdownGrace: testGrace}, newDeps(t, &fakeLister{}))
			if !tt.running {
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
				if rec.Code != tt.wantStatus {
					t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
				}
				return
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- srv.Serve(ctx, ln) }()
			status, _ := getURL(t, "http://"+ln.Addr().String()+tt.path)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Serve() error = %v", err)
			}
			// After shutdown the readiness flag is down again.
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("readyz after shutdown = %d, want 503", rec.Code)
			}
		})
	}
}

func getURL(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // a test against a local listener
	if err != nil {
		t.Fatalf("Get(%s) error = %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func TestCORS(t *testing.T) {
	tests := []struct {
		name        string
		origins     []string
		origin      string // the request's Origin header
		preflight   bool
		wantStatus  int
		wantAllowed string // Access-Control-Allow-Origin
		wantExposed bool   // Access-Control-Expose-Headers names X-Trace-Id
	}{
		{name: "no_origins_configured_means_no_cors_headers", origin: "http://ui.local", wantStatus: http.StatusOK, wantAllowed: ""},
		{name: "no_origins_configured_rejects_preflight", origin: "http://ui.local", preflight: true, wantStatus: http.StatusNotFound},
		{name: "allowed_origin_is_echoed", origins: []string{"http://ui.local"}, origin: "http://ui.local", wantStatus: http.StatusOK, wantAllowed: "http://ui.local", wantExposed: true},
		{name: "allowed_origin_preflight_is_204", origins: []string{"http://ui.local"}, origin: "http://ui.local", preflight: true, wantStatus: http.StatusNoContent, wantAllowed: "http://ui.local"},
		{name: "other_origin_gets_no_header", origins: []string{"http://ui.local"}, origin: "http://evil.local", wantStatus: http.StatusForbidden, wantAllowed: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newServer(t, &fakeLister{res: aggregator.Result{VIN: vin}}, tt.origins...)
			method := http.MethodGet
			if tt.preflight {
				method = http.MethodOptions
			}
			req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+docsPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", tt.origin)
			if tt.preflight {
				req.Header.Set("Access-Control-Request-Method", http.MethodGet)
			}
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != tt.wantAllowed {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.wantAllowed)
			}
			if got := strings.Contains(resp.Header.Get("Access-Control-Expose-Headers"), api.HeaderTraceID); got != tt.wantExposed {
				t.Errorf("Access-Control-Expose-Headers = %q, exposes %s = %v, want %v", resp.Header.Get("Access-Control-Expose-Headers"), api.HeaderTraceID, got, tt.wantExposed)
			}
		})
	}
}

// TestRequestMetricExemplar: the request-duration histogram is recorded with
// the request's span, so Prometheus exemplars link a latency bucket to its
// trace. The logger sits outside tracing, so the span has to be carried over.
func TestRequestMetricExemplar(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "documents_request_has_a_trace_exemplar", path: docsPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tel, _, reader := newTelemetryWithSinks(t)
			srv := api.NewServer(config.Server{Addr: ":0", ShutdownGrace: testGrace}, &api.Dependencies{Aggregator: &fakeLister{res: aggregator.Result{VIN: vin}}, Log: tel.Logger(), Telemetry: tel})
			srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tt.path, nil))
			var rm metricdata.ResourceMetrics
			if err := reader.Collect(t.Context(), &rm); err != nil {
				t.Fatal(err)
			}
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name != "docviewer.http.request.duration" {
						continue
					}
					h, ok := m.Data.(metricdata.Histogram[float64])
					if !ok || len(h.DataPoints) != 1 {
						t.Fatalf("histogram = %+v, want one data point", m.Data)
					}
					ex := h.DataPoints[0].Exemplars
					if len(ex) != 1 || !trace.TraceID(ex[0].TraceID).IsValid() {
						t.Fatalf("exemplars = %+v, want one with a valid trace ID", ex)
					}
					return
				}
			}
			t.Fatal("request duration histogram not recorded")
		})
	}
}

// TestTraceIDHeader: every traced response names its trace in X-Trace-Id,
// success and error alike, so a reported failure can be matched to its
// trace and log lines. The health probes are not traced and carry none.
func TestTraceIDHeader(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		lister     *fakeLister
		wantStatus int
		wantHeader bool
	}{
		{name: "success_200", path: docsPath, lister: &fakeLister{res: aggregator.Result{VIN: vin}}, wantStatus: http.StatusOK, wantHeader: true},
		{name: "invalid_vin_400", path: "/v1/vehicles/NOPE/documents", lister: &fakeLister{}, wantStatus: http.StatusBadRequest, wantHeader: true},
		{name: "sources_unavailable_502", path: docsPath, lister: &fakeLister{err: &aggregator.SourcesUnavailableError{}}, wantStatus: http.StatusBadGateway, wantHeader: true},
		{name: "panic_500", path: docsPath, lister: &fakeLister{panic: true}, wantStatus: http.StatusInternalServerError, wantHeader: true},
		{name: "unknown_route_404", path: "/nope", lister: &fakeLister{}, wantStatus: http.StatusNotFound, wantHeader: true},
		{name: "health_probe_is_untraced", path: "/healthz", lister: &fakeLister{}, wantStatus: http.StatusOK, wantHeader: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newServer(t, tt.lister)
			resp, err := ts.Client().Get(ts.URL + tt.path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			got := resp.Header.Get(api.HeaderTraceID)
			if !tt.wantHeader {
				if got != "" {
					t.Errorf("%s = %q, want none", api.HeaderTraceID, got)
				}
				return
			}
			id, err := trace.TraceIDFromHex(got)
			if err != nil || !id.IsValid() {
				t.Errorf("%s = %q, want a valid trace ID (%v)", api.HeaderTraceID, got, err)
			}
		})
	}
}
