package integration

import (
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"sync"
	"testing"
	"time"
)

func overbookingPolicyFixture(c *commercialHarness) businesshttp.ReservationOverbookingPolicy {
	in := businesshttp.ReservationOverbookingPolicyCommand{PropertyID: c.property.ID, PoolID: c.pool.ID, State: "ACTIVE", Definition: reservations.OverbookingPolicy{MaxQuantity: 1, MaxCapacityBPS: 10000, AllowedSources: []string{"DIRECT"}, EffectiveAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(20 * time.Minute), PolicyReference: "explicit-disposable-overbooking-policy:v1"}, Reason: "Independent review of one excess-demand obligation without increasing capacity"}
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_RESERVATION_OVERBOOKING", "reservations:approve", "", nil, "", map[string]any{"action": "POLICY", "command": op})
	return commercialData[businesshttp.ReservationOverbookingPolicy](c.t, c.must(c.owner, "POST", "/reservation-overbooking-policies", in, 201))
}
func overbookingInquiryFixture(c *commercialHarness, p businesshttp.ReservationOverbookingPolicy) reservations.Detail {
	q := commercialData[revenue.QuoteSnapshot](c.t, c.must(c.owner, "POST", "/reservation-overbooking-quotes", businesshttp.ReservationOverbookingQuote{PolicyID: p.ID, PolicyVersion: p.Version, Quote: businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}}, 201))
	in := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", State: "INQUIRY", PaymentResponsibility: "PAY_AT_PROPERTY", Rooms: []reservations.RoomCommand{{ProductID: q.ProductID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}, Reason: "Retain a priced inquiry while physical stock is fully occupied"}
	op := in
	in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	return commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations", in, 201))
}
func overbookingCommand(c *commercialHarness, p businesshttp.ReservationOverbookingPolicy, v reservations.Reservation, action string) businesshttp.ReservationOverbookingCommand {
	in := businesshttp.ReservationOverbookingCommand{Version: v.Version, PolicyID: p.ID, PolicyVersion: p.Version, RecoveryReference: "tracked-operator-recovery-reference", Reason: "Independent review of frozen booking terms and exact excess-demand capacity"}
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_RESERVATION_OVERBOOKING", "reservations:approve", "", &v.AmountMinor, v.Currency, map[string]any{"action": action, "reservation_id": v.ID, "command": op})
	return in
}
func TestBusinessReservationOverbookingIsBoundedAndRequiresPhysicalReconciliation(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	base := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, true), 201))
	base = commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+base.Reservation.ID.String()+"/confirm", reservations.Command{Version: base.Reservation.Version, Reason: "Confirm the sole actual physical allocation"}, 200))
	policy := overbookingPolicyFixture(c)
	inquiry := overbookingInquiryFixture(c, policy)
	c.must(c.owner, "POST", "/reservations/"+inquiry.Reservation.ID.String()+"/confirm", reservations.Command{Version: inquiry.Reservation.Version, Reason: "Ordinary inquiry cannot confirm without a physical hold"}, 409)
	command := overbookingCommand(c, policy, inquiry.Reservation, "ACCEPT")
	accepted := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+inquiry.Reservation.ID.String()+"/overbook", command, 200))
	if e := reservations.RequirePhysicalBacking(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, accepted.Rooms[0].ID, c.dates); e == nil {
		t.Fatal("confirmed oversell authorised physical room assignment")
	}
	if accepted.Reservation.State != "CONFIRMED" || len(accepted.OutstandingOverbooking) != 2 {
		t.Fatalf("exception lost its actual excess-demand provenance: %+v", accepted)
	}
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Capacity != 1 || day.Committed != 2 || day.Overbooked != 1 || day.Available != 0 || day.PhysicalAvailable != 0 {
			t.Fatalf("invented physical stock or hid excess demand %+v", day)
		}
	}
	c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}, 409)
	second := overbookingInquiryFixture(c, policy)
	c.must(c.owner, "POST", "/reservations/"+second.Reservation.ID.String()+"/overbook", overbookingCommand(c, policy, second.Reservation, "ACCEPT"), 409)
	reconcile := overbookingCommand(c, policy, accepted.Reservation, "RECONCILE")
	c.must(c.owner, "POST", "/reservations/"+accepted.Reservation.ID.String()+"/overbooking/reconcile", reconcile, 409)
	c.must(c.owner, "POST", "/reservations/"+base.Reservation.ID.String()+"/cancel", reservations.Command{Version: base.Reservation.Version, Reason: "Release the original physical allocation before restoring backing"}, 200)
	backed := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+accepted.Reservation.ID.String()+"/overbooking/reconcile", reconcile, 200))
	if e := reservations.RequirePhysicalBacking(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, backed.Rooms[0].ID, c.dates); e != nil {
		t.Fatal(e)
	}
	if len(backed.OutstandingOverbooking) != 0 {
		t.Fatal("physical conversion left outstanding excess demand")
	}
	days, e = inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Capacity != 1 || day.Committed != 1 || day.Overbooked != 0 || day.Available != 0 {
			t.Fatalf("conversion duplicated or lost inventory %+v", day)
		}
	}
	c.must(c.owner, "POST", "/reservations/"+backed.Reservation.ID.String()+"/cancel", reservations.Command{Version: backed.Reservation.Version, Reason: "Cancel the now physically backed reservation"}, 200)
	var committed, overbooked int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT sum(committed),sum(overbooked) FROM inventory_calendar WHERE pool_id=$1`, c.pool.ID).Scan(&committed, &overbooked); e != nil || committed != 0 || overbooked != 0 {
		t.Fatalf("inventory did not conserve through cancellation %d/%d %v", committed, overbooked, e)
	}
	if _, e = c.db.Pool.Exec(t.Context(), `DELETE FROM reservation_overbooking_releases`); e == nil {
		t.Fatal("exception history mutable")
	}
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE inventory_calendar SET committed=1,overbooked=1 WHERE pool_id=$1`, c.pool.ID); e == nil {
		t.Fatal("manufactured unapproved excess demand")
	}
	var refs int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_conflicts WHERE reservation_id=$1 AND classification='APPROVED_OVERBOOKING_RECOVERY' AND state='OPEN'`, accepted.Reservation.ID).Scan(&refs); e != nil || refs != 1 {
		t.Fatalf("physical conversion falsely completed customer recovery %d %v", refs, e)
	}
}

func TestBusinessReservationConcurrentOverbookingCannotExceedApprovedQuota(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	base := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, true), 201))
	c.must(c.owner, "POST", "/reservations/"+base.Reservation.ID.String()+"/confirm", reservations.Command{Version: base.Reservation.Version, Reason: "Fully consume the existing physical pool"}, 200)
	policy := overbookingPolicyFixture(c)
	a, b := overbookingInquiryFixture(c, policy), overbookingInquiryFixture(c, policy)
	first, second := overbookingCommand(c, policy, a.Reservation, "ACCEPT"), overbookingCommand(c, policy, b.Reservation, "ACCEPT")
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE business_sessions SET mfa_at=now()-interval '10 minutes' WHERE id=$1`, c.owner.tokens.SessionID); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations/"+a.Reservation.ID.String()+"/overbook", first, 403)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE business_sessions SET mfa_at=now() WHERE id=$1`, c.owner.tokens.SessionID); e != nil {
		t.Fatal(e)
	}
	forged := first
	forged.ApprovalID = second.ApprovalID
	c.must(c.owner, "POST", "/reservations/"+a.Reservation.ID.String()+"/overbook", forged, 403)
	type outcome struct {
		id     string
		status int
		raw    []byte
	}
	out := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, item := range []struct {
		v       reservations.Reservation
		command businesshttp.ReservationOverbookingCommand
	}{{a.Reservation, first}, {b.Reservation, second}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, raw := c.call(c.owner, "POST", "/reservations/"+item.v.ID.String()+"/overbook", item.command, nil)
			out <- outcome{id: item.v.ID.String(), status: status, raw: raw}
		}()
	}
	wg.Wait()
	close(out)
	accepted, conflict := 0, 0
	var successful reservations.Detail
	var pending reservations.Reservation
	for result := range out {
		switch result.status {
		case 200:
			accepted++
			successful = commercialData[reservations.Detail](t, result.raw)
		case 409:
			conflict++
			if result.id == a.Reservation.ID.String() {
				pending = a.Reservation
			} else {
				pending = b.Reservation
			}
		default:
			t.Fatalf("overbooking response %d %s", result.status, result.raw)
		}
	}
	if accepted != 1 || conflict != 1 {
		t.Fatalf("quota accepted %d rejected %d", accepted, conflict)
	}
	var permits, exceptions int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM reservation_overbooking_permits),(SELECT count(*) FROM reservation_conflicts WHERE classification='APPROVED_OVERBOOKING_RECOVERY')`).Scan(&permits, &exceptions); e != nil || permits != 1 || exceptions != 1 {
		t.Fatalf("failed overbooking leaked records %d/%d %v", permits, exceptions, e)
	}
	c.must(c.owner, "POST", "/reservations/"+successful.Reservation.ID.String()+"/cancel", reservations.Command{Version: successful.Reservation.Version, Reason: "Cancel an unbacked obligation without retaining excess demand"}, 200)
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Capacity != 1 || day.Committed != 1 || day.Overbooked != 0 {
			t.Fatalf("unbacked cancellation lost original backing %+v", day)
		}
	}
	expiredNow := policy.Definition.ExpiresAt.Add(time.Second).Truncate(time.Microsecond)
	c.service.Now = func() time.Time { return expiredNow }
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE business_sessions SET mfa_at=$2 WHERE id=$1`, c.owner.tokens.SessionID, expiredNow); e != nil {
		t.Fatal(e)
	}
	command := first
	if pending.ID == b.Reservation.ID {
		command = second
	}
	c.must(c.owner, "POST", "/reservations/"+pending.ID.String()+"/overbook", command, 409)
}

