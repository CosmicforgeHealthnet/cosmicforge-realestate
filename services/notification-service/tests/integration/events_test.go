package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	js "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func TestEventsRelayRecoveryAndContracts(t *testing.T) {
	h := newHarness(t)
	u := newUser()
	h.registerDevice(u, "device-token-unregistered-0007")
	h.mustSend(u, alert()) // EMAIL + SMS sent, PUSH fails (invalid token)
	h.dispatch()

	srv := testkit.StartNATS(t)
	conn, err := js.Connect(js.Config{URL: srv.URL, Name: "ns-test", PublishTimeout: 500 * time.Millisecond, Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	_ = conn.EnsureStream(ctx, js.StreamConfig{Name: "REALESTATE_NOTIFICATION", Subjects: []string{"realestate.notification.>"}})
	relay, _ := outbox.NewWorker(h.app.Outbox, conn, outbox.WorkerConfig{Owner: "r", BatchSize: 2, MaxAttempts: 100, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond, Logger: logging.Discard()})
	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	srv.Shutdown()
	_, _ = relay.RunOnce(ctx)
	srv.Restart()
	testkit.Eventually(t, 20*time.Second, func() bool {
		_, _ = relay.RunOnce(ctx)
		return h.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	}, "outbox drained after broker restart")

	stream, _ := conn.JetStream().Stream(ctx, "REALESTATE_NOTIFICATION")
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
		testkit.ValidateJSONSchema(t, "../../../../contracts/events/notification/"+env.EventType+".schema.json", m.Data())
		for _, pii := range []string{"owner@example.com", "+2348030000000", "device-token", "New sign-in"} {
			if strings.Contains(string(m.Data()), pii) {
				t.Fatalf("event leaks %q: %s", pii, m.Data())
			}
		}
		seen[env.EventType]++
	}
	if seen["notification.queued.v1"] != 3 || seen["notification.sent.v1"] != 2 || seen["notification.failed.v1"] != 1 {
		t.Fatalf("events %v", seen)
	}
}
