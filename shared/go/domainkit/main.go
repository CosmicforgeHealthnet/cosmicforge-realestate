package domainkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo"
	sharedcfg "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// App is a domain service's composition over its Runtime.
type App struct {
	// Actor mounts gateway-authenticated actor routes (/api/v1/<domain>).
	Actor func(r chi.Router)
	// InternalCallers may call Internal routes (/internal/v1).
	InternalCallers []platform.Service
	Internal        func(r chi.Router)
	// Consumers registers JetStream consumers in the worker.
	Consumers func(ctx context.Context, conn *jetstream.Conn) error
	// Sweep runs periodically in the worker (expiries, reconciliation).
	Sweep      func(ctx context.Context) error
	SweepEvery time.Duration
}

// Handler builds the API handler for app.
func (rt *Runtime) Handler(app App) http.Handler {
	r := rt.Router()
	if app.Actor != nil || rt.Accounts != nil {
		rt.ActorRoutes(r, func(r chi.Router) {
			if app.Actor != nil {
				app.Actor(r)
			}
		})
	}
	if app.Internal != nil {
		rt.InternalRoutes(r, app.InternalCallers, app.Internal)
	}
	return r
}

// Main runs the api or worker process of svc ("worker" as first argument
// selects the worker; "healthcheck" probes).
func Main(svc platform.Service, mode Mode, build func(rt *Runtime) App) {
	def := ":8080"
	if mode == ModeWorker {
		def = ":8081"
	}
	if v := os.Getenv("HTTP_ADDR"); v != "" {
		def = v
	}
	health.RunProbeIfRequested(os.Args, def)
	if err := run(svc, mode, build); err != nil {
		fmt.Fprintf(os.Stderr, "%s %s: %v\n", svc, mode, err)
		os.Exit(1)
	}
}

func run(svc platform.Service, mode Mode, build func(rt *Runtime) App) error {
	cfg, err := Load(sharedcfg.New(), svc, mode)
	if err != nil {
		return err
	}
	log := logging.New(logging.Config{Service: string(svc), Environment: cfg.Environment, Version: buildinfo.Version, Level: cfg.LogLevel})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdown, err := observability.Setup(ctx, observability.Config{ServiceName: string(svc), ServiceVersion: buildinfo.Version, Environment: cfg.Environment, OTLPEndpoint: cfg.OTLPEndpoint, OTLPInsecure: cfg.OTLPInsecure, SampleRatio: cfg.SampleRatio})
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()
	rt, err := Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer rt.Close()
	app := build(rt)
	log.Info(string(svc)+" "+string(mode)+" starting", "commit", buildinfo.Get().Commit)
	if mode == ModeWorker {
		if app.Sweep != nil {
			every := app.SweepEvery
			if every <= 0 {
				every = time.Minute
			}
			go func() {
				t := time.NewTicker(every)
				defer t.Stop()
				for {
					if err := app.Sweep(ctx); err != nil && !errors.Is(err, context.Canceled) {
						log.Warn("sweep failed", "error", err)
					}
					select {
					case <-ctx.Done():
						return
					case <-t.C:
					}
				}
			}()
		}
		return rt.RunWorker(ctx, app.Consumers)
	}
	return httpx.ListenAndServe(ctx, httpx.ServerConfig{Addr: cfg.HTTPAddr, Handler: rt.Handler(app), Logger: log, ShutdownTimeout: cfg.ShutdownTimeout, BeforeShutdown: rt.Health.SetShuttingDown})
}

// CommandWithPre is Command with a pre step that runs after the idempotency
// key is claimed and before the local transaction: the place for the
// authoritative call to an owning service (e.g. reporting to Business).
// A replayed key returns the stored response without repeating either step.
func (rt *Runtime) CommandWithPre(w http.ResponseWriter, r *http.Request, scope string, in any, pre func(ctx context.Context) error, fn func(ctx context.Context, tx pgx.Tx) (Change, error)) {
	body, err := readBody(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in != nil {
		if err = httpx.DecodeJSONBytes(body, in); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	rt.Idem.ServeCommand(w, r, scope, body, pre, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		c, err := fn(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = rt.Commit(ctx, tx, c); err != nil {
			return idempotency.Result{}, err
		}
		if c.Status == 0 {
			c.Status = http.StatusOK
		}
		return idempotency.Result{Status: c.Status, Body: httpx.DataEnvelope{Data: c.Body}}, nil
	})
}
