package integration

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/crm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Journey 1 — hotel operations: property, canonical inventory and rate,
// guest, reservation, Payment collection, room assignment and check-in (the
// fixture, all through the public API), then an in-house extension and an
// ancillary charge, each collected through the owning Payment contract, folio
// reconciliation, checkout to a closed folio and a clean night audit.
func TestBusinessJourneyHotelOperationsExtensionChargesCheckoutNightAudit(t *testing.T) {
	x := newPMSAmendFixture(t, true, 1)
	c := x.c
	ext, q := x.extension(0, 1, "RESERVATION_TERMS")
	extension := commercialData[businesshttp.PMSStayExtensionResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", ext, 201))
	c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions/"+extension.ExtensionID.String()+"/payment-requests", businesshttp.PMSStayExtensionCollection{PaymentPolicyID: x.pay.policy.ID, PaymentPolicyVersion: x.pay.policy.Version, PaymentPolicyFamily: "HOTEL", PayerWalletID: ids.New(), ConsentReference: "explicit-extension-payer-consent", LegalPartyReference: "canonical-test-guest-party", Email: "disposable-guest@example.invalid", Schedule: "ONE_TIME", Reason: "Collect the accepted in-house extension share"}, 202)
	// Ancillary consumption is posted and assigned to the consenting guest.
	folio := &pmsCollectionFixture{c: c, pay: x.pay, guest: x.guests[0], v: x.v, folioID: x.folios[0], stayID: x.stays[0]}
	meals := folio.post("MEALS", "restaurant-order-journey-1", 4500)
	agreement := commercialData[pmsAgreementQueued](t, c.must(c.owner, "POST", "/folios/"+x.folios[0].String()+"/collection-agreements", folio.agreement("GUEST", hospitality.FolioCollectionClaim{ItemID: meals, AmountMinor: 4500}), 202))
	x.process()
	x.process()
	if x.pay.collectionCalls != 3 {
		t.Fatalf("collections: accommodation, extension, ancillary = %d", x.pay.collectionCalls)
	}
	group := extension.Payers[0].FinancialGroupID
	x.pay.settle(group)
	x.pay.settle(agreement.FinancialGroupID)
	x.readback(group)
	folio.readback(t, agreement.ID)
	x.reconcile(x.folios[0], 200)
	d := x.stay(0)
	if d.BalanceMinor != 0 || d.Folio.State != "SETTLED" {
		t.Fatalf("journey folio balance %d state %s (extension %d)", d.BalanceMinor, d.Folio.State, q.AmountMinor)
	}
	checkout := businesshttp.PMSCheckoutCommand{StayID: x.stays[0], Version: d.Stay.Version, ReservationVersion: x.resVersion(), FolioVersion: d.Folio.Version, Reason: "Record the guest's actual departure after full settlement"}
	out := commercialData[map[string]any](t, c.must(c.owner, "POST", "/reservations/"+x.v.Reservation.ID.String()+"/check-out", checkout, 200))
	if out["folio_state"] != "CLOSED" || out["state"] != "DEPARTED" {
		t.Fatalf("journey departure %+v", out)
	}
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, x.v.Reservation.ID).Scan(&state); e != nil || state != "CHECKED_OUT" {
		t.Fatalf("journey reservation %s %v", state, e)
	}
	audit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Validate the business day after the completed hotel journey"}, 201))
	for _, ex := range audit.Snapshot.Exceptions {
		if ex.HardBlock {
			t.Fatalf("hard night-audit exception after a settled journey %+v", audit.Snapshot.Exceptions)
		}
	}
	if audit.State != "READY" {
		t.Fatalf("night audit %s %+v", audit.State, audit.Snapshot.Checks)
	}
}

// Journey 2 — early departure: a confirmed, collected stay departs early;
// the frozen policy decides the credit, unused nights return to stock (seen
// by the RMS demand observation), a refund reference stays pending the
// owning Payment/Admin service, and CRM stay history records actual nights.
func TestBusinessJourneyEarlyDepartureStockRefundCRMAndRMS(t *testing.T) {
	x := newPMSAmendFixture(t, true, 1)
	c := x.c
	snapshot := func() businesshttp.RMSDemandSnapshot {
		t.Helper()
		return commercialData[businesshttp.RMSDemandSnapshot](t, c.must(c.owner, "POST", "/revenue/demand-snapshots", businesshttp.RMSDemandCommand{PropertyID: c.property.ID, PoolID: c.pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, HistoryDays: 56}, 201))
	}
	before := snapshot()
	for _, day := range before.Dataset.Days {
		if day.ConfirmedNights != 1 {
			t.Fatalf("pre-departure demand %+v", day)
		}
	}
	cmd, terms := x.earlyDeparture(0)
	out := commercialData[businesshttp.PMSEarlyDepartureResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/early-departure", cmd, 200))
	if out.FinancialState != "REFUND_PENDING_EXTERNAL" || out.RefundExecuted || len(out.RefundRequestIDs) != 1 {
		t.Fatalf("journey refund reference %+v", out)
	}
	after := snapshot()
	for _, day := range after.Dataset.Days {
		if day.ConfirmedNights != 0 || day.SellableNights != day.Capacity-day.Blocked {
			t.Fatalf("released nights invisible to RMS %+v", day)
		}
	}
	projector := &crm.HistoryProjector{Pool: c.db.Pool, Outbox: c.domain.Auth.Service.Outbox, Now: c.domain.Auth.Service.Now}
	if e := projector.Tick(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	fact, e := crm.CurrentStayHistory(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, x.v.Reservation.ID, x.guests[0].Guest.ID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if fact.ActualNights == nil || *fact.ActualNights != 0 || fact.Completed || fact.ReservationState != "CHECKED_OUT" {
		t.Fatalf("CRM history after early departure %+v", fact)
	}
	if n := x.count(`SELECT count(*) FROM stay_history_projection WHERE reservation_id=$1`, x.v.Reservation.ID); n != 1 {
		t.Fatal("CRM stay history projection missing")
	}
	if n := x.count(`SELECT count(*) FROM reservation_versions WHERE reservation_id=$1 AND action IN ('EARLY_DEPARTURE','CHECKED_OUT')`, x.v.Reservation.ID); n != 2 {
		t.Fatalf("original reservation history not preserved %d", n)
	}
	if n := x.count(`SELECT count(*) FROM outbox_events WHERE event_type IN ('hospitality.unit_nights.released.v1','hospitality.early_departure.recorded.v1','hospitality.stay.checked_out.v1')`); n != 3 {
		t.Fatalf("journey events %d", n)
	}
	if terms.Terms.CreditMinor <= 0 {
		t.Fatal("journey expected a contractual credit")
	}
}
