package errmap_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"docviewer/internal/aggregator/source"
	"docviewer/internal/aggregator/source/errmap"
	"docviewer/internal/client"
)

func TestFromClient(t *testing.T) {
	tests := []struct {
		name          string
		in            error
		wantDone      bool // nil error and an empty, final page
		wantPermanent bool // wraps source.ErrPermanent
	}{
		{name: "not_found_is_an_empty_final_page", in: fmt.Errorf("%w: http status 404", client.ErrNotFound), wantDone: true},
		{name: "rejected_is_permanent", in: fmt.Errorf("%w: http status 403", client.ErrRejected), wantPermanent: true},
		{name: "too_large_is_permanent", in: client.ErrTooLarge, wantPermanent: true},
		{name: "malformed_is_permanent", in: client.ErrMalformed, wantPermanent: true},
		{name: "unavailable_is_transient", in: fmt.Errorf("%w: http status 503", client.ErrUnavailable)},
		{name: "deadline_is_transient", in: context.DeadlineExceeded},
		{name: "cancelled_is_transient", in: context.Canceled},
		{name: "unknown_error_is_transient", in: errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := errmap.FromClient(tt.in)
			if tt.wantDone {
				if err != nil {
					t.Fatalf("FromClient() error = %v, want nil", err)
				}
				if len(page.Docs) != 0 || page.HasMore || page.SkipTo != nil {
					t.Errorf("FromClient() page = %+v, want empty and final", page)
				}
				return
			}
			if err == nil {
				t.Fatal("FromClient() error = nil, want an error")
			}
			if got := errors.Is(err, source.ErrPermanent); got != tt.wantPermanent {
				t.Errorf("errors.Is(err, ErrPermanent) = %v, want %v (err %v)", got, tt.wantPermanent, err)
			}
			if !errors.Is(err, tt.in) {
				t.Errorf("FromClient() = %v, want it to keep the original %v", err, tt.in)
			}
		})
	}
}
