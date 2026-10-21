package api_test

import (
	"strings"
	"testing"
	"time"

	"docviewer/internal/config"
	"docviewer/internal/handler/api"
)

func validConfig() config.Config {
	return config.Config{
		Server: config.Server{Addr: ":8080", ShutdownGrace: time.Second},
		Sources: config.Sources{
			RequestTimeout: 3 * time.Second, MaxBodyBytes: 1 << 20,
			Upstreams: map[string]config.Upstream{
				"sales":   {BaseURL: "http://mock-sales:8081", Timeout: 2 * time.Second},
				"service": {BaseURL: "http://mock-service:8082", Timeout: 2 * time.Second},
			},
		},
		Resilience: config.Resilience{MaxRetries: 1, MinBudget: 500 * time.Millisecond, MaxJitter: 100 * time.Millisecond, TripAfter: 5, Cooldown: 30 * time.Second},
		Telemetry:  config.Telemetry{LogLevel: "info", Redact: config.Redact{Enabled: true}},
	}
}

func TestNewDependencies(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string // substring; empty means success
	}{
		{name: "both_sources_wire_up"},
		{
			name:   "one_source_is_enough",
			mutate: func(c *config.Config) { delete(c.Sources.Upstreams, "service") },
		},
		{
			name: "a_source_without_an_adapter_fails_startup",
			mutate: func(c *config.Config) {
				c.Sources.Upstreams["crm"] = config.Upstream{BaseURL: "http://crm:1", Timeout: time.Second}
			},
			wantErr: `source "crm" is configured but has no adapter`,
		},
		{
			name: "a_bad_base_url_fails_startup",
			mutate: func(c *config.Config) {
				c.Sources.Upstreams["sales"] = config.Upstream{BaseURL: "://bad", Timeout: time.Second}
			},
			wantErr: `source "sales"`,
		},
		{
			name:    "no_sources_fails_startup",
			mutate:  func(c *config.Config) { c.Sources.Upstreams = map[string]config.Upstream{} },
			wantErr: "no sources",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			deps, err := api.NewDependencies(cfg, newTelemetry(t))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NewDependencies() error = %v, want one mentioning %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewDependencies() error = %v", err)
			}
			if deps.Aggregator == nil || deps.Log == nil {
				t.Errorf("NewDependencies() = %+v, want an aggregator and a logger", deps)
			}
		})
	}
}
