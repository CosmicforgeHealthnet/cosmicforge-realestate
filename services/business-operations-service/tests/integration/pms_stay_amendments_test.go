package integration

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// A guaranteed in-house extension commits new canonical stock, a contiguous
// extension line, a continuation assignment of the same room, an accepted
// folio posting with fiscal detail and a revoke/re-issue access request. An
// exact idempotent replay returns the original result without new effects.
func TestBusinessPMSStayExtensionValidAvailabilityAndReplay(t *testing.T) {
	x := newPMSAmendFixture(t, false, 1)
	c := x.c
	before := x.stay(0)
	cmd, q := x.extension(0, 2, "RESERVATION_TERMS")
	path := "/stays/" + x.stays[0].String() + "/extensions"
	c.must(c.owner, "POST", path, cmd, 403) // a guaranteed share needs an exact independent approval
	cmd = x.approveExtension(0, cmd, q.AmountMinor)
	key := map[string]string{"Idempotency-Key": "stay-extension-" + ids.New().String()}
	code, raw := c.call(c.owner, "POST", path, cmd, key)
	if code != 201 {
		t.Fatalf("extension %d %s", code, raw)
	}
	out := commercialData[businesshttp.PMSStayExtensionResult](t, raw)
	if out.DateFrom != before.Assignment.DateTo || out.DateTo != cmd.DateTo || out.AmountMinor != q.AmountMinor || len(out.Payers) != 1 || out.Payers[0].Responsibility != "PAY_AT_PROPERTY" || out.Payers[0].FinancialState != "APPROVED_GUARANTEE" || out.CollectionRequired || out.AccessState != "UNKNOWN" {
		t.Fatalf("extension result %+v", out)
	}
	code, again := c.call(c.owner, "POST", path, cmd, key)
	if code != 201 || !bytes.Equal(raw, again) {
		t.Fatalf("idempotent replay %d %s", code, again)
	}
	if n := x.count(`SELECT count(*) FROM stay_extensions WHERE stay_id=$1`, x.stays[0]); n != 1 {
		t.Fatalf("replay duplicated extension %d", n)
	}
	// A stale retry with a new key is rejected against the current versions.
	c.must(c.owner, "POST", path, cmd, 409)

	after := x.stay(0)
	if after.Assignment.ID != out.AssignmentID || after.Assignment.DateTo != cmd.DateTo || after.Assignment.RoomID != before.Assignment.RoomID || after.Assignment.State != "OCCUPIED" || after.Stay.Version != before.Stay.Version+1 {
		t.Fatalf("continuation assignment %+v", after.Assignment)
	}
	for _, day := range amendDays(out.DateFrom, out.DateTo) {
		committed, provenance := x.stockState(day)
		if committed != 1 || provenance != 1 {
			t.Fatalf("extension night %s committed %d provenance %d", day, committed, provenance)
		}
		if n := x.count(`SELECT count(*) FROM room_assignment_days WHERE assignment_id=$1 AND day=$2::date AND released_at IS NULL`, out.AssignmentID, day); n != 1 {
			t.Fatalf("physical night %s not held by continuation", day)
		}
	}
	if n := x.count(`SELECT count(*) FROM room_assignment_days WHERE assignment_id=$1 AND released_at IS NULL`, before.Assignment.ID); n != 0 {
		t.Fatalf("source assignment still holds %d future nights", n)
	}
	var accommodation, extensionItem int64
	for _, item := range after.Items {
		if item.Kind == "ACCOMMODATION" {
			accommodation += item.AmountMinor
			if item.ID == out.FolioItemID {
				extensionItem = item.AmountMinor
			}
		}
	}
	if extensionItem != q.AmountMinor || after.BalanceMinor != before.BalanceMinor+q.AmountMinor {
		t.Fatalf("extension folio posting %d balance %d->%d", extensionItem, before.BalanceMinor, after.BalanceMinor)
	}
	if n := x.count(`SELECT count(*) FROM folio_fiscal_reviews WHERE folio_item_id=$1 AND state='ITEMIZED'`, out.FolioItemID); n != 1 {
		t.Fatal("extension posting lacks accepted fiscal detail")
	}
	if n := x.count(`SELECT count(*) FROM access_grant_references WHERE (assignment_id=$1 AND operation='REVOKE') OR (assignment_id=$2 AND operation='ISSUE')`, before.Assignment.ID, out.AssignmentID); n != 2 {
		t.Fatalf("access entitlement not re-issued for the new window: %d", n)
	}
	v := commercialData[reservations.Detail](t, c.must(c.owner, "GET", "/reservations/"+x.v.Reservation.ID.String(), nil, 200))
	if v.Reservation.DateTo != cmd.DateTo || v.Reservation.AmountMinor != x.v.Reservation.AmountMinor+q.AmountMinor || v.Reservation.State != "CHECKED_IN" || v.Reservation.FinancialState != "APPROVED_GUARANTEE" {
		t.Fatalf("reservation after extension %+v", v.Reservation)
	}
	if n := x.count(`SELECT count(*) FROM reservation_versions WHERE reservation_id=$1 AND action='STAY_EXTENDED'`, x.v.Reservation.ID); n != 1 {
		t.Fatal("extension lacks append-only reservation history")
	}
	if n := x.count(`SELECT count(*) FROM outbox_events WHERE event_type IN ('hospitality.stay.extended.v1','hospitality.stay.extension.accepted.v1')`); n != 2 {
		t.Fatalf("extension events %d", n)
	}
	list := commercialData[struct{ Items []json.RawMessage `json:"items"` }](t, c.must(c.owner, "GET", path, nil, 200))
	if len(list.Items) != 1 {
		t.Fatalf("extension list %+v", list)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE stay_extensions SET date_to=date_to+1 WHERE id=$1`, out.ExtensionID); e == nil {
		t.Fatal("extension history was mutable")
	}

	// The extended stay later departs normally; the extension line is not an
	// additional unit, so the reservation completes its in-house phase.
	d := x.stay(0)
	checkout := businesshttp.PMSCheckoutCommand{StayID: x.stays[0], Version: d.Stay.Version, ReservationVersion: x.resVersion(), FolioVersion: d.Folio.Version, Reason: "Record actual departure and retain the guaranteed balance for settlement"}
	op := checkout
	checkout.ApprovalID = c.approval("COMMERCIAL_PMS_DEPARTURE_DEBT", "pms:approve", "", &d.BalanceMinor, d.Folio.Currency, map[string]any{"stay_id": x.stays[0], "command": op})
	c.must(c.owner, "POST", "/reservations/"+x.v.Reservation.ID.String()+"/check-out", checkout, 200)
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM reservations WHERE id=$1`, x.v.Reservation.ID).Scan(&state); e != nil || state != "CHECKED_OUT" {
		t.Fatalf("extended reservation after departure %s %v", state, e)
	}
}

var _ = json.Marshal
var _ sync.Mutex
