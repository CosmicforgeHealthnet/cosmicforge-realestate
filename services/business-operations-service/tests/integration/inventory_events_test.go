package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestBusinessInventoryEventsKeepOwnerNamespaceAndRecoverAfterBrokerRestart(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	u := h.user()
	h.secure(u)
	branch := h.inventoryBranch(u)
	p := h.inventoryNode(u, "/properties", map[string]any{"branch_id": branch, "code": "P", "name": "Property", "attributes": map[string]any{}})
	broker := testkit.StartNATS(t)
	conn, err := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "inventory-events", PublishTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	// Reproduce the established Individual Owner subject reservation without
	// importing another service's internal packages or touching its database.
	if err = conn.EnsureStream(ctx, jetstream.StreamConfig{Name: "OWNER_PROPERTY_NAMESPACE_TEST", Subjects: []string{"realestate.property.>"}}); err != nil {
		t.Fatal(err)
	}
	if err = conn.EnsureStream(ctx, businessEvents.StreamConfig("realestate")); err != nil {
		t.Fatal(err)
	}
	var subject string
	var raw []byte
	if err = h.db.Pool.QueryRow(ctx, `SELECT subject,jsonb_strip_nulls(jsonb_build_object('event_id',event_id,'event_type',event_type,'producer',producer,'aggregate_type',aggregate_type,'aggregate_id',aggregate_id,'occurred_at',occurred_at,'trace_id',trace_id,'actor',CASE WHEN actor_id IS NOT NULL THEN jsonb_build_object('domain',actor_domain,'id',actor_id) END,'metadata',metadata,'payload',payload)) FROM outbox_events WHERE event_type='property.created.v1' AND aggregate_id=$1`, p.ID).Scan(&subject, &raw); err != nil {
		t.Fatal(err)
	}
	var envelope events.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if subject != "realestate.business.property.created.v1" || envelope.EventType != "property.created.v1" {
		t.Fatalf("namespace %s %s", subject, envelope.EventType)
	}
	w, err := outbox.NewWorker(h.service.Outbox, conn, outbox.WorkerConfig{Owner: "inventory-restart", PublishTimeout: 100 * time.Millisecond, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	broker.Shutdown()
	if _, err = w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	stats, err := h.service.Outbox.Stats(ctx)
	if err != nil || stats.Pending == 0 {
		t.Fatalf("outage lost events %+v %v", stats, err)
	}
	broker.Restart()
	testkit.Eventually(t, 5*time.Second, func() bool { return conn.Ready(ctx) == nil }, "JetStream reconnection")
	_, err = h.db.Pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=now()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	ack, err := conn.Publish(ctx, subject, envelope)
	if err != nil || !ack.Duplicate {
		t.Fatalf("restart/duplicate %+v %v", ack, err)
	}
	ownerStream, err := conn.JetStream().Stream(ctx, "OWNER_PROPERTY_NAMESPACE_TEST")
	if err != nil {
		t.Fatal(err)
	}
	info, err := ownerStream.Info(ctx)
	if err != nil || info.State.Msgs != 0 {
		t.Fatalf("owner subject leaked %v %v", info, err)
	}
}
