package integration

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func reservationPolicyFixture(c *commercialHarness, risk bool) businesshttp.ReservationPolicyView {
	c.t.Helper()
	in := businesshttp.ReservationPolicyCommand{PropertyID: c.property.ID, Definition: reservations.Policy{AllowedPaymentPolicyIDs: []ids.ID{reservationPaymentProvider(c).policy.ID}, AllowedSources: []string{"DIRECT", "GROUP", "MARKETPLACE", "OTA", "WALK_IN", "CORPORATE"}, AllowedGuarantees: []string{"PAY_AT_PROPERTY", "CHANNEL_COLLECT"}, MaxGuaranteeMinor: 1000000, HoldSeconds: 600, AllowSourceCancellation: true, RequireRiskClear: risk, CancellationHours: 24, CancellationPenaltyBPS: 5000, NoShowGraceHours: 2, NoShowRetainNights: 1, CheckInLocal: "14:00", CheckOutLocal: "11:00", PolicyReference: "hotel-disposable-fixture:v1"}, EffectiveAt: time.Now().UTC().Add(-time.Minute), Reason: "Explicit reviewed reservation acceptance policy"}
	op := in
	op.ApprovalID = ids.Nil
	in.ApprovalID = c.approval("COMMERCIAL_RESERVATION_POLICY_CHANGE", "reservations:approve", "", nil, "", map[string]any{"command": op})
	return commercialData[businesshttp.ReservationPolicyView](c.t, c.must(c.owner, "POST", "/reservation-policies", in, 201))
}
func reservationCommandFixture(c *commercialHarness, guarantee bool) reservations.Create {
	q := c.quote()
	in := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", SourceReference: "direct-booker:fixture", State: "HOLD", PaymentResponsibility: "PLATFORM_COLLECT", Reason: "Accept quoted physical inventory atomically", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}, Guests: []reservations.GuestCommand{{SourceKind: "DIRECT", SourceReference: "business-guest:fixture", IsPrimary: true}}}
	if guarantee {
		in.PaymentResponsibility = "PAY_AT_PROPERTY"
		op := in
		in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"command": op})
	}
	return in
}
func TestBusinessReservationHoldConfirmCancelConservesStockAndHistory(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	in := reservationCommandFixture(c, true)
	key := ids.New().String()
	status, raw := c.call(c.owner, http.MethodPost, "/reservations", in, map[string]string{"Idempotency-Key": key})
	if status != 201 {
		t.Fatalf("create %d %s", status, raw)
	}
	created := commercialData[reservations.Detail](t, raw)
	status, replay := c.call(c.owner, http.MethodPost, "/reservations", in, map[string]string{"Idempotency-Key": key})
	if status != 201 || commercialData[reservations.Detail](t, replay).Reservation.ID != created.Reservation.ID {
		t.Fatalf("replay %d %s", status, replay)
	}
	room := created.Rooms[0]
	if room.HoldID == nil {
		t.Fatal("owning hold missing")
	}
	c.must(c.owner, "DELETE", "/holds/"+room.HoldID.String(), businesshttp.CommercialHoldChange{Version: 1, Reason: "Attempt to steal an accepted reservation hold"}, 409)
	command := reservations.Command{Version: created.Reservation.Version, Reason: "Confirm independently approved pay-at-property guarantee"}
	confirmed := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", command, 200))
	if confirmed.Reservation.State != "CONFIRMED" {
		t.Fatalf("state %s", confirmed.Reservation.State)
	}
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Committed != 1 || day.Held != 0 || day.Available != 0 {
			t.Fatalf("hold conversion double-counted or lost stock: %+v", day)
		}
	}
	// A stale command cannot create a second commitment or history version.
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", command, 409)
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE reservation_rooms SET amount_minor=0,version=version+1 WHERE id=$1`, room.ID); e == nil {
		t.Fatal("accepted price mutable")
	}
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE reservations SET reason='Attempt an unaudited lifecycle write',version=version+1 WHERE id=$1`, created.Reservation.ID); e == nil {
		t.Fatal("reservation changed without matching history/audit/outbox")
	}
	outsider := c.user()
	c.must(outsider, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 404)
	cancelled := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/cancel", reservations.Command{Version: confirmed.Reservation.Version, Reason: "Cancel the guest booking and release exact stock"}, 200))
	if cancelled.Reservation.State != "CANCELLED" {
		t.Fatal("cancel state missing")
	}
	days, e = inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Committed != 0 || day.Held != 0 || day.Available != 1 {
			t.Fatalf("cancellation stock %+v", day)
		}
	}
	var versions, events int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_versions WHERE reservation_id=$1`, created.Reservation.ID).Scan(&versions); e != nil {
		t.Fatal(e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type IN ('reservation.created.v1','reservation.modified.v1','reservation.cancelled.v1')`, created.Reservation.ID.String()).Scan(&events); e != nil {
		t.Fatal(e)
	}
	if versions != 3 || events != 3 {
		t.Fatalf("history/events %d/%d", versions, events)
	}
}
func TestBusinessReservationConcurrentAcceptanceCannotDoubleBook(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	in := reservationCommandFixture(c, false)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Go(func() { code, _ := c.call(c.owner, "POST", "/reservations", in, nil); statuses <- code })
	}
	wg.Wait()
	close(statuses)
	success, conflict := 0, 0
	for code := range statuses {
		switch code {
		case 201:
			success++
		case 409:
			conflict++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("accepted=%d conflicts=%d", success, conflict)
	}
	var roots, rooms, prices int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM reservations),(SELECT count(*) FROM reservation_rooms),(SELECT count(*) FROM reservation_price_snapshots)`).Scan(&roots, &rooms, &prices); e != nil {
		t.Fatal(e)
	}
	if roots != 1 || rooms != 1 || prices != 1 {
		t.Fatalf("failed command leaked rows %d %d %d", roots, rooms, prices)
	}
}
func TestBusinessReservationUnpaidRiskAndNoShowFailClosed(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, true)
	in := reservationCommandFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: created.Reservation.Version, Reason: "Unpaid and unreviewed attempt must fail closed"}, 409)
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/no-show", reservations.Command{Version: created.Reservation.Version, Reason: "Cannot declare a future unconfirmed guest absent"}, 409)
	after := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if after.Reservation.Version != created.Reservation.Version || after.Reservation.State != "HOLD" {
		t.Fatal("failed commands changed accepted reservation")
	}
}

func TestBusinessReservationWaitlistOfferAcceptanceAndExpiry(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	entry := commercialData[businesshttp.ReservationWaitlist](t, c.must(c.owner, "POST", "/waitlist", businesshttp.ReservationWaitlist{PropertyID: c.property.ID, ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1, GuestReference: "direct-waitlisted-fixture", ExpiresAt: time.Now().Add(time.Hour), Reason: "Wait for canonical availability"}, 201))
	q := c.quote()
	offer := commercialData[businesshttp.ReservationWaitlistOffer](t, c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/offers", businesshttp.ReservationWaitlistOfferCommand{Version: entry.Version, QuoteID: q.QuoteID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, Reason: "Offer the newly available canonical room nights"}, 201))
	booking := reservations.Create{PropertyID: c.property.ID, Source: "DIRECT", State: "HOLD", PaymentResponsibility: "PLATFORM_COLLECT", Reason: "Accept the exact scoped waitlist offer", Rooms: []reservations.RoomCommand{{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: q.QuoteID, HoldID: &offer.HoldID, HoldVersion: 1, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}}, Guests: []reservations.GuestCommand{{SourceKind: "DIRECT", SourceReference: entry.GuestReference, IsPrimary: true}}}
	accepted := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/accept", businesshttp.ReservationWaitlistAccept{Version: entry.Version + 1, OfferID: offer.ID, OfferVersion: offer.Version, Booking: booking}, 201))
	c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/accept", businesshttp.ReservationWaitlistAccept{Version: entry.Version + 1, OfferID: offer.ID, OfferVersion: offer.Version, Booking: booking}, 409)
	if accepted.Rooms[0].HoldID == nil || *accepted.Rooms[0].HoldID != offer.HoldID {
		t.Fatal("waitlist acceptance manufactured different stock")
	}
	realNow := time.Now().UTC()
	c.domain.Auth.Service.Now = func() time.Time { return realNow.Add(11 * time.Minute) }
	if e := c.domain.ProcessReservationMaintenance(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, accepted.Reservation.ID).Scan(&state); e != nil || state != "EXPIRED" {
		t.Fatalf("reservation expiry %s %v", state, e)
	}
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", realNow.Add(11*time.Minute), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Available != 1 || day.Held != 0 || day.Committed != 0 {
			t.Fatalf("expired offer lost inventory %+v", day)
		}
	}
}

func TestBusinessReservationAmendmentRollbackPreservesAcceptedPriceAndStock(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/quotes", businesshttp.ReservationAmendmentQuote{Version: created.Reservation.Version, Quote: businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}}, 201))
	room := reservations.RoomCommand{ProductID: q.ProductID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1}
	invalid := reservations.Command{Version: created.Reservation.Version, Reason: "An oversized amendment must roll back every allocation", Rooms: []reservations.RoomCommand{room, room}}
	exposure := 2 * q.AmountMinor
	op := invalid
	invalid.ApprovalID = c.approval("COMMERCIAL_RESERVATION_CHANGE", "reservations:approve", "", &exposure, q.Currency, map[string]any{"reservation_id": created.Reservation.ID, "command": op})
	c.must(c.owner, "PATCH", "/reservations/"+created.Reservation.ID.String(), invalid, 409)
	original := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if original.Reservation.Version != created.Reservation.Version || len(original.Rooms) != 1 || original.Rooms[0].State != "ACTIVE" {
		t.Fatal("failed amendment altered the accepted contract")
	}
	oldHold, e := inventory.GetHold(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, *created.Rooms[0].HoldID)
	if e != nil || oldHold.State != "ACTIVE" {
		t.Fatalf("old hold lost on rollback %+v %v", oldHold, e)
	}
	valid := reservations.Command{Version: created.Reservation.Version, Reason: "Replace the full accepted stay at its new frozen quote", Rooms: []reservations.RoomCommand{room}}
	op = valid
	valid.ApprovalID = c.approval("COMMERCIAL_RESERVATION_CHANGE", "reservations:approve", "", &q.AmountMinor, q.Currency, map[string]any{"reservation_id": created.Reservation.ID, "command": op})
	amended := commercialData[reservations.Detail](t, c.must(c.owner, "PATCH", "/reservations/"+created.Reservation.ID.String(), valid, 200))
	if len(amended.Rooms) != 2 || amended.Reservation.Version != created.Reservation.Version+1 {
		t.Fatal("amendment did not append a price/room version")
	}
	var snapshots int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM reservation_price_snapshots WHERE reservation_id=$1`, created.Reservation.ID).Scan(&snapshots); e != nil || snapshots != 2 {
		t.Fatalf("immutable snapshots %d %v", snapshots, e)
	}
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Held != 1 || day.Available != 0 || day.Committed != 0 {
			t.Fatalf("amendment did not conserve stock %+v", day)
		}
	}
}
func TestBusinessReservationSplitMergeConservesRoomPriceAndFinancialReferences(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	start, _ := time.Parse(time.DateOnly, c.dates.DateFrom)
	middle := start.AddDate(0, 0, 1).Format(time.DateOnly)
	commands := []reservations.RoomCommand{}
	total := int64(0)
	for _, dates := range []inventory.DateRange{{DateFrom: c.dates.DateFrom, DateTo: middle}, {DateFrom: middle, DateTo: c.dates.DateTo}} {
		q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: dates.DateFrom, DateTo: dates.DateTo, Adults: 2, Quantity: 1}, 201))
		total += q.AmountMinor
		commands = append(commands, reservations.RoomCommand{ProductID: q.ProductID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1})
	}
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservations.Create{PropertyID: c.property.ID, Source: "GROUP", State: "HOLD", PaymentResponsibility: "PLATFORM_COLLECT", GroupName: "Two independently movable room lines", Rooms: commands, Reason: "Accept a canonical multi-line group stay"}, 201))
	move := created.Rooms[0]
	split := reservations.Command{Version: created.Reservation.Version, RoomIDs: []ids.ID{move.ID}, Reason: "Split one priced room line into its own reservation"}
	op := split
	split.ApprovalID = c.approval("COMMERCIAL_RESERVATION_LINEAGE", "reservations:approve", "", &move.AmountMinor, move.Currency, map[string]any{"reservation_id": created.Reservation.ID, "action": "SPLIT", "command": op})
	type lineageResult struct {
		Source reservations.Reservation `json:"source"`
		Target reservations.Detail      `json:"target"`
	}
	divided := commercialData[lineageResult](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/split", split, 200))
	if divided.Source.AmountMinor+divided.Target.Reservation.AmountMinor != total || divided.Target.Reservation.Version != 1 || divided.Target.Rooms[0].FinancialGroupID != move.FinancialGroupID || divided.Target.Rooms[0].QuoteID != move.QuoteID {
		t.Fatal("split manufactured price/financial obligation")
	}
	merge := reservations.Command{Version: divided.Target.Reservation.Version, TargetReservationID: &divided.Source.ID, TargetVersion: divided.Source.Version, RoomIDs: []ids.ID{move.ID}, Reason: "Merge the same priced room back into its original group"}
	op = merge
	merge.ApprovalID = c.approval("COMMERCIAL_RESERVATION_LINEAGE", "reservations:approve", "", &move.AmountMinor, move.Currency, map[string]any{"reservation_id": divided.Target.Reservation.ID, "action": "MERGED", "command": op})
	merged := commercialData[lineageResult](t, c.must(c.owner, "POST", "/reservations/"+divided.Target.Reservation.ID.String()+"/merge", merge, 200))
	if merged.Source.AmountMinor != 0 || merged.Source.AllocationState != "MERGED_SOURCE" || merged.Target.Reservation.AmountMinor != total || len(merged.Target.Rooms) != 2 {
		t.Fatal("merge did not conserve canonical room ownership")
	}
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Held != 1 || day.Committed != 0 {
			t.Fatalf("lineage changed stock %+v", day)
		}
	}
	var groups, prices int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM reservation_financial_groups),(SELECT count(*) FROM reservation_price_snapshots)`).Scan(&groups, &prices); e != nil || groups != 1 || prices != 2 {
		t.Fatalf("lineage duplicated obligations/snapshots %d/%d %v", groups, prices, e)
	}
}

func TestBusinessReservationRestrictionAllowsScopedProtectiveExitOnly(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	in := reservationCommandFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	staff := c.user()
	c.join(staff, c.owner, []string{"reservations:read"})
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE organisations SET state='RESTRICTED' WHERE id=$1`, c.owner.registered.OrganisationID); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/reservations", in, 403)
	command := reservations.Command{Version: created.Reservation.Version, Reason: "Release an accepted hold under organisation restriction"}
	c.must(staff, "POST", "/reservations/"+created.Reservation.ID.String()+"/cancel", command, 403)
	cancelled := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/cancel", command, 200))
	if cancelled.Reservation.State != "CANCELLED" {
		t.Fatal("protective exit failed")
	}
	c.must(c.owner, "POST", "/reservations", in, 403)
}

