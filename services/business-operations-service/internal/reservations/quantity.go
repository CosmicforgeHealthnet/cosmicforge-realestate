package reservations

import (
	"context"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// TailPartition returns the frozen amount owned by the last k of q quantity
// slots. Check-in partitions a line amount A as base b=A/q with the first A%q
// slots receiving b+1, so detaching the highest slots leaves every remaining
// slot amount unchanged and conserves A exactly.
func TailPartition(amount int64, quantity, k int) (tail, remaining int64, err error) {
	if amount < 0 || quantity < 1 || k < 1 || k > quantity {
		return 0, 0, apperrors.InvalidRequest("A bounded quantity of an accepted line is required.")
	}
	q := int64(quantity)
	base, extra := amount/q, amount%q
	tail = int64(k)*base + max(0, extra-(q-int64(k)))
	return tail, amount - tail, nil
}

// QuantityDay is one committed civil date of a line, read under the calendar
// and provenance row locks.
type QuantityDay struct {
	Day         string
	PoolID      ids.ID
	Quantity    int
	AllotmentID *ids.ID
	Allotted    int
}

// LockQuantityDays locks the line's committed provenance and the calendar rows
// in calendar order. Quantity operations require a fully backed, unfragmented
// line: every accepted night unreleased at exactly the line quantity with no
// outstanding overbooking. Anything else needs reconciliation first.
func LockQuantityDays(ctx context.Context, tx pgx.Tx, org ids.ID, room Room) ([]QuantityDay, error) {
	rows, e := tx.Query(ctx, `SELECT d.day::text,d.pool_id,d.quantity,d.allotment_id,d.allotted_quantity,d.overbooked_quantity FROM reservation_inventory_days d JOIN inventory_calendar c ON c.organisation_id=d.organisation_id AND c.pool_id=d.pool_id AND c.day=d.day WHERE d.organisation_id=$1 AND d.room_id=$2 AND d.released_at IS NULL ORDER BY d.day FOR UPDATE OF c,d`, org, room.ID)
	if e != nil {
		return nil, e
	}
	out := []QuantityDay{}
	fragmented := false
	for rows.Next() {
		var d QuantityDay
		var overbooked int
		if e = rows.Scan(&d.Day, &d.PoolID, &d.Quantity, &d.AllotmentID, &d.Allotted, &overbooked); e != nil {
			break
		}
		if d.Quantity != room.Quantity || overbooked != 0 || d.PoolID != room.PoolID {
			fragmented = true
		}
		out = append(out, d)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if rowErr != nil {
		return nil, rowErr
	}
	expected, e := (inventory.DateRange{DateFrom: room.DateFrom, DateTo: room.DateTo}).Days("UTC")
	if e != nil {
		return nil, e
	}
	if fragmented || len(out) != len(expected) {
		return nil, apperrors.Conflict("RESERVATION_QUANTITY_RECONCILIATION_REQUIRED", "Every accepted night must be committed at the exact line quantity with no outstanding overbooking or partial release.")
	}
	for i := range out {
		if out[i].Day != expected[i] {
			return nil, apperrors.Conflict("RESERVATION_QUANTITY_RECONCILIATION_REQUIRED", "Committed provenance must cover exactly the accepted civil dates.")
		}
	}
	return out, nil
}

// ReleaseProvenance retires the source line's committed rows one way. The
// calendar is changed only by the caller for genuinely released quantity.
func ReleaseProvenance(ctx context.Context, tx pgx.Tx, org, room ids.ID, now time.Time) error {
	_, e := tx.Exec(ctx, `UPDATE reservation_inventory_days SET released_at=$3 WHERE organisation_id=$1 AND room_id=$2 AND released_at IS NULL`, org, room, now)
	return e
}

// CommitProvenance records committed rows for an ACTIVE replacement line.
func CommitProvenance(ctx context.Context, tx pgx.Tx, org, property, room ids.ID, d QuantityDay, quantity, allotted int) error {
	var allotment any
	if allotted > 0 {
		allotment = *d.AllotmentID
	}
	_, e := tx.Exec(ctx, `INSERT INTO reservation_inventory_days(organisation_id,property_id,room_id,pool_id,day,quantity,allotment_id,allotted_quantity) VALUES($1,$2,$3,$4,$5::date,$6,$7,$8)`, org, property, room, d.PoolID, d.Day, quantity, allotment, allotted)
	return e
}

// DecommitCalendar returns genuinely released quantity to sellable stock.
func DecommitCalendar(ctx context.Context, tx pgx.Tx, org, actor ids.ID, d QuantityDay, quantity int, now time.Time) error {
	var actorRef any
	if !actor.IsZero() {
		actorRef = actor
	}
	tag, e := tx.Exec(ctx, `UPDATE inventory_calendar SET updated_by_service=coalesce(nullif(current_setting('business.actor_service',true),''),''),committed=committed-$4,version=version+1,updated_by=$5,updated_at=$6 WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date AND committed>=$4`, org, d.PoolID, d.Day, quantity, actorRef, now)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return apperrors.Conflict("RESERVATION_STOCK_RECONCILIATION_REQUIRED", "Committed stock differs from its canonical reservation provenance.")
	}
	return nil
}

// SlotMap returns the original accepted quote quantity and, for each line
// slot (ascending), the original quote slot it represents. Lines that were
// never fragmented map one-to-one onto their own accepted quote.
func SlotMap(ctx context.Context, q database.Querier, org ids.ID, room Room) (ids.ID, int, []int, error) {
	var root ids.ID
	var quantity int
	var slots []int32
	e := q.QueryRow(ctx, `SELECT root_room_id,root_quantity,source_slots FROM reservation_room_slot_maps WHERE organisation_id=$1 AND room_id=$2`, org, room.ID).Scan(&root, &quantity, &slots)
	if errors.Is(e, pgx.ErrNoRows) {
		out := make([]int, room.Quantity)
		for i := range out {
			out[i] = i + 1
		}
		return room.ID, room.Quantity, out, nil
	}
	if e != nil {
		return root, 0, nil, e
	}
	out := make([]int, len(slots))
	for i, v := range slots {
		out[i] = int(v)
	}
	if len(out) != room.Quantity {
		return root, 0, nil, apperrors.Conflict("RESERVATION_SLOT_MAP_MISMATCH", "Line slots must map exactly onto the original accepted quote.")
	}
	return root, quantity, out, nil
}

// SourceSlot maps a physical assignment slot of a line onto the original
// accepted quote slot used for its frozen folio and fiscal partition.
func SourceSlot(ctx context.Context, q database.Querier, org ids.ID, room Room, slot int) (int, int, error) {
	_, quantity, slots, e := SlotMap(ctx, q, org, room)
	if e != nil {
		return 0, 0, e
	}
	if slot < 1 || slot > len(slots) {
		return 0, 0, apperrors.Conflict("RESERVATION_SLOT_MAP_MISMATCH", "The assignment slot is outside the accepted line quantity.")
	}
	return slots[slot-1], quantity, nil
}
