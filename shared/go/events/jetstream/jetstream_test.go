package jetstream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/logging"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

func setup(t *testing.T) (*testkit.NATSServer, *Conn) {
	srv := testkit.StartNATS(t)
	c, err := Connect(Config{URL: srv.URL, Name: "test", PublishTimeout: time.Second, Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.EnsureStream(context.Background(), StreamConfig{Name: "REALESTATE_TEST", Subjects: []string{"realestate.test.>"}}); err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func event(t *testing.T) events.Envelope {
	e, err := events.New(context.Background(), platform.VerificationService, "test.happened.v1", "test", ids.New(), map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPublishAckAndDedup(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	e := event(t)
	a1, err := c.Publish(ctx, "realestate.test.happened.v1", e)
	if err != nil || a1.Sequence == 0 || a1.Duplicate {
		t.Fatalf("ack %+v %v", a1, err)
	}
	// Re-publishing the same event_id (outbox crash before mark-published) is de-duplicated.
	a2, err := c.Publish(ctx, "realestate.test.happened.v1", e)
	if err != nil || !a2.Duplicate {
		t.Fatalf("expected duplicate ack, got %+v %v", a2, err)
	}
	info, _ := c.JetStream().Stream(ctx, "REALESTATE_TEST")
	si, _ := info.Info(ctx)
	if si.State.Msgs != 1 {
		t.Fatalf("stream holds %d messages", si.State.Msgs)
	}
	if err := c.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPublishFailsWithoutAckWhenBrokerDown(t *testing.T) {
	srv, c := setup(t)
	srv.Shutdown()
	ctx := context.Background()
	if _, err := c.Publish(ctx, "realestate.test.happened.v1", event(t)); err == nil {
		t.Fatal("publish must fail without broker ack")
	}
	if c.Ready(ctx) == nil {
		t.Fatal("ready must fail while disconnected")
	}
	srv.Restart()
	testkit.Eventually(t, 10*time.Second, func() bool {
		_, err := c.Publish(ctx, "realestate.test.happened.v1", event(t))
		return err == nil
	}, "publish after broker restart")
}

func TestConsumeRedeliveryAttemptsAndPoison(t *testing.T) {
	_, c := setup(t)
	ctx := context.Background()
	var mu sync.Mutex
	attempts := map[ids.ID][]uint64{}
	failFirst := event(t)
	ok := event(t)
	permanent := event(t)
	var done atomic.Int32
	cons, err := c.Consume(ctx, ConsumerConfig{Stream: "REALESTATE_TEST", Durable: "test-consumer", AckWait: 2 * time.Second, MaxDeliver: 5},
		func(ctx context.Context, d events.Delivery) error {
			mu.Lock()
			attempts[d.Envelope.EventID] = append(attempts[d.Envelope.EventID], d.Attempt)
			mu.Unlock()
			switch d.Envelope.EventID {
			case failFirst.EventID:
				if d.Attempt == 1 {
					return errors.New("transient")
				}
			case permanent.EventID:
				done.Add(1)
				return events.Permanent(errors.New("bad payload"))
			}
			done.Add(1)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer cons.Stop()
	for _, e := range []events.Envelope{failFirst, ok, permanent} {
		if _, err := c.Publish(ctx, "realestate.test.happened.v1", e); err != nil {
			t.Fatal(err)
		}
	}
	// Poison (non-envelope) message is terminated, not looped.
	if err := c.nc.Publish("realestate.test.garbage.v1", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 10*time.Second, func() bool { return done.Load() >= 3 }, "deliveries")
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if got := attempts[failFirst.EventID]; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("redelivery attempts %v", got)
	}
	if got := attempts[permanent.EventID]; len(got) != 1 {
		t.Fatalf("permanent failure redelivered: %v", got)
	}
	if got := attempts[ok.EventID]; len(got) != 1 {
		t.Fatalf("acked event redelivered: %v", got)
	}
}

func TestStreamName(t *testing.T) {
	if StreamName("realestate", "media-document") != "REALESTATE_MEDIA_DOCUMENT" {
		t.Fatal(StreamName("realestate", "media-document"))
	}
	_ = nats.ErrTimeout
}
