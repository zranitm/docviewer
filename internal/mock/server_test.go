package mock_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"docviewer/internal/mock"
)

// TestCORS: the mocks are test doubles that a browser demo drives directly
// (the fault controls), so every route answers cross-origin requests from
// any origin, preflight included.
func TestCORS(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	salesStore, err := mock.LoadSales(bytes.NewReader(mock.SalesFixture), "http://sales.test")
	if err != nil {
		t.Fatal(err)
	}
	serviceStore, err := mock.LoadService(bytes.NewReader(mock.ServiceFixture), "http://service.test")
	if err != nil {
		t.Fatal(err)
	}
	handlers := map[string]http.Handler{
		"sales":   mock.NewSalesHandler(salesStore, mock.NewFaults(mock.Fault{}), log),
		"service": mock.NewServiceHandler(serviceStore, mock.NewFaults(mock.Fault{}), log),
	}
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		wantStatus  int
		wantMethods string // Access-Control-Allow-Methods must contain these, preflight only
	}{
		{name: "preflight_for_the_fault_controls", method: http.MethodOptions, path: "/admin/faults", wantStatus: http.StatusNoContent, wantMethods: "POST, DELETE"},
		{name: "preflight_for_any_route", method: http.MethodOptions, path: "/healthz", wantStatus: http.StatusNoContent, wantMethods: "GET"},
		{name: "get_carries_the_allow_origin", method: http.MethodGet, path: "/admin/faults", wantStatus: http.StatusOK},
		{name: "post_from_a_browser_origin_works", method: http.MethodPost, path: "/admin/faults", body: `{"latency_ms": 5}`, wantStatus: http.StatusOK},
		{name: "a_rejected_request_still_carries_it", method: http.MethodPost, path: "/admin/faults", body: `{`, wantStatus: http.StatusBadRequest},
	}
	for name, h := range handlers {
		for _, tt := range tests {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
				req.Header.Set("Origin", "http://localhost:5173")
				if tt.method == http.MethodOptions {
					req.Header.Set("Access-Control-Request-Method", http.MethodPost)
					req.Header.Set("Access-Control-Request-Headers", "content-type")
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != tt.wantStatus {
					body, _ := io.ReadAll(rec.Body)
					t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, body)
				}
				if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
					t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
				}
				if tt.wantMethods != "" {
					if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, tt.wantMethods) {
						t.Errorf("Access-Control-Allow-Methods = %q, want it to contain %q", got, tt.wantMethods)
					}
					if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "content-type") {
						t.Errorf("Access-Control-Allow-Headers = %q, want content-type", got)
					}
				}
			})
		}
	}
}
