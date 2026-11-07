package integration

import (
	"testing"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// An approved overbooking obligation opens a Section 11 recovery case for the
// registered guest atomically; an unregistered booking source opens none.
func TestBusinessReservationOverbookingOpensGuestRecoveryCase(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	base := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, true), 201))
	c.must(c.owner, "POST", "/reservations/"+base.Reservation.ID.String()+"/confirm", reservations.Command{Version: base.Reservation.Version, Reason: "Confirm the sole actual physical allocation"}, 200)
	policy := overbookingPolicyFixture(c)
	guest := crmGuestFixture(c)
	q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/reservation-overbooking-quotes", businesshttp.ReservationOverbookingQuote{PolicyID: policy.ID, PolicyVersion: policy.Version, Quote: businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}}, 201))
	in := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", SourceReference: "overbooked-registered-guest:" + ids.New().String(), State: "INQUIRY", PaymentResponsibility: "PAY_AT_PROPERTY", Rooms: []reservations.RoomCommand{{ProductID: q.ProductID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}, Guests: []reservations.GuestCommand{{GuestID: &guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, IsPrimary: true}}, Reason: "Retain a priced inquiry while physical stock is fully occupied"}
	op := in
	in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	inquiry := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	accepted := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+inquiry.Reservation.ID.String()+"/overbook", overbookingCommand(c, policy, inquiry.Reservation, "ACCEPT"), 200))
	if accepted.Reservation.State != "CONFIRMED" {
		t.Fatalf("overbooked state %s", accepted.Reservation.State)
	}
	var caseID ids.ID
	var issue, state, severity, evidence string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id,issue_type,state,severity,evidence_reference FROM service_recovery_cases WHERE reservation_id=$1 AND guest_id=$2`, inquiry.Reservation.ID, guest.Guest.ID).Scan(&caseID, &issue, &state, &severity, &evidence); e != nil {
		t.Fatalf("recovery case missing: %v", e)
	}
	if issue != "OVERBOOKING" || state != "OPEN" || severity != "HIGH" || len(evidence) < 20 {
		t.Fatalf("recovery case %s %s %s %s", issue, state, severity, evidence)
	}
	var linked int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_modifications WHERE reservation_id=$1 AND action='OVERBOOKING_CONFIRMED' AND change_context->>'service_recovery_case_id'=$2`, inquiry.Reservation.ID, caseID.String()).Scan(&linked); e != nil || linked != 1 {
		t.Fatalf("reservation history does not reference the recovery case %d %v", linked, e)
	}
	var compensations int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM service_recovery_compensation_refs WHERE case_id=$1`, caseID).Scan(&compensations); e != nil || compensations != 0 {
		t.Fatalf("remedy implied by opening a case %d %v", compensations, e)
	}
}
