package aggregator_test

import (
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"docviewer/internal/aggregator"
	"docviewer/internal/model"
)

func TestBuilder(t *testing.T) {
	type add struct {
		name    model.Source
		timeout time.Duration
	}
	tests := []struct {
		name      string
		adds      []add
		wantErr   string         // substring; empty means success
		wantOrder []model.Source // the order responses list sources in
	}{
		{name: "no_sources_is_an_error", wantErr: "no sources"},
		{name: "one_source", adds: []add{{"sales", time.Second}}, wantOrder: []model.Source{"sales"}},
		{name: "sources_come_out_in_name_order", adds: []add{{"service", time.Second}, {"crm", time.Second}, {"sales", time.Second}}, wantOrder: []model.Source{"crm", "sales", "service"}},
		{name: "duplicate_name_is_an_error", adds: []add{{"sales", time.Second}, {"sales", time.Second}}, wantErr: `"sales" added twice`},
		{name: "zero_timeout_is_an_error", adds: []add{{"sales", 0}}, wantErr: "timeout must be positive"},
		{name: "negative_timeout_is_an_error", adds: []add{{"sales", -time.Second}}, wantErr: "timeout must be positive"},
		{name: "first_error_is_kept", adds: []add{{"sales", 0}, {"sales", time.Second}}, wantErr: "timeout must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := aggregator.NewBuilder(aggregator.Config{RequestTimeout: 3 * time.Second}, slog.New(slog.DiscardHandler))
			for _, a := range tt.adds {
				b.AddSource(&fakeSource{name: a.name}, a.timeout)
			}
			agg, err := b.Build()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Build() error = %v, want one mentioning %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			res, err := agg.List(t.Context(), aggregator.ListRequest{Query: aggregator.Query{VIN: vin}, Limit: 20})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			got := make([]model.Source, len(res.Sources))
			for i, s := range res.Sources {
				got[i] = s.Source
			}
			if !reflect.DeepEqual(got, tt.wantOrder) {
				t.Errorf("sources = %v, want %v", got, tt.wantOrder)
			}
		})
	}
}
