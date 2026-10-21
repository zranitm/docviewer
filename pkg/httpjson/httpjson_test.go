package httpjson_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"docviewer/pkg/httpjson"
)

type payload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestGet(t *testing.T) {
	const okBody = `{"name":"a","count":2}`
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		maxBytes   int64 // 0 means a generous cap
		ctx        func(t *testing.T) context.Context
		want       payload
		wantStatus int   // a *StatusError with this code
		errIs      error // the error must wrap this
	}{
		{
			name:    "decodes_a_2xx_body",
			handler: reply(http.StatusOK, okBody),
			want:    payload{Name: "a", Count: 2},
		},
		{
			name:    "created_counts_as_2xx",
			handler: reply(http.StatusCreated, okBody),
			want:    payload{Name: "a", Count: 2},
		},
		{
			name:     "body_at_the_limit_decodes",
			handler:  reply(http.StatusOK, okBody),
			maxBytes: int64(len(okBody)),
			want:     payload{Name: "a", Count: 2},
		},
		{
			name:     "body_over_the_limit_is_too_large",
			handler:  reply(http.StatusOK, okBody),
			maxBytes: int64(len(okBody)) - 1,
			errIs:    httpjson.ErrTooLarge,
		},
		{
			name:       "not_found_is_a_status_error_without_decoding",
			handler:    reply(http.StatusNotFound, okBody), // a decodable body must not be decoded
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "server_error_is_a_status_error",
			handler:    reply(http.StatusServiceUnavailable, `{"error":"down"}`),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:    "malformed_body_is_a_decode_error",
			handler: reply(http.StatusOK, `{"name":`),
			errIs:   httpjson.ErrDecode,
		},
		{
			name:    "wrong_shape_is_a_decode_error",
			handler: reply(http.StatusOK, `{"name":"a","count":"two"}`),
			errIs:   httpjson.ErrDecode,
		},
		{
			name:    "cancelled_context_is_returned",
			handler: reply(http.StatusOK, okBody),
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			errIs: context.Canceled,
		},
		{
			name: "deadline_stops_the_call",
			handler: func(w http.ResponseWriter, r *http.Request) {
				<-r.Context().Done() // hang until the client gives up
			},
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			errIs: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)
			ctx := t.Context()
			if tt.ctx != nil {
				ctx = tt.ctx(t)
			}
			c := httpjson.Client{HTTP: srv.Client(), MaxBytes: tt.maxBytes}
			if c.MaxBytes == 0 {
				c.MaxBytes = 1 << 20
			}
			got, err := httpjson.Get[payload](ctx, c, srv.URL+"/thing")
			switch {
			case tt.wantStatus != 0:
				var se *httpjson.StatusError
				if !errors.As(err, &se) || se.Code != tt.wantStatus {
					t.Fatalf("Get() error = %v, want *StatusError with code %d", err, tt.wantStatus)
				}
			case tt.errIs != nil:
				if !errors.Is(err, tt.errIs) {
					t.Fatalf("Get() error = %v, want errors.Is %v", err, tt.errIs)
				}
			case err != nil:
				t.Fatalf("Get() error = %v", err)
			}
			if err != nil {
				if !reflect.DeepEqual(got, payload{}) {
					t.Errorf("Get() = %+v on error, want the zero value", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Get() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestGetDrainsErrorBodies proves a non-2xx body is read to its end, so the
// keep-alive connection is reused by the next request instead of being
// dropped: two calls, one connection.
func TestGetDrainsErrorBodies(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "not_found", status: http.StatusNotFound},
		{name: "server_error", status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var conns atomic.Int32
			srv := httptest.NewUnstartedServer(reply(tt.status, strings.Repeat("x", 4096)))
			srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					conns.Add(1)
				}
			}
			srv.Start()
			t.Cleanup(srv.Close)
			c := httpjson.Client{HTTP: srv.Client(), MaxBytes: 1 << 20}
			for range 2 {
				if _, err := httpjson.Get[payload](t.Context(), c, srv.URL); err == nil {
					t.Fatal("Get() error = nil, want a status error")
				}
			}
			if got := conns.Load(); got != 1 {
				t.Errorf("connections opened = %d, want 1 (body not drained)", got)
			}
		})
	}
}

func TestGetBadURL(t *testing.T) {
	c := httpjson.Client{HTTP: &http.Client{}, MaxBytes: 1 << 20}
	if _, err := httpjson.Get[payload](t.Context(), c, "://not-a-url"); err == nil {
		t.Fatal("Get() error = nil, want a URL error")
	}
}

func TestStatusErrorText(t *testing.T) {
	tests := []struct {
		name string
		code int
		want string
	}{
		{name: "not_found", code: http.StatusNotFound, want: "http status 404"},
		{name: "bad_gateway", code: http.StatusBadGateway, want: "http status 502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &httpjson.StatusError{Code: tt.code}
			if got := err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}