func TestBusinessReservationCommandCenterScopesBeforePaginationAndUsesCivilDay(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", reservationCommandFixture(c, false), 201))
	type list struct {
		Items      []json.RawMessage `json:"items"`
		NextOffset int               `json:"next_offset"`
	}
	for _, v := range []struct {
		view, day string
		count     int
	}{{"pending", "", 1}, {"arrivals", created.Reservation.DateFrom, 1}, {"arrivals", created.Reservation.DateTo, 0}, {"departures", created.Reservation.DateTo, 0}, {"cancellations", "", 0}, {"modifications", "", 0}, {"exceptions", "", 0}, {"waitlist", "", 0}, {"conflicts", "", 0}} {
		path := "/reservation-command-center?view=" + v.view + "&day=" + v.day
		out := commercialData[list](t, c.must(c.owner, "GET", path, nil, 200))
		if len(out.Items) != v.count {
			t.Fatalf("view %s count %d want %d", v.view, len(out.Items), v.count)
		}
	}
	c.must(c.owner, "GET", "/reservation-command-center?view=made-up", nil, 400)
	limited := c.user()
	c.join(limited, c.owner, []string{"reservations:read"})
	otherBranch := c.inventoryBranch(c.owner)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET scope_kind='BRANCH',scope_id=$3 WHERE organisation_id=$1 AND membership_id=(SELECT id FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2) AND capability='reservations:read'`, c.owner.registered.OrganisationID, limited.registered.AccountID, otherBranch); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/reservations?limit=1", "/reservation-command-center?view=pending&limit=1"} {
		out := commercialData[list](t, c.must(limited, "GET", path, nil, 200))
		if len(out.Items) != 0 || out.NextOffset != 0 {
			t.Fatalf("unauthorised property leaked through pagination: %+v", out)
		}
	}
	c.must(limited, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 403)
}

func TestBusinessReservationNoShowChecksEveryFrozenRoomArrival(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	start, _ := time.Parse(time.DateOnly, c.dates.DateFrom)
	middle := start.AddDate(0, 0, 1).Format(time.DateOnly)
	commands := []reservations.RoomCommand{}
	amount := int64(0)
	for _, dates := range []inventory.DateRange{{DateFrom: c.dates.DateFrom, DateTo: middle}, {DateFrom: middle, DateTo: c.dates.DateTo}} {
		q := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: dates.DateFrom, DateTo: dates.DateTo, Adults: 2, Quantity: 1}, 201))
		amount += q.AmountMinor
		commands = append(commands, reservations.RoomCommand{ProductID: q.ProductID, QuoteID: q.QuoteID, ProductVersion: q.ProductVersion, PoolVersion: q.PoolVersion, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1})
	}
	in := reservations.Create{PropertyID: c.property.ID, Source: "GROUP", State: "HOLD", PaymentResponsibility: "PAY_AT_PROPERTY", GroupName: "Independent arrival dates", Rooms: commands, Reason: "Accept separate room-level arrival and departure dates"}
	op := in
	in.GuaranteeApprovalID = c.approval("COMMERCIAL_RESERVATION_GUARANTEE", "reservations:approve", "", &amount, "NGN", map[string]any{"command": op})
	created := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations", in, 201))
	confirmed := commercialData[reservations.Detail](t, c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/confirm", reservations.Command{Version: created.Reservation.Version, Reason: "Confirm every independently guaranteed accepted room"}, 200))
	zone, e := time.LoadLocation(confirmed.Reservation.PolicySnapshot.AcceptanceTimezone)
	if e != nil {
		t.Fatal(e)
	}
	firstArrival, e := time.ParseInLocation("2006-01-02 15:04", c.dates.DateFrom+" 16:01", zone)
	if e != nil {
		t.Fatal(e)
	}
	c.service.Now = func() time.Time { return firstArrival }
	c.must(c.owner, "POST", "/reservations/"+created.Reservation.ID.String()+"/no-show", reservations.Command{Version: confirmed.Reservation.Version, Reason: "Second room arrival has not passed its own no-show deadline"}, 409)
	current := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+created.Reservation.ID.String(), nil, 200))
	if current.Reservation.Version != confirmed.Reservation.Version || current.Reservation.State != "CONFIRMED" {
		t.Fatal("early no-show modified a future room")
	}
}

func TestBusinessReservationWaitlistRejectionIsConcurrentSafeAndAudited(t *testing.T) {
	c := newCommercialHTTP(t)
	reservationPolicyFixture(c, false)
	entry := commercialData[businesshttp.ReservationWaitlist](t, c.must(c.owner, "POST", "/waitlist", businesshttp.ReservationWaitlist{PropertyID: c.property.ID, ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1, GuestReference: "direct-rejected-offer-reference", ExpiresAt: time.Now().Add(time.Hour), Reason: "Retain a waiting request after an unsuitable offer"}, 201))
	q := c.quote()
	offer := commercialData[businesshttp.ReservationWaitlistOffer](t, c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/offers", businesshttp.ReservationWaitlistOfferCommand{Version: entry.Version, QuoteID: q.QuoteID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, Reason: "Offer real canonical stock to the original waiting guest"}, 201))
	command := businesshttp.ReservationWaitlistRejection{Version: entry.Version + 1, OfferVersion: offer.Version, ContinueWaiting: true, Reason: "Guest declined this offer and asked to remain on the waitlist"}
	path := "/waitlist/" + entry.ID.String() + "/offers/" + offer.ID.String() + "/reject"
	limited := c.user()
	c.join(limited, c.owner, []string{"reservations:read"})
	c.must(limited, "POST", path, command, 403)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); status, _ := c.call(c.owner, "POST", path, command, nil); statuses <- status }()
	}
	wg.Wait()
	close(statuses)
	ok, conflict := 0, 0
	for status := range statuses {
		switch status {
		case 200:
			ok++
		case 409:
			conflict++
		default:
			t.Fatalf("rejection status %d", status)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("duplicate rejection accepted %d/%d", ok, conflict)
	}
	var state string
	var version int64
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state,version FROM waitlist_entries WHERE id=$1`, entry.ID).Scan(&state, &version); e != nil || state != "WAITING" || version != 3 {
		t.Fatalf("waitlist state %s/%d %v", state, version, e)
	}
	var audits, events int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='WAITLIST_OFFER_REJECTED'),(SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='reservation.waitlist.changed.v1')`, entry.ID).Scan(&audits, &events); e != nil || audits != 1 || events != 3 {
		t.Fatalf("non-atomic waitlist history %d/%d %v", audits, events, e)
	}
	days, e := inventory.Calendar(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.pool.ID, c.dates, c.property.Timezone, "", time.Now(), false)
	if e != nil {
		t.Fatal(e)
	}
	for _, day := range days {
		if day.Held != 0 || day.Available != 1 {
			t.Fatalf("rejected offer retained inventory %+v", day)
		}
	}
	q = c.quote()
	second := commercialData[businesshttp.ReservationWaitlistOffer](t, c.must(c.owner, "POST", "/waitlist/"+entry.ID.String()+"/offers", businesshttp.ReservationWaitlistOfferCommand{Version: version, QuoteID: q.QuoteID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, Reason: "A new offer revalidates the released canonical stock"}, 201))
	c.service.Now = func() time.Time { return second.ExpiresAt.Add(time.Second) }
	if e = c.domain.ProcessReservationMaintenance(t.Context(), 20); e != nil {
		t.Fatal(e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT state FROM waitlist_entries WHERE id=$1`, entry.ID).Scan(&state); e != nil || state != "WAITING" {
		t.Fatalf("offer expiry removed the waiting request %s %v", state, e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='reservation.waitlist.changed.v1'`, entry.ID).Scan(&events); e != nil || events != 5 {
		t.Fatalf("expiry lost its event %d %v", events, e)
	}
}
