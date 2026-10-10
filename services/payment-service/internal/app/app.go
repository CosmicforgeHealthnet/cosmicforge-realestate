package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/config"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	phttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/lifecycle"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/health"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	Cfg     *config.Config
	Pool    *pgxpool.Pool
	Service *finance.Service
	Health  *health.Handler
	Router  http.Handler
	Log     *slog.Logger
	broker  atomic.Pointer[jetstream.Conn]
}

func Build(ctx context.Context, c *config.Config, log *slog.Logger) (*App, error) {
	db, e := database.Open(ctx, database.Config{URL: c.DatabaseURL, ApplicationName: "payment-service", MaxConns: 15})
	if e != nil {
		return nil, e
	}
	s := finance.New(core.New(db), c.Environment)
	s.Outbox = outbox.NewStore(db, c.SubjectPrefix)
	s.Log = log
	a := &App{Cfg: c, Pool: db, Service: s, Log: log}
	ctx = serviceauth.WithPrincipal(ctx, serviceauth.Principal{Service: platform.PaymentService})
	if c.Readback.MaxAttempts > 0 {
		if e = s.ConfigureRecovery(ctx, c.Readback); e != nil {
			db.Close()
			return nil, e
		}
	}
	for _, pc := range c.Providers {
		merchant, e := strconv.ParseInt(pc.MerchantRef, 10, 64)
		if e != nil {
			db.Close()
			return nil, fmt.Errorf("invalid merchant reference")
		}
		p, e := providers.NewPaystack(c.ProviderBaseURL, c.ProviderSecrets[pc.CredentialsRef], merchant, pc.Mode, nil)
		if e != nil {
			db.Close()
			return nil, e
		}
		var overlap []security.Secret
		for _, ref := range pc.WebhookPreviousRefs {
			overlap = append(overlap, c.ProviderSecrets[ref])
		}
		if e = p.SetWebhookOverlap(overlap); e != nil {
			db.Close()
			return nil, e
		}
		if e = s.Register(ctx, pc, finance.Adapter{Collection: p, DVA: p, Settlement: p, Payout: p, Refund: p, Reversal: p}); e != nil {
			db.Close()
			return nil, e
		}
	}
	checks := []health.Check{{Name: "postgres", Fn: database.Ping(db)}}
	if c.Worker {
		checks = append(checks, health.Check{Name: "jetstream", Fn: func(ctx context.Context) error {
			conn := a.broker.Load()
			if conn == nil {
				return errors.New("worker broker unavailable")
			}
			return conn.Ready(ctx)
		}})
	}
	a.Health = health.New("payment-service", c.Environment, 2*time.Second, checks...)
	a.Router = phttp.NewRouter(phttp.Config{Service: s, Verifier: c.Identity.Verifier, Health: a.Health, Logger: log})
	return a, nil
}
func (a *App) Close() { a.Pool.Close() }
func (a *App) RunWorker(ctx context.Context) error {
	lifecycleService := lifecycle.New(a.Service)
	conn, e := jetstream.Connect(jetstream.Config{URL: a.Cfg.NATSURL, Name: "payment-service-worker", Logger: a.Log})
	if e != nil {
		return e
	}
	defer conn.Close()
	a.broker.Store(conn)
	defer a.broker.Store(nil)
	if a.Cfg.EnsureStream {
		if e = conn.EnsureStream(ctx, jetstream.StreamConfig{Name: jetstream.StreamName(a.Cfg.SubjectPrefix, "finance"), Subjects: []string{a.Cfg.SubjectPrefix + ".finance.>"}}); e != nil {
			return e
		}
	}
	relay, e := outbox.NewWorker(a.Service.Outbox, conn, outbox.WorkerConfig{Owner: "payment-" + ids.New().String(), PollInterval: time.Second, Logger: a.Log})
	if e != nil {
		return e
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relay.Run(workerCtx) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ctx = serviceauth.WithPrincipal(ctx, serviceauth.Principal{Service: platform.PaymentService})
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case e := <-done:
			return e
		case <-ticker.C:
			if e = lifecycleService.RecoverRefunds(ctx); e != nil {
				a.Log.ErrorContext(ctx, "refund worker failed", "error_code", "REFUND_RECOVERY_FAILURE")
			}
			if e = lifecycleService.RecoverPayouts(ctx); e != nil {
				a.Log.ErrorContext(ctx, "payout worker failed", "error_code", "PAYOUT_RECOVERY_FAILURE")
			}
			if _, e = a.Service.ProcessInbox(ctx); e != nil {
				a.Log.ErrorContext(ctx, "inbox worker failed", "error_code", "INBOX_PROCESSING_FAILURE")
			}
			if e = a.Service.RecoverUnknown(ctx); e != nil {
				a.Log.ErrorContext(ctx, "readback worker failed", "error_code", "PROVIDER_RECOVERY_FAILURE")
			}
		}
	}
}
