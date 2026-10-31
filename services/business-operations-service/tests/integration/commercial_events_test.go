package integration

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestBusinessCommercialInventoryOutboxBrokerRecoveryAndConsumerEffectiveDedupe(t *testing.T) {
	c := newCommercialHTTP(t)
	broker := testkit.StartNATS(t)
	conn, e := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "commercial-broker-recovery", PublishTimeout: 100 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if e = conn.EnsureStream(ctx, jetstream.StreamConfig{Name: "OWNER_COMMERCIAL_NAMESPACE_TEST", Subjects: []string{"realestate.inventory.>", "realestate.listing.>", "realestate.rate_plan.>"}}); e != nil {
		t.Fatal(e)
	}
	if e = conn.EnsureStream(ctx, businessEvents.StreamConfig("realestate")); e != nil {
		t.Fatal(e)
	}
	relay, e := outbox.NewWorker(c.service.Outbox, conn, outbox.WorkerConfig{Owner: "commercial-test-relay", BatchSize: 100, PublishTimeout: 100 * time.Millisecond, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = relay.RunOnce(ctx); e != nil {
		t.Fatal(e)
	}
	broker.Shutdown()
	days, _ := c.dates.Days(c.property.Timezone)
	versions := map[string]int64{}
	for _, day := range days {
		versions[day] = 1
	}
	capacity := 1
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: c.pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Capacity: &capacity, ExpectedVersions: versions, Reason: "Commit canonical availability while the broker is unavailable"}, 200)
	next := c.definition
	next.AmountMinor = 10500
	c.must(c.owner, "PATCH", "/rate-plans/"+c.rate.ID.String(), businesshttp.CommercialRatePatch{Version: c.rate.Version, Definition: &next, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Reason: "Persist an in-band canonical rate delta while the broker is unavailable"}, 200)
	if _, e = relay.RunOnce(ctx); e != nil {
		t.Fatal(e)
	}
	stats, e := c.service.Outbox.Stats(ctx)
	if e != nil || stats.Pending == 0 {
		t.Fatalf("Broker outage lost committed inventory event %#v %v", stats, e)
	}
	var raw []byte
	var subject string
	if e = c.db.Pool.QueryRow(ctx, `SELECT subject,jsonb_strip_nulls(jsonb_build_object('event_id',event_id,'event_type',event_type,'producer',producer,'aggregate_type',aggregate_type,'aggregate_id',aggregate_id,'occurred_at',occurred_at,'trace_id',trace_id,'actor',CASE WHEN actor_id IS NOT NULL THEN jsonb_build_object('domain',actor_domain,'id',actor_id) END,'metadata',metadata,'payload',payload)) FROM outbox_events WHERE event_type='inventory.changed.v1' AND aggregate_id=$1 ORDER BY occurred_at DESC,event_id DESC LIMIT 1`, c.pool.ID).Scan(&subject, &raw); e != nil {
		t.Fatal(e)
	}
	var envelope events.Envelope
	if e = json.Unmarshal(raw, &envelope); e != nil {
		t.Fatal(e)
	}
	if subject != "realestate.business.inventory.changed.v1" {
		t.Fatalf("Owner namespace collision %s", subject)
	}
	broker.Restart()
	testkit.Eventually(t, 5*time.Second, func() bool { return conn.Ready(ctx) == nil }, "Commercial JetStream recovery")
	if _, e = c.db.Pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=now() WHERE published_at IS NULL AND dead_at IS NULL`); e != nil {
		t.Fatal(e)
	}
	if _, e = relay.RunOnce(ctx); e != nil {
		t.Fatal(e)
	}
	var pendingRate int
	if e = c.db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='rate_plan.changed.v1' AND published_at IS NULL AND dead_at IS NULL`).Scan(&pendingRate); e != nil || pendingRate != 0 {
		t.Fatalf("Committed rate event did not recover %d %v", pendingRate, e)
	}
	ack, e := conn.Publish(ctx, subject, envelope)
	if e != nil || !ack.Duplicate {
		t.Fatalf("Broker republish not deduplicated %#v %v", ack, e)
	}
	var deliveries, effects atomic.Int32
	durable := "commercial-inbox-" + ids.New().String()
	consumer, e := conn.Consume(ctx, jetstream.ConsumerConfig{Stream: businessEvents.StreamConfig("realestate").Name, Durable: durable, FilterSubjects: []string{subject}, AckWait: time.Second}, func(ctx context.Context, delivery events.Delivery) error {
		if delivery.Envelope.EventID != envelope.EventID {
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
			effects.Add(1)
		}
		deliveries.Add(1)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer consumer.Stop()
	if _, e = conn.JetStream().Publish(ctx, subject, raw); e != nil {
		t.Fatal(e)
	}
	testkit.Eventually(t, 5*time.Second, func() bool { return deliveries.Load() >= 2 }, "Repeated commercial event delivery")
	if effects.Load() != 1 {
		t.Fatalf("Duplicate effective commercial updates %d", effects.Load())
	}
	owner, e := conn.JetStream().Stream(ctx, "OWNER_COMMERCIAL_NAMESPACE_TEST")
	if e != nil {
		t.Fatal(e)
	}
	info, e := owner.Info(ctx)
	if e != nil || info.State.Msgs != 0 {
		t.Fatalf("Business events leaked into Owner stream %#v %v", info, e)
	}
}
