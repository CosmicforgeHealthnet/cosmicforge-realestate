package integration

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func pmsReady(c *commercialHarness) hospitality.RoomStatus {
	c.t.Helper()
	cmd := businesshttp.PMSRoomStatusCommand{PropertyID: c.property.ID, RoomID: c.unit.ID, State: "VACANT_DIRTY", EvidenceReference: "disposable-front-desk-inspection-reference", Reason: "Record the actual physical room readiness observation"}
	path := "/room-status/" + c.unit.ID.String()
	c.must(c.owner, "PUT", path, cmd, 200)
	cmd.ExpectedVersion = 1
	cmd.State = "VACANT_CLEAN"
	c.must(c.owner, "PUT", path, cmd, 200)
	cmd.ExpectedVersion = 2
	cmd.State = "INSPECTED"
	return commercialData[hospitality.RoomStatus](c.t, c.must(c.owner, "PUT", path, cmd, 200))
}
func pmsConfirmed(c *commercialHarness) (reservations.Detail, crmRegisteredGuest) {
	c.t.Helper()
	reservationPolicyFixture(c, false)
	g := crmGuestFixture(c)
	in := reservationCommandFixture(c, false)
	in.PaymentResponsibility = "PAY_AT_PROPERTY"
	in.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: "DIRECT", SourceReference: g.SourceReference, IsPrimary: true}}
	q := c.quote()
	in.Rooms[0].QuoteID = q.QuoteID
	op := in
	in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	created := commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations", in, 201))
	v := commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: created.Reservation.Version, Reason: "Confirm the real accepted inventory under its reviewed financial guarantee"}, 200))
	return v, g
}
func pmsAssign(c *commercialHarness, v reservations.Detail, status hospitality.RoomStatus) hospitality.Assignment {
	c.t.Helper()
	in := businesshttp.PMSAssignmentCommand{ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: v.Rooms[0].ID, RoomID: c.unit.ID, RoomStatusVersion: status.Version, Slot: 1, Reason: "Assign the inspected canonical physical room to its backed quantity slot"}
	return commercialData[hospitality.Assignment](c.t, c.must(c.owner, "POST", "/room-assignments", in, 201))
}
func TestBusinessPMSConcurrentPhysicalAssignmentAndHazardHold(t *testing.T) {
	c := newCommercialHTTP(t)
	v, _ := pmsConfirmed(c)
	status := pmsReady(c)
	in := businesshttp.PMSAssignmentCommand{ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: v.Rooms[0].ID, RoomID: c.unit.ID, RoomStatusVersion: status.Version, Slot: 1, Reason: "Concurrent assignment must not duplicate canonical physical room occupancy"}
	statuses := make(chan int, 2)
	for range 2 {
		go func() { code, _ := c.call(c.owner, "POST", "/room-assignments", in, nil); statuses <- code }()
	}
	a, b := <-statuses, <-statuses
	if !(a == 201 && b == 409 || b == 201 && a == 409) {
		t.Fatalf("assignment race %d/%d", a, b)
	}
	var assigned ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT id FROM room_assignments WHERE reservation_id=$1`, v.Reservation.ID).Scan(&assigned); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/cancel", reservations.Command{Version: v.Reservation.Version, Reason: "Cannot cancel while the physical assignment is still effective"}, 409)
	c.must(c.owner, "POST", "/room-assignments/"+assigned.String()+"/release", businesshttp.PMSVersionCommand{Version: 1, Reason: "Release prearrival physical reservation before canonical cancellation"}, 200)
	hazard := businesshttp.PMSRoomStatusCommand{PropertyID: c.property.ID, RoomID: c.unit.ID, ExpectedVersion: status.Version, State: "OUT_OF_ORDER", EvidenceReference: "actual-hard-maintenance-hazard-reference", Reason: "Close physical room and conservatively close the pool to further sales"}
	c.must(c.owner, "PUT", "/room-status/"+c.unit.ID.String(), hazard, 200)
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range days {
		if d.Available != 0 || d.PhysicalAvailable != 0 {
			t.Fatal("hard physical hazard remained commercially sellable")
		}
	}
	c.must(c.owner, "POST", "/room-assignments", in, 409)
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE room_status_current SET state='INSPECTED',version=version+1 WHERE room_id=$1`, c.unit.ID); e == nil {
		t.Fatal("readiness changed without immutable operational evidence")
	}
	outsider := c.user()
	c.must(outsider, "GET", "/room-status/"+c.unit.ID.String(), nil, 404)
}
func TestBusinessPMSCheckInFolioDepartureDebtAndImmutableHistory(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "reservations:approve", "pms:approve", "folios:approve", "pricing:approve"})
	destination := pmsTwoRoomStock(c)
	v, g := pmsConfirmed(c)
	status := pmsReady(c)
	a := pmsAssign(c, v, status)
	in := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: v.Reservation.Version, RoomStatusVersion: status.Version, GuestID: g.Guest.ID, RegistrationReference: "actual-front-desk-registration-reference", Reason: "Check in the registered guest after inspecting the backed physical room"}
	arrival, e := hospitality.LocalInstant(a.DateFrom, v.Reservation.PolicySnapshot.CheckInLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		t.Fatal(e)
	}
	if time.Now().Before(arrival) {
		op := in
		in.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
	}
	path := "/reservations/" + v.Reservation.ID.String() + "/check-in"
	key := ids.New().String()
	code, raw := c.call(c.owner, "POST", path, in, map[string]string{"Idempotency-Key": key})
	if code != 201 {
		t.Fatalf("checkin %d %s", code, raw)
	}
	checked := commercialData[struct {
		StayID             ids.ID `json:"stay_id"`
		FolioID            ids.ID `json:"folio_id"`
		ReservationVersion int64  `json:"reservation_version"`
		AccessState        string `json:"access_state"`
	}](t, raw)
	if checked.AccessState != "UNKNOWN" {
		t.Fatal("device command fabricated success")
	}
	code, replay := c.call(c.owner, "POST", path, in, map[string]string{"Idempotency-Key": key})
	if code != 201 || commercialData[struct {
		StayID ids.ID `json:"stay_id"`
	}](t, replay).StayID != checked.StayID {
		t.Fatal("checkin retry duplicated stay")
	}
	detail := commercialData[businesshttp.PMSStayDetail](t, c.must(c.owner, "GET", "/stays/"+checked.StayID.String(), nil, 200))
	if detail.Stay.State != "ACTIVE" || detail.Assignment.State != "OCCUPIED" || detail.BalanceMinor != v.Rooms[0].AmountMinor {
		t.Fatalf("checkin composition %+v", detail)
	}
	c.must(c.owner, "POST", "/room-assignments/"+a.ID.String()+"/release", businesshttp.PMSVersionCommand{Version: 2, Reason: "Cannot release physical occupancy without the actual departure transaction"}, 409)
	posting := businesshttp.PMSFolioPosting{Version: detail.Folio.Version, Kind: "MEALS", SourceReference: "restaurant-order-unique-reference", Quantity: 1, AmountMinor: 1200, Currency: detail.Folio.Currency, PolicyReference: "accepted-restaurant-menu-policy-v1", Reason: "Post the actual consumed meal with its immutable service source"}
	c.must(c.owner, "POST", "/folios/"+checked.FolioID.String()+"/items", posting, 201)
	posting.Version++
	c.must(c.owner, "POST", "/folios/"+checked.FolioID.String()+"/items", posting, 409)
	oldRoom := c.unit
	c.unit = destination
	destinationStatus := pmsReady(c)
	c.unit = oldRoom
	move := businesshttp.PMSRoomMoveCommand{StayID: checked.StayID, Version: 1, ReservationVersion: checked.ReservationVersion, AssignmentVersion: 2, RoomID: destination.ID, RoomStatusVersion: destinationStatus.Version, Reason: "Move the actual in-house guest within the accepted physical product without repricing or duplicating inventory"}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/room-move", move, 200)
	request := commercialData[businesshttp.PMSGuestRequest](t, c.must(c.owner, "POST", "/guest-requests", businesshttp.PMSGuestRequestCommand{StayID: checked.StayID, Kind: "SERVICE", EvidenceReference: "actual-in-house-service-request-reference", Reason: "Record an actual guest service request against the moved stay"}, 201))
	c.must(c.owner, "PATCH", "/guest-requests/"+request.ID.String(), businesshttp.PMSGuestRequestUpdate{Version: 1, State: "RESOLVED", EvidenceReference: "actual-service-fulfilment-reference", Reason: "Record the operator's actual completed guest-service evidence"}, 200)
	c.unit = destination
	exit := businesshttp.PMSCheckoutCommand{StayID: checked.StayID, Version: 2, ReservationVersion: checked.ReservationVersion + 1, FolioVersion: 2, Reason: "Release physical room under a reviewed exception while preserving the outstanding obligation"}
	exitPath := "/reservations/" + v.Reservation.ID.String() + "/check-out"
	c.must(c.owner, "POST", exitPath, exit, 403)
	balance := detail.BalanceMinor + 1200
	op := exit
	exit.ApprovalID = c.approval("COMMERCIAL_PMS_DEPARTURE_DEBT", "pms:approve", "", &balance, detail.Folio.Currency, map[string]any{"stay_id": checked.StayID, "command": op})
	c.must(c.owner, "POST", exitPath, exit, 200)
	departed := commercialData[businesshttp.PMSStayDetail](t, c.must(c.owner, "GET", "/stays/"+checked.StayID.String(), nil, 200))
	if departed.Stay.State != "DEPARTED" || departed.Folio.State != "SETTLEMENT_PENDING" || departed.BalanceMinor != balance || departed.Assignment.State != "RELEASED" {
		t.Fatal("departure fabricated payment settlement or lost the remaining debt")
	}
	room := commercialData[hospitality.RoomStatus](t, c.must(c.owner, "GET", "/room-status/"+c.unit.ID.String(), nil, 200))
	if room.State != "VACANT_DIRTY" {
		t.Fatal("checkout auto-cleaned the physical room")
	}
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE folio_items SET amount_minor=0 WHERE folio_id=$1`, checked.FolioID); e == nil {
		t.Fatal("accepted postings were mutable")
	}
	if _, e = c.db.Pool.Exec(t.Context(), `DELETE FROM hospitality_history WHERE entity_id=$1`, checked.StayID); e == nil {
		t.Fatal("stay history could be erased")
	}
}

func TestBusinessPMSSettlementRequiresOwningReceiptAndReversalRemovesBacking(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "reservations:approve", "pms:approve"})
	reservationPolicyFixture(c, false)
	g := crmGuestFixture(c)
	cmd := reservationCommandFixture(c, false)
	cmd.Guests = []reservations.GuestCommand{{GuestID: &g.Guest.ID, SourceKind: g.SourceKind, SourceReference: g.SourceReference, IsPrimary: true}}
	v := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", cmd, 201))
	collection := reservationPaymentCommand(c, v)
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/payment-requests", collection, 202)
	if e := c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	v = commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+v.Reservation.ID.String(), nil, 200))
	v = commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/confirm", reservations.Command{Version: v.Reservation.Version, Reason: "Confirm exact collection evidence before the physical arrival workflow"}, 200))
	status := pmsReady(c)
	a := pmsAssign(c, v, status)
	check := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: v.Reservation.Version, RoomStatusVersion: status.Version, GuestID: g.Guest.ID, RegistrationReference: "registered-financial-journey-guest", Reason: "Open the physical stay without conflating collection and final settlement"}
	arrival, e := hospitality.LocalInstant(a.DateFrom, v.Reservation.PolicySnapshot.CheckInLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		t.Fatal(e)
	}
	if time.Now().Before(arrival) {
		op := check
		check.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
	}
	opened := commercialData[struct {
		StayID  ids.ID `json:"stay_id"`
		FolioID ids.ID `json:"folio_id"`
	}](t, c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-in", check, 201))
	path := "/folios/" + opened.FolioID.String() + "/reconcile"
	reconcile := businesshttp.PMSVersionCommand{Version: 1, Reason: "Reconcile the folio only against the current authoritative funded settlement"}
	c.must(c.owner, "POST", path, reconcile, 409)
	f := reservationPaymentProvider(c)
	f.mu.Lock()
	f.intent.SettlementState = "SETTLED"
	f.fundingConfirmed = true
	f.mu.Unlock()
	var reservationVersion int64
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT version FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&reservationVersion); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/payment-readback", map[string]any{"version": reservationVersion, "financial_group_id": v.FinancialGroups[0].ID, "reason": "Read actual owning Payment settlement after the provider completes funding"}, 202)
	if e = c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", path, reconcile, 200)
	balance, e := hospitality.Balance(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, opened.FolioID, time.Now())
	if e != nil || balance != 0 {
		t.Fatalf("settled balance %d %v", balance, e)
	}
	reconcile.Version = 2
	c.must(c.owner, "POST", path, reconcile, 200)
	var refs int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM folio_payment_references WHERE folio_id=$1`, opened.FolioID).Scan(&refs); e != nil || refs != 1 {
		t.Fatalf("receipt allocated twice %d %v", refs, e)
	}
	f.mu.Lock()
	f.reversed = 1
	f.mu.Unlock()
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT version FROM reservations WHERE id=$1`, v.Reservation.ID).Scan(&reservationVersion); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/payment-readback", map[string]any{"version": reservationVersion, "financial_group_id": v.FinancialGroups[0].ID, "reason": "Read actual owning Payment reversal without mutating allocation evidence"}, 202)
	if e = c.domain.ProcessReservationFinance(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	balance, e = hospitality.Balance(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, opened.FolioID, time.Now())
	if e != nil || balance != v.Reservation.AmountMinor {
		t.Fatalf("reversal concealed outstanding debt %d %v", balance, e)
	}
}

func pmsTwoRoomStock(c *commercialHarness) portfolio.Node {
	c.t.Helper()
	category := c.commercialNode("/room-types", map[string]any{"parent_id": c.property.ID, "code": "PMS-DELUXE", "name": "PMS actual two-room category", "attributes": map[string]any{"capacity": 2, "beds": 1}})
	rooms := []portfolio.Node{}
	for _, code := range []string{"PMS-201", "PMS-202"} {
		rooms = append(rooms, c.commercialNode("/rooms", map[string]any{"parent_id": c.property.ID, "room_type_id": category.ID, "code": code, "name": code, "attributes": map[string]any{"capacity": 2}}))
	}
	category = c.activateNode(category)
	for i := range rooms {
		rooms[i] = c.activateNode(rooms[i])
	}
	pool := commercialData[inventory.Pool](c.t, c.must(c.owner, "POST", "/inventory-pools", businesshttp.CommercialPoolCreate{PropertyID: c.property.ID, NodeID: category.ID, Name: "PMS real physical rooms", MemberIDs: []ids.ID{rooms[0].ID, rooms[1].ID}}, 201))
	pool = commercialData[inventory.Pool](c.t, c.must(c.owner, "PATCH", "/inventory-pools/"+pool.ID.String(), businesshttp.CommercialStateChange{Version: pool.Version, State: "ACTIVE", Reason: "Activate exactly two actual canonical physical rooms"}, 200))
	product := commercialData[inventory.Product](c.t, c.must(c.owner, "POST", "/sellable-products", businesshttp.CommercialProductCreate{PropertyID: c.property.ID, NodeID: category.ID, PoolID: pool.ID, RatePlanID: &c.rate.ID, Name: "PMS deluxe room nights", MinGuests: 1, MaxGuests: 2, PolicyCode: "PMS-ROOMS", PolicyVersion: 1, Channels: []string{}}, 201))
	product = c.activateProduct(product)
	dates, _ := c.dates.Days(c.property.Timezone)
	versions := map[string]int64{}
	for _, d := range dates {
		versions[d] = 0
	}
	capacity := 2
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Capacity: &capacity, ExpectedVersions: versions, Reason: "Open exactly the two existing physical room nights"}, 200)
	c.unit = rooms[0]
	c.pool = pool
	c.product = product
	return rooms[1]
}

func TestBusinessPMSDistinctReservationsCannotAssignOnePhysicalRoom(t *testing.T) {
	c := newCommercialHTTP(t)
	pmsTwoRoomStock(c)
	first, _ := pmsConfirmed(c)
	guest := crmGuestFixture(c)
	cmd := reservationCommandFixture(c, false)
	cmd.SourceReference = "another-independent-booker:" + ids.New().String()
	cmd.Guests = []reservations.GuestCommand{{GuestID: &guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, IsPrimary: true}}
	cmd.PaymentResponsibility = "PAY_AT_PROPERTY"
	quote := c.quote()
	cmd.Rooms[0].QuoteID = quote.QuoteID
	op := cmd
	cmd.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &quote.AmountMinor, quote.Currency, map[string]any{"command": op})
	second := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", cmd, 201))
	second = commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+second.Reservation.ID.String()+"/confirm", reservations.Command{Version: second.Reservation.Version, Reason: "Confirm a distinct backed reservation for physical occupancy contention"}, 200))
	status := pmsReady(c)
	results := make(chan int, 2)
	for _, v := range []reservations.Detail{first, second} {
		in := businesshttp.PMSAssignmentCommand{ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: v.Rooms[0].ID, RoomID: c.unit.ID, RoomStatusVersion: status.Version, Slot: 1, Reason: "Independent confirmed stays compete for exactly one actual canonical physical room"}
		go func() { code, _ := c.call(c.owner, "POST", "/room-assignments", in, nil); results <- code }()
	}
	a, b := <-results, <-results
	if !(a == 201 && b == 409 || b == 201 && a == 409) {
		t.Fatalf("distinct reservation occupancy race %d/%d", a, b)
	}
	var assigned int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM room_assignments WHERE organisation_id=$1 AND room_id=$2 AND state='ASSIGNED'`, c.owner.registered.OrganisationID, c.unit.ID).Scan(&assigned); e != nil || assigned != 1 {
		t.Fatalf("physical room assigned to %d distinct reservations: %v", assigned, e)
	}
}
