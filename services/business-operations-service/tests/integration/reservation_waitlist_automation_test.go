package integration

import (
	"errors"
	"sync"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func waitlistAutomationPolicy(c *commercialHarness, mode string) businesshttp.WaitlistAutomationPolicy {
	c.t.Helper()
	in := businesshttp.WaitlistAutomationPolicyCommand{PropertyID: c.property.ID, Mode: mode, OfferSeconds: 300, MaxOffersPerEntry: 3, MaxOffersPerRun: 5, EffectiveAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour), Reason: "Independently reviewed automatic waitlist offers"}
	if mode == "AUTOMATIC_OFFERS" {
		c.must(c.owner, "POST", "/waitlist-automation-policies", in, 403)
		op := in
		in.ApprovalID = c.approval("COMMERCIAL_RESERVATION_POLICY_CHANGE", "reservations:approve", "", nil, "", map[string]any{"waitlist_automation": op})
	}
	return commercialData[businesshttp.WaitlistAutomationPolicy](c.t, c.must(c.owner, "POST", "/waitlist-automation-policies", in, 201))
}

func waitlistEntry(c *commercialHarness, priority int, guest string) businesshttp.ReservationWaitlist {
	c.t.Helper()
	in := businesshttp.ReservationWaitlist{PropertyID: c.property.ID, ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1, Priority: priority, GuestReference: guest, ExpiresAt: time.Now().UTC().Add(48 * time.Hour), Reason: "Guest asked to wait for released stock"}
	return commercialData[businesshttp.ReservationWaitlist](c.t, c.must(c.owner, "POST", "/waitlist", in, 201))
}

// waitlistFullHouse sells the single unit so later entries must wait.
func waitlistFullHouse(c *commercialHarness) reservations.Detail {
	c.t.Helper()
	reservationPolicyFixture(c, false)
	in := reservationCommandFixture(c, true)
	created := commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations", in, 201))
	return commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: created.Reservation.Version, Reason: "Confirm the last available unit"}, 200))
}

type liveOffer struct {
	ID, EntryID, HoldID, QuoteID ids.ID
	Version                      int64
}

func liveOffers(c *commercialHarness) []liveOffer {
	c.t.Helper()
	rows, e := c.db.Pool.Query(c.t.Context(), `SELECT o.id,o.entry_id,o.hold_id,o.quote_id,o.version FROM waitlist_offers o JOIN inventory_holds h ON h.id=o.hold_id AND h.state='ACTIVE' WHERE o.organisation_id=$1 AND o.state='OFFERED' ORDER BY o.created_at`, c.owner.registered.OrganisationID)
	if e != nil {
		c.t.Fatal(e)
	}
	defer rows.Close()
	out := []liveOffer{}
	for rows.Next() {
		var o liveOffer
		if e = rows.Scan(&o.ID, &o.EntryID, &o.HoldID, &o.QuoteID, &o.Version); e != nil {
			c.t.Fatal(e)
		}
		out = append(out, o)
	}
	return out
}

func waitlistRunStates(c *commercialHarness) []string {
	c.t.Helper()
	list := commercialData[businesshttp.WaitlistAutomationRunList](c.t, c.must(c.owner, "GET", "/waitlist-automation-runs?property_id="+c.property.ID.String(), nil, 200))
	out := []string{}
	for _, r := range list.Items {
		out = append(out, r.Trigger+":"+r.State)
	}
	return out
}

