package reservations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"time"
)

func Load[T any](ctx context.Context, q database.Querier, query string, args ...any) (v T, e error) {
	var raw []byte
	e = q.QueryRow(ctx, query, args...).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, apperrors.NotFound("RESERVATION_RESOURCE_NOT_FOUND", "Reservation resource not found.").WithCause(e)
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(raw, &v)
	return
}
func Get(ctx context.Context, q database.Querier, org, id ids.ID) (Reservation, error) {
	raw, e := (&db.Queries{}).GetCanonicalReservation(ctx, q, db.GetCanonicalReservationParams{OrganisationID: org, ID: id})
	var v Reservation
	if errors.Is(e, pgx.ErrNoRows) {
		return v, apperrors.NotFound("RESERVATION_RESOURCE_NOT_FOUND", "Reservation resource not found.").WithCause(e)
	}
	if e != nil {
		return v, e
	}
	e = json.Unmarshal(raw, &v)
	return v, e
}
func Rooms(ctx context.Context, q database.Querier, org, id ids.ID, active bool) ([]Room, error) {
	rows, e := q.Query(ctx, `SELECT to_jsonb(r) FROM reservation_rooms r WHERE organisation_id=$1 AND reservation_id=$2 AND (NOT $3 OR state='ACTIVE') ORDER BY pool_id,date_from,id`, org, id, active)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Room{}
	for rows.Next() {
		var raw []byte
		var v Room
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CommitHold exchanges a real active hold for committed stock in one owning
// transaction. The organisation lock and calendar order serialize every source.
func CommitHold(ctx context.Context, tx pgx.Tx, org, actor ids.ID, room Room, now time.Time) error {
	if room.HoldID == nil {
		return apperrors.Conflict("RESERVATION_HOLD_REQUIRED", "Canonical active hold required before confirming inventory.")
	}
	hold, e := inventory.GetHold(ctx, tx, org, *room.HoldID)
	if e != nil {
		return e
	}
	var expectedVersion int64
	if e = tx.QueryRow(ctx, `SELECT hold_version FROM reservation_holds WHERE organisation_id=$1 AND room_id=$2 AND hold_id=$3`, org, room.ID, hold.ID).Scan(&expectedVersion); e != nil {
		return e
	}
	if hold.Version != expectedVersion {
		return apperrors.Conflict("RESERVATION_HOLD_STALE", "Bound hold changed after reservation acceptance.")
	}
	if hold.State != "ACTIVE" || !now.Before(hold.ExpiresAt) || hold.ProductID != room.ProductID || hold.QuoteID != room.QuoteID || hold.PoolID != room.PoolID || hold.Quantity != room.Quantity || hold.DateFrom != room.DateFrom || hold.DateTo != room.DateTo {
		return apperrors.Conflict("RESERVATION_HOLD_STALE", "The exact unexpired hold must still own these room nights.")
	}
	rows, e := tx.Query(ctx, `SELECT d.day::text,d.quantity,d.allotment_id,d.allotted_quantity FROM inventory_hold_days d JOIN inventory_calendar c ON c.organisation_id=d.organisation_id AND c.pool_id=d.pool_id AND c.day=d.day WHERE d.organisation_id=$1 AND d.hold_id=$2 ORDER BY d.day FOR UPDATE OF c`, org, hold.ID)
	if e != nil {
		return e
	}
	type held struct {
		day       string
		quantity  int
		allotment *ids.ID
		allotted  int
	}
	days := []held{}
	for rows.Next() {
		var d held
		if e = rows.Scan(&d.day, &d.quantity, &d.allotment, &d.allotted); e != nil {
			break
		}
		days = append(days, d)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	expected, e := (inventory.DateRange{DateFrom: room.DateFrom, DateTo: room.DateTo}).Days("UTC")
	if e != nil || len(expected) != len(days) {
		return apperrors.Conflict("RESERVATION_HOLD_INCOMPLETE", "The hold must contain every accepted civil date.")
	}
	if _, e = tx.Exec(ctx, `UPDATE inventory_holds SET state='HANDED_OFF',handoff_reference=$3,version=version+1,updated_at=$4 WHERE organisation_id=$1 AND id=$2`, org, hold.ID, room.ReservationID.String(), now); e != nil {
		return e
	}
	for _, d := range days {
		if d.quantity != room.Quantity {
			return apperrors.Conflict("RESERVATION_HOLD_INCOMPLETE", "Held day quantity differs from accepted room quantity.")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_inventory_days(organisation_id,property_id,room_id,pool_id,day,quantity,allotment_id,allotted_quantity) VALUES($1,$2,$3,$4,$5::date,$6,$7,$8)`, org, room.PropertyID, room.ID, room.PoolID, d.day, d.quantity, d.allotment, d.allotted); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_calendar SET updated_by_service=coalesce(nullif(current_setting('business.actor_service',true),''),''),committed=committed+$4,version=version+1,updated_by=$5,updated_at=$6 WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date`, org, room.PoolID, d.day, d.quantity, actor, now); e != nil {
			return e
		}
	}
	return nil
}

// Release never edits a historical price. A retained night remains committed
// explicitly; cancellation/refund obligations are separate financial references.
func Release(ctx context.Context, tx pgx.Tx, org, actor ids.ID, room Room, from string, now time.Time) error {
	rows, e := tx.Query(ctx, `SELECT d.day::text,d.quantity,d.overbooked_quantity-coalesce((SELECT sum(j.quantity) FROM reservation_overbooking_releases j WHERE j.organisation_id=d.organisation_id AND j.room_id=d.room_id AND j.day=d.day),0) FROM reservation_inventory_days d JOIN inventory_calendar c ON c.organisation_id=d.organisation_id AND c.pool_id=d.pool_id AND c.day=d.day WHERE d.organisation_id=$1 AND d.room_id=$2 AND d.released_at IS NULL AND d.day>=$3::date ORDER BY d.day FOR UPDATE OF c`, org, room.ID, from)
	if e != nil {
		return e
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
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	for _, d := range days {
		if d.overbooked > 0 {
			if _, e = tx.Exec(ctx, `INSERT INTO reservation_overbooking_releases(id,organisation_id,property_id,room_id,pool_id,day,quantity,kind,reason,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7,'CANCELLED','Canonical reservation release',$8)`, ids.New(), org, room.PropertyID, room.ID, room.PoolID, d.day, d.overbooked, actor); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE reservation_inventory_days SET released_at=$4 WHERE organisation_id=$1 AND room_id=$2 AND day=$3::date AND released_at IS NULL`, org, room.ID, d.day, now); e != nil {
			return e
		}
		tag, err := tx.Exec(ctx, `UPDATE inventory_calendar SET updated_by_service=coalesce(nullif(current_setting('business.actor_service',true),''),''),committed=committed-$4,overbooked=overbooked-$7,version=version+1,updated_by=$5,updated_at=$6 WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date AND committed>=$4`, org, room.PoolID, d.day, d.quantity, actor, now, d.overbooked)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return apperrors.Conflict("RESERVATION_STOCK_RECONCILIATION_REQUIRED", "Committed stock differs from its canonical reservation provenance.")
		}
	}
	if room.HoldID != nil {
		var state string
		e = tx.QueryRow(ctx, `SELECT state FROM inventory_holds WHERE organisation_id=$1 AND id=$2`, org, *room.HoldID).Scan(&state)
		if e != nil {
			return e
		}
		if state == "ACTIVE" {
			if _, e = tx.Exec(ctx, `UPDATE inventory_holds SET state='CANCELLED',version=version+1,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, org, *room.HoldID, now); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE inventory_calendar SET updated_by_service=coalesce(nullif(current_setting('business.actor_service',true),''),''),version=version+1,updated_by=$5,updated_at=$6 WHERE organisation_id=$1 AND pool_id=$2 AND day>=$3::date AND day<$4::date`, org, room.PoolID, room.DateFrom, room.DateTo, actor, now)
			return e
		}
	}
	return nil
}
