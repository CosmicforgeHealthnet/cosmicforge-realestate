// Package outbox implements the transactional outbox (IDEMPOTENCY_AND_OUTBOX.md).
//
// Producers call Store.Add inside the SAME PostgreSQL transaction as the
// authoritative state change. A Worker then:
//
//  1. reclaims expired leases (crashed workers);
//  2. claims a batch with FOR UPDATE SKIP LOCKED + a time-bounded lease, so
//     concurrent workers never claim the same row;
//  3. publishes each event to NATS JetStream;
//  4. marks it published only after broker acknowledgement, fenced on the
//     lease owner;
//  5. on failure schedules a bounded exponential retry; after MaxAttempts the
//     row is marked dead for operator attention (never retried forever).
//
// Crash windows: a crash after commit but before publish leaves the row
// pending (published later); a crash after publish but before mark-published
// causes a re-publish with the same event_id, which JetStream de-duplicates
// within its window and idempotent consumers tolerate beyond it.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/observability"
	"go.opentelemetry.io/otel/attribute"
)

// SchemaSQL is the canonical outbox table; each producing service includes
// it in its own migrations.
const SchemaSQL = `
CREATE TABLE outbox_events (
    event_id          uuid PRIMARY KEY,
    event_type        text        NOT NULL,
    producer          text        NOT NULL,
    aggregate_type    text        NOT NULL,
    aggregate_id      uuid        NOT NULL,
    subject           text        NOT NULL,
    trace_id          text,
    actor_domain      text,
    actor_id          uuid,
    metadata          jsonb,
    payload           jsonb       NOT NULL,
    occurred_at       timestamptz NOT NULL,
    attempts          integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at   timestamptz NOT NULL DEFAULT now(),
    lease_owner       text,
    lease_expires_at  timestamptz,
    published_at      timestamptz,
    dead_at           timestamptz,
    last_error        text,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outbox_events_pending_idx ON outbox_events (next_attempt_at, occurred_at)
    WHERE published_at IS NULL AND dead_at IS NULL;
CREATE INDEX outbox_events_aggregate_idx ON outbox_events (aggregate_type, aggregate_id, occurred_at);
`

// Store reads and writes the outbox table.
type Store struct {
	db            database.DB
	subjectPrefix string
}

// NewStore returns a Store; subjectPrefix defaults to events.DefaultSubjectPrefix.
func NewStore(db database.DB, subjectPrefix string) *Store {
	if subjectPrefix == "" {
		subjectPrefix = events.DefaultSubjectPrefix
	}
	return &Store{db: db, subjectPrefix: subjectPrefix}
}

// Add inserts e within tx. It must be called in the transaction that changes
// the authoritative state the event describes.
func (s *Store) Add(ctx context.Context, tx database.Querier, e events.Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	var actorDomain *string
	var actorID any
	if e.Actor != nil {
		actorDomain, actorID = &e.Actor.Domain, e.Actor.ID
	}
	var md any
	if len(e.Metadata) > 0 {
		b, _ := json.Marshal(e.Metadata)
		md = string(b)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (event_id, event_type, producer, aggregate_type, aggregate_id, subject,
		    trace_id, actor_domain, actor_id, metadata, payload, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,nullif($7,''),$8,$9,$10,$11,$12)`,
		e.EventID, e.EventType, e.Producer, e.AggregateType, e.AggregateID,
		events.Subject(s.subjectPrefix, e.EventType), e.TraceID, actorDomain, actorID, md, string(e.Payload), e.OccurredAt)
	return err
}

// Claimed is a leased outbox row.
type Claimed struct {
	Subject  string
	Envelope events.Envelope
	Attempts int
}

// ReclaimExpired releases leases whose holder died. Claim also treats expired
// leases as claimable; this step makes recovery explicit and observable.
func (s *Store) ReclaimExpired(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE outbox_events SET lease_owner = NULL, lease_expires_at = NULL
		 WHERE published_at IS NULL AND dead_at IS NULL
		   AND lease_owner IS NOT NULL AND lease_expires_at < now()`)
	return tag.RowsAffected(), err
}

