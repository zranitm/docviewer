package config_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"docviewer/internal/config"
	"docviewer/pkg/configload"
)

// defaultsPath is the committed configs/config.yaml, loaded as main does.
const defaultsPath = "../../configs/config.yaml"

// defaults is what configs/config.yaml must decode to (LLD section 9).
func defaults() config.Config {
	return config.Config{
		Server: config.Server{Addr: ":8080", CORSOrigins: []string{}, ShutdownGrace: 10 * time.Second},
		Sources: config.Sources{
			RequestTimeout: 3 * time.Second,
			MaxBodyBytes:   1 << 20,
			Upstreams: map[string]config.Upstream{
				"sales":   {BaseURL: "http://mock-sales:8081", Timeout: 2 * time.Second},
				"service": {BaseURL: "http://mock-service:8082", Timeout: 2 * time.Second},
			},
		},
		Resilience: config.Resilience{
			MaxRetries: 1,
			MinBudget:  500 * time.Millisecond,
			MaxJitter:  100 * time.Millisecond,
			TripAfter:  5,
			Cooldown:   30 * time.Second,
		},
		Telemetry: config.Telemetry{LogLevel: "info", Redact: config.Redact{Enabled: true}},
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string    // DOCVIEWER_* overrides on top of the committed defaults
		mutate  func(*config.Config) // the expected differences from defaults()
		wantErr bool
		errHas  []string // substrings the error must contain
		errNot  []string // substrings the error must never contain
	}{
		{
			name: "committed_defaults_load",
		},
		{
			name: "upstreams_overridden_as_json",
			env: map[string]string{
				"DOCVIEWER_SOURCES_UPSTREAMS": `{"sales":{"base_url":"http://localhost:8081","timeout":"2s"},"service":{"base_url":"http://localhost:8082","timeout":"2s"}}`,
			},
			mutate: func(c *config.Config) {
				c.Sources.Upstreams = map[string]config.Upstream{
					"sales":   {BaseURL: "http://localhost:8081", Timeout: 2 * time.Second},
					"service": {BaseURL: "http://localhost:8082", Timeout: 2 * time.Second},
				}
			},
		},
		{
			name: "cors_origins_from_env",
			env:  map[string]string{"DOCVIEWER_SERVER_CORS_ORIGINS": "http://localhost:5173,http://ui.local"},
			mutate: func(c *config.Config) {
				c.Server.CORSOrigins = []string{"http://localhost:5173", "http://ui.local"}
			},
		},
		{
			name: "redact_disabled_from_env",
			env:  map[string]string{"DOCVIEWER_TELEMETRY_REDACT_ENABLED": "false"},
			mutate: func(c *config.Config) {
				c.Telemetry.Redact.Enabled = false
			},
		},
		{
			name:    "source_timeout_equal_to_request_timeout",
			env:     map[string]string{"DOCVIEWER_SOURCES_REQUEST_TIMEOUT": "2s"},
			wantErr: true,
			errHas:  []string{"sources.request_timeout"},
		},
		{
			name:    "source_timeout_above_request_timeout",
			env:     map[string]string{"DOCVIEWER_SOURCES_REQUEST_TIMEOUT": "1s"},
			wantErr: true,
			errHas:  []string{"sources.request_timeout"},
		},
		{
			name:    "min_budget_at_source_timeout",
			env:     map[string]string{"DOCVIEWER_RESILIENCE_MIN_BUDGET": "2s"},
			wantErr: true,
			errHas:  []string{"resilience.min_budget"},
		},
		{
			name:    "no_upstreams",
			env:     map[string]string{"DOCVIEWER_SOURCES_UPSTREAMS": "{}"},
			wantErr: true,
			errHas:  []string{"sources.upstreams"},
		},
		{
			name:    "upstream_without_base_url",
			env:     map[string]string{"DOCVIEWER_SOURCES_UPSTREAMS": `{"sales":{"timeout":"2s"}}`},
			wantErr: true,
			errHas:  []string{"sources.upstreams.sales.base_url"},
		},
		{
			name:    "upstream_bad_url_is_not_echoed",
			env:     map[string]string{"DOCVIEWER_SOURCES_UPSTREAMS": `{"sales":{"base_url":"nota url","timeout":"2s"}}`},
			wantErr: true,
			errHas:  []string{"sources.upstreams.sales.base_url"},
			errNot:  []string{"nota"},
		},
		{
			name:    "upstream_zero_timeout",
			env:     map[string]string{"DOCVIEWER_SOURCES_UPSTREAMS": `{"sales":{"base_url":"http://localhost:8081","timeout":"0s"}}`},
			wantErr: true,
			errHas:  []string{"sources.upstreams.sales.timeout"},
		},
		{
			name:    "bad_log_level",
			env:     map[string]string{"DOCVIEWER_TELEMETRY_LOG_LEVEL": "verbose"},
			wantErr: true,
			errHas:  []string{"telemetry.log_level"},
		},
		{
			name:    "zero_shutdown_grace",
			env:     map[string]string{"DOCVIEWER_SERVER_SHUTDOWN_GRACE": "0s"},
			wantErr: true,
			errHas:  []string{"server.shutdown_grace"},
		},
		{
			name:    "zero_max_body_bytes",
			env:     map[string]string{"DOCVIEWER_SOURCES_MAX_BODY_BYTES": "0"},
			wantErr: true,
			errHas:  []string{"sources.max_body_bytes"},
		},
		{
			name:    "negative_max_retries",
			env:     map[string]string{"DOCVIEWER_RESILIENCE_MAX_RETRIES": "-1"},
			wantErr: true,
			errHas:  []string{"resilience.max_retries"},
		},
		{
			name:    "zero_trip_after",
			env:     map[string]string{"DOCVIEWER_RESILIENCE_TRIP_AFTER": "0"},
			wantErr: true,
			errHas:  []string{"resilience.trip_after"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got, err := configload.Load[config.Config](configload.Options{Path: defaultsPath, EnvPrefix: "DOCVIEWER", Strict: true})
			if tt.wantErr {
				if err == nil {
					t.Fatal("Load() error = nil, want an error")
				}
				for _, s := range tt.errHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q does not mention %q", err, s)
					}
				}
				for _, s := range tt.errNot {
					if strings.Contains(err.Error(), s) {
						t.Errorf("error %q echoes %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			want := defaults()
			if tt.mutate != nil {
				tt.mutate(&want)
			}
			if !reflect.DeepEqual(*got, want) {
				t.Errorf("Load() = %+v\nwant %+v", *got, want)
			}
		})
	}
}