func TestBusinessReservationOverbookingPausesPublishedChannelsAfterOwningReadback(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	_, client := newCommercialSupplyConsumer(t)
	connection := c.connectMarketplace(client)
	listing, job := c.publishListing(connection)
	if e := c.domain.ProcessChannelSync(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if c.syncState(job) != "SUCCEEDED" {
		t.Fatal("signed provider publication did not complete")
	}
	base := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, true), 201))
	c.must(c.owner, "POST", "/reservations/"+base.Reservation.ID.String()+"/confirm", reservations.Command{Version: base.Reservation.Version, Reason: "Commit the existing physical room before considering excess demand"}, 200)
	policy := overbookingPolicyFixture(c)
	inquiry := overbookingInquiryFixture(c, policy)
	c.must(c.owner, "POST", "/reservations/"+inquiry.Reservation.ID.String()+"/overbook", overbookingCommand(c, policy, inquiry.Reservation, "ACCEPT"), 200)
	var desired, delivery string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT desired_state,delivery_state FROM listing_publication WHERE listing_id=$1`, listing.ID).Scan(&desired, &delivery); e != nil || desired != "PAUSED" || delivery != "UNKNOWN" {
		t.Fatalf("fabricated provider pause before owning readback %s/%s %v", desired, delivery, e)
	}
	if e := c.domain.ProcessChannelSync(t.Context(), 2); e != nil {
		t.Fatal(e)
	}
	var confirmed bool
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT desired_state='PAUSED' AND delivery_state='CONFIRMED' AND acknowledged_version=canonical_version AND readback_version=canonical_version FROM listing_publication WHERE listing_id=$1`, listing.ID).Scan(&confirmed); e != nil || !confirmed {
		t.Fatalf("signed safety pause did not reconcile %v %v", confirmed, e)
	}
}
