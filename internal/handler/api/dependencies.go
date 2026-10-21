// Package api is the HTTP entry point: routes (Gin), handlers, middleware,
// error mapping, and the wiring of its own dependencies.
package api

import (
	"fmt"
	"log/slog"

	"docviewer/internal/aggregator"
	"docviewer/internal/aggregator/source"
	"docviewer/internal/aggregator/source/sales"
	"docviewer/internal/aggregator/source/service"
	"docviewer/internal/config"
	"docviewer/internal/model"
	"docviewer/internal/telemetry"
	"docviewer/pkg/httpjson"
)

// Dependencies is everything the HTTP handlers use, and nothing else.
type Dependencies struct {
	Aggregator Lister
	Log        *slog.Logger
	Telemetry  *telemetry.Telemetry // instruments and providers for the middleware
}

// sourceBuilder builds one source (its client and adapter) from its config.
type sourceBuilder func(baseURL string, hc httpjson.Client) (source.Source, error)

// sourceBuilders is the one list a new source is added to. Each adapter
// package provides its Name and Build; nothing else in the wiring changes.
var sourceBuilders = map[model.Source]sourceBuilder{
	sales.Name:   sales.Build,
	service.Name: service.Build,
}

// NewDependencies builds the HTTP API's graph from configuration: one HTTP
// stack, client and adapter per configured source, then the aggregator.
func NewDependencies(cfg config.Config, tel *telemetry.Telemetry) (*Dependencies, error) {
	b := aggregator.NewBuilder(aggregator.Config{RequestTimeout: cfg.Sources.RequestTimeout}, tel.Logger()).Observe(tel.Metrics())
	for name, up := range cfg.Sources.Upstreams {
		build, ok := sourceBuilders[model.Source(name)]
		if !ok {
			return nil, fmt.Errorf("source %q is configured but has no adapter", name)
		}
		hc := httpjson.Client{HTTP: upstreamHTTP(name, cfg.Resilience, tel), MaxBytes: cfg.Sources.MaxBodyBytes}
		src, err := build(up.BaseURL, hc)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", name, err)
		}
		b.AddSource(src, up.Timeout)
	}
	agg, err := b.Build()
	if err != nil {
		return nil, err
	}
	return &Dependencies{Aggregator: agg, Log: tel.Logger(), Telemetry: tel}, nil
}
