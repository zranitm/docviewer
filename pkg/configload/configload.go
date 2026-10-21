// Package configload loads a typed configuration from one YAML document with
// environment overrides on top, validated at start. It knows nothing about
// the schema it loads: the schema is a struct with mapstructure and validate
// tags, and optionally a Validate method for rules tags can't express.
//
// Every key of the schema is bound to an environment variable, so a key the
// YAML omits can still be set from the environment and an empty variable can
// blank a value. The name is the prefix plus the key path in upper case
// (server.addr becomes PREFIX_SERVER_ADDR), unless a field carries an
// envprefix tag, which gives its subtree exactly that name (or NAME_<path>).
// Slices are set as comma-separated values; maps as k=v pairs or a JSON object.
package configload

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// Options controls a Load.
type Options struct {
	Path      string // the file Load reads; LoadFrom ignores it
	EnvPrefix string // "DOCVIEWER": server.addr is overridden by DOCVIEWER_SERVER_ADDR
	Strict    bool   // fail on a YAML key that no field names
}

// Validator is implemented by a schema with rules no validate tag can express.
type Validator interface{ Validate() error }

const (
	tagKey       = "mapstructure"
	tagEnvPrefix = "envprefix"
	tagValidate  = "validate"
	tagSkip      = "-"

	envPathSep = "_"
	keySep     = "."
	listSep    = ","
	pairSep    = "="

	configPathEnv = "CONFIG_PATH"
	localFile     = "config.local.yaml"
	defaultFile   = "config.yaml"
)

// Load reads opts.Path and decodes it into T.
func Load[T any](opts Options) (*T, error) {
	f, err := os.Open(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer f.Close()
	return LoadFrom[T](f, opts.Path, opts)
}

// LoadFrom decodes one YAML document from r into T, applies environment
// overrides, then validates: struct tags first, with errors named by YAML
// key, then T's Validate method if it has one. Validation messages never
// echo string values, so secrets don't reach logs. source names r in errors.
func LoadFrom[T any](r io.Reader, source string, opts Options) (*T, error) {
	cfg, err := loadFrom[T](r, opts)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", source, err)
	}
	return cfg, nil
}

func loadFrom[T any](r io.Reader, opts Options) (*T, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(r); err != nil {
		return nil, err
	}
	settings := v.AllSettings()

	var cfg T
	applyEnv(settings, reflect.TypeOf(cfg), opts.EnvPrefix)

	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           &cfg,
		TagName:          tagKey,
		ErrorUnused:      opts.Strict,
		WeaklyTypedInput: true, // environment values are strings
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(listSep),
			stringToMapHook,
		),
	})
	if err != nil {
		return nil, err
	}
	if err := dec.Decode(settings); err != nil {
		return nil, err
	}
	if err := validateTags(&cfg); err != nil {
		return nil, err
	}
	if val, ok := any(&cfg).(Validator); ok {
		if err := val.Validate(); err != nil {
			return nil, err
		}
	}
	return &cfg, nil
}

// applyEnv walks t's fields and, for every leaf whose variable is set, puts
// the variable's string into m at the field's key, replacing whatever the
// YAML had there. A struct field recurses with its own sub-map; everything
// else (scalars, durations, slices, maps) is a leaf set as a whole.
func applyEnv(m map[string]any, t reflect.Type, envBase string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		f := t.Field(i)
		key := fieldKey(f)
		if key == "" {
			continue
		}
		envName := envBase + envPathSep + strings.ToUpper(key)
		if own, ok := f.Tag.Lookup(tagEnvPrefix); ok {
			envName = own
		}
		if isSection(f.Type) {
			sub, ok := m[key].(map[string]any)
			if !ok {
				sub = map[string]any{}
				m[key] = sub
			}
			applyEnv(sub, f.Type, envName)
			continue
		}
		if val, ok := os.LookupEnv(envName); ok {
			m[key] = val
		}
	}
}

// fieldKey is the YAML key of f: its mapstructure tag, else its name in lower
// case; empty for unexported or skipped fields.
func fieldKey(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	name, _, _ := strings.Cut(f.Tag.Get(tagKey), listSep)
	switch name {
	case tagSkip:
		return ""
	case "":
		return strings.ToLower(f.Name)
	}
	return name
}

// isSection reports whether t is a struct of keys (a YAML section), as
// opposed to a struct that decodes from one value, such as time.Time.
func isSection(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct && t != reflect.TypeFor[time.Time]()
}

// stringToMapHook decodes a map set from the environment: a JSON object, or
// k=v pairs separated by commas. Values stay strings; later hooks turn them
// into the map's element type.
func stringToMapHook(from, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || to.Kind() != reflect.Map {
		return data, nil
	}
	s := strings.TrimSpace(data.(string))
	switch {
	case s == "":
		return map[string]any{}, nil
	case strings.HasPrefix(s, "{"):
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, fmt.Errorf("map from JSON: %w", err)
		}
		return m, nil
	}
	m := map[string]any{}
	for _, pair := range strings.Split(s, listSep) {
		k, val, ok := strings.Cut(pair, pairSep)
		if !ok {
			return nil, fmt.Errorf("map entry %q: want key%svalue", k, pairSep)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	return m, nil
}

// validateTags checks the validate tags and reports every failing field by
// its YAML key (server.port, peers.sales.base_url), never by Go name or value.
func validateTags(cfg any) error {
	val := validator.New(validator.WithRequiredStructEnabled())
	val.RegisterTagNameFunc(fieldKey)
	err := val.Struct(cfg)
	if err == nil {
		return nil
	}
	var fieldErrs validator.ValidationErrors
	if !errors.As(err, &fieldErrs) {
		return err
	}
	errs := make([]error, 0, len(fieldErrs))
	for _, fe := range fieldErrs {
		errs = append(errs, fmt.Errorf("%s: fails %q", yamlKey(fe.Namespace()), fe.ActualTag()+paramSuffix(fe.Param())))
	}
	return errors.Join(errs...)
}

// yamlKey turns a validator namespace (schema.peers[sales].base_url) into a
// YAML key path (peers.sales.base_url).
func yamlKey(namespace string) string {
	_, path, _ := strings.Cut(namespace, keySep) // drop the root struct's name
	r := strings.NewReplacer("[", keySep, "]", "")
	return r.Replace(path)
}

func paramSuffix(p string) string {
	if p == "" {
		return ""
	}
	return pairSep + p
}

// ResolvePath picks $CONFIG_PATH, else dir/config.local.yaml (git-ignored),
// else dir/config.yaml (committed defaults). The last is returned even when
// absent, so Load reports the missing file.
func ResolvePath(dir string) string {
	if p := os.Getenv(configPathEnv); p != "" {
		return p
	}
	local := filepath.Join(dir, localFile)
	if _, err := os.Stat(local); err == nil {
		return local
	}
	return filepath.Join(dir, defaultFile)
}

// LoadDotEnv sets variables from each file that exists, never overriding one
// already set, so real environment variables win over a .env file and the
// first file wins over later ones. A missing file is skipped. Call it before
// ResolvePath and Load.
func LoadDotEnv(paths ...string) error {
	for _, p := range paths {
		if err := godotenv.Load(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("dotenv %s: %w", p, err)
		}
	}
	return nil
}
