package model_test

import (
	"testing"

	"docviewer/internal/model"
)

func TestCanonicalType(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "sales_upper_snake_is_lower_cased", in: "PURCHASE_AGREEMENT", want: "purchase_agreement"},
		{name: "service_snake_passes_through", in: "repair_order", want: "repair_order"},
		{name: "mixed_case_is_normalised", in: "Recall_Notice", want: "recall_notice"},
		{name: "surrounding_space_is_trimmed", in: " sales_invoice ", want: "sales_invoice"},
		{name: "unknown_type_is_other", in: "WARRANTY_EXTENSION", want: model.TypeOther},
		{name: "empty_is_other", in: "", want: model.TypeOther},
		{name: "other_stays_other", in: "other", want: model.TypeOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.CanonicalType(tt.in); got != tt.want {
				t.Errorf("CanonicalType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestChecksum(t *testing.T) {
	const digest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "lower_case_digest_passes", in: digest, want: digest},
		{name: "upper_case_is_lowered", in: "9F86D081884C7D659A2FEAA0C55AD015A3BF4F1B2B0B822CD15D6C15B0F00A08", want: digest},
		{name: "surrounding_space_is_trimmed", in: " " + digest + "\n", want: digest},
		{name: "empty_stays_empty", in: "", want: ""},
		{name: "wrong_length_is_dropped", in: digest[:40], want: ""},
		{name: "non_hex_is_dropped", in: "zz86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.Checksum(tt.in); got != tt.want {
				t.Errorf("Checksum(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
