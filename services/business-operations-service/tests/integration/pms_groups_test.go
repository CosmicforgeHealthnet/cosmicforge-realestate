package integration

import (
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func pmsGroupReviewer(c *commercialHarness) {
	c.t.Helper()
	c.approver = c.user()
	c.secure(c.approver)
	c.join(c.approver, c.owner, []string{"approvals:decide", "pricing:approve", "reservations:approve", "pms:approve", "folios:approve"})
}
func pmsGroupFixture(c *commercialHarness) businesshttp.PMSGroup {
	c.t.Helper()
	return commercialData[businesshttp.PMSGroup](c.t, c.must(c.owner, "POST", "/groups", businesshttp.PMSGroupCreate{PropertyID: c.property.ID, Name: "Disposable group", SourceReference: "actual-group-reference:" + ids.New().String(), BillingReference: "actual-group-billing-reference", Reason: "Create an actual property-local group context for canonical operations"}, 201))
}
func pmsGroupBooking(c *commercialHarness, g businesshttp.PMSGroup, quantity int, guests []reservations.GuestCommand) reservations.Detail {
	c.t.Helper()
	q := commercialData[revenue.QuoteSnapshot](c.t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: quantity}, 201))
	booking := reservations.Create{PropertyID: c.property.ID, Source: "GROUP", SourceReference: "actual-group-booker:" + ids.New().String(), State: "CONFIRMED", GroupID: &g.ID, PaymentResponsibility: "PAY_AT_PROPERTY", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: quantity}}, Guests: guests, Reason: "Reserve actual canonical group stock under independently approved financial responsibility"}
	op := booking
	booking.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	return commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/groups/"+g.ID.String()+"/blocks", businesshttp.PMSGroupBlockCommand{Booking: booking}, 201))
}
func TestBusinessPMSGroupRosterAtomicStayCommandsAndMasterFolio(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	pmsGroupReviewer(c)
	secondRoom := pmsTwoRoomStock(c)
	reservationPolicyFixture(c, false)
	g := pmsGroupFixture(c)
	one, two := crmGuestFixture(c), crmGuestFixture(c)
	v := pmsGroupBooking(c, g, 2, []reservations.GuestCommand{{GuestID: &one.Guest.ID, SourceKind: one.SourceKind, SourceReference: one.SourceReference, IsPrimary: true}, {GuestID: &two.Guest.ID, SourceKind: two.SourceKind, SourceReference: two.SourceReference}})
	firstRoom := c.unit
	ready1 := pmsReady(c)
	c.unit = secondRoom
	ready2 := pmsReady(c)
	c.unit = firstRoom
	commands := []businesshttp.PMSAssignmentCommand{{ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: v.Rooms[0].ID, RoomID: firstRoom.ID, RoomStatusVersion: ready1.Version, Slot: 1, Reason: "Assign the first actual group physical room"}, {ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: v.Rooms[0].ID, RoomID: secondRoom.ID, RoomStatusVersion: ready2.Version, Slot: 2, Reason: "Assign the second actual group physical room"}}
	assignments := []hospitality.Assignment{}
	for _, cmd := range commands {
		assignments = append(assignments, commercialData[hospitality.Assignment](t, c.must(c.owner, "POST", "/room-assignments", cmd, 201)))
	}
	initial := businesshttp.PMSCheckInCommand{AssignmentID: assignments[0].ID, AssignmentVersion: 1, ReservationVersion: v.Reservation.Version, RoomStatusVersion: ready1.Version, GuestID: one.Guest.ID, RegistrationReference: "actual-group-arrival-registration", Reason: "An arbitrary booking guest cannot bypass the current approved group rooming list"}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-in", initial, 409)
	audit := commercialData[struct {
		Snapshot businesshttp.PMSAuditSnapshot `json:"snapshot"`
	}](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Unreviewed group occupants must remain a real hard operational exception"}, 201))
	if audit.Snapshot.Checks["GROUP_ROOMING_MISSING"] != 2 {
		t.Fatalf("unreviewed group rooming exception lost %+v", audit.Snapshot.Checks)
	}

	version := v.Reservation.Version
	for i, guest := range []crmRegisteredGuest{one, two} {
		cmd := businesshttp.PMSRoomingCommand{ReservationID: v.Reservation.ID, ReservationVersion: version, ReservationRoomID: v.Rooms[0].ID, Slot: i + 1, GuestID: guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, RegistrationReference: "actual-group-occupant-registration-reference", State: "ACTIVE", Reason: "Independently review the exact registered group guest and quantity slot"}
		op := cmd
		cmd.ApprovalID = c.approval("COMMERCIAL_PMS_ROOMING_CHANGE", "pms:approve", "", nil, "", map[string]any{"group_id": g.ID, "command": op})
		c.must(c.owner, "POST", "/groups/"+g.ID.String()+"/rooming-list", cmd, 201)
		version++
	}
	audit = commercialData[struct {
		Snapshot businesshttp.PMSAuditSnapshot `json:"snapshot"`
	}](t, c.must(c.owner, "POST", "/night-audit/validate", businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Current approved rooming lists remove only the exact resolved occupancy exception"}, 201))
	if audit.Snapshot.Checks["GROUP_ROOMING_MISSING"] != 0 {
		t.Fatal("approved current group rooming was not reconciled")
	}
	c.must(c.owner, "GET", "/groups/"+g.ID.String(), nil, 200)
	c.must(c.owner, "GET", "/groups/"+g.ID.String()+"/blocks", nil, 200)
	roster := commercialData[struct {
		Items []struct {
			Slot  int    `json:"slot"`
			State string `json:"state"`
		} `json:"items"`
	}](t, c.must(c.owner, "GET", "/groups/"+g.ID.String()+"/rooming-list", nil, 200))
	if len(roster.Items) != 2 {
		t.Fatal("current group rooming list omitted actual approved slots")
	}
	items := []businesshttp.PMSCheckInCommand{}
	for i, guest := range []crmRegisteredGuest{one, two} {
		cmd := businesshttp.PMSCheckInCommand{AssignmentID: assignments[i].ID, AssignmentVersion: 1, ReservationVersion: version + int64(i), RoomStatusVersion: 3, GuestID: guest.Guest.ID, RegistrationReference: "actual-reviewed-group-registration", Reason: "Atomically check in the exact approved group occupant and physical slot"}
		arrival, e := hospitality.LocalInstant(c.dates.DateFrom, v.Reservation.PolicySnapshot.CheckInLocal, v.Reservation.PolicySnapshot.AcceptanceTimezone)
		if e != nil {
			t.Fatal(e)
		}
		if time.Now().Before(arrival) {
			op := cmd
			cmd.ApprovalID = c.approval("COMMERCIAL_PMS_EARLY_CHECK_IN", "pms:approve", "", nil, "", map[string]any{"command": op})
		}
		items = append(items, cmd)
	}
	stale := append([]businesshttp.PMSCheckInCommand(nil), items...)
	stale[1].AssignmentVersion = 99
	c.must(c.owner, "POST", "/groups/"+g.ID.String()+"/check-in", businesshttp.PMSGroupCheckIns{Items: stale, Reason: "One stale member must roll back every group check-in write"}, 409)
	var stays int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM stay_records WHERE reservation_id=$1`, v.Reservation.ID).Scan(&stays); e != nil || stays != 0 {
		t.Fatalf("partial group checkin escaped rollback %d %v", stays, e)
	}
	other := pmsGroupFixture(c)
	c.must(c.owner, "POST", "/groups/"+other.ID.String()+"/check-in", businesshttp.PMSGroupCheckIns{Items: items, Reason: "Reject cross-group assignments within the same organisation"}, 403)
	type opened struct {
		StayID             ids.ID `json:"stay_id"`
		FolioID            ids.ID `json:"folio_id"`
		ReservationVersion int64  `json:"reservation_version"`
	}
	checked := commercialData[struct {
		Items []opened `json:"items"`
	}](t, c.must(c.owner, "POST", "/groups/"+g.ID.String()+"/check-in", businesshttp.PMSGroupCheckIns{Items: items, Reason: "Commit all exact group arrivals and their folios atomically"}, 201))
	if len(checked.Items) != 2 {
		t.Fatal("group member results missing")
	}
	version += 2
	master := businesshttp.PMSMasterFolioCommand{PropertyID: c.property.ID, GroupID: g.ID, Currency: v.Reservation.Currency, FolioVersions: map[string]int64{}, BillingReference: g.BillingReference, Reason: "Aggregate actual source folios without transferring payer duties or fabricating receipts"}
	for _, item := range checked.Items {
		master.FolioIDs = append(master.FolioIDs, item.FolioID)
		master.FolioVersions[item.FolioID.String()] = 1
	}
	op := master
	master.ApprovalID = c.approval("COMMERCIAL_PMS_GROUP_BILLING", "folios:approve", "", &v.Reservation.AmountMinor, v.Reservation.Currency, map[string]any{"command": op})
	aggregate := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/master-folios", master, 201))
	view := commercialData[struct {
		Balance int64 `json:"balance_minor"`
		Closed  bool  `json:"all_source_folios_closed"`
	}](t, c.must(c.owner, "GET", "/master-folios/"+aggregate.ID.String(), nil, 200))
	if view.Balance != v.Reservation.AmountMinor || view.Closed {
		t.Fatal("master folio concealed unpaid guaranteed obligations")
	}
	exits := []businesshttp.PMSCheckoutCommand{}
	for i, item := range checked.Items {
		cmd := businesshttp.PMSCheckoutCommand{StayID: item.StayID, Version: 1, ReservationVersion: version + int64(i), FolioVersion: 1, Reason: "Approve actual physical departure while retaining the exact unpaid source folio"}
		amount := v.Reservation.AmountMinor / 2
		op := cmd
		cmd.ApprovalID = c.approval("COMMERCIAL_PMS_DEPARTURE_DEBT", "pms:approve", "", &amount, v.Reservation.Currency, map[string]any{"stay_id": item.StayID, "command": op})
		exits = append(exits, cmd)
	}
	c.must(c.owner, "POST", "/groups/"+g.ID.String()+"/check-out", businesshttp.PMSGroupCheckOuts{Items: exits, Reason: "Commit all reviewed group departures without marking remaining debt settled"}, 200)
	view = commercialData[struct {
		Balance int64 `json:"balance_minor"`
		Closed  bool  `json:"all_source_folios_closed"`
	}](t, c.must(c.owner, "GET", "/master-folios/"+aggregate.ID.String(), nil, 200))
	if view.Balance != v.Reservation.AmountMinor || view.Closed {
		t.Fatal("group checkout fabricated aggregate settlement")
	}
	c.must(c.owner, "PATCH", "/groups/"+g.ID.String(), businesshttp.PMSGroupPatch{Version: 1, State: "COMPLETED", Reason: "Financially unresolved group cannot be marked completed"}, 409)
	c.must(c.owner, "PATCH", "/groups/"+other.ID.String(), businesshttp.PMSGroupPatch{Version: 1, State: "CANCELLED", Reason: "Close an unused group context without releasing any unrelated stock"}, 200)
	c.must(c.owner, "GET", "/master-folios?property_id="+c.property.ID.String(), nil, 200)
	outsider := c.user()
	c.must(outsider, "GET", "/groups/"+g.ID.String(), nil, 404)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE rooming_lists SET guest_id=$2 WHERE group_id=$1`, g.ID, ids.New()); e == nil {
		t.Fatal("approved occupant history could be rewritten")
	}
}
func TestBusinessPMSConferenceUsesActualFacilityReservationCapacity(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	g := pmsGroupFixture(c)
	guest := crmGuestFixture(c)
	facility := c.activateNode(c.commercialNode("/facilities", map[string]any{"parent_id": c.property.ID, "code": "GROUP-MEETING", "name": "Actual two-person meeting facility", "attributes": map[string]any{"capacity": 2}}))
	pool := commercialData[inventory.Pool](t, c.must(c.owner, "POST", "/inventory-pools", businesshttp.CommercialPoolCreate{PropertyID: c.property.ID, NodeID: facility.ID, Name: "Actual meeting capacity"}, 201))
	pool = commercialData[inventory.Pool](t, c.must(c.owner, "PATCH", "/inventory-pools/"+pool.ID.String(), businesshttp.CommercialStateChange{Version: pool.Version, State: "ACTIVE", Reason: "Activate actual facility capacity without a parallel event calendar"}, 200))
	product := commercialData[inventory.Product](t, c.must(c.owner, "POST", "/sellable-products", businesshttp.CommercialProductCreate{PropertyID: c.property.ID, NodeID: facility.ID, PoolID: pool.ID, RatePlanID: &c.rate.ID, Name: "Actual conference capacity units", MinGuests: 1, MaxGuests: 2, PolicyCode: "CONFERENCE", PolicyVersion: 1, Channels: []string{}}, 201))
	product = c.activateProduct(product)
	versions := map[string]int64{}
	dates, _ := c.dates.Days(c.property.Timezone)
	for _, day := range dates {
		versions[day] = 0
	}
	capacity := 2
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Capacity: &capacity, ExpectedVersions: versions, Reason: "Open exactly the existing meeting facility capacity units"}, 200)
	c.pool = pool
	c.product = product
	booking := pmsGroupBooking(c, g, 2, []reservations.GuestCommand{{GuestID: &guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, IsPrimary: true}})
	event := businesshttp.PMSEventBookingCommand{PropertyID: c.property.ID, GroupID: g.ID, ReservationID: booking.Reservation.ID, ReservationVersion: booking.Reservation.Version, FacilityID: facility.ID, SourceReference: "actual-conference-sales-reference", ConversationReference: "opaque-unverified-cf-meet-reference", Reason: "Associate the event with its actual canonical facility allocation"}
	c.must(c.owner, "POST", "/events", event, 201)
	c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}, 409)
	c.must(c.owner, "POST", "/reservations/"+booking.Reservation.ID.String()+"/cancel", reservations.Command{Version: booking.Reservation.Version, Reason: "Cancel actual facility commitments through the canonical owning command"}, 200)
	events := commercialData[struct {
		Items []struct {
			State string `json:"reservation_state"`
		} `json:"items"`
	}](t, c.must(c.owner, "GET", "/events?property_id="+c.property.ID.String(), nil, 200))
	if len(events.Items) != 1 || events.Items[0].State != "CANCELLED" {
		t.Fatal("event metadata concealed the canonical facility cancellation")
	}
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Available != 2 {
			t.Fatal("event metadata retained or duplicated canonical facility stock")
		}
	}
}