// Claim leases up to limit due events for owner.
func (s *Store) Claim(ctx context.Context, owner string, limit int, lease time.Duration) ([]Claimed, error) {
	rows, err := s.db.Query(ctx, `
		WITH due AS (
		    SELECT event_id FROM outbox_events
		     WHERE published_at IS NULL AND dead_at IS NULL
		       AND next_attempt_at <= now()
		       AND (lease_expires_at IS NULL OR lease_expires_at < now())
		     ORDER BY next_attempt_at, occurred_at
		     LIMIT $1
		     FOR UPDATE SKIP LOCKED)
		UPDATE outbox_events o
		   SET lease_owner = $2, lease_expires_at = now() + $3::interval, attempts = o.attempts + 1
		  FROM due WHERE o.event_id = due.event_id
		RETURNING o.event_id, o.event_type, o.producer, o.aggregate_type, o.aggregate_id, o.subject,
		          coalesce(o.trace_id,''), o.actor_domain, o.actor_id, o.metadata, o.payload, o.occurred_at, o.attempts`,
		limit, owner, fmt.Sprintf("%d milliseconds", lease.Milliseconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Claimed
	for rows.Next() {
		var c Claimed
		var actorDomain *string
		var actorID ids.ID
		var md []byte
		var payload []byte
		e := &c.Envelope
		if err := rows.Scan(&e.EventID, &e.EventType, &e.Producer, &e.AggregateType, &e.AggregateID, &c.Subject,
			&e.TraceID, &actorDomain, &actorID, &md, &payload, &e.OccurredAt, &c.Attempts); err != nil {
			return nil, err
		}
		e.OccurredAt = e.OccurredAt.UTC()
		e.Payload = payload
		if actorDomain != nil && !actorID.IsZero() {
			e.Actor = &events.Actor{Domain: *actorDomain, ID: actorID}
		}
		if len(md) > 0 {
			_ = json.Unmarshal(md, &e.Metadata)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkPublished records broker acknowledgement, fenced on lease ownership.
// It returns false if the lease was lost (another worker owns the row now).
func (s *Store) MarkPublished(ctx context.Context, owner string, eventID ids.ID) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE outbox_events SET published_at = now(), lease_owner = NULL, lease_expires_at = NULL, last_error = NULL
		 WHERE event_id = $1 AND lease_owner = $2 AND published_at IS NULL`, eventID, owner)
	return tag.RowsAffected() == 1, err
}

// MarkFailed schedules a retry, or marks the row dead when attempts >= maxAttempts.
func (s *Store) MarkFailed(ctx context.Context, owner string, eventID ids.ID, cause string, retryAt time.Time, maxAttempts int) error {
	if len(cause) > 500 {
		cause = cause[:500]
	}
	_, err := s.db.Exec(ctx, `
		UPDATE outbox_events
		   SET lease_owner = NULL, lease_expires_at = NULL, last_error = $3, next_attempt_at = $4,
		       dead_at = CASE WHEN attempts >= $5 THEN now() ELSE NULL END
		 WHERE event_id = $1 AND lease_owner = $2 AND published_at IS NULL`,
		eventID, owner, cause, retryAt, maxAttempts)
	return err
}

// Stats summarises the outbox for health/metrics.
type Stats struct {
	Pending          int64
	Dead             int64
	OldestPendingAge time.Duration
}

// Stats returns backlog figures.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	var oldest *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE published_at IS NULL AND dead_at IS NULL),
		       count(*) FILTER (WHERE dead_at IS NOT NULL),
		       min(occurred_at) FILTER (WHERE published_at IS NULL AND dead_at IS NULL)
		  FROM outbox_events`).Scan(&st.Pending, &st.Dead, &oldest)
	if oldest != nil {
		st.OldestPendingAge = time.Since(*oldest)
	}
	return st, err
}

// PurgePublished deletes published rows older than retention.
func (s *Store) PurgePublished(ctx context.Context, retention time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM outbox_events WHERE published_at < now() - $1::interval`,
		fmt.Sprintf("%d milliseconds", retention.Milliseconds()))
	return tag.RowsAffected(), err
}

// WorkerConfig configures a relay worker.
type WorkerConfig struct {
	Owner          string        // unique per worker process (e.g. hostname+pid+uuid)
	BatchSize      int           // default 50
	Lease          time.Duration // default 30s
	PollInterval   time.Duration // default 500ms
	MaxAttempts    int           // default 12
	BaseBackoff    time.Duration // default 1s
	MaxBackoff     time.Duration // default 5m
	PublishTimeout time.Duration // per event; default 5s
	Logger         *slog.Logger
}

// Worker relays outbox rows to a Publisher.
type Worker struct {
	store     *Store
	pub       events.Publisher
	cfg       WorkerConfig
	published observability.Counter
	failed    observability.Counter
}

// NewWorker validates configuration.
func NewWorker(store *Store, pub events.Publisher, cfg WorkerConfig) (*Worker, error) {
	if cfg.Owner == "" {
		return nil, errors.New("outbox: worker owner required")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 12
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.PublishTimeout <= 0 {
		cfg.PublishTimeout = 5 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Worker{store: store, pub: pub, cfg: cfg,
		published: observability.NewCounter("outbox", "outbox_events_published_total", "Outbox events acknowledged by the broker"),
		failed:    observability.NewCounter("outbox", "outbox_publish_failures_total", "Outbox publish failures"),
	}, nil
}

// Backoff returns the delay before retry number attempt (1-based), bounded
// by MaxBackoff with jitter.
func (w *Worker) Backoff(attempt int) time.Duration {
	d := w.cfg.BaseBackoff
	for i := 1; i < attempt && d < w.cfg.MaxBackoff; i++ {
		d *= 2
	}
	if d > w.cfg.MaxBackoff {
		d = w.cfg.MaxBackoff
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// RunOnce performs one reclaim/claim/publish cycle and returns the number of
// events acknowledged.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	if n, err := w.store.ReclaimExpired(ctx); err != nil {
		return 0, err
	} else if n > 0 {
		w.cfg.Logger.Warn("outbox reclaimed expired leases", "count", n)
	}
	batch, err := w.store.Claim(ctx, w.cfg.Owner, w.cfg.BatchSize, w.cfg.Lease)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, c := range batch {
		pctx, cancel := context.WithTimeout(ctx, w.cfg.PublishTimeout)
		ack, perr := w.pub.Publish(pctx, c.Subject, c.Envelope)
		cancel()
		if perr != nil {
			w.failed.Add(ctx, 1, attribute.String("event_type", c.Envelope.EventType))
			retryAt := time.Now().Add(w.Backoff(c.Attempts))
			if err := w.store.MarkFailed(ctx, w.cfg.Owner, c.Envelope.EventID, perr.Error(), retryAt, w.cfg.MaxAttempts); err != nil {
				return published, err
			}
			level := slog.LevelWarn
			if c.Attempts >= w.cfg.MaxAttempts {
				level = slog.LevelError
			}
			w.cfg.Logger.Log(ctx, level, "outbox publish failed", "event_id", c.Envelope.EventID.String(),
				"event_type", c.Envelope.EventType, "attempt", c.Attempts, "dead", c.Attempts >= w.cfg.MaxAttempts, "error", perr.Error())
			continue
		}
		ok, err := w.store.MarkPublished(ctx, w.cfg.Owner, c.Envelope.EventID)
		if err != nil {
			return published, err
		}
		if !ok {
			w.cfg.Logger.Warn("outbox lease lost before mark-published; event may be re-published (consumers are idempotent)",
				"event_id", c.Envelope.EventID.String())
			continue
		}
		w.published.Add(ctx, 1, attribute.String("event_type", c.Envelope.EventType), attribute.Bool("duplicate", ack.Duplicate))
		published++
	}
	return published, nil
}

// Run loops until ctx is cancelled. Errors (e.g. database unavailable) are
// logged and retried after the poll interval.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()
	for {
		n, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.cfg.Logger.Error("outbox cycle failed", "error", err.Error())
		}
		if n >= w.cfg.BatchSize {
			if ctx.Err() != nil {
				return nil
			}
			continue // drain backlog without waiting
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// WithTx is a convenience for producers: run fn and add events atomically.
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) ([]events.Envelope, error)) error {
	return database.WithTx(ctx, s.db, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		evs, err := fn(ctx, tx)
		if err != nil {
			return err
		}
		for _, e := range evs {
			if err := s.Add(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}
