package mock_test

import (
	"reflect"
	"testing"

	"docviewer/internal/mock"
	"docviewer/pkg/configload"
)

func TestConfigFiles(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		prefix string
		env    map[string]string
		want   mock.Config
	}{
		{
			name:   "mock_sales_defaults",
			path:   "../../configs/mock-sales.yaml",
			prefix: "MOCK_SALES",
			want:   mock.Config{Server: mock.Server{Addr: ":8081"}, PublicBaseURL: "http://localhost:8081"},
		},
		{
			name:   "mock_service_defaults",
			path:   "../../configs/mock-service.yaml",
			prefix: "MOCK_SERVICE",
			want:   mock.Config{Server: mock.Server{Addr: ":8082"}, PublicBaseURL: "http://localhost:8082"},
		},
		{
			name:   "startup_fault_from_env",
			path:   "../../configs/mock-service.yaml",
			prefix: "MOCK_SERVICE",
			env:    map[string]string{"MOCK_SERVICE_FAULTS_STATUS": "403", "MOCK_SERVICE_FAULTS_LATENCY_MS": "250"},
			want:   mock.Config{Server: mock.Server{Addr: ":8082"}, PublicBaseURL: "http://localhost:8082", Faults: mock.Fault{Status: 403, LatencyMS: 250}},
		},
		{
			name:   "public_base_url_from_env",
			path:   "../../configs/mock-sales.yaml",
			prefix: "MOCK_SALES",
			env:    map[string]string{"MOCK_SALES_PUBLIC_BASE_URL": "http://sales.example.test"},
			want:   mock.Config{Server: mock.Server{Addr: ":8081"}, PublicBaseURL: "http://sales.example.test"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got, err := configload.Load[mock.Config](configload.Options{Path: tt.path, EnvPrefix: tt.prefix, Strict: true})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("Load() = %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestConfigRejects(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "invalid_startup_fault", env: map[string]string{"MOCK_SALES_FAULTS_ERROR_RATE": "7"}},
		{name: "missing_public_base_url", env: map[string]string{"MOCK_SALES_PUBLIC_BASE_URL": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if _, err := configload.Load[mock.Config](configload.Options{Path: "../../configs/mock-sales.yaml", EnvPrefix: "MOCK_SALES", Strict: true}); err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
		})
	}
}
