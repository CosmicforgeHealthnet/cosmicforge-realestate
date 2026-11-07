package integration

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// pmsAmendFixture is an in-house hotel stay built only through the public
// Business API: canonical stock, accepted quote, reservation, financial
// backing (approved guarantee or disposable Payment contract double), physical
// assignment and check-in. Extra calendar nights are opened for extensions.
type pmsAmendFixture struct {
	c        *commercialHarness
	pay      *folioPaymentFake
	guests   []crmRegisteredGuest
	v        reservations.Detail
	rooms    []portfolio.Node
	stays    []ids.ID
	folios   []ids.ID
	prepaid  bool
	capacity int
}

type pmsOpenedStay struct {
	StayID  ids.ID `json:"stay_id"`
	FolioID ids.ID `json:"folio_id"`
}

func newPMSAmendFixture(t *testing.T, prepaid bool, quantity int, extraCapacity ...int) *pmsAmendFixture {
	t.Helper()
	c := newCommercialHTTPForArrival(t, 0)
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "reservations:approve", "pms:approve", "folios:approve", "pricing:approve", "night_audit:approve"})
	x := &pmsAmendFixture{c: c, prepaid: prepaid, capacity: 1, rooms: []portfolio.Node{c.unit}}
	if quantity > 1 {
		spare := pmsTwoRoomStock(c)
		x.rooms = []portfolio.Node{c.unit, spare}
		x.capacity = 2
	}
	reservationPolicyFixture(c, false)
	x.pay = newFolioPaymentFake(reservationPaymentProvider(c).policy)
	c.domain.Payment = x.pay
	// Open further canonical nights after the accepted departure for extensions.
	loc, _ := time.LoadLocation(c.property.Timezone)
	end, _ := time.ParseInLocation(time.DateOnly, c.dates.DateTo, loc)
	extra := map[string]int64{}
	for i := 0; i < 6; i++ {
		extra[end.AddDate(0, 0, i).Format(time.DateOnly)] = 0
	}
	capacity := x.capacity
	if len(extraCapacity) > 0 {
		capacity = extraCapacity[0]
	}
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: c.pool.ID, DateFrom: c.dates.DateTo, DateTo: end.AddDate(0, 0, 6).Format(time.DateOnly), Capacity: &capacity, ExpectedVersions: extra, Reason: "Open existing physical stock for in-house extensions"}, 200)
	guests := []reservations.GuestCommand{}
	for i := 0; i < quantity; i++ {
		g := crmGuestFixture(c)
		x.guests = append(x.guests, g)
		guests = append(guests, reservations.GuestCommand{GuestID: &g.Guest.ID, SourceKind: "DIRECT", SourceReference: g.SourceReference, IsPrimary: i == 0})
	}
	q := x.quote(c.dates.DateFrom, c.dates.DateTo, quantity)
	in := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", SourceReference: "direct-booker:" + ids.New().String(), State: "HOLD", PaymentResponsibility: "PAY_AT_PROPERTY", Reason: "Accept quoted physical inventory atomically", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, ProductVersion: x.productVersion(), PoolVersion: x.poolVersion(), QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: quantity}}, Guests: guests}
	if prepaid {
		in.PaymentResponsibility = "PLATFORM_COLLECT"
	} else {
		op := in
		in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	}
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	if prepaid {
		cmd := businesshttp.ReservationCollectionCommand{Version: v.Reservation.Version, FinancialGroupID: v.FinancialGroups[0].ID, PaymentPolicyID: x.pay.policy.ID, PaymentPolicyVersion: x.pay.policy.Version, PaymentPolicyFamily: "HOTEL", PayerWalletID: ids.New(), PayerType: "GUEST", ConsentReference: "explicit-test-payer-consent", LegalPartyReference: "canonical-test-guest-party", Email: "disposable-guest@example.invalid", Schedule: "ONE_TIME", Reason: "Collect the frozen accepted hotel obligation"}
		c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/payment-requests", cmd, 202)
		x.process()
		v = commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+v.Reservation.ID.String(), nil, 200))
	}
	x.v = commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/confirm", reservations.Command{Version: v.Reservation.Version, Reason: "Confirm the real accepted inventory and financial backing"}, 200))
	for slot := 1; slot <= quantity; slot++ {
		opened := x.checkIn(slot)
		x.stays = append(x.stays, opened.StayID)
		x.folios = append(x.folios, opened.FolioID)
	}
	if prepaid {
		// Settlement is read back from the owning Payment contract double, then
		// applied to the folio once.
		x.pay.settle(x.v.FinancialGroups[0].ID)
		x.readback(x.v.FinancialGroups[0].ID)
		for _, f := range x.folios {
			x.reconcile(f, 200)
		}
	}
	return x
}

