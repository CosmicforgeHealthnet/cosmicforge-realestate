package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func journeyReviewer(c *commercialHarness) {
	c.t.Helper()
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "reservations:approve", "pms:approve", "folios:approve", "pricing:approve"})
}

// journeyStay assigns, checks in and returns the opened stay detail.
func journeyStay(c *commercialHarness, reservationID ids.ID, room portfolio.Node, guest ids.ID) businesshttp.PMSStayDetail {
	c.t.Helper()
	pmsReadyRoom(c, room)
	v := commercialData[reservations.Detail](c.t, c.must(c.owner, "GET", "/reservations/"+reservationID.String(), nil, 200))
	line := activeLine(c.t, v)
	a := commercialData[hospitality.Assignment](c.t, c.must(c.owner, "POST", "/room-assignments", businesshttp.PMSAssignmentCommand{ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: line.ID, RoomID: room.ID, RoomStatusVersion: 3, Slot: 1, Reason: "Assign the inspected physical room to the remaining backed slot"}, 201))
	v = commercialData[reservations.Detail](c.t, c.must(c.owner, "GET", "/reservations/"+reservationID.String(), nil, 200))
	in := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: v.Reservation.Version, RoomStatusVersion: 3, GuestID: guest, RegistrationReference: "journey-front-desk-registration:" + ids.New().String()[24:], Reason: "Check in the registered guest of the remaining unit"}
	arrival, e := hospitality.LocalInstant(a.DateFrom, v.Reservation.PolicySnapshot.CheckInLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		c.t.Fatal(e)
	}
	if time.Now().Before(arrival) {
		op := in
		in.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
	}
	opened := commercialData[struct {
		StayID ids.ID `json:"stay_id"`
	}](c.t, c.must(c.owner, "POST", "/reservations/"+reservationID.String()+"/check-in", in, 201))
	return commercialData[businesshttp.PMSStayDetail](c.t, c.must(c.owner, "GET", "/stays/"+opened.StayID.String(), nil, 200))
}

