package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events/jetstream"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	testkit "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/testing"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBusinessCRMActualStayHistoryRebuildBrokerRecoveryAndFinancialExpiry(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	v, stay := pmsCollectedStay(c)
	projector := &crm.HistoryProjector{Pool: c.db.Pool, Outbox: c.domain.Auth.Service.Outbox, Now: c.domain.Auth.Service.Now}
	if e := projector.Tick(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	fact, e := crm.CurrentStayHistory(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, v.Reservation.ID, stay.Stay.GuestID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if fact.Completed || fact.ActualNights != nil || fact.SettledObligationMinor != nil {
		t.Fatalf("invented in-house outcome %+v", fact)
	}
	var version int64
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT version FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&version); e != nil {
		t.Fatal(e)
	}
	fabricated, e := events.New(t.Context(), platform.BusinessOperationsService, "reservation.completed.v1", "reservation", v.Reservation.ID, map[string]any{"organisation_id": c.owner.registered.OrganisationID, "version": version, "completed": true, "settled_obligation_minor": 99999999})
	if e != nil {
		t.Fatal(e)
	}
	if e = projector.Handle(t.Context(), events.Delivery{Envelope: fabricated}); e != nil {
		t.Fatal(e)
	}
	var stored crm.StayHistory
	var raw []byte
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT snapshot FROM stay_history_projection WHERE reservation_id=$1`, v.Reservation.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &stored); e != nil {
		t.Fatal(e)
	}
	if stored.Completed || stored.SettledObligationMinor != nil {
		t.Fatal("broker payload became completion truth")
	}
	forged := fabricated
	forged.EventID = ids.New()
	forged.Producer = string(platform.MarketplaceService)
	if e = projector.Handle(t.Context(), events.Delivery{Envelope: forged}); !events.IsPermanent(e) {
		t.Fatalf("forged producer accepted %v", e)
	}
	for _, payload := range []map[string]any{
		{"organisation_id": ids.New(), "version": version},
		{"organisation_id": c.owner.registered.OrganisationID, "version": version + 1000},
	} {
		invalid := fabricated
		invalid.EventID = ids.New()
		invalid.Payload, _ = json.Marshal(payload)
		if e = projector.Handle(t.Context(), events.Delivery{Envelope: invalid}); !events.IsPermanent(e) {
			t.Fatalf("forged tenant/future source accepted %v", e)
		}
	}
	broker := testkit.StartNATS(t)
	conn, e := jetstream.Connect(jetstream.Config{URL: broker.URL, Name: "crm-actual-stay-history", PublishTimeout: 100 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if e = conn.EnsureStream(ctx, businessEvents.StreamConfig("realestate")); e != nil {
		t.Fatal(e)
	}
	preRelay, e := outbox.NewWorker(outbox.NewStore(c.db.Pool, "realestate"), conn, outbox.WorkerConfig{Owner: ids.New().String(), BatchSize: 100, PublishTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	for range 10 {
		n, e := preRelay.RunOnce(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			break
		}
	}
	broker.Shutdown()
	pmsDeferredDeparture(c, v, stay)
	pmsReadSettled(c, v)
	folioPath := "/folios/" + stay.Folio.ID.String()
	c.must(c.owner, "POST", folioPath+"/reconcile", businesshttp.PMSVersionCommand{Version: 2, Reason: "Reconcile authoritative settlement before actual CRM completion"}, 200)
	c.must(c.owner, "POST", folioPath+"/close", businesshttp.PMSVersionCommand{Version: 3, Reason: "Finalize actual departure and settlement before guest history projection"}, 200)
	var completedAt *time.Time
	if e = c.db.Pool.QueryRow(ctx, `SELECT completed_at FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&completedAt); e != nil || completedAt == nil {
		t.Fatalf("missing recorded completion time %v", e)
	}
	relay, e := outbox.NewWorker(outbox.NewStore(c.db.Pool, "realestate"), conn, outbox.WorkerConfig{Owner: ids.New().String(), BatchSize: 100, PublishTimeout: 100 * time.Millisecond, BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = relay.RunOnce(ctx); e != nil {
		t.Fatal(e)
	}
	var pending int
	if e = c.db.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='reservation.completed.v1' AND published_at IS NULL AND attempts>0`).Scan(&pending); e != nil || pending != 1 {
		t.Fatalf("lost completed source %d %v", pending, e)
	}
	broker.Restart()
	testkit.Eventually(t, 5*time.Second, func() bool { return conn.Ready(ctx) == nil }, "CRM broker reconnect")
	quarantine := &workers.Notifications{Pool: c.db.Pool}
	consumer, e := conn.Consume(ctx, jetstream.ConsumerConfig{Stream: businessEvents.StreamConfig("realestate").Name, Durable: "crm-history-" + ids.New().String(), FilterSubjects: []string{"realestate.business.reservation.>", "realestate.business.hospitality.stay.>", "realestate.business.hospitality.folio.updated.v1"}, AckWait: time.Second, OnFailure: quarantine.Quarantine}, projector.Handle)
	if e != nil {
		t.Fatal(e)
	}
	defer consumer.Stop()
	poison, err := forged.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, e = conn.JetStream().Publish(ctx, "realestate.business.reservation.completed.v1", poison); e != nil {
		t.Fatal(e)
	}
	testkit.Eventually(t, 5*time.Second, func() bool {
		var found bool
		e := c.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM event_failures WHERE event_id=$1)`, forged.EventID.String()).Scan(&found)
		return e == nil && found
	}, "forged CRM source quarantined by the real consumer")

	testkit.Eventually(t, 10*time.Second, func() bool {
		if _, e := relay.RunOnce(ctx); e != nil {
			return false
		}
		var done bool
		e := c.db.Pool.QueryRow(ctx, `SELECT (snapshot->>'completed')::boolean FROM stay_history_projection WHERE reservation_id=$1`, v.Reservation.ID).Scan(&done)
		if e != nil || !done {
			return false
		}
		var consumed bool
		e = c.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM processed_events p JOIN outbox_events o ON o.event_id=p.event_id WHERE p.consumer=$1 AND o.event_type='reservation.completed.v1')`, crm.HistoryConsumer).Scan(&consumed)
		return e == nil && consumed
	}, "actual completed guest history after broker recovery")
	path := "/guests/" + stay.Stay.GuestID.String() + "/stay-history"
	view := commercialData[businesshttp.CRMStayHistoryList](t, c.must(c.owner, "GET", path, nil, 200))
	if len(view.Items) != 1 || !view.Items[0].History.Completed || view.Items[0].History.ActualNights == nil || view.Items[0].History.SettledObligationMinor == nil || *view.Items[0].History.SettledObligationMinor != v.Reservation.AmountMinor {
		t.Fatalf("actual history %+v", view)
	}
	// Rebuild and event replay preserve each current source version exactly once.
	var before, after int
	if e = c.db.Pool.QueryRow(ctx, `SELECT count(*) FROM guest_stay_history`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", path+"/rebuild", businesshttp.CRMStayHistoryRebuild{GuestVersion: 1, Reason: "Rebuild owned history without manufacturing visits or duplicate snapshots"}, 200)
	var event events.Envelope
	if e = c.db.Pool.QueryRow(ctx, `SELECT event_id,event_type,producer,aggregate_type,aggregate_id,occurred_at,payload FROM outbox_events WHERE event_type='reservation.completed.v1'`).Scan(&event.EventID, &event.EventType, &event.Producer, &event.AggregateType, &event.AggregateID, &event.OccurredAt, &event.Payload); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = projector.Handle(ctx, events.Delivery{Envelope: event}); e != nil {
			t.Fatal(e)
		}
	}
	if e = c.db.Pool.QueryRow(ctx, `SELECT count(*) FROM guest_stay_history`).Scan(&after); e != nil || before != after {
		t.Fatalf("duplicate projection effect %d/%d %v", before, after, e)
	}
	if _, e = c.db.Pool.Exec(ctx, `UPDATE guest_stay_history SET source_digest=repeat('a',64)`); e == nil {
		t.Fatal("immutable stay evidence mutated")
	}
	outsider := c.user()
	c.must(outsider, "GET", path, nil, 404)
	restrictedReader := c.user()
	c.secure(restrictedReader)
	c.join(restrictedReader, c.owner, []string{"guests:read", "reservations:read"})
	c.must(restrictedReader, "GET", path, nil, 403)
	c.domain.Auth.Service.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	expired := commercialData[businesshttp.CRMStayHistoryList](t, c.must(c.owner, "GET", path, nil, 200))
	if len(expired.Items) != 1 || !expired.Items[0].History.Completed || expired.Items[0].History.SettledObligationMinor != nil || expired.Items[0].ProjectionCurrent {
		t.Fatalf("stale spend exposed %+v", expired)
	}
	// A later authenticated reversal removes monetary eligibility, retaining the
	// actual physical visit and immutable earlier observations.
	provider := reservationPaymentProvider(c)
	provider.mu.Lock()
	provider.reversed = 1
	provider.mu.Unlock()
	pmsReadSettled(c, v)
	if _, e = c.db.Pool.Exec(ctx, `DELETE FROM crm_history_cursors`); e != nil {
		t.Fatal(e)
	}
	projector.Now = c.domain.Auth.Service.Now
	if e = projector.Tick(ctx, 50); e != nil {
		t.Fatal(e)
	}
	reversed := commercialData[businesshttp.CRMStayHistoryList](t, c.must(c.owner, "GET", path, nil, 200))
	if reversed.Items[0].History.SettledObligationMinor != nil || !reversed.Items[0].History.Completed {
		t.Fatal("financial reversal erased physical truth or retained spend")
	}
}

func TestBusinessCRMCancelledReservationHistoryDoesNotInventPhysicalStay(t *testing.T) {
	c := newCommercialHTTP(t)
	v, g := pmsConfirmed(c)
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/cancel", reservations.Command{Version: v.Reservation.Version, Reason: "Cancel an actual prearrival reservation without inventing physical guest nights"}, 200)
	p := &crm.HistoryProjector{Pool: c.db.Pool, Outbox: c.domain.Auth.Service.Outbox, Now: c.domain.Auth.Service.Now}
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- p.Tick(t.Context(), 50) }()
	}
	for range 2 {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	list := commercialData[businesshttp.CRMStayHistoryList](t, c.must(c.owner, "GET", "/guests/"+g.Guest.ID.String()+"/stay-history", nil, 200))
	if len(list.Items) != 1 || list.Items[0].History.ReservationState != "CANCELLED" || len(list.Items[0].History.Stays) != 0 || list.Items[0].History.Completed || list.Items[0].History.ActualNights != nil || list.Items[0].History.SettledObligationMinor != nil {
		t.Fatalf("fabricated cancelled stay %+v", list)
	}
	var count int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM guest_stay_history`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("concurrent projection duplicated %d %v", count, e)
	}
}