func (x *pmsAmendFixture) process() {
	x.c.t.Helper()
	if e := x.c.domain.ProcessReservationFinance(x.c.t.Context(), 10); e != nil {
		x.c.t.Fatal(e)
	}
}
func (x *pmsAmendFixture) readback(group ids.ID) {
	x.c.t.Helper()
	x.c.must(x.c.owner, "POST", "/reservations/"+x.v.Reservation.ID.String()+"/payment-readback", map[string]any{"version": x.resVersion(), "financial_group_id": group, "reason": "Read owning Payment funded settlement without a second collection"}, 202)
	x.process()
}
func (x *pmsAmendFixture) reconcile(folio ids.ID, status int) {
	x.c.t.Helper()
	f := commercialData[pmsFolioRead](x.c.t, x.c.must(x.c.owner, "GET", "/folios/"+folio.String(), nil, 200))
	x.c.must(x.c.owner, "POST", "/folios/"+folio.String()+"/reconcile", businesshttp.PMSVersionCommand{Version: f.Folio.Version, Reason: "Reconcile the folio against current authoritative funded settlements only"}, status)
}
func (x *pmsAmendFixture) productVersion() int64 {
	var v int64
	if e := x.c.db.Pool.QueryRow(x.c.t.Context(), `SELECT version FROM sellable_products WHERE id=$1`, x.c.product.ID).Scan(&v); e != nil {
		x.c.t.Fatal(e)
	}
	return v
}
func (x *pmsAmendFixture) poolVersion() int64 {
	var v int64
	if e := x.c.db.Pool.QueryRow(x.c.t.Context(), `SELECT version FROM inventory_pools WHERE id=$1`, x.c.pool.ID).Scan(&v); e != nil {
		x.c.t.Fatal(e)
	}
	return v
}
func (x *pmsAmendFixture) resVersion() int64 {
	var v int64
	if e := x.c.db.Pool.QueryRow(x.c.t.Context(), `SELECT version FROM reservations WHERE id=$1`, x.v.Reservation.ID).Scan(&v); e != nil {
		x.c.t.Fatal(e)
	}
	return v
}
func (x *pmsAmendFixture) quote(from, to string, quantity int) revenue.QuoteSnapshot {
	x.c.t.Helper()
	return commercialData[revenue.QuoteSnapshot](x.c.t, x.c.must(x.c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: x.c.product.ID, DateFrom: from, DateTo: to, Adults: 2, Quantity: quantity}, 201))
}
func (x *pmsAmendFixture) ready(room portfolio.Node) hospitality.RoomStatus {
	x.c.t.Helper()
	path := "/room-status/" + room.ID.String()
	var version int64
	_ = x.c.db.Pool.QueryRow(x.c.t.Context(), `SELECT version FROM room_status_current WHERE room_id=$1`, room.ID).Scan(&version)
	cmd := businesshttp.PMSRoomStatusCommand{PropertyID: x.c.property.ID, RoomID: room.ID, ExpectedVersion: version, State: "VACANT_DIRTY", EvidenceReference: "disposable-front-desk-inspection-reference", Reason: "Record the actual physical room readiness observation"}
	if version == 0 {
		x.c.must(x.c.owner, "PUT", path, cmd, 200)
		cmd.ExpectedVersion++
	}
	cmd.State = "VACANT_CLEAN"
	x.c.must(x.c.owner, "PUT", path, cmd, 200)
	cmd.ExpectedVersion++
	cmd.State = "INSPECTED"
	return commercialData[hospitality.RoomStatus](x.c.t, x.c.must(x.c.owner, "PUT", path, cmd, 200))
}
func (x *pmsAmendFixture) checkIn(slot int) pmsOpenedStay {
	c := x.c
	c.t.Helper()
	room := x.rooms[slot-1]
	status := x.ready(room)
	a := commercialData[hospitality.Assignment](c.t, c.must(c.owner, "POST", "/room-assignments", businesshttp.PMSAssignmentCommand{ReservationID: x.v.Reservation.ID, ReservationVersion: x.resVersion(), ReservationRoomID: x.v.Rooms[0].ID, RoomID: room.ID, RoomStatusVersion: status.Version, Slot: slot, Reason: "Assign the inspected canonical physical room to its backed quantity slot"}, 201))
	in := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: x.resVersion(), RoomStatusVersion: status.Version, GuestID: x.guests[slot-1].Guest.ID, RegistrationReference: "actual-front-desk-registration-reference", Reason: "Check in the registered guest for this exact unit slot"}
	arrival, e := hospitality.LocalInstant(a.DateFrom, x.v.Reservation.PolicySnapshot.CheckInLocal, x.v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		c.t.Fatal(e)
	}
	if time.Now().Before(arrival) {
		op := in
		in.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
	}
	return commercialData[pmsOpenedStay](c.t, c.must(c.owner, "POST", "/reservations/"+x.v.Reservation.ID.String()+"/check-in", in, 201))
}
func (x *pmsAmendFixture) stay(i int) businesshttp.PMSStayDetail {
	x.c.t.Helper()
	return commercialData[businesshttp.PMSStayDetail](x.c.t, x.c.must(x.c.owner, "GET", "/stays/"+x.stays[i].String(), nil, 200))
}

