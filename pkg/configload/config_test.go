package configload_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"docviewer/pkg/configload"
)

// schema is a made-up configuration, so these tests know nothing about the
// service: one field of every kind the loader must handle.
type schema struct {
	Server server                   `mapstructure:"server"`
	Tags   []string                 `mapstructure:"tags"`
	Limits map[string]time.Duration `mapstructure:"limits"`
	Peers  map[string]peer          `mapstructure:"peers" validate:"dive"`
	Shared shared                   `mapstructure:"shared" envprefix:"SHARED"`
	Token  string                   `mapstructure:"token" envprefix:"API_TOKEN"`
}

type server struct {
	Name    string        `mapstructure:"name"`
	Addr    string        `mapstructure:"addr" validate:"required"`
	Port    int           `mapstructure:"port" validate:"min=1,max=65535"`
	Timeout time.Duration `mapstructure:"timeout" validate:"gt=0"`
	Debug   bool          `mapstructure:"debug"`
	Origin  string        `mapstructure:"origin" validate:"omitempty,url"`
}

type peer struct {
	BaseURL string        `mapstructure:"base_url" validate:"required,url"`
	Timeout time.Duration `mapstructure:"timeout"`
}

type shared struct {
	Token string `mapstructure:"token"`
}

const reservedPort = 9999

var errReservedPort = errors.New("port 9999 is reserved")

// Validate is the cross-field rule no tag can express.
func (s *schema) Validate() error {
	if s.Server.Port == reservedPort {
		return errReservedPort
	}
	return nil
}

const prefix = "APP"

// baseYAML sets every key except server.origin, which stays for the
// "set from the environment only" case.
const baseYAML = `
server:
  name: svc
  addr: ":8080"
  port: 8080
  timeout: 3s
  debug: false
tags: [a, b]
limits:
  read: 1s
peers:
  sales:
    base_url: http://sales:8081
    timeout: 2s
shared:
  token: from-yaml
token: leaf-from-yaml
`

// base is what baseYAML decodes to with no overrides.
func base() schema {
	return schema{
		Server: server{Name: "svc", Addr: ":8080", Port: 8080, Timeout: 3 * time.Second},
		Tags:   []string{"a", "b"},
		Limits: map[string]time.Duration{"read": time.Second},
		Peers:  map[string]peer{"sales": {BaseURL: "http://sales:8081", Timeout: 2 * time.Second}},
		Shared: shared{Token: "from-yaml"},
		Token:  "leaf-from-yaml",
	}
}

