package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/migrations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"github.com/jackc/pgx/v5"
)

// This exercises the service's actual migration and shared relay against a
// disposable PostgreSQL database and a real, disk-backed JetStream server.
func TestBusinessOutboxRecovery(t *testing.T) {
	db := testkit.NewDatabase(t, migrations.FS)
	broker := testkit.StartNATS(t)
	conn, err := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "business-outbox-test", PublishTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := conn.EnsureStream(ctx, businessEvents.StreamConfig("realestate")); err != nil {
		t.Fatal(err)
	}
	store := outbox.NewStore(db.Pool, "realestate")
	event, err := events.New(ctx, platform.BusinessOperationsService, "business.staff.updated.v1", "staff", ids.New(), map[string]string{"status": "ACTIVE"})
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("abort owning transaction")
	err = store.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) ([]events.Envelope, error) {
		if err := store.Add(ctx, tx, event); err != nil {
			return nil, err
		}
		return nil, rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rollback: %v", err)
	}
	stats, err := store.Stats(ctx)
	if err != nil || stats.Pending != 0 {
		t.Fatalf("rolled back event survived: %+v %v", stats, err)
	}
	if err := store.WithTx(ctx, func(context.Context, pgx.Tx) ([]events.Envelope, error) { return []events.Envelope{event}, nil }); err != nil {
		t.Fatal(err)
	}
	worker := func(owner string, max int) *outbox.Worker {
		w, err := outbox.NewWorker(store, conn, outbox.WorkerConfig{Owner: owner, MaxAttempts: max, PublishTimeout: 100 * time.Millisecond, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	broker.Shutdown()
	if n, err := worker("before-restart", 12).RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("outage: %d %v", n, err)
	}
	stats, err = store.Stats(ctx)
	if err != nil || stats.Pending != 1 {
		t.Fatalf("outage lost event: %+v %v", stats, err)
	}
	broker.Restart()
	for conn.Ready(ctx) != nil {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	// Simulate a new relay process recovering the pending committed row.
	if _, err := db.Pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=now()`); err != nil {
		t.Fatal(err)
	}
	if n, err := worker("after-restart", 12).RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovery: %d %v", n, err)
	}
	ack, err := conn.Publish(ctx, events.Subject("realestate", event.EventType), event)
	if err != nil || !ack.Duplicate {
		t.Fatalf("republish must deduplicate: %+v %v", ack, err)
	}
	// Force broker-level redelivery of the same envelope without a message-id
	// optimization, then prove a real durable consumer commits it only once.
	raw, err := event.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.JetStream().Publish(ctx, events.Subject("realestate", event.EventType), raw); err != nil {
		t.Fatal(err)
	}
	var delivered, effects atomic.Int32
	consumer, err := conn.Consume(ctx, jetstream.ConsumerConfig{Stream: businessEvents.StreamConfig("realestate").Name, Durable: "business-recovery-consumer", AckWait: time.Second}, func(ctx context.Context, delivery events.Delivery) error {
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		fresh, err := events.MarkProcessed(ctx, tx, "business-recovery-consumer", delivery.Envelope)
		if err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		if fresh {
			effects.Add(1)
		}
		delivered.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 5*time.Second, func() bool { return delivered.Load() >= 2 }, "real consumer duplicate delivery")
	consumer.Stop()
	if effects.Load() != 1 {
		t.Fatalf("consumer committed %d effects for one event", effects.Load())
	}
	for i := 0; i < 2; i++ {
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := events.MarkProcessed(ctx, tx, "business-test-consumer", event)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if fresh != (i == 0) {
			t.Fatalf("consumer idempotency: iteration %d fresh=%v", i, fresh)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// A crashed lease is reclaimable; an obsolete process cannot mark it published.
	event.EventID = ids.New()
	if err := store.WithTx(ctx, func(context.Context, pgx.Tx) ([]events.Envelope, error) { return []events.Envelope{event}, nil }); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx, "crashed", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE outbox_events SET lease_expires_at=now()-interval '1 second' WHERE lease_owner='crashed'`); err != nil {
		t.Fatal(err)
	}
	if n, err := worker("replacement", 12).RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("lease recovery: %d %v", n, err)
	}
	if ok, err := store.MarkPublished(ctx, "crashed", event.EventID); err != nil || ok {
		t.Fatalf("stale owner fence: %v %v", ok, err)
	}
	// Unsupported subjects are retained as dead rows after the bounded retry budget.
	event.EventID = ids.New()
	event.EventType = "unsupported.poison.v1"
	if err := store.WithTx(ctx, func(context.Context, pgx.Tx) ([]events.Envelope, error) { return []events.Envelope{event}, nil }); err != nil {
		t.Fatal(err)
	}
	if n, err := worker("quarantine", 1).RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("poison: %d %v", n, err)
	}
	stats, err = store.Stats(ctx)
	if err != nil || stats.Dead != 1 || stats.Pending != 0 {
		t.Fatalf("quarantine: %+v %v", stats, err)
	}
	runctx, stop := context.WithCancel(ctx)
	stop()
	if err := worker("shutdown", 12).Run(runctx); err != nil {
		t.Fatal(err)
	}
}
