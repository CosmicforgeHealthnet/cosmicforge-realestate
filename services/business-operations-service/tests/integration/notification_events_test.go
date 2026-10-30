package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

type businessNotificationFake struct {
	mu       sync.Mutex
	down     bool
	attempts int
	accepted map[string]notification.Accepted
	requests []notification.SendRequest
}

func (f *businessNotificationFake) Send(ctx context.Context, in notification.SendRequest, key string) (notification.Accepted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.down {
		return notification.Accepted{}, errors.New("notification provider unavailable")
	}
	if f.accepted == nil {
		f.accepted = map[string]notification.Accepted{}
	}
	if accepted, ok := f.accepted[key]; ok {
		return accepted, nil
	}
	accepted := notification.Accepted{NotificationID: ids.New()}
	f.accepted[key] = accepted
	f.requests = append(f.requests, in)
	return accepted, nil
}
func (f *businessNotificationFake) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts, len(f.accepted)
}
func TestBusinessSecurityNotificationsRealBrokerRecovery(t *testing.T) {
	service, db := newBusinessAuth(t)
	email := "security-notification@example.test"
	if _, err := service.Register(t.Context(), businessauth.Registration{Email: email, Password: "correct horse battery", DisplayName: "Notification Owner", OrganisationName: "Notification Business"}, "notification-register"); err != nil {
		t.Fatal(err)
	}
	var envelope events.Envelope
	var payload []byte
	if err := db.Pool.QueryRow(t.Context(), `SELECT event_id,event_type,producer,aggregate_type,aggregate_id,payload,occurred_at FROM outbox_events WHERE event_type=$1`, businessEvents.NotificationRequested).Scan(&envelope.EventID, &envelope.EventType, &envelope.Producer, &envelope.AggregateType, &envelope.AggregateID, &payload, &envelope.OccurredAt); err != nil {
		t.Fatal(err)
	}
	envelope.Payload = payload
	if strings.Contains(string(payload), email) || strings.Contains(string(payload), "REGISTRATION") {
		t.Fatal("notification secrets/contact details stored in cleartext outbox")
	}
	broker := testkit.StartNATS(t)
	conn, err := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "business-notification-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.EnsureStream(t.Context(), businessEvents.StreamConfig("realestate")); err != nil {
		t.Fatal(err)
	}
	provider := &businessNotificationFake{down: true}
	worker := &workers.Notifications{Pool: db.Pool, Sealer: service.Sealer, Sender: provider}
	var seen atomic.Int32
	consumer, err := conn.Consume(t.Context(), jetstream.ConsumerConfig{Stream: businessEvents.StreamConfig("realestate").Name, Durable: workers.NotificationConsumer, FilterSubjects: []string{events.Subject("realestate", businessEvents.NotificationRequested)}, AckWait: 2 * time.Second, OnFailure: worker.Quarantine}, func(ctx context.Context, d events.Delivery) error { seen.Add(1); return worker.Handle(ctx, d) })
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Stop()
	relay, err := outbox.NewWorker(service.Outbox, conn, outbox.WorkerConfig{Owner: "notification-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 5*time.Second, func() bool { attempts, _ := provider.counts(); return attempts > 0 }, "provider outage attempt")
	var processed int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM processed_events WHERE consumer=$1`, workers.NotificationConsumer).Scan(&processed); err != nil || processed != 0 {
		t.Fatalf("failed delivery acknowledged: %d %v", processed, err)
	}
	provider.mu.Lock()
	provider.down = false
	provider.mu.Unlock()
	testkit.Eventually(t, 8*time.Second, func() bool { _, accepted := provider.counts(); return accepted == 1 }, "notification recovery")
	testkit.Eventually(t, 5*time.Second, func() bool {
		var count int
		err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_acceptances`).Scan(&count)
		return err == nil && count == 1
	}, "durable acceptance reference")
	before := seen.Load()
	raw, err := envelope.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.JetStream().Publish(t.Context(), events.Subject("realestate", businessEvents.NotificationRequested), raw); err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 5*time.Second, func() bool { return seen.Load() > before }, "duplicate consumption")
	if _, accepted := provider.counts(); accepted != 1 {
		t.Fatal("duplicate notification acceptance")
	}
	envelope.EventID = ids.New()
	envelope.Payload = append([]byte(nil), payload...)
	var job businessEvents.NotificationJob
	if err := envelope.DecodePayload(&job); err != nil {
		t.Fatal(err)
	}
	job.Ciphertext[0] ^= 0xff
	envelope.Payload, _ = json.Marshal(job)
	if _, err := conn.Publish(t.Context(), events.Subject("realestate", businessEvents.NotificationRequested), envelope); err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 5*time.Second, func() bool {
		var count int
		err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM event_failures WHERE event_id=$1 AND failure_class='PERMANENT'`, envelope.EventID.String()).Scan(&count)
		return err == nil && count == 1
	}, "poison notification quarantine")
}