// J3: multi-room reservation -> quantity split -> partial cancellation ->
// financial conservation -> remaining rooms stay -> checkout.
func TestBusinessJourneyGroupReservationFragmentationToCheckout(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	journeyReviewer(c)
	reservationPolicyFixture(c, false)
	rooms := quantityStock(c, 3)
	booker, traveller := crmGuestFixture(c), crmGuestFixture(c)
	v := quantityBooking(c, 3, "journey-group-booker:"+ids.New().String(), []reservations.GuestCommand{{GuestID: &booker.Guest.ID, SourceKind: booker.SourceKind, SourceReference: booker.SourceReference, IsPrimary: true}, {GuestID: &traveller.Guest.ID, SourceKind: traveller.SourceKind, SourceReference: traveller.SourceReference}})
	original := activeLine(t, v)
	path := "/reservations/" + v.Reservation.ID.String()

	// Quantity split: the traveller's unit becomes its own canonical booking.
	var travellerRow ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id FROM reservation_guests WHERE reservation_id=$1 AND guest_id=$2`, v.Reservation.ID, traveller.Guest.ID).Scan(&travellerRow); e != nil {
		t.Fatal(e)
	}
	split := businesshttp.ReservationQuantityCommand{Version: v.Reservation.Version, RoomID: original.ID, RoomVersion: original.Version, Quantity: 1, GuestIDs: []ids.ID{travellerRow}, NewReservation: true, Reason: "Traveller settles and stays separately"}
	tail, _, _ := reservations.TailPartition(original.AmountMinor, original.Quantity, 1)
	split.ApprovalID = c.quantityApproval(v.Reservation, "QUANTITY_SPLIT", split, tail)
	splitResult := commercialData[businesshttp.ReservationQuantityResult](t, c.must(c.owner, "POST", path+"/quantity-split", split, 200))
	child := *splitResult.Target

	// Partial cancellation of one remaining unit under the frozen policy.
	parent := splitResult.Reservation
	parentLine := activeLine(t, parent)
	cancelResult := commercialData[businesshttp.ReservationQuantityResult](t, c.must(c.owner, "POST", path+"/quantity-cancel", businesshttp.ReservationQuantityCommand{Version: parent.Reservation.Version, RoomID: parentLine.ID, RoomVersion: parentLine.Version, Quantity: 1, Reason: "One booked room is no longer needed"}, 200))
	parent = cancelResult.Reservation
	cancelled := cancelResult.Change.Change

	// Financial conservation: every unit of the frozen accepted amount is
	// either still owed by an active line or recorded as a cancelled obligation.
	if parent.Reservation.AmountMinor+child.Reservation.AmountMinor+cancelled.ReleasedAmountMinor != original.AmountMinor {
		t.Fatalf("amount not conserved %d+%d+%d != %d", parent.Reservation.AmountMinor, child.Reservation.AmountMinor, cancelled.ReleasedAmountMinor, original.AmountMinor)
	}
	assertStockConservation(c, 2)
	var penalty, accepted int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT accepted_amount_minor,penalty_minor FROM reservation_cancellation_obligations WHERE reservation_id=$1 AND kind='PARTIAL_CANCELLED'`, v.Reservation.ID).Scan(&accepted, &penalty); e != nil || accepted != cancelled.ReleasedAmountMinor {
		t.Fatalf("partial obligation %d %d %v", accepted, penalty, e)
	}

	// Remaining rooms stay: each remaining unit checks in on its own room.
	parentStay := journeyStay(c, v.Reservation.ID, rooms[0], booker.Guest.ID)
	childStay := journeyStay(c, child.Reservation.ID, rooms[1], traveller.Guest.ID)
	billed := int64(0)
	for _, stay := range []businesshttp.PMSStayDetail{parentStay, childStay} {
		for _, item := range stay.Items {
			if item.Kind == "ACCOMMODATION" {
				billed += item.AmountMinor
			}
		}
	}
	if billed+cancelled.ReleasedAmountMinor != original.AmountMinor {
		t.Fatalf("accommodation billed %d + cancelled %d != accepted %d", billed, cancelled.ReleasedAmountMinor, original.AmountMinor)
	}

	// Checkout: departure without Payment settlement keeps the debt explicit.
	for _, x := range []struct {
		reservation ids.ID
		stay        businesshttp.PMSStayDetail
	}{{v.Reservation.ID, parentStay}, {child.Reservation.ID, childStay}} {
		pmsDeferredDeparture(c, reservations.Detail{Reservation: reservations.Reservation{ID: x.reservation}}, x.stay)
	}
	for _, id := range []ids.ID{v.Reservation.ID, child.Reservation.ID} {
		final := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+id.String(), nil, 200))
		if final.Reservation.State != "CHECKED_OUT" {
			t.Fatalf("reservation %s state %s", id, final.Reservation.State)
		}
	}
	var settled int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM folios WHERE reservation_id IN ($1,$2) AND state='CLOSED'`, v.Reservation.ID, child.Reservation.ID).Scan(&settled); e != nil || settled != 0 {
		t.Fatalf("folio closed without settled Payment evidence %d %v", settled, e)
	}
	assertStockConservation(c, 2)
}

// J4: full inventory -> cancellation/release -> automatic hold -> offer ->
// guest acceptance -> confirmed canonical reservation.
func TestBusinessJourneyWaitlistReleaseToConfirmedReservation(t *testing.T) {
	c := newCommercialHTTP(t)
	sold := waitlistFullHouse(c)
	entry := waitlistEntry(c, 20, "journey-waitlist-guest:"+ids.New().String())
	waitlistAutomationPolicy(c, "AUTOMATIC_OFFERS")
	assertStockConservationPool(c, 1)
	c.must(c.owner, "POST", "/reservations/"+sold.Reservation.ID.String()+"/cancel", reservations.Command{Version: sold.Reservation.Version, Reason: "Guest cancelled and released the last unit"}, 200)
	if e := c.domain.ProcessWaitlistAutomation(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	offers := liveOffers(c)
	if len(offers) != 1 || offers[0].EntryID != entry.ID {
		t.Fatalf("automatic offer %+v", offers)
	}
	var held int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT quantity FROM inventory_holds WHERE id=$1 AND state='ACTIVE'`, offers[0].HoldID).Scan(&held); e != nil || held != 1 {
		t.Fatalf("automatic hold %d %v", held, e)
	}
	// Nothing is booked until the guest accepts.
	var bookings int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservations WHERE organisation_id=$1 AND state='CONFIRMED'`, c.owner.registered.OrganisationID).Scan(&bookings); e != nil || bookings != 0 {
		t.Fatalf("offer created a booking %d %v", bookings, e)
	}
	var entryVersion, quoteAmount int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT version FROM waitlist_entries WHERE id=$1),(SELECT amount_minor FROM price_quotes WHERE id=$2)`, entry.ID, offers[0].QuoteID).Scan(&entryVersion, &quoteAmount); e != nil {
		t.Fatal(e)
	}
	booking := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", SourceReference: "journey-waitlist-conversion:" + ids.New().String(), State: "CONFIRMED", PaymentResponsibility: "PAY_AT_PROPERTY", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: offers[0].QuoteID, HoldID: &offers[0].HoldID, HoldVersion: 1, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Quantity: 1}}, Guests: []reservations.GuestCommand{{SourceKind: "DIRECT", SourceReference: entry.GuestReference, IsPrimary: true}}, Reason: "Guest accepted the automatic waitlist offer"}
	op := booking
	booking.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &quoteAmount, "NGN", map[string]any{"command": op})
	accepted := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/accept", businesshttp.ReservationWaitlistAccept{Version: entryVersion, OfferID: offers[0].ID, OfferVersion: offers[0].Version, Booking: booking}, 201))
	if accepted.Reservation.State != "CONFIRMED" || activeLine(t, accepted).Quantity != 1 {
		t.Fatalf("confirmed canonical reservation %+v", accepted.Reservation)
	}
	assertStockConservationPool(c, 1)
	// The consumed offer cannot be accepted twice.
	c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/accept", businesshttp.ReservationWaitlistAccept{Version: entryVersion + 1, OfferID: offers[0].ID, OfferVersion: offers[0].Version + 1, Booking: booking}, 409)
}
