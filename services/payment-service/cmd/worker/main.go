package main

import (
	"context"
	"fmt"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/app"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/config"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/go-chi/chi/v5"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	health.RunProbeIfRequested(os.Args, addr)
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "payment worker failed:", e)
		os.Exit(1)
	}
}
func run() error {
	c, e := config.Load(sharedcfg.New(), true)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	log := logging.New(logging.Config{Service: "payment-service-worker", Environment: c.Environment, Level: "info"})
	shutdown, e := observability.Setup(ctx, observability.Config{ServiceName: "payment-service-worker", Environment: c.Environment, SampleRatio: 0.1, OTLPEndpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")})
	if e != nil {
		return e
	}
	defer shutdown(context.Background())
	a, e := app.Build(ctx, c, log)
	if e != nil {
		return e
	}
	defer a.Close()
	r := chi.NewRouter()
	a.Health.Register(r)
	done := make(chan error, 2)
	go func() {
		done <- httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: c.HTTPAddr, Handler: r, Logger: log, ShutdownTimeout: c.ShutdownTimeout, BeforeShutdown: a.Health.SetShuttingDown})
	}()
	go func() { done <- a.RunWorker(ctx) }()
	e = <-done
	cancel()
	second := <-done
	if e != nil {
		return e
	}
	return second
}
