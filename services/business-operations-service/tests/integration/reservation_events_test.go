package integration

import (
	"context"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"sync/atomic"
	"testing"
	"time"
)

func TestBusinessReservationActualEventsSurviveBrokerOutageAndConsumerRedelivery(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	broker := testkit.StartNATS(t)
	conn, e := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "canonical-reservation-outbox", PublishTimeout: 100 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if e = conn.EnsureStream(ctx, businessEvents.StreamConfig("realestate")); e != nil {
		t.Fatal(e)
	}
	broker.Shutdown()
	command := reservationCommandFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", command, 201))
	// A failed second acceptance cannot leak a canonical event or consume stock.
	c.must(c.owner, "POST", "/reservations", command, 409)
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/cancel", reservations.Command{Version: created.Reservation.Version, Reason: "Release a pending reservation while the broker is offline"}, 200)
	store := outbox.NewStore(c.db.Pool, "realestate")
	worker, e := outbox.NewWorker(store, conn, outbox.WorkerConfig{Owner: ids.New().String(), BatchSize: 100, PublishTimeout: 100 * time.Millisecond, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	if n, e := worker.RunOnce(ctx); e != nil || n != 0 {
		t.Fatalf("offline publication %d %v", n, e)
	}
	var pending int
	if e = c.db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type LIKE 'reservation.%' AND published_at IS NULL`).Scan(&pending); e != nil || pending != 2 {
		t.Fatalf("lost or phantom reservation events %d %v", pending, e)
	}
	broker.Restart()
	testkit.Eventually(t, 5*time.Second, func() bool { return conn.Ready(ctx) == nil }, "JetStream restart")
	var deliveries, effects atomic.Int32
	received := make(chan events.Envelope, 4)
	durable := "reservation-" + ids.New().String()
	consumer, e := conn.Consume(ctx, jetstream.ConsumerConfig{Stream: businessEvents.StreamConfig("realestate").Name, Durable: durable, FilterSubjects: []string{"realestate.business.reservation.>"}, AckWait: time.Second}, func(ctx context.Context, delivery events.Delivery) error {
		if delivery.Envelope.AggregateID != created.Reservation.ID {
			return nil
		}
		tx, e := c.db.Pool.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		fresh, e := events.MarkProcessed(ctx, tx, durable, delivery.Envelope)
		if e != nil {
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		if fresh {
			received <- delivery.Envelope
			effects.Add(1)
		}
		deliveries.Add(1)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer consumer.Stop()
	if _, e = c.db.Pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=now()`); e != nil {
		t.Fatal(e)
	}
	if _, e = worker.RunOnce(ctx); e != nil {
		t.Fatal(e)
	}
	testkit.Eventually(t, 5*time.Second, func() bool { return effects.Load() == 2 }, "canonical events after broker recovery")
	var createdEvent events.Envelope
	for range 2 {
		select {
		case event := <-received:
			if event.EventType == "reservation.created.v1" {
				createdEvent = event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if createdEvent.EventID.ValidateV7() != nil {
		t.Fatal("reservation event did not have a canonical UUIDv7 envelope")
	}
	var subject string
	if e = c.db.Pool.QueryRow(ctx, `SELECT subject FROM outbox_events WHERE event_id=$1`, createdEvent.EventID).Scan(&subject); e != nil {
		t.Fatal(e)
	}
	raw, e := createdEvent.Marshal()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = conn.JetStream().Publish(ctx, subject, raw); e != nil {
		t.Fatal(e)
	}
	testkit.Eventually(t, 5*time.Second, func() bool { return deliveries.Load() >= 3 }, "real duplicate reservation delivery")
	if effects.Load() != 2 {
		t.Fatalf("consumer duplicated canonical effects %d", effects.Load())
	}
	if ack, e := conn.Publish(ctx, subject, createdEvent); e != nil || !ack.Duplicate {
		t.Fatalf("broker event-id deduplication %+v %v", ack, e)
	}
}
