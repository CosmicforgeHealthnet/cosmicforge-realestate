package integration

import (
	"testing"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type pmsUnitReleaseDays struct {
	Days []struct {
		RoomID    ids.ID `json:"reservation_room_id"`
		Day       string `json:"day"`
		Accepted  int    `json:"accepted_quantity"`
		Remaining int    `json:"remaining_quantity"`
		Released  int    `json:"released_unit_nights"`
		Conserved bool   `json:"conserved"`
	} `json:"days"`
}

// Early departure applies the frozen notice window to the nights from the
// actual departure date, releases those unit nights to canonical stock,
// credits the guaranteed folio obligation and records the physical departure.
func TestBusinessPMSEarlyDepartureReleasesUnusedNightsAndCreditsFolio(t *testing.T) {
	x := newPMSAmendFixture(t, false, 1)
	c := x.c
	before := x.stay(0)
	cmd, terms := x.earlyDeparture(0)
	nights := amendDays(c.dates.DateFrom, c.dates.DateTo)
	if terms.Terms.UnusedNights != len(nights) || terms.Terms.UnusedMinor != before.BalanceMinor || !terms.Terms.PenaltyApplied || terms.Terms.PenaltyMinor != terms.Terms.UnusedMinor/2 || terms.Terms.CreditMinor != terms.Terms.UnusedMinor-terms.Terms.PenaltyMinor || terms.OutstandingAfterMinor != before.BalanceMinor-terms.Terms.CreditMinor || len(terms.Segments) != 1 || terms.Segments[0].Payers[0].Collection != "GUARANTEE" {
		t.Fatalf("frozen early departure terms %+v", terms)
	}
	// The preview never mutates canonical state.
	if committed, _ := x.stockState(nights[0]); committed != 1 || x.count(`SELECT count(*) FROM reservation_unit_releases WHERE reservation_id=$1`, x.v.Reservation.ID) != 0 {
		t.Fatal("preview changed stock")
	}
	path := "/stays/" + x.stays[0].String() + "/early-departure"
	unapproved := cmd
	unapproved.ApprovalID = ids.Nil
	c.must(c.owner, "POST", path, unapproved, 403)
	changed := cmd
	changed.ExpectedCreditMinor++
	c.must(c.owner, "POST", path, changed, 409)
	out := commercialData[businesshttp.PMSEarlyDepartureResult](t, c.must(c.owner, "POST", path, cmd, 200))
	if out.FinancialState != "FOLIO_CREDIT_ONLY" || out.RefundableMinor == nil || *out.RefundableMinor != 0 || len(out.RefundRequestIDs) != 0 || out.RefundExecuted || len(out.ReleasedNights) != len(nights) || out.Departure["state"] != "DEPARTED" || out.Departure["folio_state"] != "SETTLEMENT_PENDING" {
		t.Fatalf("early departure result %+v", out)
	}
	for _, day := range nights {
		committed, provenance := x.stockState(day)
		if committed != 0 || provenance != 0 {
			t.Fatalf("unused night %s still committed %d/%d", day, committed, provenance)
		}
	}
	releases := commercialData[pmsUnitReleaseDays](t, c.must(c.owner, "GET", "/reservations/"+x.v.Reservation.ID.String()+"/unit-releases", nil, 200))
	if len(releases.Days) != len(nights) {
		t.Fatalf("release conservation %+v", releases)
	}
	for _, d := range releases.Days {
		if d.Accepted != 1 || d.Remaining != 0 || d.Released != 1 || !d.Conserved {
			t.Fatalf("per-date conservation %+v", d)
		}
	}
	after := x.stay(0)
	if after.Stay.State != "DEPARTED" || after.BalanceMinor != terms.OutstandingAfterMinor || after.Folio.State != "SETTLEMENT_PENDING" {
		t.Fatalf("departed stay %+v balance %d", after.Stay, after.BalanceMinor)
	}
	var credit int64
	for _, item := range after.Items {
		if item.Kind == "ADJUSTMENT" {
			credit += item.AmountMinor
		}
	}
	if credit != -terms.Terms.CreditMinor {
		t.Fatalf("folio credit %d", credit)
	}
	var reservationState, roomState string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT r.state,s.state FROM reservations r JOIN room_status_current s ON s.room_id=$2 WHERE r.id=$1`, x.v.Reservation.ID, before.Assignment.RoomID).Scan(&reservationState, &roomState); e != nil || reservationState != "CHECKED_OUT" || roomState != "VACANT_DIRTY" {
		t.Fatalf("reservation %s room %s %v", reservationState, roomState, e)
	}
	if n := x.count(`SELECT count(*) FROM access_grant_references WHERE assignment_id=$1 AND operation='REVOKE'`, before.Assignment.ID); n != 1 {
		t.Fatal("early departure did not revoke access")
	}
	// The released nights are sellable again through the canonical engine.
	again := x.quote(c.dates.DateFrom, c.dates.DateTo, 1)
	c.must(c.owner, "POST", "/holds", inventory.HoldCommand{ProductID: c.product.ID, ProductVersion: x.productVersion(), PoolVersion: x.poolVersion(), QuoteID: again.QuoteID, DateFrom: again.DateFrom, DateTo: again.DateTo, Quantity: 1, DurationSeconds: 300}, 201)
	c.must(c.owner, "GET", path+"/preview", nil, 409)
	stored := commercialData[map[string]any](t, c.must(c.owner, "GET", path, nil, 200))
	if stored["financial_state"] != "FOLIO_CREDIT_ONLY" {
		t.Fatalf("stored early departure %+v", stored)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_unit_release_days SET quantity_after=quantity_before WHERE room_id=$1`, x.v.Rooms[0].ID); e == nil {
		t.Fatal("release ledger was mutable")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_inventory_days SET released_at=NULL WHERE room_id=$1`, x.v.Rooms[0].ID); e == nil {
		t.Fatal("released stock could be revived")
	}
}

// An extension collection that was queued but never read back is an unknown
// Payment outcome: early departure records READBACK_REQUIRED (no invented
// refund amount), and the stale collection never charges afterwards.
func TestBusinessPMSEarlyDepartureWithUnknownExtensionCollectionStaysPending(t *testing.T) {
	x := newPMSAmendFixture(t, true, 1)
	c := x.c
	cmd, _ := x.extension(0, 1, "RESERVATION_TERMS")
	ext := commercialData[businesshttp.PMSStayExtensionResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/extensions", cmd, 201))
	collect := businesshttp.PMSStayExtensionCollection{PaymentPolicyID: x.pay.policy.ID, PaymentPolicyVersion: x.pay.policy.Version, PaymentPolicyFamily: "HOTEL", PayerWalletID: ids.New(), ConsentReference: "explicit-extension-payer-consent", LegalPartyReference: "canonical-test-guest-party", Email: "disposable-guest@example.invalid", Schedule: "ONE_TIME", Reason: "Collect the accepted in-house extension share"}
	path := "/stays/" + x.stays[0].String() + "/extensions/" + ext.ExtensionID.String() + "/payment-requests"
	c.must(c.owner, "POST", path, collect, 202)
	departure, terms := x.earlyDeparture(0)
	if terms.Segments[1].Payers[0].Collection != "UNKNOWN" || terms.Segments[0].Payers[0].Collection != "COLLECTED" {
		t.Fatalf("collection classification %+v", terms.Segments)
	}
	out := commercialData[businesshttp.PMSEarlyDepartureResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/early-departure", departure, 200))
	if out.FinancialState != "READBACK_REQUIRED" || out.RefundableMinor != nil || out.RefundExecuted {
		t.Fatalf("unknown outcome invented a refund %+v", out)
	}
	x.process()
	if x.pay.collectionCalls != 1 {
		t.Fatalf("stale extension collection charged after early departure: %d", x.pay.collectionCalls)
	}
	c.must(c.owner, "POST", path, collect, 409)
	audit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Validate the day with an unresolved early-departure outcome"}, 201))
	if audit.Snapshot.Checks["UNRESOLVED_STAY_AMENDMENT_OBLIGATIONS"] < 1 || audit.State != "EXCEPTIONS" {
		t.Fatalf("night audit ignored unknown outcome %+v", audit.Snapshot.Checks)
	}
}

// A collected (platform) stay departs early: the collected unused share is a
// refund obligation pending authorised Payment/Admin execution, never a
// completed refund. Night audit blocks on it; a later Payment reversal removes
// folio backing without rewriting history.
func TestBusinessPMSEarlyDeparturePendingRefundAndPaymentReversal(t *testing.T) {
	x := newPMSAmendFixture(t, true, 1)
	c := x.c
	before := x.stay(0)
	if before.BalanceMinor != 0 || before.Folio.State != "SETTLED" {
		t.Fatalf("prepaid fixture %+v %d", before.Folio, before.BalanceMinor)
	}
	cmd, terms := x.earlyDeparture(0)
	if terms.Segments[0].Payers[0].Collection != "COLLECTED" || terms.OutstandingAfterMinor != -terms.Terms.CreditMinor {
		t.Fatalf("prepaid terms %+v", terms)
	}
	out := commercialData[businesshttp.PMSEarlyDepartureResult](t, c.must(c.owner, "POST", "/stays/"+x.stays[0].String()+"/early-departure", cmd, 200))
	if out.FinancialState != "REFUND_PENDING_EXTERNAL" || out.RefundableMinor == nil || *out.RefundableMinor != terms.Terms.CreditMinor || len(out.RefundRequestIDs) != 1 || out.RefundExecuted {
		t.Fatalf("refund obligation %+v", out)
	}
	var state, lastError string
	var amount int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state,coalesce(last_error,''),(request->>'amount_minor')::bigint FROM reservation_financial_requests WHERE id=$1 AND kind='REFUND'`, out.RefundRequestIDs[0]).Scan(&state, &lastError, &amount); e != nil || state != "DEPENDENCY_BLOCKED" || lastError != "PAYMENT_REFUND_REQUIRES_AUTHORISED_ADMIN_HANDOFF" || amount != terms.Terms.CreditMinor {
		t.Fatalf("refund request %s %s %d %v", state, lastError, amount, e)
	}
	audit := commercialData[businesshttp.PMSAuditResult](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Validate the business day with the pending early-departure refund"}, 201))
	if audit.Snapshot.Checks["PENDING_RESERVATION_FINANCIAL_REMEDIES"] < 1 || audit.State != "EXCEPTIONS" || audit.Snapshot.Checks["FISCAL_DETAIL_REVIEW_REQUIRED"] != 0 {
		t.Fatalf("night audit %s %+v", audit.State, audit.Snapshot.Checks)
	}
	d := x.stay(0)
	if d.BalanceMinor != -terms.Terms.CreditMinor || d.Folio.State != "SETTLEMENT_PENDING" {
		t.Fatalf("credited prepaid folio %d %s", d.BalanceMinor, d.Folio.State)
	}
	// Payment reverses the original collection after the amendment: folio
	// backing disappears, the refund remains unexecuted and history is kept.
	group := x.v.FinancialGroups[0].ID
	x.pay.reverse(group, x.v.Reservation.AmountMinor)
	x.readback(group)
	d = x.stay(0)
	if d.BalanceMinor != x.v.Reservation.AmountMinor-terms.Terms.CreditMinor {
		t.Fatalf("reversal concealed debt %d", d.BalanceMinor)
	}
	var groupState, financial string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT g.state,r.financial_state FROM reservation_financial_groups g JOIN reservations r ON r.id=g.reservation_id WHERE g.id=$1`, group).Scan(&groupState, &financial); e != nil || groupState != "DISPUTED" || financial != "DISPUTED" {
		t.Fatalf("reversed backing %s %s %v", groupState, financial, e)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservation_financial_requests WHERE id=$1`, out.RefundRequestIDs[0]).Scan(&state); e != nil || state != "DEPENDENCY_BLOCKED" {
		t.Fatalf("refund request %s %v", state, e)
	}
	if n := x.count(`SELECT count(*) FROM folio_payment_references WHERE folio_id=$1`, x.folios[0]); n != 1 {
		t.Fatalf("payment allocation history rewritten %d", n)
	}
}
