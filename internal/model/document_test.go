package model_test

import (
	"testing"

	"docviewer/internal/model"
)

func TestDocumentID(t *testing.T) {
	tests := []struct {
		name       string
		source     model.Source
		upstreamID string
		want       string
	}{
		{name: "sales_string_id", source: "sales", upstreamID: "S-1001", want: "sales:S-1001"},
		{name: "service_numeric_id", source: "service", upstreamID: "9001", want: "service:9001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.DocumentID(tt.source, tt.upstreamID); got != tt.want {
				t.Errorf("DocumentID(%q, %q) = %q, want %q", tt.source, tt.upstreamID, got, tt.want)
			}
		})
	}
}

func TestUpstreamID(t *testing.T) {
	tests := []struct {
		name   string
		source model.Source
		id     string
		want   string
	}{
		{name: "strips_own_prefix", source: "sales", id: "sales:S-1001", want: "S-1001"},
		{name: "keeps_foreign_prefix", source: "sales", id: "service:9001", want: "service:9001"},
		{name: "keeps_unprefixed_id", source: "sales", id: "S-1001", want: "S-1001"},
		{name: "strips_only_the_first_separator", source: "sales", id: "sales:S-10:01", want: "S-10:01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.UpstreamID(tt.source, tt.id); got != tt.want {
				t.Errorf("UpstreamID(%q, %q) = %q, want %q", tt.source, tt.id, got, tt.want)
			}
		})
	}
}