func TestBusinessCRMSourceFailureBackoffPreservesOtherGuestsAndRecovers(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	pmsTwoRoomStock(c)
	failed, stay := pmsCollectedStay(c)
	guest := crmGuestFixture(c)
	in := reservationCommandFixture(c, false)
	in.State = "INQUIRY"
	in.SourceReference = "source-review-control:" + ids.New().String()
	in.Guests = []reservations.GuestCommand{{GuestID: &guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, IsPrimary: true}}
	control := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	// Exercise a real least-privilege deployment error without modifying owned
	// canonical facts or disabling guards: this role cannot read folio items.
	role := "cf_crm_source_" + strings.ReplaceAll(ids.New().String(), "-", "_")
	quoted := pgx.Identifier{role}.Sanitize()
	if _, e := c.db.Pool.Exec(t.Context(), "CREATE ROLE "+quoted+" NOLOGIN"); e != nil {
		t.Fatal(e)
	}
	var restricted *pgxpool.Pool
	t.Cleanup(func() {
		if restricted != nil {
			restricted.Close()
		}
		if _, e := c.db.Pool.Exec(context.Background(), "DROP OWNED BY "+quoted); e != nil {
			t.Errorf("test-role grant cleanup: %v", e)
		}
		if _, e := c.db.Pool.Exec(context.Background(), "DROP ROLE "+quoted); e != nil {
			t.Errorf("test-role cleanup: %v", e)
		}
	})
	for _, sql := range []string{"GRANT USAGE ON SCHEMA public TO " + quoted, "GRANT SELECT ON ALL TABLES IN SCHEMA public TO " + quoted, "REVOKE SELECT ON folio_items FROM " + quoted, "GRANT INSERT,UPDATE ON stay_history_projection,crm_history_cursors,outbox_events TO " + quoted, "GRANT INSERT ON guest_stay_history,commercial_history,audit_events TO " + quoted} {
		if _, e := c.db.Pool.Exec(t.Context(), sql); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := c.db.Pool.Exec(t.Context(), "GRANT UPDATE ON organisations TO "+quoted); e != nil {
		t.Fatal(e)
	}
	config := c.db.Pool.Config()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error { _, e := conn.Exec(ctx, "SET ROLE "+quoted); return e }
	var e error
	restricted, e = pgxpool.NewWithConfig(t.Context(), config)
	if e != nil {
		t.Fatal(e)
	}
	projector := &crm.HistoryProjector{Pool: restricted, Outbox: c.domain.Auth.Service.Outbox, Now: c.domain.Auth.Service.Now}
	if e = projector.Tick(t.Context(), 50); e == nil {
		t.Fatal("missing source permission was silently ignored")
	}
	if !strings.Contains(e.Error(), "folio_items") {
		t.Fatalf("unexpected failure before source review: %v", e)
	}
	var failureCount, partial, successful int
	var code *string
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT failure_count,last_error_code FROM crm_history_cursors WHERE reservation_id=$1`, failed.Reservation.ID).Scan(&failureCount, &code); e != nil || failureCount != 1 || code == nil {
		t.Fatalf("missing durable source review: %d %v", failureCount, e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM guest_stay_history h JOIN stay_history_projection p ON p.id=h.projection_id WHERE p.reservation_id=$1`, failed.Reservation.ID).Scan(&partial); e != nil || partial != 0 {
		t.Fatalf("partial source writes leaked: %d %v", partial, e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM stay_history_projection WHERE reservation_id=$1`, control.Reservation.ID).Scan(&successful); e != nil || successful != 1 {
		t.Fatalf("unrelated guest starved: %d %v", successful, e)
	}
	if e = projector.Tick(t.Context(), 50); e != nil {
		t.Fatalf("backoff was ignored: %v", e)
	}
	pending := commercialData[businesshttp.CRMStayHistoryList](t, c.must(c.owner, "GET", "/guests/"+stay.Stay.GuestID.String()+"/stay-history", nil, 200))
	if pending.PendingProjectionCount != 1 || pending.SourceReviewCount != 1 {
		t.Fatalf("source-review lag hidden %+v", pending)
	}
	// Repeated real source failures enter durable quarantine; the bad source
	// never writes partial history or consumes another guest's retry budget.
	for range 11 {
		if _, e = c.db.Pool.Exec(t.Context(), `UPDATE crm_history_cursors SET next_check_at=now()-interval '1 second' WHERE reservation_id=$1`, failed.Reservation.ID); e != nil {
			t.Fatal(e)
		}
		if e = projector.Tick(t.Context(), 50); e == nil {
			t.Fatal("unfixed source stopped reporting its failure")
		}
	}
	var deferred bool
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT failure_count,last_error_code,next_check_at>now()+interval '59 minutes' FROM crm_history_cursors WHERE reservation_id=$1`, failed.Reservation.ID).Scan(&failureCount, &code, &deferred); e != nil || failureCount != 12 || code == nil || *code != "CRM_HISTORY_SOURCE_QUARANTINED" || !deferred {
		t.Fatalf("source quarantine missing: %d %v %v", failureCount, code, e)
	}
	projector.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if e = projector.Tick(t.Context(), 50); e != nil {
		t.Fatalf("quarantine was silently retried: %v", e)
	}
	if _, e = c.db.Pool.Exec(t.Context(), "GRANT SELECT ON folio_items TO "+quoted); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/guests/"+stay.Stay.GuestID.String()+"/stay-history/rebuild", businesshttp.CRMStayHistoryRebuild{GuestVersion: 1, Reason: "Rebuild verified canonical source after the operator repairs least-privilege permissions"}, 200)
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT failure_count,last_error_code FROM crm_history_cursors WHERE reservation_id=$1`, failed.Reservation.ID).Scan(&failureCount, &code); e != nil || failureCount != 0 || code != nil {
		t.Fatalf("source recovery was not cleared: %d %v", failureCount, e)
	}
}