// extension builds a current extension command for `nights` further nights.
func (x *pmsAmendFixture) extension(i, nights int, payer string) (businesshttp.PMSStayExtensionCommand, revenue.QuoteSnapshot) {
	x.c.t.Helper()
	d := x.stay(i)
	from, _ := time.Parse(time.DateOnly, d.Assignment.DateTo)
	to := from.AddDate(0, 0, nights).Format(time.DateOnly)
	q := x.quote(d.Assignment.DateTo, to, 1)
	return businesshttp.PMSStayExtensionCommand{Version: d.Stay.Version, ReservationVersion: x.resVersion(), AssignmentVersion: d.Assignment.Version, FolioVersion: d.Folio.Version, QuoteID: q.QuoteID, ProductVersion: x.productVersion(), PoolVersion: x.poolVersion(), DateTo: to, PayerMode: payer, Reason: "Extend the in-house stay at the guest's documented request"}, q
}
func (x *pmsAmendFixture) approveExtension(i int, cmd businesshttp.PMSStayExtensionCommand, guaranteed int64) businesshttp.PMSStayExtensionCommand {
	op := cmd
	op.ApprovalID = ids.Nil
	cmd.ApprovalID = x.c.approval("COMMERCIAL_PMS_STAY_EXTENSION", "pms:approve", "", &guaranteed, "NGN", map[string]any{"stay_id": x.stays[i], "command": op})
	return cmd
}
func (x *pmsAmendFixture) preview(i int) businesshttp.PMSEarlyDepartureTerms {
	x.c.t.Helper()
	return commercialData[businesshttp.PMSEarlyDepartureTerms](x.c.t, x.c.must(x.c.owner, "GET", "/stays/"+x.stays[i].String()+"/early-departure/preview", nil, 200))
}

// earlyDeparture builds an approved early departure command from the preview.
func (x *pmsAmendFixture) earlyDeparture(i int) (businesshttp.PMSEarlyDepartureCommand, businesshttp.PMSEarlyDepartureTerms) {
	x.c.t.Helper()
	terms := x.preview(i)
	d := x.stay(i)
	cmd := businesshttp.PMSEarlyDepartureCommand{Version: d.Stay.Version, ReservationVersion: x.resVersion(), FolioVersion: d.Folio.Version, ExpectedCreditMinor: terms.Terms.CreditMinor, ExpectedOutstandingMinor: terms.OutstandingAfterMinor, Reason: "Record the guest's documented early departure"}
	op := cmd
	exposure := terms.ApprovalExposureMinor
	cmd.ApprovalID = x.c.approval("COMMERCIAL_PMS_EARLY_DEPARTURE", "pms:approve", "", &exposure, "NGN", map[string]any{"stay_id": x.stays[i], "command": op})
	return cmd, terms
}

// stockState returns committed calendar stock and unreleased reservation
// provenance for one pool day; both must always agree.
func (x *pmsAmendFixture) stockState(day string) (committed, provenance int) {
	x.c.t.Helper()
	if e := x.c.db.Pool.QueryRow(x.c.t.Context(), `SELECT c.committed,(SELECT coalesce(sum(d.quantity),0) FROM reservation_inventory_days d WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND d.released_at IS NULL)::integer FROM inventory_calendar c WHERE c.pool_id=$1 AND c.day=$2::date`, x.c.pool.ID, day).Scan(&committed, &provenance); e != nil {
		x.c.t.Fatal(e)
	}
	return
}
func (x *pmsAmendFixture) count(query string, args ...any) int {
	x.c.t.Helper()
	var n int
	if e := x.c.db.Pool.QueryRow(x.c.t.Context(), query, args...).Scan(&n); e != nil {
		x.c.t.Fatal(e)
	}
	return n
}
func amendDays(from, to string) []string {
	start, _ := time.Parse(time.DateOnly, from)
	end, _ := time.Parse(time.DateOnly, to)
	out := []string{}
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format(time.DateOnly))
	}
	return out
}
