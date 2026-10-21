// Command docviewer is the Unified Document Viewer service: it loads
// configuration, sets up telemetry, builds the HTTP API's dependencies and
// server, runs until SIGINT or SIGTERM, and shuts down in reverse.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"docviewer/internal/config"
	"docviewer/internal/handler/api"
	"docviewer/internal/telemetry"
	"docviewer/pkg/configload"
	"docviewer/pkg/otelsetup"
)

const (
	envPrefix      = "DOCVIEWER"
	configDir      = "configs"
	dotEnvFile     = ".env"
	telemetryGrace = 5 * time.Second // to flush spans, metrics and logs on exit
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	// Configuration first: there is no logger until the level is known, so
	// configuration errors go to stderr. The loader never echoes secrets.
	if err := configload.LoadDotEnv(dotEnvFile); err != nil {
		fmt.Fprintln(os.Stderr, "docviewer:", err)
		return 1
	}
	cfg, err := configload.Load[config.Config](configload.Options{Path: configload.ResolvePath(configDir), EnvPrefix: envPrefix, Strict: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, "docviewer: invalid configuration:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tel, err := telemetry.New(ctx, cfg.Telemetry, otelsetup.Options{ServiceName: api.ServiceName, Version: version, LogWriter: os.Stdout})
	if err != nil {
		fmt.Fprintln(os.Stderr, "docviewer:", err)
		return 1
	}
	log := tel.Logger()
	code := 0
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryGrace)
		defer cancel()
		if err := tel.Shutdown(flushCtx); err != nil {
			log.Error("telemetry did not flush cleanly", "err", err)
		}
	}()

	deps, err := api.NewDependencies(*cfg, tel)
	if err != nil {
		log.Error("wiring failed", "err", err)
		return 1
	}
	log.Info("starting", "version", version, "sources", len(cfg.Sources.Upstreams), "redact", cfg.Telemetry.Redact.Enabled)
	if err := api.NewServer(cfg.Server, deps).Run(ctx); err != nil {
		log.Error("server stopped with an error", "err", err)
		code = 1
	}
	log.Info("stopped")
	return code
}
