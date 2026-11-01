package reservations

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// RequirePhysicalBacking is a necessary PMS assignment guard, in addition to
// tenant/RBAC, current financial/risk, physical room readiness and unique room
// assignment checks. A confirmed overbooked obligation is not a physical room.
func RequirePhysicalBacking(ctx context.Context, q database.Querier, org, room ids.ID, dates inventory.DateRange) error {
	days, e := dates.Days("UTC")
	if e != nil {
		return e
	}
	var backed bool
	e = q.QueryRow(ctx, `SELECT count(*)=$5 AND coalesce(bool_and(r.state='ACTIVE' AND root.state IN ('CONFIRMED','CHECKED_IN') AND d.released_at IS NULL AND d.quantity=r.quantity AND d.overbooked_quantity=coalesce((SELECT sum(j.quantity) FROM reservation_overbooking_releases j WHERE j.organisation_id=d.organisation_id AND j.room_id=d.room_id AND j.day=d.day AND j.kind='CONVERTED'),0)),false) FROM reservation_inventory_days d JOIN reservation_rooms r ON r.organisation_id=d.organisation_id AND r.id=d.room_id JOIN reservations root ON root.organisation_id=r.organisation_id AND root.id=r.reservation_id WHERE d.organisation_id=$1 AND d.room_id=$2 AND d.day>=$3::date AND d.day<$4::date`, org, room, dates.DateFrom, dates.DateTo, len(days)).Scan(&backed)
	if e != nil {
		return e
	}
	if !backed {
		return apperrors.Conflict("PHYSICAL_RESERVATION_BACKING_REQUIRED", "Every assigned room night must have actual committed backing and no outstanding overbooking.")
	}
	return nil
}