func TestBusinessPMSGroupRoomingRevocationCannotFallBackToOldApproval(t *testing.T) {
	c := newCommercialHTTPForArrival(t, 0)
	pmsGroupReviewer(c)
	reservationPolicyFixture(c, false)
	g := pmsGroupFixture(c)
	guest := crmGuestFixture(c)
	v := pmsGroupBooking(c, g, 1, []reservations.GuestCommand{{GuestID: &guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, IsPrimary: true}})
	roster := businesshttp.PMSRoomingCommand{ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: v.Rooms[0].ID, Slot: 1, GuestID: guest.Guest.ID, SourceKind: guest.SourceKind, SourceReference: guest.SourceReference, RegistrationReference: "actual-revocable-group-registration", State: "ACTIVE", Reason: "Approve an exact registered occupant before testing immediate rooming revocation"}
	op := roster
	roster.ApprovalID = c.approval("COMMERCIAL_PMS_ROOMING_CHANGE", "pms:approve", "", nil, "", map[string]any{"group_id": g.ID, "command": op})
	c.must(c.owner, "POST", "/groups/"+g.ID.String()+"/rooming-list", roster, 201)
	v.Reservation.Version++
	status := pmsReady(c)
	a := pmsAssign(c, v, status)
	revoked := businesshttp.PMSRoomingCommand{ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: v.Rooms[0].ID, Slot: 1, ExpectedVersion: 1, GuestID: guest.Guest.ID, State: "REVOKED", Reason: "Withdraw the exact planned occupant without restoring an older independent approval"}
	c.must(c.owner, "POST", "/groups/"+g.ID.String()+"/rooming-list", revoked, 201)
	check := businesshttp.PMSCheckInCommand{AssignmentID: a.ID, AssignmentVersion: a.Version, ReservationVersion: v.Reservation.Version + 1, RoomStatusVersion: status.Version, GuestID: guest.Guest.ID, RegistrationReference: "actual-group-registration-attempt", Reason: "Current revocation must deny physical check-in despite retained prior approval evidence"}
	c.must(c.owner, "POST", "/reservations/"+v.Reservation.ID.String()+"/check-in", check, 409)
	var count int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM stay_records WHERE reservation_id=$1`, v.Reservation.ID).Scan(&count); e != nil || count != 0 {
		t.Fatalf("revoked roster admitted a stay %d %v", count, e)
	}
}
