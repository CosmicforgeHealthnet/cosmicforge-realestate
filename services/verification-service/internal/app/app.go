// Package app wires verification-service components for the api and worker
// processes (and for integration tests).
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/config"
	vhttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/integrations/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/workers"
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
	Cfg       *config.Config
	Log       *slog.Logger
	Pool      *pgxpool.Pool
	Service   *usecases.Service
	Idem      *idempotency.Store
	Outbox    *outbox.Store
	Providers *providers.Registry
	Sandbox   *providers.Sandbox
	Health    *health.Handler
	Router    http.Handler
}

// Overrides lets tests substitute collaborators.
type Overrides struct {
	Providers []providers.Provider
	Media     usecases.MediaClient
	Now       func() time.Time
}

// Build wires the shared components (database, use cases, HTTP router).
func Build(ctx context.Context, cfg *config.Config, log *slog.Logger, ov Overrides) (*App, error) {
	pool, err := database.Open(ctx, database.Config{URL: cfg.DatabaseURL, ApplicationName: "verification-service", MaxConns: cfg.DBMaxConns})
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	a := &App{Cfg: cfg, Log: log, Pool: pool}
	ps := ov.Providers
	if ps == nil {
		for _, name := range cfg.Providers {
			if name == providers.SandboxName {
				a.Sandbox = providers.NewSandbox(cfg.SandboxSecret.Reveal())
				ps = append(ps, a.Sandbox)
			}
		}
	}
	a.Providers, err = providers.NewRegistry(cfg.DefaultProvider, ps...)
	if err != nil {
		pool.Close()
		return nil, err
	}
	mc := ov.Media
	if mc == nil {
		mc, err = media.New(cfg.MediaServiceURL, cfg.Identity.Signer, cfg.MediaTimeout)
		if err != nil {
			pool.Close()
			return nil, err
		}
	}
	a.Outbox = outbox.NewStore(pool, cfg.SubjectPrefix)
	a.Idem = idempotency.NewStore(pool, 30*time.Second, 24*time.Hour)
	a.Service = usecases.New(pool, a.Outbox, a.Providers, mc, usecases.Options{
		Validity: cfg.Validity, ProviderTimeout: cfg.ProviderTimeout, MaxSubmitTries: cfg.MaxSubmitTries,
		MaxPollTries: cfg.MaxPollTries, PollInterval: cfg.PollInterval, Now: ov.Now,
	}, log)
	a.Health = health.New(string(platform.VerificationService), cfg.Environment, 2*time.Second,
		health.Check{Name: "postgres", Fn: database.Ping(pool)})
	a.Router = vhttp.NewRouter(vhttp.Config{Service: a.Service, Idempotency: a.Idem, Verifier: cfg.Identity.Verifier, Health: a.Health, Logger: log})
	return a, nil
}

// Close releases resources.
func (a *App) Close() { a.Pool.Close() }

// Worker builds the background runner and its NATS connection.
func (a *App) Worker(ctx context.Context) (*workers.Runner, *jetstream.Conn, error) {
	conn, err := jetstream.Connect(jetstream.Config{URL: a.Cfg.NATSURL, Name: "verification-service-worker", Logger: a.Log})
	if err != nil {
		return nil, nil, err
	}
	if a.Cfg.EnsureStream {
		if err := conn.EnsureStream(ctx, jetstream.StreamConfig{
			Name:     jetstream.StreamName(a.Cfg.SubjectPrefix, "verification"),
			Subjects: []string{a.Cfg.SubjectPrefix + ".verification.>"},
		}); err != nil {
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
	a.Health = health.New(string(platform.VerificationService), a.Cfg.Environment, 2*time.Second,
		health.Check{Name: "postgres", Fn: database.Ping(a.Pool)}, health.Check{Name: "nats", Fn: conn.Ready})
	r := workers.New(workers.Config{Owner: owner, DispatchInterval: a.Cfg.DispatchPollInterval, ExpiryInterval: a.Cfg.ExpiryPollInterval},
		a.Service, relay, a.Outbox, a.Idem, a.Log)
	return r, conn, nil
}
