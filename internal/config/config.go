// Package config is the service's configuration schema (LLD section 9):
// sections with tags, plus the cross-field rules tags can't express.
// Defaults live in configs/config.yaml; every key can be overridden as
// DOCVIEWER_ plus the key path in upper case.
package config

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// Config is the whole schema.
type Config struct {
	Server     Server     `mapstructure:"server"`
	Sources    Sources    `mapstructure:"sources"`
	Resilience Resilience `mapstructure:"resilience"`
	Telemetry  Telemetry  `mapstructure:"telemetry"`
}

// Server is the HTTP listener.
type Server struct {
	Addr          string        `mapstructure:"addr" validate:"required"`
	CORSOrigins   []string      `mapstructure:"cors_origins" validate:"dive,url"` // empty: no browser origin may call the API
	ShutdownGrace time.Duration `mapstructure:"shutdown_grace" validate:"gt=0"`
}

// Sources is the fan-out and its upstreams, by source name.
type Sources struct {
	RequestTimeout time.Duration       `mapstructure:"request_timeout" validate:"gt=0"`
	MaxBodyBytes   int64               `mapstructure:"max_body_bytes" validate:"gt=0"`
	Upstreams      map[string]Upstream `mapstructure:"upstreams" validate:"required,min=1,dive"`
}

// Upstream is one source system.
type Upstream struct {
	BaseURL string        `mapstructure:"base_url" validate:"required,url"`
	Timeout time.Duration `mapstructure:"timeout" validate:"gt=0"`
}

// Resilience is the retry and breaker policy shared by every upstream client.
type Resilience struct {
	MaxRetries int           `mapstructure:"max_retries" validate:"min=0"`
	MinBudget  time.Duration `mapstructure:"min_budget" validate:"gte=0"`
	MaxJitter  time.Duration `mapstructure:"max_jitter" validate:"gte=0"`
	TripAfter  uint32        `mapstructure:"trip_after" validate:"min=1"`
	Cooldown   time.Duration `mapstructure:"cooldown" validate:"gt=0"`
}

// Telemetry is logging and redaction.
type Telemetry struct {
	LogLevel string `mapstructure:"log_level" validate:"oneof=debug info warn error"`
	Redact   Redact `mapstructure:"redact"`
}

// Redact switches PII redaction in logs and traces (LLD section 8).
type Redact struct {
	Enabled bool `mapstructure:"enabled"` // true in configs/config.yaml
}

// Validate holds the rules tags can't express: every source deadline sits
// below the request deadline, and the retry budget below every source deadline.
func (c *Config) Validate() error {
	for _, name := range slices.Sorted(maps.Keys(c.Sources.Upstreams)) {
		up := c.Sources.Upstreams[name]
		if up.Timeout >= c.Sources.RequestTimeout {
			return fmt.Errorf("sources.upstreams.%s.timeout must be below sources.request_timeout", name)
		}
		if c.Resilience.MinBudget >= up.Timeout {
			return fmt.Errorf("resilience.min_budget must be below sources.upstreams.%s.timeout", name)
		}
	}
	return nil
}
