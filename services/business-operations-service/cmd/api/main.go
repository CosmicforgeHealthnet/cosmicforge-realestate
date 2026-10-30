package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
)

func main() {
	health.RunProbeIfRequested(os.Args, envOr("HTTP_ADDR", ":8080"))
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "business-operations-service api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(sharedcfg.New(), config.ModeAPI)
	if err != nil {
		return err
	}
	log := logging.New(logging.Config{Service: "business-operations-service", Environment: cfg.Environment, Version: buildinfo.Version, Level: cfg.LogLevel})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "business-operations-service", ServiceVersion: buildinfo.Version, Environment: cfg.Environment, OTLPEndpoint: cfg.OTLPEndpoint, OTLPInsecure: cfg.OTLPInsecure, SampleRatio: cfg.SampleRatio})
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()
	a, err := app.Build(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer a.Close()
	log.Info("business operations api starting", "commit", buildinfo.Get().Commit)
	return httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: cfg.HTTPAddr, Handler: a.Router, Logger: log, ShutdownTimeout: cfg.ShutdownTimeout, BeforeShutdown: a.Health.SetShuttingDown})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