func TestLoadFrom(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string            // empty means baseYAML
		env     map[string]string // set for this case only
		strict  bool
		mutate  func(*schema) // the expected differences from base()
		wantErr bool
		errIs   error    // the error must wrap this
		errHas  []string // substrings the error must contain
		errNot  []string // substrings the error must never contain
	}{
		{
			name: "yaml_only",
		},
		{
			name: "env_overrides_yaml",
			env:  map[string]string{"APP_SERVER_ADDR": ":9090"},
			mutate: func(s *schema) {
				s.Server.Addr = ":9090"
			},
		},
		{
			name: "env_sets_key_the_yaml_omits",
			env:  map[string]string{"APP_SERVER_ORIGIN": "http://ui.local"},
			mutate: func(s *schema) {
				s.Server.Origin = "http://ui.local"
			},
		},
		{
			name: "empty_env_blanks_value",
			env:  map[string]string{"APP_SERVER_NAME": ""},
			mutate: func(s *schema) {
				s.Server.Name = ""
			},
		},
		{
			name: "envprefix_on_struct_drops_service_prefix",
			env:  map[string]string{"SHARED_TOKEN": "from-env", "APP_SHARED_TOKEN": "ignored"},
			mutate: func(s *schema) {
				s.Shared.Token = "from-env"
			},
		},
		{
			name: "envprefix_on_field_is_the_exact_name",
			env:  map[string]string{"API_TOKEN": "leaf-env", "APP_TOKEN": "ignored"},
			mutate: func(s *schema) {
				s.Token = "leaf-env"
			},
		},
		{
			name: "duration_from_env",
			env:  map[string]string{"APP_SERVER_TIMEOUT": "1500ms"},
			mutate: func(s *schema) {
				s.Server.Timeout = 1500 * time.Millisecond
			},
		},
		{
			name: "bool_from_env",
			env:  map[string]string{"APP_SERVER_DEBUG": "true"},
			mutate: func(s *schema) {
				s.Server.Debug = true
			},
		},
		{
			name: "int_from_env",
			env:  map[string]string{"APP_SERVER_PORT": "8443"},
			mutate: func(s *schema) {
				s.Server.Port = 8443
			},
		},
		{
			name: "slice_from_env_comma_separated",
			env:  map[string]string{"APP_TAGS": "x,y,z"},
			mutate: func(s *schema) {
				s.Tags = []string{"x", "y", "z"}
			},
		},
		{
			name: "map_from_env_as_pairs",
			env:  map[string]string{"APP_LIMITS": "read=2s,write=4s"},
			mutate: func(s *schema) {
				s.Limits = map[string]time.Duration{"read": 2 * time.Second, "write": 4 * time.Second}
			},
		},
		{
			name: "map_from_env_as_json_replaces_the_map",
			env: map[string]string{
				"APP_PEERS": `{"svc":{"base_url":"http://localhost:8082","timeout":"1s"},"crm":{"base_url":"http://localhost:8083"}}`,
			},
			mutate: func(s *schema) {
				s.Peers = map[string]peer{
					"svc": {BaseURL: "http://localhost:8082", Timeout: time.Second},
					"crm": {BaseURL: "http://localhost:8083"},
				}
			},
		},
		{
			name:    "strict_rejects_unknown_key",
			yaml:    baseYAML + "serverr:\n  addr: typo\n",
			strict:  true,
			wantErr: true,
			errHas:  []string{"serverr"},
		},
		{
			name: "lenient_ignores_unknown_key",
			yaml: baseYAML + "serverr:\n  addr: typo\n",
		},
		{
			name:    "tag_error_names_the_yaml_key",
			yaml:    strings.Replace(baseYAML, "port: 8080", "port: 0", 1),
			wantErr: true,
			errHas:  []string{"server.port"},
			errNot:  []string{"Server.Port"},
		},
		{
			name:    "tag_error_never_echoes_the_value",
			yaml:    strings.Replace(baseYAML, "  name: svc\n", "  name: svc\n  origin: hunter2 is not a url\n", 1),
			wantErr: true,
			errHas:  []string{"server.origin"},
			errNot:  []string{"hunter2"},
		},
		{
			name:    "nested_map_tag_error_names_the_entry",
			yaml:    strings.Replace(baseYAML, "base_url: http://sales:8081", "base_url: s3cret", 1),
			wantErr: true,
			errHas:  []string{"peers.sales.base_url"},
			errNot:  []string{"s3cret"},
		},
		{
			name:    "missing_required_key",
			yaml:    strings.Replace(baseYAML, "  addr: \":8080\"\n", "", 1),
			wantErr: true,
			errHas:  []string{"server.addr"},
		},
		{
			name:    "validate_method_error_is_wrapped",
			yaml:    strings.Replace(baseYAML, "port: 8080", "port: 9999", 1),
			wantErr: true,
			errIs:   errReservedPort,
		},
		{
			name:    "invalid_yaml",
			yaml:    "server: [",
			wantErr: true,
		},
		{
			name:    "yaml_type_mismatch_names_the_key",
			yaml:    strings.Replace(baseYAML, "port: 8080", "port: eighty", 1),
			wantErr: true,
			errHas:  []string{"server.port"},
		},
		{
			name:    "env_type_mismatch_names_the_key",
			env:     map[string]string{"APP_SERVER_PORT": "eighty"},
			wantErr: true,
			errHas:  []string{"server.port"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			yaml := tt.yaml
			if yaml == "" {
				yaml = baseYAML
			}
			got, err := configload.LoadFrom[schema](strings.NewReader(yaml), tt.name, configload.Options{EnvPrefix: prefix, Strict: tt.strict})
			if tt.wantErr {
				checkErr(t, err, tt.errIs, tt.errHas, tt.errNot)
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom() error = %v", err)
			}
			want := base()
			if tt.mutate != nil {
				tt.mutate(&want)
			}
			if !reflect.DeepEqual(*got, want) {
				t.Errorf("LoadFrom() = %+v\nwant %+v", *got, want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		write   bool // create the file first
		wantErr bool
	}{
		{name: "reads_the_file", write: true},
		{name: "missing_file_is_an_error", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if tt.write {
				writeFile(t, path, baseYAML)
			}
			got, err := configload.Load[schema](configload.Options{Path: path, EnvPrefix: prefix, Strict: true})
			if tt.wantErr {
				checkErr(t, err, nil, []string{filepath.Base(path)}, nil)
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if want := base(); !reflect.DeepEqual(*got, want) {
				t.Errorf("Load() = %+v\nwant %+v", *got, want)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	tests := []struct {
		name       string
		configPath string   // CONFIG_PATH; empty means unset
		files      []string // present in dir
		want       string   // relative to dir, unless configPath is set
	}{
		{
			name:       "config_path_wins",
			configPath: filepath.Join("elsewhere", "custom.yaml"),
			files:      []string{"config.local.yaml", "config.yaml"},
			want:       filepath.Join("elsewhere", "custom.yaml"),
		},
		{
			name:  "local_file_preferred",
			files: []string{"config.local.yaml", "config.yaml"},
			want:  "config.local.yaml",
		},
		{
			name:  "committed_defaults_otherwise",
			files: []string{"config.yaml"},
			want:  "config.yaml",
		},
		{
			name: "defaults_even_when_absent",
			want: "config.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CONFIG_PATH", tt.configPath)
			dir := t.TempDir()
			for _, f := range tt.files {
				writeFile(t, filepath.Join(dir, f), "")
			}
			want := tt.want
			if tt.configPath == "" {
				want = filepath.Join(dir, want)
			}
			if got := configload.ResolvePath(dir); got != want {
				t.Errorf("ResolvePath() = %q, want %q", got, want)
			}
		})
	}
}

func TestLoadDotEnv(t *testing.T) {
	tests := []struct {
		name    string
		preset  string            // the variable's value before the call; empty means unset
		files   map[string]string // name -> content; {KEY} is the variable's name
		paths   []string          // the files to load, in order; one not in files is never created
		want    string            // the variable's value afterwards
		wantErr bool
	}{
		{
			name:  "sets_a_variable_that_is_unset",
			files: map[string]string{"a.env": "{KEY}=from-file\n"},
			paths: []string{"a.env"},
			want:  "from-file",
		},
		{
			name:   "never_overrides_a_set_variable",
			preset: "from-environment",
			files:  map[string]string{"a.env": "{KEY}=from-file\n"},
			paths:  []string{"a.env"},
			want:   "from-environment",
		},
		{
			name:  "first_file_wins",
			files: map[string]string{"a.env": "{KEY}=first\n", "b.env": "{KEY}=second\n"},
			paths: []string{"a.env", "b.env"},
			want:  "first",
		},
		{
			name:  "missing_file_is_skipped",
			files: map[string]string{"b.env": "{KEY}=from-b\n"},
			paths: []string{"missing.env", "b.env"},
			want:  "from-b",
		},
		{
			name:    "malformed_file_is_an_error",
			files:   map[string]string{"a.env": "BAD-KEY=value\n"},
			paths:   []string{"a.env"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "DOTENV_TEST_" + strings.ToUpper(tt.name)
			t.Setenv(key, tt.preset) // restores the original state afterwards
			if tt.preset == "" {
				os.Unsetenv(key)
			}
			dir := t.TempDir()
			for name, content := range tt.files {
				writeFile(t, filepath.Join(dir, name), strings.ReplaceAll(content, "{KEY}", key))
			}
			paths := make([]string, len(tt.paths))
			for i, p := range tt.paths {
				paths[i] = filepath.Join(dir, p)
			}
			err := configload.LoadDotEnv(paths...)
			if tt.wantErr {
				if err == nil {
					t.Fatal("LoadDotEnv() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadDotEnv() error = %v", err)
			}
			if got := os.Getenv(key); got != tt.want {
				t.Errorf("%s = %q, want %q", key, got, tt.want)
			}
		})
	}
}

func checkErr(t *testing.T, err, is error, has, not []string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want an error")
	}
	if is != nil && !errors.Is(err, is) {
		t.Errorf("error = %v, want errors.Is %v", err, is)
	}
	for _, s := range has {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q", err, s)
		}
	}
	for _, s := range not {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error %q echoes %q", err, s)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
