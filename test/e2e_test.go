// Package e2e runs the service as production builds it (api.NewDependencies
// and api.NewServer) against both mock systems in process. Each PRD
// acceptance criterion (AC-01 to AC-12) is a row named after its ID.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"docviewer/internal/config"
	"docviewer/internal/handler/api"
	"docviewer/internal/mock"
	"docviewer/internal/telemetry"
	"docviewer/pkg/otelsetup"
)

// The fixture VINs (internal/mock/fixtures).
const (
	vinBoth    = "1M8GDM9AXKP042788"
	vinSales   = "WVWZZZ1JZXW000002"
	vinService = "JH4KA7561PC000003"
	vinMany    = "5YJ3E1EA7KF000004"
	vinNone    = "1HGCM82633A000005"

	sourceTimeout  = 300 * time.Millisecond
	requestTimeout = time.Second
	pagesBound     = 100
)

type page struct {
	VIN       string `json:"vin"`
	Documents []struct {
		Type     string    `json:"type"`
		Title    string    `json:"title"`
		Summary  *string   `json:"summary"` // nil when the key is absent
		IssuedAt time.Time `json:"issued_at"`
		Sources  []struct {
			Source string `json:"source"`
			ID     string `json:"id"`
			URL    string `json:"url"`
		} `json:"sources"`
	} `json:"documents"`
	NextCursor *string `json:"next_cursor"`
	Sources    []struct {
		Source string `json:"source"`
		Status string `json:"status"`
	} `json:"sources"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// stack is the service plus both mocks, all in process.
type stack struct {
	api           *httptest.Server
	salesFaults   *mock.Faults
	serviceFaults *mock.Faults
	salesCalls    atomic.Int32
	serviceCalls  atomic.Int32
	metrics       *sdkmetric.ManualReader
}

func counting(n *atomic.Int32, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/files/") {
			n.Add(1)
		}
		next.ServeHTTP(w, r)
	})
}

// stackOptions vary a stack: which upstreams the service is configured with
// (both by default) and its resilience settings (a generous default that no
// acceptance test trips).
type stackOptions struct {
	sources    []string
	resilience *config.Resilience
}

// newStack starts the mocks and the service with the default options.
func newStack(t *testing.T, sources ...string) *stack {
	t.Helper()
	return newStackWith(t, stackOptions{sources: sources})
}

// newStackWith starts the mocks and the service as opts says.
func newStackWith(t *testing.T, opts stackOptions) *stack {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	s := &stack{salesFaults: mock.NewFaults(mock.Fault{}), serviceFaults: mock.NewFaults(mock.Fault{}), metrics: sdkmetric.NewManualReader()}

	salesSrv := httptest.NewServer(nil)
	t.Cleanup(salesSrv.Close)
	salesStore, err := mock.LoadSales(bytes.NewReader(mock.SalesFixture), salesSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	salesSrv.Config.Handler = counting(&s.salesCalls, mock.NewSalesHandler(salesStore, s.salesFaults, log))

	serviceSrv := httptest.NewServer(nil)
	t.Cleanup(serviceSrv.Close)
	serviceStore, err := mock.LoadService(bytes.NewReader(mock.ServiceFixture), serviceSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	serviceSrv.Config.Handler = counting(&s.serviceCalls, mock.NewServiceHandler(serviceStore, s.serviceFaults, log))

	sources := opts.sources
	if len(sources) == 0 {
		sources = []string{"sales", "service"}
	}
	upstreams := map[string]config.Upstream{}
	for _, name := range sources {
		base := map[string]string{"sales": salesSrv.URL, "service": serviceSrv.URL}[name]
		upstreams[name] = config.Upstream{BaseURL: base, Timeout: sourceTimeout}
	}
	resilience := config.Resilience{MaxRetries: 1, MinBudget: 50 * time.Millisecond, MaxJitter: 10 * time.Millisecond, TripAfter: 5, Cooldown: 30 * time.Second}
	if opts.resilience != nil {
		resilience = *opts.resilience
	}
	cfg := config.Config{
		Server:     config.Server{Addr: ":0", ShutdownGrace: time.Second},
		Sources:    config.Sources{RequestTimeout: requestTimeout, MaxBodyBytes: 1 << 20, Upstreams: upstreams},
		Resilience: resilience,
		Telemetry:  config.Telemetry{LogLevel: "info", Redact: config.Redact{Enabled: true}},
	}
	tel, err := telemetry.New(t.Context(), cfg.Telemetry, otelsetup.Options{ServiceName: "docviewer-e2e", LogWriter: io.Discard, Exporters: &otelsetup.Exporters{Traces: tracetest.NewInMemoryExporter(), Metrics: s.metrics}})
	if err != nil {
		t.Fatalf("telemetry.New() error = %v", err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	deps, err := api.NewDependencies(cfg, tel)
	if err != nil {
		t.Fatalf("NewDependencies() error = %v", err)
	}
	s.api = httptest.NewServer(api.NewServer(cfg.Server, deps).Handler())
	t.Cleanup(s.api.Close)
	return s
}

func (s *stack) get(t *testing.T, path string) (int, page, []byte) {
	t.Helper()
	resp, err := s.api.Client().Get(s.api.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var p page
	if len(body) > 0 {
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("GET %s: body %s is not JSON: %v", path, body, err)
		}
	}
	return resp.StatusCode, p, body
}

func docsPath(vin string, query string) string {
	p := "/v1/vehicles/" + url.PathEscape(vin) + "/documents"
	if query != "" {
		p += "?" + query
	}
	return p
}

// walk follows next_cursor until it is null and returns every page.
func (s *stack) walk(t *testing.T, vin string, limit int) []page {
	t.Helper()
	var pages []page
	cursor := ""
	for range pagesBound {
		q := "limit=" + itoa(limit)
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		status, p, body := s.get(t, docsPath(vin, q))
		if status != http.StatusOK {
			t.Fatalf("page %d: status %d, body %s", len(pages)+1, status, body)
		}
		pages = append(pages, p)
		if p.NextCursor == nil {
			return pages
		}
		cursor = *p.NextCursor
	}
	t.Fatalf("walk did not end within %d pages", pagesBound)
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// breakerState reads the breaker gauge for a source: 0 closed, 1 half-open,
// 2 open; -1 when nothing has been recorded for it.
func (s *stack) breakerState(t *testing.T, source string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := s.metrics.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "docviewer.breaker.state" {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("breaker state = %T, want an int64 gauge", m.Data)
			}
			for _, dp := range g.DataPoints {
				if v, has := dp.Attributes.Value("source"); has && v.AsString() == source {
					return dp.Value
				}
			}
		}
	}
	return -1
}

func statusOf(p page, source string) string {
	for _, s := range p.Sources {
		if s.Source == source {
			return s.Status
		}
	}
	return ""
}

func ids(pages []page) []string {
	var out []string
	for _, p := range pages {
		for _, d := range p.Documents {
			out = append(out, d.Sources[0].ID)
		}
	}
	return out
}

// TestAcceptanceCriteria is the PRD's section 6, one row per criterion.
func TestAcceptanceCriteria(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{name: "AC-01_both_systems_one_list_newest_first_with_sources", run: func(t *testing.T) {
			s := newStack(t)
			status, p, body := s.get(t, docsPath(vinBoth, ""))
			if status != http.StatusOK {
				t.Fatalf("status %d, body %s", status, body)
			}
			if len(p.Documents) != 10 {
				t.Fatalf("got %d documents, want 10 (5 Sales + 5 Service)", len(p.Documents))
			}
			seen := map[string]bool{}
			for i, d := range p.Documents {
				seen[d.Sources[0].Source] = true
				if i > 0 && d.IssuedAt.After(p.Documents[i-1].IssuedAt) {
					t.Errorf("document %d is newer than document %d", i, i-1)
				}
			}
			if !seen["sales"] || !seen["service"] {
				t.Errorf("sources seen = %v, want both", seen)
			}
			if p.Documents[0].Sources[0].ID != "service:9003" || p.Documents[1].Sources[0].ID != "service:9001" {
				t.Errorf("newest two = %s, %s; want service:9003 then service:9001 (same timestamp, numeric ID order)", p.Documents[0].Sources[0].ID, p.Documents[1].Sources[0].ID)
			}
			if statusOf(p, "sales") != "ok" || statusOf(p, "service") != "ok" {
				t.Errorf("statuses = %+v, want ok and ok", p.Sources)
			}
		}},
		{name: "AC-02_lower_case_and_spaces_are_normalised", run: func(t *testing.T) {
			s := newStack(t)
			status, p, body := s.get(t, docsPath("  1m8gdm9axkp042788 ", ""))
			if status != http.StatusOK {
				t.Fatalf("status %d, body %s", status, body)
			}
			if p.VIN != vinBoth {
				t.Errorf("vin = %q, want %q", p.VIN, vinBoth)
			}
			if len(p.Documents) != 10 {
				t.Errorf("got %d documents, want 10", len(p.Documents))
			}
		}},
		{name: "AC-03_invalid_vin_explains_why_and_queries_nothing", run: func(t *testing.T) {
			s := newStack(t)
			status, p, _ := s.get(t, docsPath("1M8GDM9AXKP04278O", ""))
			if status != http.StatusBadRequest || p.Error == nil || p.Error.Code != "invalid_vin" {
				t.Fatalf("status %d, error %+v; want 400 invalid_vin", status, p.Error)
			}
			if !strings.Contains(p.Error.Message, "'O' is not allowed") {
				t.Errorf("message = %q, want the rule that failed", p.Error.Message)
			}
			if s.salesCalls.Load()+s.serviceCalls.Load() != 0 {
				t.Error("an upstream was queried for an invalid VIN")
			}
		}},
		{name: "AC-04_one_system_down_shows_the_other_and_names_it", run: func(t *testing.T) {
			s := newStack(t)
			s.serviceFaults.Set(mock.Fault{Hang: true})
			status, p, body := s.get(t, docsPath(vinBoth, ""))
			if status != http.StatusOK {
				t.Fatalf("status %d, body %s", status, body)
			}
			if len(p.Documents) != 5 {
				t.Errorf("got %d documents, want the 5 from Sales", len(p.Documents))
			}
			if statusOf(p, "service") != "timeout" || statusOf(p, "sales") != "ok" {
				t.Errorf("statuses = %+v, want service timeout, sales ok", p.Sources)
			}
			s.serviceFaults.Set(mock.Fault{Status: http.StatusInternalServerError})
			_, p, _ = s.get(t, docsPath(vinBoth, ""))
			if statusOf(p, "service") != "error" {
				t.Errorf("after a 500: statuses = %+v, want service error", p.Sources)
			}
		}},
		{name: "AC-05_load_more_continues_with_nothing_skipped_or_repeated", run: func(t *testing.T) {
			s := newStack(t)
			for _, limit := range []int{20, 7, 100} {
				pages := s.walk(t, vinMany, limit)
				got := ids(pages)
				if len(got) != 128 {
					t.Errorf("limit %d: %d documents, want 128 (8 Sales + 120 Service)", limit, len(got))
				}
				if dup := firstDuplicate(got); dup != "" {
					t.Errorf("limit %d: %s appears twice", limit, dup)
				}
				var prev time.Time
				for i, p := range pages {
					for j, d := range p.Documents {
						if (i > 0 || j > 0) && d.IssuedAt.After(prev) {
							t.Errorf("limit %d: page %d document %d is newer than the one before", limit, i+1, j)
						}
						prev = d.IssuedAt
					}
				}
				if !slices.Contains(got, "service:10000") || slices.Index(got, "service:10001") > slices.Index(got, "service:10000") || slices.Index(got, "service:10000") > slices.Index(got, "service:9999") {
					t.Errorf("limit %d: the 10001, 10000, 9999 tie is not in numeric order", limit)
				}
			}
		}},
		{name: "AC-06_both_systems_fail_is_an_error", run: func(t *testing.T) {
			s := newStack(t)
			s.salesFaults.Set(mock.Fault{Status: http.StatusServiceUnavailable})
			s.serviceFaults.Set(mock.Fault{Hang: true})
			status, p, _ := s.get(t, docsPath(vinBoth, ""))
			if status != http.StatusBadGateway || p.Error == nil || p.Error.Code != "sources_unavailable" {
				t.Fatalf("status %d, error %+v; want 502 sources_unavailable", status, p.Error)
			}
			if !strings.Contains(p.Error.Message, "retry") {
				t.Errorf("message = %q, want it to say the user can retry", p.Error.Message)
			}
			if statusOf(p, "sales") != "error" || statusOf(p, "service") != "timeout" {
				t.Errorf("statuses = %+v, want sales error and service timeout", p.Sources)
			}
			if p.NextCursor != nil {
				t.Error("a 502 carries a next cursor")
			}
		}},
		{name: "AC-07_no_documents_anywhere_is_an_empty_list_not_an_error", run: func(t *testing.T) {
			s := newStack(t)
			status, p, body := s.get(t, docsPath(vinNone, ""))
			if status != http.StatusOK {
				t.Fatalf("status %d, body %s", status, body)
			}
			if len(p.Documents) != 0 || p.NextCursor != nil {
				t.Errorf("page = %s, want no documents and a null cursor", body)
			}
			if !bytes.Contains(body, []byte(`"documents":[]`)) {
				t.Errorf("documents must be an empty array, got %s", body)
			}
			if statusOf(p, "sales") != "ok" || statusOf(p, "service") != "ok" {
				t.Errorf("statuses = %+v, want ok and ok (a 404 is an answer)", p.Sources)
			}
		}},
		{name: "AC-08_results_or_a_clear_failure_within_the_deadline", run: func(t *testing.T) {
			s := newStack(t)
			s.serviceFaults.Set(mock.Fault{Hang: true})
			start := time.Now()
			status, _, _ := s.get(t, docsPath(vinBoth, ""))
			took := time.Since(start)
			if status != http.StatusOK {
				t.Errorf("status %d, want 200 with Sales only", status)
			}
			if took > requestTimeout {
				t.Errorf("took %v, over the %v request deadline", took, requestTimeout)
			}
		}},
		{name: "AC-09_summary_shown_when_the_source_has_one", run: func(t *testing.T) {
			s := newStack(t)
			_, p, _ := s.get(t, docsPath(vinBoth, ""))
			with, without := 0, 0
			for _, d := range p.Documents {
				switch d.Sources[0].ID {
				case "service:9001":
					if d.Summary == nil || *d.Summary != "Oil and filter changed, brake pads replaced" {
						t.Errorf("service:9001 summary = %v", d.Summary)
					}
				case "sales:S-1004":
					if d.Summary == nil || *d.Summary != "Paid in full" {
						t.Errorf("sales:S-1004 summary = %v", d.Summary)
					}
				case "sales:S-1001":
					if d.Summary != nil {
						t.Errorf("sales:S-1001 has a summary %q, want the key absent", *d.Summary)
					}
				}
				if d.Summary != nil {
					with++
				} else {
					without++
				}
			}
			if with == 0 || without == 0 {
				t.Errorf("summaries present = %d, absent = %d; want both kinds", with, without)
			}
		}},
		{name: "AC-10_opening_a_document_opens_its_file_from_the_source", run: func(t *testing.T) {
			s := newStack(t)
			_, p, _ := s.get(t, docsPath(vinBoth, "limit=1"))
			if len(p.Documents) != 1 {
				t.Fatal("no document to open")
			}
			resp, err := http.Get(p.Documents[0].Sources[0].URL) //nolint:gosec,noctx // the mock's own link
			if err != nil {
				t.Fatalf("GET file: %v", err)
			}
			defer resp.Body.Close()
			head := make([]byte, 5)
			_, _ = io.ReadFull(resp.Body, head)
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/pdf" || string(head) != "%PDF-" {
				t.Errorf("file: status %d, type %q, head %q", resp.StatusCode, resp.Header.Get("Content-Type"), head)
			}
		}},
		{name: "AC-11_same_file_in_both_systems_appears_once_per_system", run: func(t *testing.T) {
			s := newStack(t)
			_, p, _ := s.get(t, docsPath(vinBoth, ""))
			var sources []string
			for _, d := range p.Documents {
				if d.Title == "PDI checklist, signed" {
					sources = append(sources, d.Sources[0].Source)
				}
			}
			slices.Sort(sources)
			if !slices.Equal(sources, []string{"sales", "service"}) {
				t.Errorf("PDI appears from %v, want once from sales and once from service", sources)
			}
		}},
		{name: "AC-12_permanent_rejection_ends_the_list_without_that_system", run: func(t *testing.T) {
			s := newStack(t)
			s.serviceFaults.Set(mock.Fault{Status: http.StatusForbidden})
			pages := s.walk(t, vinMany, 3)
			got := ids(pages)
			if len(got) != 8 {
				t.Errorf("%d documents, want the 8 from Sales", len(got))
			}
			for i, p := range pages {
				if statusOf(p, "service") != "unavailable" {
					t.Errorf("page %d: service status %q, want unavailable on every page", i+1, statusOf(p, "service"))
				}
			}
			if s.serviceCalls.Load() != 1 {
				t.Errorf("service was called %d times, want once: never again after a permanent rejection", s.serviceCalls.Load())
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// TestEndToEnd covers the API contract beyond the acceptance criteria.
func TestEndToEnd(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{name: "circuit_breaker_fails_fast_then_recovers_after_the_cooldown", run: func(t *testing.T) {
			const (
				tripAfter = 3
				cooldown  = 200 * time.Millisecond
			)
			s := newStackWith(t, stackOptions{resilience: &config.Resilience{MaxRetries: 1, MinBudget: 50 * time.Millisecond, MaxJitter: 10 * time.Millisecond, TripAfter: tripAfter, Cooldown: cooldown}})
			s.salesFaults.Set(mock.Fault{Status: http.StatusServiceUnavailable})
			// Each failing call is retried once and counts as one failure; the
			// page is still served from Service.
			for i := range tripAfter {
				status, p, body := s.get(t, docsPath(vinBoth, ""))
				if status != http.StatusOK || statusOf(p, "sales") != "error" || statusOf(p, "service") != "ok" {
					t.Fatalf("call %d: status %d, statuses %+v, body %s; want 200 with sales error and service ok", i+1, status, p.Sources, body)
				}
			}
			if got := s.breakerState(t, "sales"); got != 2 {
				t.Fatalf("breaker gauge after %d failures = %d, want 2 (open)", tripAfter, got)
			}
			if got := s.salesCalls.Load(); got != 2*tripAfter {
				t.Errorf("sales was called %d times, want %d (each call retried once)", got, 2*tripAfter)
			}
			// Open: the page is served without calling Sales at all.
			before := s.salesCalls.Load()
			status, p, _ := s.get(t, docsPath(vinBoth, ""))
			if status != http.StatusOK || statusOf(p, "sales") != "error" || len(p.Documents) != 5 {
				t.Fatalf("open breaker: status %d, statuses %+v, %d documents; want 200, sales error, the 5 Service documents", status, p.Sources, len(p.Documents))
			}
			if s.salesCalls.Load() != before {
				t.Error("an open breaker still called Sales")
			}
			// Sales recovers. After the cooldown one probe goes through, is not
			// retried, and closes the breaker.
			s.salesFaults.Reset()
			time.Sleep(cooldown + 50*time.Millisecond)
			status, p, _ = s.get(t, docsPath(vinBoth, ""))
			if status != http.StatusOK || statusOf(p, "sales") != "ok" || len(p.Documents) != 10 {
				t.Fatalf("after the cooldown: status %d, statuses %+v, %d documents; want 200, sales ok, all 10 documents", status, p.Sources, len(p.Documents))
			}
			if got := s.salesCalls.Load() - before; got != 1 {
				t.Errorf("sales was called %d times after recovery, want exactly one probe", got)
			}
			if got := s.breakerState(t, "sales"); got != 0 {
				t.Errorf("breaker gauge after the probe = %d, want 0 (closed)", got)
			}
		}},
		{name: "200_from_one_system_when_the_other_has_nothing", run: func(t *testing.T) {
			s := newStack(t)
			_, p, _ := s.get(t, docsPath(vinSales, ""))
			if len(p.Documents) != 3 || statusOf(p, "service") != "ok" || p.NextCursor != nil {
				t.Errorf("page = %+v, want 3 Sales documents, service ok (404 is done), last page", p)
			}
			_, p, _ = s.get(t, docsPath(vinService, ""))
			if len(p.Documents) != 4 || statusOf(p, "sales") != "ok" {
				t.Errorf("page = %+v, want 4 Service documents, sales ok", p)
			}
			for _, d := range p.Documents {
				if d.Sources[0].ID == "service:9104" && d.Type != "other" {
					t.Errorf("warranty_claim mapped to %q, want other", d.Type)
				}
			}
		}},
		{name: "last_page_has_a_null_cursor_and_done_statuses", run: func(t *testing.T) {
			s := newStack(t)
			pages := s.walk(t, vinBoth, 4)
			last := pages[len(pages)-1]
			if last.NextCursor != nil {
				t.Error("last page has a cursor")
			}
			if len(pages) < 3 {
				t.Errorf("%d pages, want at least 3 for 10 documents at 4 per page", len(pages))
			}
		}},
		{name: "page_size_can_change_between_pages", run: func(t *testing.T) {
			s := newStack(t)
			_, p1, _ := s.get(t, docsPath(vinBoth, "limit=2"))
			_, p2, _ := s.get(t, docsPath(vinBoth, "limit=5&cursor="+url.QueryEscape(*p1.NextCursor)))
			if len(p2.Documents) != 5 {
				t.Errorf("page 2 has %d documents, want 5", len(p2.Documents))
			}
			if ids([]page{p1, p2})[2] == ids([]page{p1})[1] {
				t.Error("page 2 repeats page 1's last document")
			}
		}},
		{name: "400_invalid_limit", run: func(t *testing.T) {
			s := newStack(t)
			for _, q := range []string{"limit=0", "limit=101", "limit=x"} {
				status, p, _ := s.get(t, docsPath(vinBoth, q))
				if status != http.StatusBadRequest || p.Error == nil || p.Error.Code != "invalid_limit" {
					t.Errorf("%s: status %d, error %+v; want 400 invalid_limit", q, status, p.Error)
				}
			}
		}},
		{name: "400_invalid_cursor", run: func(t *testing.T) {
			s := newStack(t)
			status, p, _ := s.get(t, docsPath(vinBoth, "cursor=not-a-cursor"))
			if status != http.StatusBadRequest || p.Error == nil || p.Error.Code != "invalid_cursor" {
				t.Errorf("status %d, error %+v; want 400 invalid_cursor", status, p.Error)
			}
		}},
		{name: "400_cursor_for_another_vin", run: func(t *testing.T) {
			s := newStack(t)
			_, p1, _ := s.get(t, docsPath(vinBoth, "limit=1"))
			status, p, _ := s.get(t, docsPath(vinMany, "cursor="+url.QueryEscape(*p1.NextCursor)))
			if status != http.StatusBadRequest || p.Error == nil || p.Error.Code != "invalid_cursor" {
				t.Errorf("status %d, error %+v; want 400 invalid_cursor", status, p.Error)
			}
		}},
		{name: "400_cursor_outdated_when_a_source_was_added", run: func(t *testing.T) {
			old := newStack(t, "sales")
			_, p1, _ := old.get(t, docsPath(vinMany, "limit=1"))
			if p1.NextCursor == nil {
				t.Fatal("no cursor from the one-source deployment")
			}
			current := newStack(t)
			status, p, _ := current.get(t, docsPath(vinMany, "cursor="+url.QueryEscape(*p1.NextCursor)))
			if status != http.StatusBadRequest || p.Error == nil || p.Error.Code != "cursor_outdated" {
				t.Errorf("status %d, error %+v; want 400 cursor_outdated", status, p.Error)
			}
		}},
		{name: "cursor_from_a_removed_source_still_works", run: func(t *testing.T) {
			current := newStack(t)
			_, p1, _ := current.get(t, docsPath(vinMany, "limit=1"))
			reduced := newStack(t, "sales")
			status, p, body := reduced.get(t, docsPath(vinMany, "cursor="+url.QueryEscape(*p1.NextCursor)))
			if status != http.StatusOK {
				t.Errorf("status %d, body %s; want 200 (the removed source's entry is ignored)", status, body)
			}
			if len(p.Sources) != 1 {
				t.Errorf("statuses = %+v, want sales only", p.Sources)
			}
		}},
		{name: "502_when_everything_rejects_says_retrying_wont_help", run: func(t *testing.T) {
			s := newStack(t)
			s.salesFaults.Set(mock.Fault{Status: http.StatusForbidden})
			s.serviceFaults.Set(mock.Fault{Status: http.StatusUnauthorized})
			status, p, _ := s.get(t, docsPath(vinBoth, ""))
			if status != http.StatusBadGateway || p.Error == nil || !strings.Contains(p.Error.Message, "won't help") {
				t.Errorf("status %d, error %+v; want 502 saying retrying won't help", status, p.Error)
			}
		}},
		{name: "permanent_rejection_on_a_later_page_ends_the_list_with_a_200", run: func(t *testing.T) {
			s := newStack(t)
			_, p1, _ := s.get(t, docsPath(vinBoth, "limit=4"))
			s.salesFaults.Set(mock.Fault{Status: http.StatusForbidden})
			s.serviceFaults.Set(mock.Fault{Status: http.StatusUnauthorized})
			status, p, body := s.get(t, docsPath(vinBoth, "limit=4&cursor="+url.QueryEscape(*p1.NextCursor)))
			if status != http.StatusOK || len(p.Documents) != 0 || p.NextCursor != nil {
				t.Errorf("status %d, body %s; want a terminal 200 with no documents and a null cursor", status, body)
			}
			if statusOf(p, "sales") != "unavailable" || statusOf(p, "service") != "unavailable" {
				t.Errorf("statuses = %+v, want both unavailable", p.Sources)
			}
		}},
		{name: "404_unknown_route", run: func(t *testing.T) {
			s := newStack(t)
			status, p, _ := s.get(t, "/v2/vehicles")
			if status != http.StatusNotFound || p.Error == nil || p.Error.Code != "not_found" {
				t.Errorf("status %d, error %+v; want 404 not_found", status, p.Error)
			}
		}},
		{name: "health_endpoints", run: func(t *testing.T) {
			s := newStack(t)
			if status, _, _ := s.get(t, "/healthz"); status != http.StatusOK {
				t.Errorf("healthz = %d", status)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func firstDuplicate(ids []string) string {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return id
		}
		seen[id] = true
	}
	return ""
}
