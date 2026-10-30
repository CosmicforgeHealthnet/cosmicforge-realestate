package jetstream

import (
	"context"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
)

func TestShortAckWaitHasUsableHandlerDeadline(t *testing.T) {
	_, conn := setup(t)
	observed := make(chan error, 1)
	consumer, err := conn.Consume(context.Background(), ConsumerConfig{Stream: "REALESTATE_TEST", Durable: "short-ack", AckWait: time.Second}, func(ctx context.Context, _ events.Delivery) error {
		select {
		case observed <- ctx.Err():
		default:
		}
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Stop()
	if _, err := conn.Publish(context.Background(), "realestate.test.happened.v1", event(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-observed:
		if err != nil {
			t.Fatalf("handler started with expired context: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not receive event")
	}
}
