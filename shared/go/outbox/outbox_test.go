package outbox

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

const businessSQL = `CREATE TABLE things (id uuid PRIMARY KEY, status text NOT NULL);`

// fakePublisher records publishes and can fail on demand.
type fakePublisher struct {
	mu        sync.Mutex
	published []events.Envelope
	fail      atomic.Bool
	delay     time.Duration
}

func (f *fakePublisher) Publish(ctx context.Context, subject string, e events.Envelope) (events.PublishAck, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.fail.Load() {
		return events.PublishAck{}, errors.New("broker unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, e)
	return events.PublishAck{Sequence: uint64(len(f.published))}, nil
}

func (f *fakePublisher) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.published) }

func setup(t *testing.T) (*Store, *testkit.Database) {
	db := testkit.NewDatabase(t, nil, SchemaSQL, businessSQL, events.ProcessedSchemaSQL)
	return NewStore(db.Pool, "realestate"), db
}

func produce(t *testing.T, s *Store, n int) []ids.ID {
	t.Helper()
	ctx := requestcontext.With(context.Background(), requestcontext.Values{RequestID: "req-1", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736"})
	var out []ids.ID
	for i := 0; i < n; i++ {
		id := ids.New()
		err := s.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) ([]events.Envelope, error) {
			if _, err := tx.Exec(ctx, `INSERT INTO things (id, status) VALUES ($1,'DONE')`, id); err != nil {
				return nil, err
			}
			e, err := events.New(ctx, platform.VerificationService, "thing.done.v1", "thing", id, map[string]string{"id": id.String()})
			return []events.Envelope{e}, err
		})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func worker(t *testing.T, s *Store, p events.Publisher, owner string, mut func(*WorkerConfig)) *Worker {
	cfg := WorkerConfig{Owner: owner, BatchSize: 10, Lease: time.Second, MaxAttempts: 3, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond, Logger: logging.Discard()}
	if mut != nil {
		mut(&cfg)
	}
	w, err := NewWorker(s, p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func pending(t *testing.T, db *testkit.Database) int {
	var n int
	_ = db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL`).Scan(&n)
	return n
}

func TestAtomicWithBusinessState(t *testing.T) {
	s, db := setup(t)
	ctx := context.Background()
	id := ids.New()
	err := s.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) ([]events.Envelope, error) {
		_, _ = tx.Exec(ctx, `INSERT INTO things (id, status) VALUES ($1,'DONE')`, id)
		e, _ := events.New(ctx, platform.VerificationService, "thing.done.v1", "thing", id, map[string]any{})
		return []events.Envelope{e}, errors.New("business failure after event built")
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	var things, rows int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM things`).Scan(&things)
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&rows)
	if things != 0 || rows != 0 {
		t.Fatal("state and outbox must commit or roll back together")
	}
}

func TestPublishAndEnvelopeRoundTrip(t *testing.T) {
	s, db := setup(t)
	produce(t, s, 3)
	p := &fakePublisher{}
	n, err := worker(t, s, p, "w1", nil).RunOnce(context.Background())
	if err != nil || n != 3 || p.count() != 3 || pending(t, db) != 0 {
		t.Fatalf("n=%d err=%v published=%d", n, err, p.count())
	}
	e := p.published[0]
	if e.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || e.Metadata["request_id"] != "req-1" || e.Validate() != nil {
		t.Fatalf("envelope not preserved: %+v", e)
	}
}

func TestConcurrentClaimNoOverlap(t *testing.T) {
	s, _ := setup(t)
	produce(t, s, 40)
	ctx := context.Background()
	var mu sync.Mutex
	seen := map[ids.ID]string{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		owner := "w" + string(rune('a'+i))
		go func() {
			defer wg.Done()
			claimed, err := s.Claim(ctx, owner, 15, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, c := range claimed {
				if prev, dup := seen[c.Envelope.EventID]; dup {
					t.Errorf("event %s claimed by %s and %s", c.Envelope.EventID, prev, owner)
				}
				seen[c.Envelope.EventID] = owner
			}
		}()
	}
	wg.Wait()
	if len(seen) != 40 {
		t.Fatalf("claimed %d of 40", len(seen))
	}
}

func TestDuplicateWorkersPublishEachEventOnce(t *testing.T) {
	s, db := setup(t)
	produce(t, s, 30)
	p := &fakePublisher{delay: 2 * time.Millisecond}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		w := worker(t, s, p, "dup-"+string(rune('a'+i)), func(c *WorkerConfig) { c.Lease = time.Minute; c.BatchSize = 5 })
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pending(t, db) > 0 {
				if _, err := w.RunOnce(ctx); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	ids := map[ids.ID]int{}
	for _, e := range p.published {
		ids[e.EventID]++
	}
	if len(ids) != 30 || p.count() != 30 {
		t.Fatalf("published %d events, %d unique", p.count(), len(ids))
	}
}

func TestExpiredLeaseRecovery(t *testing.T) {
	s, db := setup(t)
	produce(t, s, 2)
	ctx := context.Background()
	// Worker A claims then "crashes" (never publishes or releases).
	claimed, err := s.Claim(ctx, "crashed", 10, 150*time.Millisecond)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim %d %v", len(claimed), err)
	}
	p := &fakePublisher{}
	wb := worker(t, s, p, "survivor", nil)
	if n, _ := wb.RunOnce(ctx); n != 0 {
		t.Fatal("leased rows must not be claimable before expiry")
	}
	time.Sleep(250 * time.Millisecond)
	n, err := wb.RunOnce(ctx)
	if err != nil || n != 2 || pending(t, db) != 0 {
		t.Fatalf("recovery n=%d err=%v", n, err)
	}
	// The crashed worker resurrects and tries to mark: fenced out.
	ok, err := s.MarkPublished(ctx, "crashed", claimed[0].Envelope.EventID)
	if err != nil || ok {
		t.Fatal("stale owner must not mark published")
	}
}

func TestCrashAfterCommitBeforePublish(t *testing.T) {
	s, db := setup(t)
	produce(t, s, 1) // committed; relay process "crashed" before running
	if pending(t, db) != 1 {
		t.Fatal("row must be durable after commit")
	}
	p := &fakePublisher{}
	if n, _ := worker(t, s, p, "restarted", nil).RunOnce(context.Background()); n != 1 {
		t.Fatal("event not published after restart")
	}
}

func TestRetryBackoffAndDead(t *testing.T) {
	s, db := setup(t)
	produce(t, s, 1)
	ctx := context.Background()
	p := &fakePublisher{}
	p.fail.Store(true)
	w := worker(t, s, p, "w", nil)
	for i := 0; i < 3; i++ {
		_, _ = w.RunOnce(ctx)
		time.Sleep(15 * time.Millisecond)
	}
	var attempts int
	var dead *time.Time
	var lastErr string
	_ = db.Pool.QueryRow(ctx, `SELECT attempts, dead_at, last_error FROM outbox_events`).Scan(&attempts, &dead, &lastErr)
	if attempts != 3 || dead == nil || lastErr == "" {
		t.Fatalf("attempts=%d dead=%v err=%q", attempts, dead, lastErr)
	}
	p.fail.Store(false)
	if n, _ := w.RunOnce(ctx); n != 0 {
		t.Fatal("dead events must not be retried automatically")
	}
	st, _ := s.Stats(ctx)
	if st.Dead != 1 || st.Pending != 0 {
		t.Fatalf("stats %+v", st)
	}
	for a := 1; a <= 20; a++ {
		if d := w.Backoff(a); d > 5*time.Millisecond {
			t.Fatalf("backoff exceeded max: %s", d)
		}
	}
}

// End-to-end with real JetStream: crash after publish before mark-published,
// broker outage, duplicate delivery tolerance via processed_events.
func TestJetStreamRecoveryAndDuplicateTolerance(t *testing.T) {
	s, db := setup(t)
	srv := testkit.StartNATS(t)
	conn, err := jetstream.Connect(jetstream.Config{URL: srv.URL, Name: "outbox-test", PublishTimeout: 500 * time.Millisecond, Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	if err := conn.EnsureStream(ctx, jetstream.StreamConfig{Name: "REALESTATE_THING", Subjects: []string{"realestate.thing.>"}}); err != nil {
		t.Fatal(err)
	}
	ids1 := produce(t, s, 2)

	// Crash after publish before mark-published: publish directly, leave row pending.
	claimed, _ := s.Claim(ctx, "crashy", 1, 100*time.Millisecond)
	if _, err := conn.Publish(ctx, claimed[0].Subject, claimed[0].Envelope); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)

	// Broker outage: relay fails, rows stay pending.
	srv.Shutdown()
	w := worker(t, s, conn, "relay", func(c *WorkerConfig) { c.MaxAttempts = 50 })
	if n, _ := w.RunOnce(ctx); n != 0 {
		t.Fatal("nothing can be acknowledged while broker is down")
	}
	if pending(t, db) != 2 {
		t.Fatal("events lost during outage")
	}
	srv.Restart()
	testkit.Eventually(t, 15*time.Second, func() bool {
		_, _ = w.RunOnce(ctx)
		return pending(t, db) == 0
	}, "relay after broker recovery")

	// Stream de-duplicated the re-publish of the first event.
	st, _ := conn.JetStream().Stream(ctx, "REALESTATE_THING")
	info, _ := st.Info(ctx)
	if info.State.Msgs != 2 {
		t.Fatalf("stream has %d messages, want 2 (dedup by event_id)", info.State.Msgs)
	}

	// Consumer side: deliver everything twice (simulated redelivery) and
	// prove side effects happen once via processed_events.
	var effects atomic.Int32
	handler := func(ctx context.Context, d events.Delivery) error {
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		first, err := events.MarkProcessed(ctx, tx, "thing-consumer", d.Envelope)
		if err != nil {
			return err
		}
		if first {
			effects.Add(1)
		}
		return tx.Commit(ctx)
	}
	var seen atomic.Int32
	cons, err := conn.Consume(ctx, jetstream.ConsumerConfig{Stream: "REALESTATE_THING", Durable: "thing-consumer", AckWait: 2 * time.Second},
		func(ctx context.Context, d events.Delivery) error {
			seen.Add(1)
			if err := handler(ctx, d); err != nil {
				return err
			}
			return handler(ctx, d) // duplicate delivery of the same event
		})
	if err != nil {
		t.Fatal(err)
	}
	defer cons.Stop()
	testkit.Eventually(t, 10*time.Second, func() bool { return seen.Load() >= 2 }, "consumption")
	if effects.Load() != 2 {
		t.Fatalf("side effects=%d, want exactly one per event (%v)", effects.Load(), ids1)
	}
}