func TestBusinessWaitlistAutomationReleaseOfferExpiryFairnessAndAcceptance(t *testing.T) {
	c := newCommercialHTTP(t)
	sold := waitlistFullHouse(c)
	low := waitlistEntry(c, 10, "waitlist-guest-low:"+ids.New().String())
	first := waitlistEntry(c, 50, "waitlist-guest-first:"+ids.New().String())
	second := waitlistEntry(c, 50, "waitlist-guest-second:"+ids.New().String())
	outsider := c.user()
	c.must(outsider, "POST", "/waitlist-automation-policies", businesshttp.WaitlistAutomationPolicyCommand{PropertyID: c.property.ID, Mode: "MANUAL", OfferSeconds: 300, MaxOffersPerEntry: 3, MaxOffersPerRun: 5, EffectiveAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour), Reason: "Outsider cannot configure automation"}, 404)
	policy := waitlistAutomationPolicy(c, "AUTOMATIC_OFFERS")
	if policy.Version != 1 || policy.ApprovalID == nil {
		t.Fatalf("policy %+v", policy)
	}
	// Full house: the worker evaluates waiting entries but offers nothing.
	if e := c.domain.ProcessWaitlistAutomation(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	if len(liveOffers(c)) != 0 {
		t.Fatal("offer created without released stock")
	}
	if states := waitlistRunStates(c); len(states) != 1 || states[0] != "NEW_ENTRY:NO_ELIGIBLE_CANDIDATE" {
		t.Fatalf("full-house run %v", states)
	}
	// Cancellation releases stock; the database records a durable signal.
	c.must(c.owner, "POST", "/reservations/"+sold.Reservation.ID.String()+"/cancel", reservations.Command{Version: sold.Reservation.Version, Reason: "Guest cancelled and released the unit"}, 200)
	var signals int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_release_signals WHERE organisation_id=$1 AND processed_at IS NULL AND source='CALENDAR_RELEASE'`, c.owner.registered.OrganisationID).Scan(&signals); e != nil || signals != 2 {
		t.Fatalf("release signals %d %v", signals, e)
	}
	// Concurrent workers cannot offer the last unit twice.
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Go(func() { errs <- c.domain.ProcessWaitlistAutomation(t.Context(), 50) })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	offers := liveOffers(c)
	if len(offers) != 1 || offers[0].EntryID != first.ID {
		t.Fatalf("priority/first-come offer %+v (first=%s)", offers, first.ID)
	}
	var available, pending int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM inventory_release_signals WHERE organisation_id=$1 AND processed_at IS NULL),(SELECT count(*) FROM waitlist_automation_offers WHERE organisation_id=$1)`, c.owner.registered.OrganisationID).Scan(&pending, &available); e != nil || pending != 0 || available != 1 {
		t.Fatalf("signals pending=%d automatic offers=%d %v", pending, available, e)
	}
	// A further run with nothing released offers nothing more.
	if e := c.domain.ProcessWaitlistAutomation(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	if len(liveOffers(c)) != 1 {
		t.Fatal("duplicate offer for the same released unit")
	}
	// Offer expiry releases the hold and advances to the next eligible guest:
	// same priority, fewer prior automatic offers wins.
	original := c.service.Now
	c.service.Now = func() time.Time { return original().Add(6 * time.Minute) }
	e := c.domain.ProcessWaitlistAutomation(t.Context(), 50)
	c.service.Now = original
	if e != nil {
		t.Fatal(e)
	}
	offers = liveOffers(c)
	if len(offers) != 1 || offers[0].EntryID != second.ID {
		t.Fatalf("next candidate after expiry %+v (second=%s first=%s low=%s)", offers, second.ID, first.ID, low.ID)
	}
	var firstState, expiredOffer string
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT state FROM waitlist_entries WHERE id=$1),(SELECT state FROM waitlist_offers WHERE entry_id=$1 ORDER BY created_at LIMIT 1)`, first.ID).Scan(&firstState, &expiredOffer); e != nil || firstState != "WAITING" || expiredOffer != "EXPIRED" {
		t.Fatalf("expired offer lifecycle %s %s %v", firstState, expiredOffer, e)
	}
	// Guest acceptance (not notification) creates the confirmed reservation.
	offer := offers[0]
	var entryVersion int64
	var quoteAmount int64
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT version FROM waitlist_entries WHERE id=$1),(SELECT amount_minor FROM price_quotes WHERE id=$2)`, second.ID, offer.QuoteID).Scan(&entryVersion, &quoteAmount); e != nil {
		t.Fatal(e)
	}
	booking := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", SourceReference: "waitlist-conversion:" + ids.New().String(), State: "CONFIRMED", PaymentResponsibility: "PAY_AT_PROPERTY", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: offer.QuoteID, HoldID: &offer.HoldID, HoldVersion: 1, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Quantity: 1}}, Guests: []reservations.GuestCommand{{SourceKind: "DIRECT", SourceReference: second.GuestReference, IsPrimary: true}}, Reason: "Guest accepted the automatic waitlist offer"}
	op := booking
	booking.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &quoteAmount, "NGN", map[string]any{"command": op})
	accepted := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/waitlist/"+second.ID.String()+"/accept", businesshttp.ReservationWaitlistAccept{Version: entryVersion, OfferID: offer.ID, OfferVersion: offer.Version, Booking: booking}, 201))
	if accepted.Reservation.State != "CONFIRMED" {
		t.Fatalf("waitlist conversion state %s", accepted.Reservation.State)
	}
	var entryState, offerState string
	var reservation ids.ID
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT e.state,o.state,o.reservation_id FROM waitlist_entries e JOIN waitlist_offers o ON o.entry_id=e.id AND o.id=$2 WHERE e.id=$1`, second.ID, offer.ID).Scan(&entryState, &offerState, &reservation); e != nil || entryState != "ACCEPTED" || offerState != "ACCEPTED" || reservation != accepted.Reservation.ID {
		t.Fatalf("acceptance %s %s %v %v", entryState, offerState, reservation, e)
	}
	assertStockConservationPool(c, 1)
	states := waitlistRunStates(c)
	if len(states) < 3 || states[0] != "RELEASE_SIGNAL:OFFERED" {
		t.Fatalf("run history %v", states)
	}
	var events int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE event_type='reservation.waitlist.changed.v1' AND payload->'snapshot'->>'automatic'='true'`).Scan(&events); e != nil || events != 2 {
		t.Fatalf("automatic offer events %d %v", events, e)
	}
}

