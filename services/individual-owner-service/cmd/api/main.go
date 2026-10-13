// Command api serves individual-owner-service HTTP APIs.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
)

func main() {
	app.RunProbeIfRequested(os.Args, envOr("HTTP_ADDR", ":8080"))
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "individual-owner-service api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(sharedcfg.New(), config.ModeAPI)
	if err != nil {
		return err
	}
	log := logging.New(logging.Config{Service: "individual-owner-service", Environment: cfg.Environment, Version: buildinfo.Version, Level: cfg.LogLevel})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTel, err := observability.Setup(ctx, observability.Config{ServiceName: "individual-owner-service", ServiceVersion: buildinfo.Version,
		Environment: cfg.Environment, OTLPEndpoint: cfg.OTLPEndpoint, OTLPInsecure: cfg.OTLPInsecure, SampleRatio: cfg.SampleRatio})
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTel(ctx)
	}()
	a, err := app.Build(ctx, cfg, log, app.Overrides{})
	if err != nil {
		return err
	}
	defer a.Close()
	log.Info("individual-owner-service api starting", "commit", buildinfo.Get().Commit)
	return httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: cfg.HTTPAddr, Handler: a.Router, Logger: log,
		ShutdownTimeout: cfg.ShutdownTimeout, BeforeShutdown: a.Health.SetShuttingDown})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
