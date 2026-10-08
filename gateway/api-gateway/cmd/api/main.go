// Command api runs the CosmicForge Real Estate API Gateway.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/gateway/api-gateway/internal/server"
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
		fmt.Fprintln(os.Stderr, "api-gateway:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(sharedcfg.New())
	if err != nil {
		return err
	}
	log := logging.New(logging.Config{Service: "api-gateway", Environment: cfg.Environment, Version: buildinfo.Version, Level: cfg.LogLevel})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTel, err := observability.Setup(ctx, observability.Config{
		ServiceName: "api-gateway", ServiceVersion: buildinfo.Version, Environment: cfg.Environment,
		OTLPEndpoint: cfg.OTLPEndpoint, OTLPInsecure: cfg.OTLPInsecure, SampleRatio: cfg.SampleRatio,
	})
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTel(context.Background()) }()

	opts := &redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword.Reveal(), DB: cfg.RedisDB}
	if cfg.RedisTLS {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	gw, err := server.New(cfg, rdb, log)
	if err != nil {
		return err
	}
	log.Info("api-gateway starting", "routes", len(cfg.Routes), "commit", buildinfo.Get().Commit)
	return httpx.ListenAndServe(ctx, httpx.ServerConfig{
		Addr: cfg.HTTPAddr, Handler: gw.Handler, Logger: log,
		ShutdownTimeout: cfg.ShutdownTimeout, DrainDelay: cfg.DrainDelay,
		BeforeShutdown: gw.Health().SetShuttingDown,
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
