package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestBusinessPMSOfflineStaleAssignmentDurableTaskAndRealReplay(t *testing.T) {
	c := newCommercialHTTP(t)
	v, _ := pmsConfirmed(c)
	status := pmsReady(c)
	assignment := businesshttp.PMSAssignmentCommand{ReservationID: v.Reservation.ID, ReservationVersion: v.Reservation.Version, ReservationRoomID: v.Rooms[0].ID, RoomID: c.unit.ID, RoomStatusVersion: status.Version - 1, Slot: 1, Reason: "Apply a real offline physical assignment only under its current inspected version"}
	in := businesshttp.PMSOfflineCommand{PropertyID: c.property.ID, ClientOperationID: ids.New(), OriginalActionAt: time.Now().Add(-time.Hour), Action: "ASSIGN_ROOM", TargetID: v.Reservation.ID, Assignment: &assignment, Reason: "Reconcile a queued client command against live server state and current membership"}
	result := commercialData[businesshttp.PMSOfflineResult](t, c.must(c.owner, "POST", "/front-desk/offline-operations", in, 202))
	if result.State != "RECONCILIATION_REQUIRED" || result.TaskID == nil || result.FailureCode != "PHYSICAL_ROOM_UNAVAILABLE" {
		t.Fatalf("stale replay fabricated physical success %+v", result)
	}
	again := commercialData[businesshttp.PMSOfflineResult](t, c.must(c.owner, "POST", "/front-desk/offline-operations", in, 202))
	if again.ID != result.ID {
		t.Fatal("stable client operation created duplicate reconciliation work")
	}
	var assignments int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM room_assignments WHERE reservation_id=$1`, v.Reservation.ID).Scan(&assignments); e != nil || assignments != 0 {
		t.Fatalf("failed offline operation left a partial assignment %d %v", assignments, e)
	}
	assignment.RoomStatusVersion = status.Version
	c.must(c.owner, "POST", "/front-desk/offline-operations", in, 409)
	in.ClientOperationID = ids.New()
	applied := commercialData[businesshttp.PMSOfflineResult](t, c.must(c.owner, "POST", "/front-desk/offline-operations", in, 201))
	if applied.State != "APPLIED" || applied.TaskID != nil {
		t.Fatalf("valid current assignment not actually applied %+v", applied)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM room_assignments WHERE reservation_id=$1`, v.Reservation.ID).Scan(&assignments); e != nil || assignments != 1 {
		t.Fatalf("replay omitted or duplicated actual physical assignment %d %v", assignments, e)
	}
	c.must(c.owner, "POST", "/front-desk/reconciliation-tasks/"+result.TaskID.String()+"/resolve", businesshttp.PMSOfflineResolution{Disposition: "CANCELLED", EvidenceReference: "reviewed-replacement-operation-reference", Reason: "Cancel the obsolete stale operation after an independently retried valid assignment"}, 201)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE offline_operation_receipts SET state='APPLIED' WHERE id=$1`, result.ID); e == nil {
		t.Fatal("reconciliation disposition rewrote original failed-operation truth")
	}
}
