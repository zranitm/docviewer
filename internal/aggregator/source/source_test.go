package source_test

import (
	"errors"
	"testing"

	"docviewer/internal/aggregator/source"
)

func TestErrUnsupportedIsPermanent(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unsupported_wraps_permanent", err: source.ErrUnsupported, want: true},
		{name: "permanent_itself", err: source.ErrPermanent, want: true},
		{name: "other_error_is_transient", err: errors.New("connection reset"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, source.ErrPermanent); got != tt.want {
				t.Errorf("errors.Is(%v, ErrPermanent) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
