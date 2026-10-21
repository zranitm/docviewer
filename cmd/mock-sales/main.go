// Command mock-sales is the mock Sales System API (:8081): embedded fixtures
// served with keyset paging, plus fault controls for the demo.
package main

import (
	"bytes"
	"cmp"
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"docviewer/internal/mock"
	"docviewer/pkg/configload"
)

const (
	envPrefix         = "MOCK_SALES"
	defaultConfigPath = "configs/mock-sales.yaml"
)

func main() { os.Exit(run()) }

func run() int {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := configload.Load[mock.Config](configload.Options{
		Path:      cmp.Or(os.Getenv("CONFIG_PATH"), defaultConfigPath),
		EnvPrefix: envPrefix,
		Strict:    true,
	})
	if err != nil {
		log.Error("invalid configuration", "err", err)
		return 1
	}
	store, err := mock.LoadSales(bytes.NewReader(mock.SalesFixture), cfg.PublicBaseURL)
	if err != nil {
		log.Error("fixtures did not load", "err", err)
		return 1
	}
	ln, err := net.Listen("tcp", cfg.Server.Addr)
	if err != nil {
		log.Error("listen failed", "addr", cfg.Server.Addr, "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("mock sales system listening", "addr", ln.Addr().String(), "public_base_url", cfg.PublicBaseURL, "faults", cfg.Faults)
	if err := mock.Serve(ctx, ln, mock.NewSalesHandler(store, mock.NewFaults(cfg.Faults), log), log); err != nil {
		log.Error("server stopped with an error", "err", err)
		return 1
	}
	log.Info("mock sales system stopped")
	return 0
}
