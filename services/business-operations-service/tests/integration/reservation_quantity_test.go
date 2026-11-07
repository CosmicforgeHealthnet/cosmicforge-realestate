package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// quantityStock opens n actual canonical physical rooms as one pool/product.
func quantityStock(c *commercialHarness, n int) []portfolio.Node {
	c.t.Helper()
	suffix := ids.New().String()[24:]
	category := c.commercialNode("/room-types", map[string]any{"parent_id": c.property.ID, "code": "QTY-" + suffix, "name": "Quantity test category", "attributes": map[string]any{"capacity": 2, "beds": 1}})
	rooms := []portfolio.Node{}
	members := []ids.ID{}
	for i := range n {
		code := fmt.Sprintf("QTY-%s-%d", suffix, i+1)
		rooms = append(rooms, c.commercialNode("/rooms", map[string]any{"parent_id": c.property.ID, "room_type_id": category.ID, "code": code, "name": code, "attributes": map[string]any{"capacity": 2}}))
	}
	category = c.activateNode(category)
	for i := range rooms {
		rooms[i] = c.activateNode(rooms[i])
		members = append(members, rooms[i].ID)
	}
	pool := commercialData[inventory.Pool](c.t, c.must(c.owner, "POST", "/inventory-pools", businesshttp.CommercialPoolCreate{PropertyID: c.property.ID, NodeID: category.ID, Name: "Quantity physical rooms", MemberIDs: members}, 201))
	pool = commercialData[inventory.Pool](c.t, c.must(c.owner, "PATCH", "/inventory-pools/"+pool.ID.String(), businesshttp.CommercialStateChange{Version: pool.Version, State: "ACTIVE", Reason: "Activate the actual canonical physical rooms"}, 200))
	product := commercialData[inventory.Product](c.t, c.must(c.owner, "POST", "/sellable-products", businesshttp.CommercialProductCreate{PropertyID: c.property.ID, NodeID: category.ID, PoolID: pool.ID, RatePlanID: &c.rate.ID, Name: "Quantity room nights", MinGuests: 1, MaxGuests: 2, PolicyCode: "QTY-ROOMS", PolicyVersion: 1, Channels: []string{}}, 201))
	product = c.activateProduct(product)
	days, _ := c.dates.Days(c.property.Timezone)
	versions := map[string]int64{}
	for _, d := range days {
		versions[d] = 0
	}
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Capacity: &n, ExpectedVersions: versions, Reason: "Open exactly the existing physical room nights"}, 200)
	c.unit = rooms[0]
	c.pool = pool
	c.product = product
	return rooms
}

// quantityBooking accepts and confirms one multi-unit line under an
// independently approved pay-at-property guarantee.
func quantityBooking(c *commercialHarness, quantity int, source string, guests []reservations.GuestCommand) reservations.Detail {
	c.t.Helper()
	q := commercialData[revenue.QuoteSnapshot](c.t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: quantity}, 201))
	in := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", SourceReference: source, State: "CONFIRMED", PaymentResponsibility: "PAY_AT_PROPERTY", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: quantity}}, Guests: guests, Reason: "Accept a multi-unit booking under its reviewed guarantee"}
	op := in
	in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	v := commercialData[reservations.Detail](c.t, c.must(c.owner, "POST", "/reservations", in, 201))
	if v.Reservation.State != "CONFIRMED" || len(v.Rooms) != 1 || v.Rooms[0].Quantity != quantity {
		c.t.Fatalf("confirmed multi-unit booking %+v", v.Reservation)
	}
	return v
}

