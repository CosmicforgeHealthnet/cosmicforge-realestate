// Package workers runs individual-owner-service background loops (Section
// 11 workers implemented for the account/security context):
//
//   - outbox relay to NATS JetStream (shared, lease-based, bounded retries,
//     dead-lettering after MaxAttempts);
//   - security-alert dispatch to notification-service (lease-based claim,
//     idempotent sends, exponential backoff with jitter, dead-lettering);
//   - account-closure finalization after the cooling-off period (SKIP LOCKED);
//   - housekeeping: expired one-time challenges, long-ended sessions,
//     expired idempotency keys and published outbox rows;
//   - Sections 6–13 domain jobs (usecases.DomainJobs): payout cooling-off,
//     property imports, verification reconciliation and expiry, consent and
//     mandate expiry, and the jobs of later contexts.
//
// Every loop is safe to run in several worker instances at once.
package workers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/usecases"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/idempotency"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"go.opentelemetry.io/otel/attribute"
)

// Config configures the loops.
type Config struct {
	Owner                string
	DispatchInterval     time.Duration
	ClosureInterval      time.Duration
	HousekeepingInterval time.Duration
	SessionRetention     time.Duration
	BatchSize            int
	Lease                time.Duration
	DomainInterval       time.Duration
	ImportLease          time.Duration
}

// Runner owns the loops.
type Runner struct {
	cfg     Config
	svc     *usecases.Service
	relay   *outbox.Worker
	store   *outbox.Store
	idem    *idempotency.Store
	log     *slog.Logger
	cycles  observability.Counter
	failure observability.Counter
	jobs    []usecases.Job
}

// New builds a runner.
func New(cfg Config, svc *usecases.Service, relay *outbox.Worker, store *outbox.Store, idem *idempotency.Store, log *slog.Logger) *Runner {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.DispatchInterval <= 0 {
		cfg.DispatchInterval = time.Second
	}
	if cfg.ClosureInterval <= 0 {
		cfg.ClosureInterval = time.Minute
	}
	if cfg.HousekeepingInterval <= 0 {
		cfg.HousekeepingInterval = time.Hour
	}
	if cfg.SessionRetention <= 0 {
		cfg.SessionRetention = 90 * 24 * time.Hour
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.DomainInterval <= 0 {
		cfg.DomainInterval = 30 * time.Second
	}
	return &Runner{cfg: cfg, svc: svc, relay: relay, store: store, idem: idem, log: log,
		jobs:    svc.DomainJobs(usecases.JobConfig{Owner: cfg.Owner, ImportLease: cfg.ImportLease, Batch: int32(cfg.BatchSize)}),
		cycles:  observability.NewCounter("individual-owner-service", "owner_worker_cycles_total", "Worker loop cycles"),
		failure: observability.NewCounter("individual-owner-service", "owner_worker_failures_total", "Worker loop failures")}
}

// Once runs every non-relay loop once (tests and manual operation).
func (r *Runner) Once(ctx context.Context) error {
	for _, fn := range []func(context.Context) error{r.dispatch, r.closures, r.housekeep} {
		if err := fn(ctx); err != nil {
			return err
		}
	}
	for _, j := range r.jobs {
		if err := j.Run(requestcontext.Job(ctx, "owner-"+j.Name)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) dispatch(ctx context.Context) error {
	_, err := r.svc.DispatchDue(ctx, r.cfg.Owner, r.cfg.BatchSize, r.cfg.Lease)
	return err
}

func (r *Runner) closures(ctx context.Context) error {
	closed, held, err := r.svc.FinalizeDueClosures(ctx, r.cfg.BatchSize)
	if closed+held > 0 {
		r.log.InfoContext(ctx, "account closures processed", "closed", closed, "held", held)
	}
	return err
}

func (r *Runner) housekeep(ctx context.Context) error {
	if _, _, err := r.svc.Housekeep(ctx, r.cfg.SessionRetention); err != nil {
		return err
	}
	if _, err := r.idem.Purge(ctx); err != nil {
		return err
	}
	_, err := r.store.PurgePublished(ctx, 7*24*time.Hour)
	return err
}

// Run blocks until ctx is cancelled and every loop has finished its current
// iteration (graceful shutdown).
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	loop := func(name string, every time.Duration, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				jctx := requestcontext.Job(ctx, "owner-"+name)
				r.cycles.Add(jctx, 1, attribute.String("loop", name))
				if err := fn(jctx); err != nil && ctx.Err() == nil {
					r.failure.Add(jctx, 1, attribute.String("loop", name))
					r.log.ErrorContext(jctx, "worker loop failed", "loop", name, "error", err.Error())
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	if r.relay != nil {
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.relay.Run(ctx) }()
	}
	loop("dispatch", r.cfg.DispatchInterval, r.dispatch)
	loop("closure", r.cfg.ClosureInterval, r.closures)
	loop("housekeeping", r.cfg.HousekeepingInterval, r.housekeep)
	for _, j := range r.jobs {
		loop(j.Name, r.cfg.DomainInterval, j.Run)
	}
	wg.Wait()
}
