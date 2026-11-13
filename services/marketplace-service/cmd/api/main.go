// Command api serves marketplace-service HTTP APIs: health/readiness/version
// and the internal Business supply contract (business-supply.v1).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/marketplace-service/internal/supply"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func main() {
	health.RunProbeIfRequested(os.Args, envOr("HTTP_ADDR", ":8080"))
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "marketplace-service api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(sharedcfg.New())
	if err != nil {
		return err
	}
	log := logging.New(logging.Config{Service: "marketplace-service", Environment: cfg.Environment, Version: buildinfo.Version, Level: cfg.LogLevel})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTel, err := observability.Setup(ctx, observability.Config{ServiceName: "marketplace-service", ServiceVersion: buildinfo.Version,
		Environment: cfg.Environment, OTLPEndpoint: cfg.OTLPEndpoint, OTLPInsecure: cfg.OTLPInsecure, SampleRatio: cfg.SampleRatio})
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTel(context.Background()) }()
	pool, err := database.Open(ctx, database.Config{URL: cfg.DatabaseURL, ApplicationName: "marketplace-service", MaxConns: cfg.DBMaxConns})
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	hh := health.New(string(platform.MarketplaceService), cfg.Environment, 2*time.Second, health.Check{Name: "postgres", Fn: database.Ping(pool)})
	router := supply.NewRouter(supply.RouterConfig{Service: &supply.Service{Pool: pool, TTL: cfg.ConnectionTTL}, Verifier: cfg.Identity.Verifier, Health: hh, Logger: log})
	log.Info("marketplace-service api starting", "commit", buildinfo.Get().Commit)
	return httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: cfg.HTTPAddr, Handler: router, Logger: log,
		ShutdownTimeout: cfg.ShutdownTimeout, BeforeShutdown: hh.SetShuttingDown})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
