package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	js "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

const contracts = "../../../../contracts/events/owners/"

func TestAccountEventsRelayToJetStreamWithRecoveryAndContracts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// Produce: activation, restriction, suspension, reinstatement.
	o := h.register(true)
	path := "/internal/v1/owner/accounts/" + o.id.String()
	admin := adminActor()
	h.expect(h.do(call{method: "POST", path: path + "/restrict", as: platform.AdminService, delegate: admin, key: "e1", body: map[string]any{"reason_code": "FRAUD_REVIEW"}}), 200, "")
	h.expect(h.do(call{method: "POST", path: path + "/suspend", as: platform.AdminService, delegate: admin, key: "e2", body: map[string]any{"reason_code": "FRAUD_CONFIRMED"}}), 200, "")
	h.expect(h.do(call{method: "POST", path: path + "/reinstate", as: platform.AdminService, delegate: admin, key: "e3",
		body: map[string]any{"reason_code": "APPEAL_UPHELD", "resolution_reference": "APPEAL-77"}}), 200, "")
	if n := h.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`); n != 4 {
		t.Fatalf("pending events %d", n)
	}

	srv := testkit.StartNATS(t)
	conn, err := js.Connect(js.Config{URL: srv.URL, Name: "io-test", PublishTimeout: 500 * time.Millisecond, Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.EnsureStream(ctx, js.StreamConfig{Name: "REALESTATE_OWNER", Subjects: []string{"realestate.owner.>"}}); err != nil {
		t.Fatal(err)
	}
	relay, _ := outbox.NewWorker(h.app.Outbox, conn, outbox.WorkerConfig{Owner: "relay", BatchSize: 2, MaxAttempts: 100,
		BaseBackoff: time.Millisecond, MaxBackoff: 10 * time.Millisecond, Logger: logging.Discard()})
	if n, err := relay.RunOnce(ctx); err != nil || n != 2 {
		t.Fatalf("first batch: %d %v", n, err)
	}
	// Broker outage: nothing is marked published; recovery drains the rest.
	srv.Shutdown()
	if n, _ := relay.RunOnce(ctx); n != 0 {
		t.Fatal("published while broker down")
	}
	srv.Restart()
	testkit.Eventually(t, 20*time.Second, func() bool {
		_, _ = relay.RunOnce(ctx)
		return h.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	}, "relay drains after recovery")
	// Crash after publish but before mark-published: re-publishing the same
	// event_id is de-duplicated by JetStream.
	if _, err := h.db.Pool.Exec(ctx, `UPDATE outbox_events SET published_at = NULL WHERE event_type='owner.account.activated.v1'`); err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 10*time.Second, func() bool {
		_, _ = relay.RunOnce(ctx)
		return h.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	}, "republish")

	stream, _ := conn.JetStream().Stream(ctx, "REALESTATE_OWNER")
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{AckPolicy: jetstream.AckNonePolicy})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(10, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	seen := map[string]bool{}
	for m := range batch.Messages() {
		e, err := events.Unmarshal(m.Data())
		if err != nil {
			t.Fatal(err)
		}
		if seen[e.EventID.String()] {
			t.Fatalf("duplicate delivery of %s not de-duplicated", e.EventID)
		}
		seen[e.EventID.String()] = true
		got[e.EventType]++
		testkit.ValidateJSONSchema(t, contracts+e.EventType+".schema.json", m.Data())
		var p map[string]any
		_ = json.Unmarshal(e.Payload, &p)
		for _, pii := range []string{"email", "phone", "display_name"} {
			if _, ok := p[pii]; ok {
				t.Fatalf("PII %s in event", pii)
			}
		}
		if e.AggregateID != o.id || e.Producer != "individual-owner-service" {
			t.Fatalf("%+v", e)
		}
	}
	if got["owner.account.activated.v1"] != 2 || got["owner.account.restricted.v1"] != 2 {
		t.Fatalf("events %v", got)
	}
}
