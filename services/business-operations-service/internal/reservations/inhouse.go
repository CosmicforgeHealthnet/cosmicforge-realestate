package reservations

import (
	"context"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// UnitNight is one released unit night with its before/after committed line
// quantity. Accepted quantity = remaining committed + released unit nights.
type UnitNight struct {
	Day            string `json:"day"`
	QuantityBefore int    `json:"quantity_before"`
	QuantityAfter  int    `json:"quantity_after"`
}

// ReleaseUnitNights returns one unit slot's committed nights from `from` to the
// end of the line to canonical stock. The release ledger row (releaseID) must
// already exist in the same transaction. Calendar rows are locked in civil
// order so concurrent releases, commits and holds serialize on the same stock.
// The line, its price snapshot and other unit slots are never changed.
func ReleaseUnitNights(ctx context.Context, tx pgx.Tx, org, actor, releaseID ids.ID, room Room, slot int, from string, now time.Time) ([]UnitNight, error) {
	if slot < 1 || slot > room.Quantity {
		return nil, apperrors.InvalidRequest("Unit slot exceeds the accepted line quantity.")
	}
	rows, e := tx.Query(ctx, `SELECT d.day::text,d.quantity,d.overbooked_quantity FROM reservation_inventory_days d JOIN inventory_calendar c ON c.organisation_id=d.organisation_id AND c.pool_id=d.pool_id AND c.day=d.day WHERE d.organisation_id=$1 AND d.room_id=$2 AND d.released_at IS NULL AND d.day>=$3::date ORDER BY d.day FOR UPDATE OF c,d`, org, room.ID, from)
	if e != nil {
		return nil, e
	}
	type day struct {
		day        string
		quantity   int
		overbooked int
	}
	days := []day{}
	for rows.Next() {
		var d day
		if e = rows.Scan(&d.day, &d.quantity, &d.overbooked); e != nil {
			break
		}
		days = append(days, d)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if rowErr != nil {
		return nil, rowErr
	}
	out := make([]UnitNight, 0, len(days))
	for _, d := range days {
		after := d.quantity - 1
		// A physically backed in-house line has no outstanding overbooking; any
		// converted provenance must still fit the remaining committed quantity.
		if d.overbooked > after && after > 0 {
			return nil, apperrors.Conflict("OVERBOOKING_RECONCILIATION_REQUIRED", "Converted overbooking provenance must be reconciled before releasing a unit night.")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_unit_release_days(organisation_id,property_id,release_id,room_id,pool_id,day,slot,quantity_before,quantity_after) VALUES($1,$2,$3,$4,$5,$6::date,$7,$8,$9)`, org, room.PropertyID, releaseID, room.ID, room.PoolID, d.day, slot, d.quantity, after); e != nil {
			return nil, e
		}
		if after > 0 {
			_, e = tx.Exec(ctx, `UPDATE reservation_inventory_days SET quantity=$4,allotted_quantity=least(allotted_quantity,$4) WHERE organisation_id=$1 AND room_id=$2 AND day=$3::date AND released_at IS NULL`, org, room.ID, d.day, after)
		} else {
			_, e = tx.Exec(ctx, `UPDATE reservation_inventory_days SET released_at=$4 WHERE organisation_id=$1 AND room_id=$2 AND day=$3::date AND released_at IS NULL`, org, room.ID, d.day, now)
		}
		if e != nil {
			return nil, e
		}
		tag, err := tx.Exec(ctx, `UPDATE inventory_calendar SET updated_by_service=coalesce(nullif(current_setting('business.actor_service',true),''),''),committed=committed-1,version=version+1,updated_by=$4,updated_at=$5 WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date AND committed>=1`, org, room.PoolID, d.day, actor, now)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, apperrors.Conflict("RESERVATION_STOCK_RECONCILIATION_REQUIRED", "Committed stock differs from its canonical reservation provenance.")
		}
		out = append(out, UnitNight{Day: d.day, QuantityBefore: d.quantity, QuantityAfter: after})
	}
	return out, nil
}

// StayChainEnd is the exclusive end of one unit slot: the accepted line end or
// the last contiguous in-house extension of that slot.
func StayChainEnd(ctx context.Context, q database.Querier, org ids.ID, room Room, slot int) (string, error) {
	var end string
	e := q.QueryRow(ctx, `SELECT greatest($3::date,coalesce(max(date_to),$3::date))::text FROM stay_extensions WHERE organisation_id=$1 AND source_room_id=$2 AND slot=$4`, org, room.ID, room.DateTo, slot).Scan(&end)
	return end, e
}
