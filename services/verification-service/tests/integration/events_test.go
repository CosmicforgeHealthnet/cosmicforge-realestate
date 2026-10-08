package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/verification-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	js "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

const contracts = "../../../../contracts/events/verification/"

// produceAllEvents drives cases through every catalogued transition.
func produceAllEvents(t *testing.T, h *harness) {
	verified := h.createReady(ids.New(), "", nil)
	h.submitAndProcess(verified)
	rejected := h.createReady(ids.New(), "", map[string]string{"sandbox_outcome": "REJECTED"})
	h.submitAndProcess(rejected)
	review := h.createReady(ids.New(), "", map[string]string{"sandbox_outcome": "INCONCLUSIVE"})
	h.submitAndProcess(review)
	h.dispatch()
	h.do(call{method: "POST", path: "/internal/v1/verifications/" + verified.String() + "/expire", as: platform.AdminService,
		actor: &serviceauth.Delegation{ActorDomain: platform.DomainAdmin, ActorID: ids.New()}, key: ids.New().String(),
		body: map[string]any{"reason_code": "DOCUMENT_REVOKED"}})
}

func TestOutboxRelayToJetStreamWithRecoveryAndContracts(t *testing.T) {
	h := newHarness(t)
	produceAllEvents(t, h)
	var pending int
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&pending)
	if pending != 10 {
		t.Fatalf("expected 10 pending events, got %d", pending)
	}

	srv := testkit.StartNATS(t)
	conn, err := js.Connect(js.Config{URL: srv.URL, Name: "vs-test", PublishTimeout: 500 * time.Millisecond, Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	if err := conn.EnsureStream(ctx, js.StreamConfig{Name: "REALESTATE_VERIFICATION", Subjects: []string{"realestate.verification.>"}}); err != nil {
		t.Fatal(err)
	}
	relay, _ := outbox.NewWorker(h.app.Outbox, conn, outbox.WorkerConfig{Owner: "relay", BatchSize: 4, MaxAttempts: 100,
		BaseBackoff: time.Millisecond, MaxBackoff: 10 * time.Millisecond, Logger: logging.Discard()})

	// Publish part of the backlog, then lose the broker.
	if n, err := relay.RunOnce(ctx); err != nil || n != 4 {
		t.Fatalf("first batch: %d %v", n, err)
	}
	srv.Shutdown()
	if n, _ := relay.RunOnce(ctx); n != 0 {
		t.Fatal("nothing may be marked published while the broker is down")
	}
	srv.Restart()
	testkit.Eventually(t, 20*time.Second, func() bool {
		_, _ = relay.RunOnce(ctx)
		var n int
		_ = h.db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&n)
		return n == 0
	}, "relay drains outbox after recovery")

	stream, _ := conn.JetStream().Stream(ctx, "REALESTATE_VERIFICATION")
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{AckPolicy: jetstream.AckNonePolicy})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(20, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for m := range batch.Messages() {
		env, err := events.Unmarshal(m.Data())
		if err != nil {
			t.Fatal(err)
		}
		if m.Subject() != "realestate."+env.EventType || env.Producer != "verification-service" || env.TraceID == "" {
			t.Fatalf("bad routing/metadata: %s %+v", m.Subject(), env)
		}
		testkit.ValidateJSONSchema(t, contracts+env.EventType+".schema.json", m.Data())
		seen[env.EventType]++
	}
	want := map[string]int{
		"verification.submitted.v1": 3, "verification.processing.v1": 3, "verification.verified.v1": 1,
		"verification.rejected.v1": 1, "verification.manual_review_required.v1": 1, "verification.expired.v1": 1,
	}
	for k, v := range want {
		if seen[k] != v {
			t.Fatalf("event counts %v", seen)
		}
	}
}

func TestWorkerRunnerEndToEndAndGracefulStop(t *testing.T) {
	h := newHarness(t)
	srv := testkit.StartNATS(t)
	conn, err := js.Connect(js.Config{URL: srv.URL, Name: "vs-runner", Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	_ = conn.EnsureStream(ctx, js.StreamConfig{Name: "REALESTATE_VERIFICATION", Subjects: []string{"realestate.verification.>"}})
	relay, _ := outbox.NewWorker(h.app.Outbox, conn, outbox.WorkerConfig{Owner: "runner", PollInterval: 20 * time.Millisecond, Logger: logging.Discard()})
	r := workers.New(workers.Config{Owner: "runner", DispatchInterval: 20 * time.Millisecond, ExpiryInterval: time.Second},
		h.app.Service, relay, h.app.Outbox, h.app.Idem, logging.Discard())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.Run(ctx) }()

	// A consumer with de-duplication sees each event once even if redelivered.
	var mu sync.Mutex
	got := map[ids.ID]int{}
	c, err := conn.Consume(context.Background(), js.ConsumerConfig{Stream: "REALESTATE_VERIFICATION", Durable: "test-consumer", AckWait: 2 * time.Second},
		func(ctx context.Context, d events.Delivery) error {
			mu.Lock()
			defer mu.Unlock()
			got[d.Envelope.EventID]++
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	id := h.createReady(ids.New(), "", nil)
	h.submitAndProcess(id)
	testkit.Eventually(t, 10*time.Second, func() bool { return h.status(id) == "VERIFIED" }, "worker processes the case")
	testkit.Eventually(t, 10*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 3 }, "events delivered")
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop gracefully")
	}
	b, _ := json.Marshal(got)
	if len(b) == 0 {
		t.Fatal("unreachable")
	}
}
