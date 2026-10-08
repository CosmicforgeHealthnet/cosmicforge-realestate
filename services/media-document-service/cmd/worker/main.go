// Command worker runs media-document-service background processing: outbox
// relay, malware scanning, object deletion, retention purge.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/go-chi/chi/v5"

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
	health.RunProbeIfRequested(os.Args, envOr("HTTP_ADDR", ":8081"))
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "media-document-service worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(sharedcfg.New(), config.ModeWorker)
	if err != nil {
		return err
	}
	log := logging.New(logging.Config{Service: "media-document-service-worker", Environment: cfg.Environment, Version: buildinfo.Version, Level: cfg.LogLevel})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownTel, err := observability.Setup(ctx, observability.Config{ServiceName: "media-document-service-worker", ServiceVersion: buildinfo.Version,
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
	runLoops, conn, err := a.Worker(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	r := chi.NewRouter()
	a.Health.Register(r)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); runLoops(ctx) }()
	log.Info("media-document-service worker starting", "commit", buildinfo.Get().Commit)
	err = httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: cfg.HTTPAddr, Handler: r, Logger: log,
		ShutdownTimeout: cfg.ShutdownTimeout, BeforeShutdown: a.Health.SetShuttingDown})
	wg.Wait()
	return err
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
