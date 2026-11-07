package integration

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/payment"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
)

// journeyPaymentNotFound models the real Payment Service answer for a quote
// that was never created during an outage: an authoritative 404, which is the
// only readback that lets the worker proceed to a first collection.
type journeyPaymentNotFound struct{ *reservationPaymentFake }

func (f journeyPaymentNotFound) Receipt(ctx context.Context, id ids.ID) (payment.Receipt, error) {
	f.mu.Lock()
	unknown := !f.unavailable && f.quote.ID != id
	f.mu.Unlock()
	if unknown {
		return payment.Receipt{}, &httpx.RemoteError{Service: "payment-service", Status: 404, Code: "NOT_FOUND", Message: "quote not found"}
	}
	return f.reservationPaymentFake.Receipt(ctx, id)
}

// Journey 9: a Payment outage and a JetStream outage during one booking leave
// durable pending state; worker recovery collects once, publishes every
// committed event once and a redelivered event has no second effect.
func TestJourney09PaymentAndBrokerOutageRecoverWithoutDuplicateEffects(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	broker := testkit.StartNATS(t)
	conn, e := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "journey-09-outbox", PublishTimeout: 100 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if e = conn.EnsureStream(ctx, businessEvents.StreamConfig("realestate")); e != nil {
		t.Fatal(e)
	}
	broker.Shutdown()

	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	id := created.Reservation.ID.String()
	command := reservationPaymentCommand(c, created)
	f := c.domain.Payment.(*reservationPaymentFake)
	c.domain.Payment = journeyPaymentNotFound{f}
	f.unavailable = true
	key := ids.New().String()
	status, first := c.call(c.owner, http.MethodPost, "/reservations/"+id+"/payment-requests", command, map[string]string{"Idempotency-Key": key})
	if status != 202 {
		t.Fatalf("durable collection request %d %s", status, first)
	}
	status, replay := c.call(c.owner, http.MethodPost, "/reservations/"+id+"/payment-requests", command, map[string]string{"Idempotency-Key": key})
	if status != 202 || string(replay) != string(first) {
		t.Fatalf("idempotent replay changed the durable request %d %s", status, replay)
	}
	if e = c.domain.ProcessReservationFinance(ctx, 1); e != nil {
		t.Fatal(e)
	}
	var state string
	var requests int
	if e = c.db.Pool.QueryRow(ctx, `SELECT max(state),count(*) FROM reservation_financial_requests WHERE reservation_id=$1`, created.Reservation.ID).Scan(&state, &requests); e != nil || state != "RETRY" || requests != 1 {
		t.Fatalf("outage did not stay pending: state=%s requests=%d err=%v", state, requests, e)
	}
	c.must(c.owner, "POST", "/reservations/"+id+"/confirm", reservations.Command{Version: created.Reservation.Version, Reason: "An unavailable Payment Service is not a collection"}, 409)

	// Recovery: the provider commits the collection but the response is lost.
	// Readback must find the original intent instead of collecting again.
	f.mu.Lock()
	f.unavailable = false
	f.loseCollectionResponse = true
	f.fundingConfirmed = true
	f.mu.Unlock()
	var paid reservations.Detail
	for range 4 {
		if _, e = c.db.Pool.Exec(ctx, `UPDATE reservation_financial_requests SET available_at=now(),version=version+1 WHERE reservation_id=$1 AND state IN ('RETRY','QUEUED','PENDING')`, created.Reservation.ID); e != nil {
			t.Fatal(e)
		}
		if e = c.domain.ProcessReservationFinance(ctx, 1); e != nil {
			t.Fatal(e)
		}
		paid = commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+id, nil, 200))
		if paid.Reservation.FinancialState == "VERIFIED" {
			break
		}
	}
	if paid.Reservation.FinancialState != "VERIFIED" {
		t.Fatalf("recovered collection not read back: %s", paid.Reservation.FinancialState)
	}
	f.mu.Lock()
	calls := f.collectionCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("recovery collected %d times", calls)
	}
	confirmed := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+id+"/confirm", reservations.Command{Version: paid.Reservation.Version, Reason: "Confirm after authoritative collection readback"}, 200))
	if confirmed.Reservation.State != "CONFIRMED" {
		t.Fatalf("confirmation state %s", confirmed.Reservation.State)
	}
	// Repeated worker passes after success are no-ops.
	if e = c.domain.ProcessReservationFinance(ctx, 5); e != nil {
		t.Fatal(e)
	}
	f.mu.Lock()
	calls = f.collectionCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("idle worker pass collected again: %d", calls)
	}

	store := outbox.NewStore(c.db.Pool, "realestate")
	worker, e := outbox.NewWorker(store, conn, outbox.WorkerConfig{Owner: ids.New().String(), BatchSize: 200, PublishTimeout: 100 * time.Millisecond, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	if n, e := worker.RunOnce(ctx); e != nil || n != 0 {
		t.Fatalf("offline broker publication %d %v", n, e)
	}
	var expected int32
	if e = c.db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, created.Reservation.ID).Scan(&expected); e != nil || expected < 2 {
		t.Fatalf("committed reservation events were not retained while offline: %d %v", expected, e)
	}
	broker.Restart()
	testkit.Eventually(t, 5*time.Second, func() bool { return conn.Ready(ctx) == nil }, "JetStream restart")
	var deliveries, effects atomic.Int32
	durable := "journey-09-" + ids.New().String()
	var lastSubject string
	var lastEnvelope events.Envelope
	seen := make(chan events.Envelope, 64)
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
		deliveries.Add(1)
		if fresh {
			effects.Add(1)
			seen <- delivery.Envelope
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer consumer.Stop()
	if _, e = c.db.Pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=now() WHERE published_at IS NULL`); e != nil {
		t.Fatal(e)
	}
	for range 5 {
		if _, e = worker.RunOnce(ctx); e != nil {
			t.Fatal(e)
		}
	}
	var reservationEvents int32
	if e = c.db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type LIKE 'reservation.%'`, created.Reservation.ID).Scan(&reservationEvents); e != nil {
		t.Fatal(e)
	}
	testkit.Eventually(t, 10*time.Second, func() bool { return effects.Load() == reservationEvents }, "every committed reservation event after broker recovery")
	lastEnvelope = <-seen
	if e = c.db.Pool.QueryRow(ctx, `SELECT subject FROM outbox_events WHERE event_id=$1`, lastEnvelope.EventID).Scan(&lastSubject); e != nil {
		t.Fatal(e)
	}
	raw, e := lastEnvelope.Marshal()
	if e != nil {
		t.Fatal(e)
	}
	before := deliveries.Load()
	if _, e = conn.JetStream().Publish(ctx, lastSubject, raw); e != nil {
		t.Fatal(e)
	}
	testkit.Eventually(t, 5*time.Second, func() bool { return deliveries.Load() > before }, "redelivered duplicate")
	if effects.Load() != reservationEvents {
		t.Fatalf("redelivery duplicated an effect: %d effects for %d events", effects.Load(), reservationEvents)
	}
	var unpublished int
	if e = c.db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND published_at IS NULL`, created.Reservation.ID).Scan(&unpublished); e != nil || unpublished != 0 {
		t.Fatalf("events still pending after recovery: %d %v", unpublished, e)
	}
}

// Journey 10: another organisation, a suspended staff member and an expired
// delegation cannot read or change a protected reservation.
func TestJourney10TenantRevocationAndDelegationExpiryProtectReservations(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, true), 201))
	path := "/reservations/" + created.Reservation.ID.String()
	cancel := reservations.Command{Version: created.Reservation.Version, Reason: "Attempted change from outside the authorised scope"}

	// Another organisation: the record is invisible and immutable.
	outsider := c.user()
	c.secure(outsider)
	for _, attempt := range []struct{ method, path string }{{"GET", path}, {"POST", path + "/cancel"}, {"GET", path + "/lineage"}} {
		var body any
		if attempt.method == "POST" {
			body = cancel
		}
		if status, raw := c.call(outsider, attempt.method, attempt.path, body, nil); status != 403 && status != 404 {
			t.Fatalf("cross-organisation %s %s got %d %s", attempt.method, attempt.path, status, raw)
		}
	}

	// A staff member loses access as soon as they are suspended.
	staff := c.user()
	c.secure(staff)
	member := c.join(staff, c.owner, []string{"reservations:read", "reservations:cancel"})
	c.must(staff, "GET", path, nil, 200)
	var version int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT version FROM staff_memberships WHERE id=$1`, member).Scan(&version); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "PATCH", "/staff/"+member.String(), map[string]any{"version": version, "status": "SUSPENDED", "reason": "Security containment"}, 200)
	if status, _ := c.call(staff, "GET", path, nil, nil); status != 401 && status != 403 {
		t.Fatalf("suspended staff read %d", status)
	}
	if status, _ := c.call(staff, "POST", path+"/cancel", cancel, nil); status != 401 && status != 403 {
		t.Fatalf("suspended staff cancel %d", status)
	}

	// Delegated read authority stops at its expiry.
	delegate := c.user()
	c.secure(delegate)
	delegateMember := c.join(delegate, c.owner, nil)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE membership_id=$1`, delegateMember); e != nil {
		t.Fatal(e)
	}
	// The joined test membership reuses the owner's role; give the delegate a
	// role without reservation capabilities so only the delegation can authorize.
	var roleID ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `INSERT INTO role_templates(id,organisation_id,role_key,name,version,active) VALUES($1,$2,$3,'Delegated reader',1,true) RETURNING id`, ids.New(), c.owner.registered.OrganisationID, ids.New()).Scan(&roleID); e != nil {
		t.Fatal(e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `INSERT INTO role_permissions(role_id,permission) VALUES($1,'rates:read')`, roleID); e != nil {
		t.Fatal(e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_memberships SET role_id=$2 WHERE id=$1`, delegateMember, roleID); e != nil {
		t.Fatal(e)
	}
	if status, _ := c.call(delegate, "GET", path, nil, nil); status != 403 {
		t.Fatalf("delegate without authority read %d", status)
	}
	now := time.Now().UTC()
	c.service.Now = func() time.Time { return now }
	expiry := now.Add(time.Hour)
	grant := func(capability string) map[string]any {
		return map[string]any{"delegate_membership_id": delegateMember, "grant": map[string]any{"capability": capability, "scope_kind": "PROPERTY", "scope_id": c.property.ID, "effective_at": now, "expires_at": expiry, "inherit": true}, "reason": "Cover front office absence"}
	}
	// Guest-bearing reservation authority is never delegable.
	c.must(c.owner, "POST", "/delegations", grant("reservations:read"), 400)
	ratePath := "/rate-plans/" + c.rate.ID.String()
	if status, _ := c.call(delegate, "GET", ratePath, nil, nil); status != 403 {
		t.Fatalf("delegate read rate before delegation %d", status)
	}
	c.must(c.owner, "POST", "/delegations", grant("rates:read"), 201)
	c.must(delegate, "GET", ratePath, nil, 200)
	if status, _ := c.call(delegate, "GET", path, nil, nil); status != 403 {
		t.Fatalf("rate delegation exposed a reservation %d", status)
	}
	if status, _ := c.call(delegate, "POST", path+"/cancel", cancel, nil); status != 403 {
		t.Fatalf("rate delegation allowed a cancellation %d", status)
	}
	now = expiry.Add(time.Second)
	if status, _ := c.call(delegate, "GET", ratePath, nil, nil); status != 403 {
		t.Fatalf("expired delegation read %d", status)
	}
	after := commercialData[reservations.Detail](t, c.must(c.owner, "GET", path, nil, 200))
	if after.Reservation.Version != created.Reservation.Version || after.Reservation.State != created.Reservation.State {
		t.Fatal("an unauthorised principal changed the reservation")
	}
}
