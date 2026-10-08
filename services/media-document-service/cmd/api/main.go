// Command api serves media-document-service HTTP APIs.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/media-document-service/internal/config"
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
		fmt.Fprintln(os.Stderr, "media-document-service api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(sharedcfg.New(), config.ModeAPI)
	if err != nil {
		return err
	}
	log := logging.New(logging.Config{Service: "media-document-service", Environment: cfg.Environment, Version: buildinfo.Version, Level: cfg.LogLevel})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTel, err := observability.Setup(ctx, observability.Config{ServiceName: "media-document-service", ServiceVersion: buildinfo.Version,
		Environment: cfg.Environment, OTLPEndpoint: cfg.OTLPEndpoint, OTLPInsecure: cfg.OTLPInsecure, SampleRatio: cfg.SampleRatio})
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTel(context.Background()) }()
	a, err := app.Build(ctx, cfg, log, app.Overrides{})
	if err != nil {
		return err
	}
	defer a.Close()
	log.Info("media-document-service api starting", "commit", buildinfo.Get().Commit)
	return httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: cfg.HTTPAddr, Handler: a.Router, Logger: log,
		ShutdownTimeout: cfg.ShutdownTimeout, BeforeShutdown: a.Health.SetShuttingDown})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
