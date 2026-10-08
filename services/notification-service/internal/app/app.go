// Package app wires notification-service for the api and worker processes.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/channels"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/config"
	nhttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/notification-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// App holds wired components.
type App struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Pool    *pgxpool.Pool
	Senders channels.Registry
	Service *usecases.Service
	Idem    *idempotency.Store
	Outbox  *outbox.Store
	Health  *health.Handler
	Router  http.Handler
}

// Overrides lets tests substitute collaborators.
type Overrides struct {
	Senders channels.Registry
	Now     func() time.Time
	Jitter  func(time.Duration) time.Duration
}

// Build wires the database, channel adapters, use cases and router.
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger, ov Overrides) (*App, error) {
	pool, err := database.Open(ctx, database.Config{URL: cfg.DatabaseURL, ApplicationName: "notification-service", MaxConns: cfg.DBMaxConns})
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	a := &App{Cfg: cfg, Log: log, Pool: pool, Senders: ov.Senders}
	if a.Senders == nil {
		if a.Senders, err = Senders(ctx, cfg); err != nil {
			pool.Close()
			return nil, err
		}
	}
	a.Outbox = outbox.NewStore(pool, cfg.SubjectPrefix)
	a.Idem = idempotency.NewStore(pool, 30*time.Second, 24*time.Hour)
	a.Service = usecases.New(pool, a.Outbox, a.Senders, usecases.Options{DispatchLease: cfg.DispatchLease, SendTimeout: cfg.SendTimeout,
		RetryBase: cfg.RetryBase, RetryMax: cfg.RetryMax, ContentRetention: cfg.ContentRetention, DefaultLocale: cfg.DefaultLocale,
		Now: ov.Now, Jitter: ov.Jitter}, log)
	a.Health = health.New(string(platform.NotificationService), cfg.Environment, 3*time.Second,
		health.Check{Name: "postgres", Fn: database.Ping(pool)})
	a.Router = nhttp.NewRouter(nhttp.Config{Service: a.Service, Idempotency: a.Idem, Verifier: cfg.Identity.Verifier, Health: a.Health, Logger: log})
	return a, nil
}

// Close releases resources.
func (a *App) Close() { a.Pool.Close() }

// Worker runs the background loops until ctx ends: outbox relay, delivery
// dispatch and housekeeping.
func (a *App) Worker(ctx context.Context) (func(context.Context), *jetstream.Conn, error) {
	conn, err := jetstream.Connect(jetstream.Config{URL: a.Cfg.NATSURL, Name: "notification-service-worker", Logger: a.Log})
	if err != nil {
		return nil, nil, err
	}
	if a.Cfg.EnsureStream {
		if err := conn.EnsureStream(ctx, jetstream.StreamConfig{Name: jetstream.StreamName(a.Cfg.SubjectPrefix, "notification"),
			Subjects: []string{a.Cfg.SubjectPrefix + ".notification.>"}}); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("ensure stream: %w", err)
		}
	}
	host, _ := os.Hostname()
	owner := fmt.Sprintf("%s-%s", host, ids.New().String()[24:])
	relay, err := outbox.NewWorker(a.Outbox, conn, outbox.WorkerConfig{Owner: owner, PollInterval: a.Cfg.OutboxPollInterval, Logger: a.Log})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	a.Health = health.New(string(platform.NotificationService), a.Cfg.Environment, 3*time.Second,
		health.Check{Name: "postgres", Fn: database.Ping(a.Pool)}, health.Check{Name: "nats", Fn: conn.Ready})
	run := func(ctx context.Context) {
		var wg sync.WaitGroup
		loop := func(name string, every time.Duration, fn func(context.Context) error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				t := time.NewTicker(every)
				defer t.Stop()
				for {
					if err := fn(ctx); err != nil && ctx.Err() == nil {
						a.Log.Error("worker loop failed", "loop", name, "error", err.Error())
					}
					select {
					case <-ctx.Done():
						return
					case <-t.C:
					}
				}
			}()
		}
		wg.Add(1)
		go func() { defer wg.Done(); _ = relay.Run(ctx) }()
		loop("dispatch", a.Cfg.DispatchPollInterval, func(ctx context.Context) error {
			// Drain while full batches are claimed.
			for ctx.Err() == nil {
				n, err := a.Service.DispatchDue(ctx, owner, a.Cfg.DispatchBatch)
				if err != nil || n < a.Cfg.DispatchBatch {
					return err
				}
			}
			return nil
		})
		loop("purge", a.Cfg.PurgeInterval, func(ctx context.Context) error {
			if _, err := a.Service.PurgeContent(ctx); err != nil {
				return err
			}
			if _, err := a.Idem.Purge(ctx); err != nil {
				return err
			}
			_, err := a.Outbox.PurgePublished(ctx, 7*24*time.Hour)
			return err
		})
		wg.Wait()
	}
	return run, conn, nil
}
