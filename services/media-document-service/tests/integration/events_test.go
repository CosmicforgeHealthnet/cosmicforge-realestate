package integration

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	js "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestEventsRelayRecoveryAndContracts(t *testing.T) {
	h := newHarness(t)
	ready := h.ready(ids.New(), "IMAGE", "image/png", "a.png", pngBytes, "")
	u, _ := h.initiate(ids.New(), "IMAGE", "image/png", "b.png", pngBytes, "", "")
	h.put(u, append([]byte("MZ"), pngBytes[2:]...))
	h.complete(u.Media.MediaID, "k")
	h.scan()
	h.do(call{method: "POST", path: "/internal/v1/media/" + ready.String() + "/deletion", as: platform.IndividualOwnerService, key: "d", body: map[string]any{"reason": "X"}})
	if _, err := h.app.Service.DeleteDue(context.Background(), "w", 10); err != nil {
		t.Fatal(err)
	}

	srv := testkit.StartNATS(t)
	conn, err := js.Connect(js.Config{URL: srv.URL, Name: "md-test", PublishTimeout: 500 * time.Millisecond, Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	_ = conn.EnsureStream(ctx, js.StreamConfig{Name: "REALESTATE_MEDIA", Subjects: []string{"realestate.media.>"}})
	relay, _ := outbox.NewWorker(h.app.Outbox, conn, outbox.WorkerConfig{Owner: "r", BatchSize: 3, MaxAttempts: 100, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond, Logger: logging.Discard()})
	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Shutdown()
	_, _ = relay.RunOnce(ctx)
	srv.Restart()
	testkit.Eventually(t, 20*time.Second, func() bool {
		_, _ = relay.RunOnce(ctx)
		var n int
		_ = h.db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&n)
		return n == 0
	}, "outbox drained after broker restart")

	stream, _ := conn.JetStream().Stream(ctx, "REALESTATE_MEDIA")
	cons, _ := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{AckPolicy: jetstream.AckNonePolicy})
	batch, err := cons.Fetch(50, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for m := range batch.Messages() {
		env, err := events.Unmarshal(m.Data())
		if err != nil {
			t.Fatal(err)
		}
		testkit.ValidateJSONSchema(t, "../../../../contracts/events/media/"+env.EventType+".schema.json", m.Data())
		seen[env.EventType]++
	}
	want := map[string]int{"media.upload_completed.v1": 2, "media.scanning.v1": 2, "media.ready.v1": 1, "media.rejected.v1": 1, "media.deleted.v1": 1}
	for k, v := range want {
		if seen[k] != v {
			t.Fatalf("events %v", seen)
		}
	}
}