func activeLine(t *testing.T, v reservations.Detail) reservations.Room {
	t.Helper()
	var found []reservations.Room
	for _, r := range v.Rooms {
		if r.State == "ACTIVE" {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one active line, got %d", len(found))
	}
	return found[0]
}

// assertStockConservation proves calendar commitment equals the sum of
// unreleased canonical provenance for every date of the pool.
func assertStockConservation(c *commercialHarness, wantCommitted int) {
	c.t.Helper()
	rows, e := c.db.Pool.Query(c.t.Context(), `SELECT c.day::text,c.committed,(SELECT coalesce(sum(d.quantity),0) FROM reservation_inventory_days d WHERE d.organisation_id=c.organisation_id AND d.pool_id=c.pool_id AND d.day=c.day AND d.released_at IS NULL) FROM inventory_calendar c WHERE c.organisation_id=$1 AND c.pool_id=$2 AND c.day>=$3::date AND c.day<$4::date ORDER BY c.day`, c.owner.registered.OrganisationID, c.pool.ID, c.dates.DateFrom, c.dates.DateTo)
	if e != nil {
		c.t.Fatal(e)
	}
	defer rows.Close()
	days := 0
	for rows.Next() {
		var day string
		var committed, provenance int
		if e = rows.Scan(&day, &committed, &provenance); e != nil {
			c.t.Fatal(e)
		}
		days++
		if committed != provenance || committed != wantCommitted {
			c.t.Fatalf("day %s committed=%d provenance=%d want=%d", day, committed, provenance, wantCommitted)
		}
	}
	if days != 2 {
		c.t.Fatalf("calendar days %d", days)
	}
	var violations int
	if e = c.db.Pool.QueryRow(c.t.Context(), `SELECT count(*) FROM reservation_quantity_change_days d JOIN reservation_quantity_changes q ON q.id=d.change_id WHERE q.organisation_id=$1 AND (d.original_quantity<>d.remaining_quantity+d.transferred_quantity+d.released_quantity OR q.original_amount_minor<>q.remaining_amount_minor+q.transferred_amount_minor+q.released_amount_minor)`, c.owner.registered.OrganisationID).Scan(&violations); e != nil || violations != 0 {
		c.t.Fatalf("ledger conservation violations=%d %v", violations, e)
	}
}

func (c *commercialHarness) quantityApproval(v reservations.Reservation, action string, command any, amount int64) ids.ID {
	c.t.Helper()
	return c.approval("COMMERCIAL_RESERVATION_LINEAGE", "reservations:approve", v.Channel, &amount, v.Currency, map[string]any{"reservation_id": v.ID, "action": action, "command": command})
}

func TestBusinessReservationQuantityPartialCancelSplitMergeConservation(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	quantityStock(c, 4)
	v := quantityBooking(c, 4, "direct-group-booker:"+ids.New().String(), []reservations.GuestCommand{{SourceKind: "DIRECT", SourceReference: "booker-a:" + ids.New().String(), IsPrimary: true}, {SourceKind: "DIRECT", SourceReference: "unit-guest-b:" + ids.New().String()}})
	org := c.owner.registered.OrganisationID
	line := activeLine(t, v)
	assertStockConservation(c, 4)
	path := "/reservations/" + v.Reservation.ID.String()

	// Tenant isolation and whole-line guard.
	outsider := c.user()
	c.must(outsider, "POST", path+"/quantity-cancel", businesshttp.ReservationQuantityCommand{Version: v.Reservation.Version, RoomID: line.ID, RoomVersion: line.Version, Quantity: 1, Reason: "Outsider cannot fragment this booking"}, 404)
	c.must(c.owner, "POST", path+"/quantity-cancel", businesshttp.ReservationQuantityCommand{Version: v.Reservation.Version, RoomID: line.ID, RoomVersion: line.Version, Quantity: 4, Reason: "Cancelling every unit uses whole-line cancel"}, 400)

	// Partial cancellation of one unit with idempotent replay.
	cancel := businesshttp.ReservationQuantityCommand{Version: v.Reservation.Version, RoomID: line.ID, RoomVersion: line.Version, Quantity: 1, Reason: "Guest released one of four rooms"}
	key := ids.New().String()
	status, raw := c.call(c.owner, http.MethodPost, path+"/quantity-cancel", cancel, map[string]string{"Idempotency-Key": key})
	if status != 200 {
		t.Fatalf("partial cancel %d %s", status, raw)
	}
	cancelled := commercialData[businesshttp.ReservationQuantityResult](t, raw)
	status, replay := c.call(c.owner, http.MethodPost, path+"/quantity-cancel", cancel, map[string]string{"Idempotency-Key": key})
	if status != 200 || commercialData[businesshttp.ReservationQuantityResult](t, replay).Change.Change.ID != cancelled.Change.Change.ID {
		t.Fatalf("replay %d %s", status, replay)
	}
	c.must(c.owner, "POST", path+"/quantity-cancel", cancel, 409) // stale version with a fresh key
	ch := cancelled.Change.Change
	if ch.Action != "PARTIAL_CANCEL" || ch.OriginalQuantity != 4 || ch.RemainingQuantity != 3 || ch.ReleasedQuantity != 1 || ch.OriginalAmountMinor != line.AmountMinor || ch.RemainingAmountMinor+ch.ReleasedAmountMinor != line.AmountMinor || ch.CancelledRoomID == nil {
		t.Fatalf("partial cancel ledger %+v", ch)
	}
	for _, d := range cancelled.Change.Days {
		if d.OriginalQuantity != 4 || d.RemainingQuantity != 3 || d.ReleasedQuantity != 1 || d.TransferredQuantity != 0 {
			t.Fatalf("partial cancel day %+v", d)
		}
	}
	if cancelled.Reservation.Reservation.AmountMinor != ch.RemainingAmountMinor || cancelled.Reservation.Reservation.State != "CONFIRMED" {
		t.Fatalf("reservation after partial cancel %+v", cancelled.Reservation.Reservation)
	}
	assertStockConservation(c, 3)
	var obligations int
	var accepted int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*),coalesce(sum(accepted_amount_minor),0) FROM reservation_cancellation_obligations WHERE reservation_id=$1 AND kind='PARTIAL_CANCELLED'`, v.Reservation.ID).Scan(&obligations, &accepted); e != nil || obligations != 1 || accepted != ch.ReleasedAmountMinor {
		t.Fatalf("frozen-policy partial obligation %d %d %v", obligations, accepted, e)
	}
	var refunds int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_financial_requests WHERE reservation_id=$1 AND kind='REFUND' AND state='SUCCEEDED'`, v.Reservation.ID).Scan(&refunds); e != nil || refunds != 0 {
		t.Fatalf("refund fabricated %d %v", refunds, e)
	}
	// Accepted lines are never edited in place.
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_rooms SET quantity=2,version=version+1 WHERE id=$1`, ch.RemainingRoomID); e == nil {
		t.Fatal("accepted quantity mutable")
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_quantity_changes SET reason='rewrite the ledger' WHERE id=$1`, ch.ID); e == nil {
		t.Fatal("quantity ledger mutable")
	}

	// Split one unit into a new canonical reservation, moving its unit guest.
	current := cancelled.Reservation
	remainingLine := activeLine(t, current)
	var unitGuest, booker ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT id FROM reservation_guests WHERE reservation_id=$1 AND NOT is_primary),(SELECT id FROM reservation_guests WHERE reservation_id=$1 AND is_primary)`, v.Reservation.ID).Scan(&unitGuest, &booker); e != nil {
		t.Fatal(e)
	}
	split := businesshttp.ReservationQuantityCommand{Version: current.Reservation.Version, RoomID: remainingLine.ID, RoomVersion: remainingLine.Version, Quantity: 1, GuestIDs: []ids.ID{unitGuest}, NewReservation: true, Reason: "Detach one room for a separately settled traveller"}
	tail, _, _ := reservations.TailPartition(remainingLine.AmountMinor, remainingLine.Quantity, 1)
	c.must(c.owner, "POST", path+"/quantity-split", split, 403) // approval required
	split.ApprovalID = c.quantityApproval(current.Reservation, "QUANTITY_SPLIT", func() businesshttp.ReservationQuantityCommand { op := split; op.ApprovalID = ids.Nil; return op }(), tail)
	splitResult := commercialData[businesshttp.ReservationQuantityResult](t, c.must(c.owner, "POST", path+"/quantity-split", split, 200))
	sc := splitResult.Change.Change
	if sc.Action != "SPLIT" || sc.TransferredQuantity != 1 || sc.RemainingQuantity != 2 || sc.TransferredAmountMinor != tail || splitResult.Target == nil || sc.TargetReservationID == v.Reservation.ID {
		t.Fatalf("split ledger %+v", sc)
	}
	child := *splitResult.Target
	if child.Reservation.State != "CONFIRMED" || child.Reservation.ParentReservationID == nil || *child.Reservation.ParentReservationID != v.Reservation.ID || child.Reservation.AmountMinor != tail || activeLine(t, child).Quantity != 1 {
		t.Fatalf("split child %+v", child.Reservation)
	}
	if splitResult.Reservation.Reservation.AmountMinor+child.Reservation.AmountMinor != cancelled.Reservation.Reservation.AmountMinor {
		t.Fatal("split did not conserve accepted amount")
	}
	assertStockConservation(c, 3)
	var movedTo, movedRoom ids.ID
	var copiedBooker int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT reservation_id,room_id,(SELECT count(*) FROM reservation_guests WHERE reservation_id=$2 AND is_primary) FROM reservation_guests WHERE id=$1`, unitGuest, child.Reservation.ID).Scan(&movedTo, &movedRoom, &copiedBooker); e != nil || movedTo != child.Reservation.ID || movedRoom != *sc.TransferredRoomID || copiedBooker != 1 {
		t.Fatalf("guest assignment did not follow detached unit %v %v %d %v", movedTo, movedRoom, copiedBooker, e)
	}
	var lineage int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_lineage WHERE source_reservation_id=$1 AND target_reservation_id=$2 AND action='SPLIT'`, v.Reservation.ID, child.Reservation.ID).Scan(&lineage); e != nil || lineage != 1 {
		t.Fatalf("lineage %d %v", lineage, e)
	}

	// An unrelated booking can never merge into this split family.
	other := quantityBooking(c, 1, "independent-booker:"+ids.New().String(), []reservations.GuestCommand{{SourceKind: "DIRECT", SourceReference: "independent-guest:" + ids.New().String(), IsPrimary: true}})
	parent := splitResult.Reservation
	parentLine := activeLine(t, parent)
	otherLine := activeLine(t, other)
	foreign := businesshttp.ReservationQuantityMergeCommand{Version: parent.Reservation.Version, RoomID: parentLine.ID, RoomVersion: parentLine.Version, SourceReservationID: other.Reservation.ID, SourceVersion: other.Reservation.Version, SourceRoomID: otherLine.ID, SourceRoomVersion: otherLine.Version, Reason: "Attempt to merge an independent source booking"}
	foreign.ApprovalID = c.quantityApproval(parent.Reservation, "QUANTITY_MERGE", foreign, otherLine.AmountMinor)
	c.must(c.owner, "POST", path+"/quantity-merge", foreign, 409)

	// Merge the detached unit back: one line again, same stock and amount.
	childLine := activeLine(t, child)
	merge := businesshttp.ReservationQuantityMergeCommand{Version: parent.Reservation.Version, RoomID: parentLine.ID, RoomVersion: parentLine.Version, SourceReservationID: child.Reservation.ID, SourceVersion: child.Reservation.Version, SourceRoomID: childLine.ID, SourceRoomVersion: childLine.Version, Reason: "Traveller rejoined the original booking"}
	merge.ApprovalID = c.quantityApproval(parent.Reservation, "QUANTITY_MERGE", merge, childLine.AmountMinor)
	merged := commercialData[businesshttp.ReservationQuantityResult](t, c.must(c.owner, "POST", path+"/quantity-merge", merge, 200))
	mc := merged.Change.Change
	mergedLine := activeLine(t, merged.Reservation)
	if mc.Action != "MERGE" || mergedLine.Quantity != 3 || mergedLine.AmountMinor != cancelled.Reservation.Reservation.AmountMinor || merged.Reservation.Reservation.AmountMinor != mergedLine.AmountMinor || merged.Target == nil || merged.Target.Reservation.AllocationState != "MERGED_SOURCE" || merged.Target.Reservation.AmountMinor != 0 {
		t.Fatalf("merge result %+v %+v", mc, mergedLine)
	}
	assertStockConservation(c, 4) // three merged units plus the independent booking
	var guestBack ids.ID
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT room_id FROM reservation_guests WHERE id=$1 AND reservation_id=$2`, unitGuest, v.Reservation.ID).Scan(&guestBack); e != nil || guestBack != mergedLine.ID {
		t.Fatalf("merged unit guest %v %v", guestBack, e)
	}
	var changes int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_quantity_changes WHERE organisation_id=$1`, org).Scan(&changes); e != nil || changes != 3 {
		t.Fatalf("ledger entries %d %v", changes, e)
	}
	list := commercialData[businesshttp.ReservationQuantityChangeList](t, c.must(c.owner, "GET", path+"/quantity-changes", nil, 200))
	if len(list.Items) != 3 || len(list.Items[0].Days) != 2 {
		t.Fatalf("quantity change history %d", len(list.Items))
	}
	var events int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE event_type='reservation.quantity_changed.v1' AND payload->>'organisation_id'=$1`, org.String()).Scan(&events); e != nil || events != 3 {
		t.Fatalf("quantity events %d %v", events, e)
	}
	// Physical assignment blocks fragmentation of that line.
	pmsReadyRoom(c, c.unit)
	assign := businesshttp.PMSAssignmentCommand{ReservationID: v.Reservation.ID, ReservationVersion: merged.Reservation.Reservation.Version, ReservationRoomID: mergedLine.ID, RoomID: c.unit.ID, RoomStatusVersion: 3, Slot: 1, Reason: "Assign the first backed quantity slot"}
	c.must(c.owner, "POST", "/room-assignments", assign, 201)
	after := commercialData[reservations.Detail](t, c.must(c.owner, "GET", path, nil, 200))
	c.must(c.owner, "POST", path+"/quantity-cancel", businesshttp.ReservationQuantityCommand{Version: after.Reservation.Version, RoomID: mergedLine.ID, RoomVersion: mergedLine.Version, Quantity: 1, Reason: "Assigned lines change through front desk"}, 409)
}

