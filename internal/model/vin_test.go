package model_test

import (
	"errors"
	"strings"
	"testing"

	"docviewer/internal/model"
)

func TestParseVIN(t *testing.T) {
	const valid = "1M8GDM9AXKP042788" // PRD A-02's example
	tests := []struct {
		name   string
		in     string
		want   model.VIN
		errHas string // empty means success; otherwise ErrInvalidVIN with this in its text
	}{
		{name: "valid_upper_case", in: valid, want: valid},
		{name: "lower_case_normalised", in: "1m8gdm9axkp042788", want: valid},
		{name: "surrounding_whitespace_trimmed", in: " \t1M8GDM9AXKP042788\n", want: valid},
		{name: "check_digit_not_enforced", in: "1M8GDM9AXKP042789", want: "1M8GDM9AXKP042789"},
		{name: "empty", in: "", errHas: "must be 17 characters, got 0"},
		{name: "whitespace_only", in: "   ", errHas: "must be 17 characters, got 0"},
		{name: "too_short", in: "1M8GDM9AXKP04278", errHas: "must be 17 characters, got 16"},
		{name: "too_long", in: "1M8GDM9AXKP0427888", errHas: "must be 17 characters, got 18"},
		{name: "letter_I_rejected", in: "1M8GDM9AXKP04278I", errHas: "'I' is not allowed (position 17)"},
		{name: "letter_O_rejected", in: "1M8GDM9AXKP0427O8", errHas: "'O' is not allowed (position 16)"},
		{name: "letter_Q_rejected", in: "QM8GDM9AXKP042788", errHas: "'Q' is not allowed (position 1)"},
		{name: "lower_case_forbidden_letter_rejected", in: "1m8gdm9axkp04278q", errHas: "'Q' is not allowed (position 17)"},
		{name: "internal_space_rejected", in: "1M8GDM9AX P042788", errHas: "invalid character ' ' (position 10)"},
		{name: "symbol_rejected", in: "1M8GDM9AXKP-42788", errHas: "invalid character '-' (position 12)"},
		{name: "non_ascii_letter_rejected", in: "1M8GDM9AXKP04278É", errHas: "invalid character 'É' (position 17)"},
		{name: "fullwidth_digit_rejected", in: "１M8GDM9AXKP042788", errHas: "invalid character '１' (position 1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.ParseVIN(tt.in)
			if tt.errHas != "" {
				if !errors.Is(err, model.ErrInvalidVIN) {
					t.Fatalf("ParseVIN(%q) error = %v, want ErrInvalidVIN", tt.in, err)
				}
				if !strings.Contains(err.Error(), tt.errHas) {
					t.Errorf("ParseVIN(%q) error %q does not mention %q", tt.in, err, tt.errHas)
				}
				if got != "" {
					t.Errorf("ParseVIN(%q) = %q on error, want empty", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVIN(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseVIN(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
