package domainkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
)

// StreamConfig is the service's own event stream (subjects <prefix>.<area>.>).
func (rt *Runtime) StreamConfig() jetstream.StreamConfig {
	return jetstream.StreamConfig{Name: jetstream.StreamName(rt.Cfg.SubjectPrefix, rt.Cfg.Area), Subjects: []string{rt.Cfg.SubjectPrefix + "." + rt.Cfg.Area + ".>"}}
}

// RunWorker connects JetStream, ensures the service's own stream, starts the
// consumers registered by setup and the outbox relay, and serves health on
// the worker address until ctx ends (graceful shutdown).
func (rt *Runtime) RunWorker(ctx context.Context, setup func(ctx context.Context, conn *jetstream.Conn) error) error {
	conn, err := jetstream.Connect(jetstream.Config{URL: rt.Cfg.NATSURL, Name: string(rt.Cfg.Service) + "-worker", Logger: rt.Log})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.EnsureStream(ctx, rt.StreamConfig()); err != nil {
		return fmt.Errorf("ensure stream: %w", err)
	}
	rt.Health = health.New(string(rt.Cfg.Service), rt.Cfg.Environment, 2*time.Second, health.Check{Name: "postgres", Fn: database.Ping(rt.Pool)}, health.Check{Name: "nats", Fn: conn.Ready})
	if setup != nil {
		if err = setup(ctx, conn); err != nil {
			return err
		}
	}
	relay := make(chan error, 1)
	go func() { relay <- rt.Relay(ctx, conn) }()
	r := chi.NewRouter()
	rt.Health.Register(r)
	serve := httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: rt.Cfg.HTTPAddr, Handler: r, Logger: rt.Log, ShutdownTimeout: rt.Cfg.ShutdownTimeout, BeforeShutdown: rt.Health.SetShuttingDown})
	if err = <-relay; err != nil && !errors.Is(err, context.Canceled) {
		return errors.Join(serve, err)
	}
	if errors.Is(serve, http.ErrServerClosed) {
		return nil
	}
	return serve
}