func pmsReadyRoom(c *commercialHarness, room portfolio.Node) {
	c.t.Helper()
	cmd := businesshttp.PMSRoomStatusCommand{PropertyID: c.property.ID, RoomID: room.ID, State: "VACANT_DIRTY", EvidenceReference: "disposable-front-desk-inspection-reference", Reason: "Record the actual physical room readiness observation"}
	path := "/room-status/" + room.ID.String()
	c.must(c.owner, "PUT", path, cmd, 200)
	cmd.ExpectedVersion, cmd.State = 1, "VACANT_CLEAN"
	c.must(c.owner, "PUT", path, cmd, 200)
	cmd.ExpectedVersion, cmd.State = 2, "INSPECTED"
	c.must(c.owner, "PUT", path, cmd, 200)
}

func TestBusinessReservationQuantityConcurrentChangesConserveStock(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	quantityStock(c, 6)
	v := quantityBooking(c, 6, "concurrent-booker:"+ids.New().String(), []reservations.GuestCommand{{SourceKind: "DIRECT", SourceReference: "concurrent-guest:" + ids.New().String(), IsPrimary: true}})
	path := "/reservations/" + v.Reservation.ID.String()
	released := 0
	for round := range 2 {
		current := commercialData[reservations.Detail](t, c.must(c.owner, "GET", path, nil, 200))
		line := activeLine(t, current)
		type attempt struct {
			path string
			body businesshttp.ReservationQuantityCommand
			kind string
		}
		attempts := []attempt{}
		for i := range 2 {
			attempts = append(attempts, attempt{path + "/quantity-cancel", businesshttp.ReservationQuantityCommand{Version: current.Reservation.Version, RoomID: line.ID, RoomVersion: line.Version, Quantity: 1, Reason: fmt.Sprintf("Concurrent cancel %d of round %d", i, round)}, "cancel"})
		}
		for i := range 2 {
			split := businesshttp.ReservationQuantityCommand{Version: current.Reservation.Version, RoomID: line.ID, RoomVersion: line.Version, Quantity: 1, NewReservation: true, Reason: fmt.Sprintf("Concurrent split %d of round %d", i, round)}
			tail, _, _ := reservations.TailPartition(line.AmountMinor, line.Quantity, 1)
			split.ApprovalID = c.quantityApproval(current.Reservation, "QUANTITY_SPLIT", split, tail)
			attempts = append(attempts, attempt{path + "/quantity-split", split, "split"})
		}
		var wg sync.WaitGroup
		results := make([]int, len(attempts))
		for i, a := range attempts {
			wg.Go(func() { results[i], _ = c.call(c.owner, http.MethodPost, a.path, a.body, nil) })
		}
		wg.Wait()
		winners := 0
		for i, code := range results {
			switch code {
			case 200:
				winners++
				if attempts[i].kind == "cancel" {
					released++
				}
			case 409:
			default:
				t.Fatalf("round %d unexpected status %d", round, code)
			}
		}
		if winners != 1 {
			t.Fatalf("round %d winners=%d %v", round, winners, results)
		}
		assertStockConservation(c, 6-released)
	}
	var releasedRows int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_quantity_change_days WHERE organisation_id=$1 AND released_quantity>0`, c.owner.registered.OrganisationID).Scan(&releasedRows); e != nil {
		t.Fatal(e)
	}
	if releasedRows != released*2 {
		t.Fatalf("double release: ledger rows %d for %d cancellations", releasedRows, released)
	}
	// Direct provenance rewrites remain forbidden.
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE reservation_inventory_days SET quantity=quantity+1 WHERE organisation_id=$1 AND released_at IS NULL`, c.owner.registered.OrganisationID); e == nil {
		t.Fatal("committed provenance rewritten")
	}
}
