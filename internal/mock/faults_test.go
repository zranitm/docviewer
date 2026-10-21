package mock_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"docviewer/internal/mock"
)

func TestFaultValidate(t *testing.T) {
	tests := []struct {
		name    string
		fault   mock.Fault
		wantErr bool
	}{
		{name: "zero_value_is_no_fault", fault: mock.Fault{}},
		{name: "latency_and_rate", fault: mock.Fault{LatencyMS: 2500, ErrorRate: 0.5}},
		{name: "hang", fault: mock.Fault{Hang: true}},
		{name: "fixed_status", fault: mock.Fault{Status: 403}},
		{name: "negative_latency", fault: mock.Fault{LatencyMS: -1}, wantErr: true},
		{name: "rate_above_one", fault: mock.Fault{ErrorRate: 1.5}, wantErr: true},
		{name: "rate_below_zero", fault: mock.Fault{ErrorRate: -0.1}, wantErr: true},
		{name: "status_not_an_error", fault: mock.Fault{Status: 200}, wantErr: true},
		{name: "status_out_of_range", fault: mock.Fault{Status: 600}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fault.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestFaultsMiddleware puts a trivial handler behind each fault.
func TestFaultsMiddleware(t *testing.T) {
	tests := []struct {
		name       string
		fault      mock.Fault
		timeout    time.Duration // client timeout; 0 means none
		wantStatus int
		wantErr    bool // the client gives up
	}{
		{name: "no_fault_passes_through", wantStatus: http.StatusOK},
		{name: "fixed_status_replaces_every_answer", fault: mock.Fault{Status: http.StatusForbidden}, wantStatus: http.StatusForbidden},
		{name: "error_rate_one_fails_every_call", fault: mock.Fault{ErrorRate: 1}, wantStatus: http.StatusServiceUnavailable},
		{name: "error_rate_zero_never_fails", fault: mock.Fault{ErrorRate: 0}, wantStatus: http.StatusOK},
		{name: "latency_within_the_timeout_answers", fault: mock.Fault{LatencyMS: 10}, timeout: time.Second, wantStatus: http.StatusOK},
		{name: "latency_beyond_the_timeout_is_a_client_error", fault: mock.Fault{LatencyMS: 5000}, timeout: 50 * time.Millisecond, wantErr: true},
		{name: "hang_never_answers", fault: mock.Fault{Hang: true}, timeout: 50 * time.Millisecond, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			faults := mock.NewFaults(tt.fault)
			srv := httptest.NewServer(faults.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})))
			t.Cleanup(srv.Close)
			hc := srv.Client()
			hc.Timeout = tt.timeout
			resp, err := hc.Get(srv.URL)
			if tt.wantErr {
				if err == nil {
					resp.Body.Close()
					t.Fatal("Get() error = nil, want the client to give up")
				}
				return
			}
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestFaultsAdmin(t *testing.T) {
	initial := mock.Fault{LatencyMS: 100}
	tests := []struct {
		name       string
		method     string
		body       string
		wantStatus int
		wantFault  mock.Fault // the current fault after the call
	}{
		{name: "get_returns_the_startup_default", method: http.MethodGet, wantStatus: http.StatusOK, wantFault: initial},
		{name: "post_sets_the_fault", method: http.MethodPost, body: `{"status": 403}`, wantStatus: http.StatusOK, wantFault: mock.Fault{Status: 403}},
		{name: "post_replaces_rather_than_merges", method: http.MethodPost, body: `{"hang": true}`, wantStatus: http.StatusOK, wantFault: mock.Fault{Hang: true}},
		{name: "post_invalid_is_rejected_and_keeps_the_fault", method: http.MethodPost, body: `{"error_rate": 2}`, wantStatus: http.StatusBadRequest, wantFault: mock.Fault{Hang: true}},
		{name: "post_bad_json_is_rejected", method: http.MethodPost, body: `{`, wantStatus: http.StatusBadRequest, wantFault: mock.Fault{Hang: true}},
		{name: "delete_resets_to_no_fault", method: http.MethodDelete, wantStatus: http.StatusOK, wantFault: mock.Fault{}},
		{name: "other_methods_are_not_allowed", method: http.MethodPut, body: `{}`, wantStatus: http.StatusMethodNotAllowed, wantFault: mock.Fault{}},
	}
	faults := mock.NewFaults(initial)
	srv := httptest.NewServer(faults.AdminHandler())
	t.Cleanup(srv.Close)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tt.method, srv.URL, strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, tt.wantStatus, body)
			}
			if got := faults.Current(); !reflect.DeepEqual(got, tt.wantFault) {
				t.Errorf("Current() = %+v, want %+v", got, tt.wantFault)
			}
			if resp.StatusCode == http.StatusOK {
				var echoed mock.Fault
				if err := json.NewDecoder(resp.Body).Decode(&echoed); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				if !reflect.DeepEqual(echoed, tt.wantFault) {
					t.Errorf("body = %+v, want the current fault %+v", echoed, tt.wantFault)
				}
			}
		})
	}
}

// TestServe starts a mock server on a free port and stops it through its
// context, as the mains do on SIGTERM.
func TestServe(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "serves_then_stops_on_cancel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- mock.Serve(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), slog.New(slog.DiscardHandler))
			}()
			resp, err := http.Get("http://" + ln.Addr().String() + "/")
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Errorf("status = %d, want 204", resp.StatusCode)
			}
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					t.Errorf("Serve() error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Serve() did not return after cancel")
			}
		})
	}
}
