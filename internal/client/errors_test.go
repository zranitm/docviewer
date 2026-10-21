package client_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"

	"docviewer/internal/client"
	"docviewer/pkg/httpjson"
)

func TestFromHTTP(t *testing.T) {
	connErr := &url.Error{Op: "Get", URL: "http://sales/x", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}
	tests := []struct {
		name  string
		in    error
		want  error // the sentinel the result must wrap; nil means the input is returned unchanged
		keeps error // the original the result must still wrap, for logs
	}{
		{name: "nil_stays_nil"},
		{name: "cancelled_passes_through", in: context.Canceled},
		{name: "deadline_passes_through", in: context.DeadlineExceeded},
		{name: "wrapped_deadline_passes_through", in: fmt.Errorf("get: %w", context.DeadlineExceeded)},
		{name: "404_is_not_found", in: &httpjson.StatusError{Code: http.StatusNotFound}, want: client.ErrNotFound},
		{name: "400_is_rejected", in: &httpjson.StatusError{Code: http.StatusBadRequest}, want: client.ErrRejected},
		{name: "401_is_rejected", in: &httpjson.StatusError{Code: http.StatusUnauthorized}, want: client.ErrRejected},
		{name: "403_is_rejected", in: &httpjson.StatusError{Code: http.StatusForbidden}, want: client.ErrRejected},
		{name: "422_is_rejected", in: &httpjson.StatusError{Code: http.StatusUnprocessableEntity}, want: client.ErrRejected},
		{name: "429_is_unavailable", in: &httpjson.StatusError{Code: http.StatusTooManyRequests}, want: client.ErrUnavailable},
		{name: "500_is_unavailable", in: &httpjson.StatusError{Code: http.StatusInternalServerError}, want: client.ErrUnavailable},
		{name: "503_is_unavailable", in: &httpjson.StatusError{Code: http.StatusServiceUnavailable}, want: client.ErrUnavailable},
		{name: "other_4xx_is_unavailable", in: &httpjson.StatusError{Code: http.StatusConflict}, want: client.ErrUnavailable},
		{name: "too_large_is_too_large", in: httpjson.ErrTooLarge, want: client.ErrTooLarge},
		{name: "decode_error_is_malformed", in: fmt.Errorf("%w: unexpected end", httpjson.ErrDecode), want: client.ErrMalformed},
		{name: "connection_error_is_unavailable", in: connErr, want: client.ErrUnavailable, keeps: connErr},
		{name: "unknown_error_is_unavailable", in: errors.New("boom"), want: client.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := client.FromHTTP(tt.in)
			if tt.want == nil {
				if !errors.Is(got, tt.in) || (tt.in == nil) != (got == nil) {
					t.Fatalf("FromHTTP(%v) = %v, want it unchanged", tt.in, got)
				}
				if tt.in != nil && got.Error() != tt.in.Error() {
					t.Errorf("FromHTTP(%v) = %q, want the input's own text", tt.in, got)
				}
				return
			}
			if !errors.Is(got, tt.want) {
				t.Fatalf("FromHTTP(%v) = %v, want errors.Is %v", tt.in, got, tt.want)
			}
			if !errors.Is(got, tt.in) {
				t.Errorf("FromHTTP(%v) = %v, want it to keep the original", tt.in, got)
			}
			var se *httpjson.StatusError
			if errors.As(tt.in, &se) {
				var kept *httpjson.StatusError
				if !errors.As(got, &kept) || kept.Code != se.Code {
					t.Errorf("FromHTTP(%v) lost the status code", tt.in)
				}
			}
		})
	}
}