func assertStockConservationPool(c *commercialHarness, committed int) {
	c.t.Helper()
	var bad int
	if e := c.db.Pool.QueryRow(c.t.Context(), `SELECT count(*) FROM inventory_calendar c WHERE c.organisation_id=$1 AND c.pool_id=$2 AND c.day>=$3::date AND c.day<$4::date AND (c.committed<>$5 OR c.committed<>(SELECT coalesce(sum(d.quantity),0) FROM reservation_inventory_days d WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND d.released_at IS NULL))`, c.owner.registered.OrganisationID, c.pool.ID, c.dates.DateFrom, c.dates.DateTo, committed).Scan(&bad); e != nil || bad != 0 {
		c.t.Fatalf("stock conservation %d %v", bad, e)
	}
}

func TestBusinessWaitlistAutomationRestartRecoveryFencingAndPause(t *testing.T) {
	c := newCommercialHTTP(t)
	sold := waitlistFullHouse(c)
	entry := waitlistEntry(c, 0, "waitlist-restart-guest:"+ids.New().String())
	waitlistAutomationPolicy(c, "AUTOMATIC_OFFERS")
	c.must(c.owner, "POST", "/reservations/"+sold.Reservation.ID.String()+"/cancel", reservations.Command{Version: sold.Reservation.Version, Reason: "Guest cancelled and released the unit"}, 200)
	org := c.owner.registered.OrganisationID
	// A worker claims the property lease and crashes before running.
	generation, claimed, e := c.domain.ClaimWaitlistAutomationLease(t.Context(), org, c.property.ID, "crashed-worker", 30*time.Second)
	if e != nil || !claimed || generation != 1 {
		t.Fatalf("claim %d %v %v", generation, claimed, e)
	}
	if _, again, _ := c.domain.ClaimWaitlistAutomationLease(t.Context(), org, c.property.ID, "duplicate-worker", 30*time.Second); again {
		t.Fatal("live lease claimed twice")
	}
	if e = c.domain.ProcessWaitlistAutomation(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	if len(liveOffers(c)) != 0 {
		t.Fatal("worker ignored a live lease")
	}
	// The lease expires; a restarted worker reclaims it with a new generation.
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE waitlist_automation_leases SET lease_expires_at=now()-interval '1 second' WHERE organisation_id=$1 AND property_id=$2`, org, c.property.ID); e != nil {
		t.Fatal(e)
	}
	if e = c.domain.ProcessWaitlistAutomation(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	offers := liveOffers(c)
	if len(offers) != 1 || offers[0].EntryID != entry.ID {
		t.Fatalf("recovered offer %+v", offers)
	}
	// The crashed worker resumes with its stale generation and is fenced.
	if _, e = c.domain.RunWaitlistAutomation(t.Context(), org, c.property.ID, "crashed-worker", generation); !errors.Is(e, businesshttp.ErrWaitlistLeaseLost) {
		t.Fatalf("stale worker not fenced: %v", e)
	}
	if len(liveOffers(c)) != 1 {
		t.Fatal("stale worker created a duplicate offer")
	}
	var gen int64
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT lease_generation FROM waitlist_automation_leases WHERE organisation_id=$1 AND property_id=$2`, org, c.property.ID).Scan(&gen); e != nil || gen != 2 {
		t.Fatalf("lease generation %d %v", gen, e)
	}
	if _, e = c.db.Pool.Exec(t.Context(), `DELETE FROM inventory_release_signals WHERE organisation_id=$1`, org); e == nil {
		t.Fatal("release signal evidence deletable")
	}
	// Pause: a MANUAL version needs no approval and stops automation at once.
	paused := waitlistAutomationPolicy(c, "MANUAL")
	if paused.Version != 2 || paused.ApprovalID != nil {
		t.Fatalf("pause %+v", paused)
	}
	another := waitlistEntry(c, 90, "waitlist-paused-guest:"+ids.New().String())
	triggered := commercialData[businesshttp.WaitlistAutomationTriggered](t, c.must(c.owner, "POST", "/waitlist-automation-runs", businesshttp.WaitlistAutomationTrigger{PropertyID: c.property.ID, Reason: "Operator requests an evaluation while paused"}, 202))
	if triggered.PolicyMode != "MANUAL" || triggered.Executed {
		t.Fatalf("trigger %+v", triggered)
	}
	// Free the unit again by declining the live offer; nothing is re-offered.
	c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/cancel", reservations.Command{Version: 2, Reason: "Guest no longer needs the room"}, 200)
	if e = c.domain.ProcessWaitlistAutomation(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	var offered int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM waitlist_offers WHERE entry_id=$1`, another.ID).Scan(&offered); e != nil || offered != 0 {
		t.Fatalf("paused automation offered %d %v", offered, e)
	}
}
