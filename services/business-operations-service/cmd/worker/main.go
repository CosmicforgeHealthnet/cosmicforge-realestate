package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/config"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/go-chi/chi/v5"
)

func main() {
	health.RunProbeIfRequested(os.Args, envOr("HTTP_ADDR", ":8081"))
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "business-operations-service worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(sharedcfg.New(), config.ModeWorker)
	if err != nil {
		return err
	}
	log := logging.New(logging.Config{Service: "business-operations-service-worker", Environment: cfg.Environment, Version: buildinfo.Version, Level: cfg.LogLevel})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "business-operations-service-worker", ServiceVersion: buildinfo.Version, Environment: cfg.Environment, OTLPEndpoint: cfg.OTLPEndpoint, OTLPInsecure: cfg.OTLPInsecure, SampleRatio: cfg.SampleRatio})
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()
	a, err := app.Build(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer a.Close()
	conn, err := jetstream.Connect(jetstream.Config{URL: cfg.NATSURL, Name: "business-operations-service-worker", Logger: log, CredsFile: cfg.NATSCredsFile, Token: cfg.NATSToken})
	if err != nil {
		return fmt.Errorf("nats: %w", err)
	}
	defer conn.Close()
	if err := conn.EnsureStream(ctx, businessEvents.StreamConfig(cfg.SubjectPrefix)); err != nil {
		return fmt.Errorf("ensure business operations event stream: %w", err)
	}
	jobs, err := workers.Start(ctx, a.Pool, conn, cfg.SubjectPrefix, sharedcfg.New(), log)
	if err != nil {
		return fmt.Errorf("business event workers: %w", err)
	}
	defer jobs.Stop()
	host, _ := os.Hostname()
	owner := fmt.Sprintf("%s-%s", host, ids.New().String()[24:])
	relay, err := outbox.NewWorker(a.Outbox, conn, outbox.WorkerConfig{Owner: owner, PollInterval: cfg.OutboxPollInterval, Logger: log})
	if err != nil {
		return err
	}
	a.Health = health.New("business-operations-service-worker", cfg.Environment, 2*time.Second, health.Check{Name: "postgres", Fn: database.Ping(a.Pool)}, health.Check{Name: "nats", Fn: conn.Ready})
	r := chi.NewRouter()
	a.Health.Register(r)
	var wg sync.WaitGroup
	wg.Add(1)
	wg.Add(1)
	go func() { defer wg.Done(); _ = jobs.Run(ctx) }()
	go func() {
		defer wg.Done()
		if err := relay.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("outbox relay stopped", "error", err)
		}
	}()
	log.Info("business operations worker starting", "commit", buildinfo.Get().Commit)
	err = httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: cfg.HTTPAddr, Handler: r, Logger: log, ShutdownTimeout: cfg.ShutdownTimeout, BeforeShutdown: a.Health.SetShuttingDown})
	stop()
	wg.Wait()
	return err
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
