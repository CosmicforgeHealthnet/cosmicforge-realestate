// Package workers runs verification-service background loops: the outbox
// relay, the provider dispatcher (submission, read-back, polling), VERIFIED
// expiry, and idempotency/outbox housekeeping.
package workers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
)

// Config configures the loops.
type Config struct {
	Owner                string
	DispatchInterval     time.Duration
	ExpiryInterval       time.Duration
	HousekeepingInterval time.Duration
	BatchSize            int
}

// Runner owns the loops.
type Runner struct {
	cfg   Config
	svc   *usecases.Service
	relay *outbox.Worker
	store *outbox.Store
	idem  *idempotency.Store
	log   *slog.Logger
}

// New builds a runner.
func New(cfg Config, svc *usecases.Service, relay *outbox.Worker, store *outbox.Store, idem *idempotency.Store, log *slog.Logger) *Runner {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.HousekeepingInterval <= 0 {
		cfg.HousekeepingInterval = time.Hour
	}
	return &Runner{cfg: cfg, svc: svc, relay: relay, store: store, idem: idem, log: log}
}

// Run blocks until ctx is cancelled and all loops have stopped.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	loop := func(name string, every time.Duration, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				if err := fn(ctx); err != nil && ctx.Err() == nil {
					r.log.Error("worker loop failed", "loop", name, "error", err.Error())
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
	go func() { defer wg.Done(); _ = r.relay.Run(ctx) }()
	loop("dispatch", r.cfg.DispatchInterval, func(ctx context.Context) error {
		_, err := r.svc.DispatchDue(ctx, r.cfg.Owner, r.cfg.BatchSize)
		return err
	})
	loop("expiry", r.cfg.ExpiryInterval, func(ctx context.Context) error {
		_, err := r.svc.ExpireDue(ctx, r.cfg.BatchSize)
		return err
	})
	loop("housekeeping", r.cfg.HousekeepingInterval, func(ctx context.Context) error {
		if _, err := r.idem.Purge(ctx); err != nil {
			return err
		}
		_, err := r.store.PurgePublished(ctx, 7*24*time.Hour)
		return err
	})
	wg.Wait()
}
