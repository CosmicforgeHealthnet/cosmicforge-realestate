package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (h *Handler) reservationOverbookingReconcile(w http.ResponseWriter, r *http.Request) {
	var in ReservationOverbookingCommand
	if !decode(w, r, &in) {
		return
	}
	if !validOverbookingCommand(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, policy reference, independent approval and reason required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "reservations:overbook", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:overbooking:reconcile:"+pre.ID.String(), in, "reservations:overbook", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:overbook", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		policy, e := reservations.Load[ReservationOverbookingPolicy](ctx, tx, `SELECT to_jsonb(p) FROM reservation_overbooking_policies p WHERE organisation_id=$1 AND id=$2`, t.Organisation, in.PolicyID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.AllocationState != "ACTIVE" || v.State != "CONFIRMED" || policy.Version != in.PolicyVersion || policy.PropertyID != v.PropertyID {
			return 0, nil, apperrors.Conflict("OVERBOOKING_RECONCILIATION_STATE", "Current confirmed reservation and pool policy reference required. Suspending new overbooking does not prevent reducing an accepted exposure.")
		}
		if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return 0, nil, e
		}
		pool, e := inventory.GetPool(ctx, tx, t.Organisation, policy.PoolID)
		if e != nil {
			return 0, nil, e
		}
		if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
			return 0, nil, e
		}
		rooms, e := reservations.Rooms(ctx, tx, t.Organisation, v.ID, true)
		if e != nil {
			return 0, nil, e
		}
		type release struct {
			room     ids.ID
			day      string
			quantity int
		}
		releases := []release{}
		free := map[string]int{}
		for _, room := range rooms {
			if room.PoolID != pool.ID {
				return 0, nil, apperrors.PermissionDenied("Reconcile only this approved physical pool.")
			}
			stock, e := inventory.Calendar(ctx, tx, t.Organisation, pool.ID, inventory.DateRange{DateFrom: room.DateFrom, DateTo: room.DateTo}, v.PolicySnapshot.AcceptanceTimezone, "", h.Auth.Service.Now().UTC(), true)
			if e != nil {
				return 0, nil, e
			}
			for _, day := range stock {
				if _, ok := free[day.Day]; !ok {
					free[day.Day] = day.PhysicalAvailable
				}
			}
			rows, e := tx.Query(ctx, `SELECT d.day::text,d.overbooked_quantity-coalesce((SELECT sum(j.quantity) FROM reservation_overbooking_releases j WHERE j.organisation_id=d.organisation_id AND j.room_id=d.room_id AND j.day=d.day),0) FROM reservation_inventory_days d WHERE organisation_id=$1 AND room_id=$2 AND released_at IS NULL ORDER BY day`, t.Organisation, room.ID)
			if e != nil {
				return 0, nil, e
			}
			for rows.Next() {
				var day string
				var qty int
				if e = rows.Scan(&day, &qty); e != nil {
					break
				}
				if qty <= 0 {
					continue
				}
				if free[day] < qty {
					e = apperrors.Conflict("OVERBOOKING_PHYSICAL_BACKING_UNAVAILABLE", "Every outstanding overbooked night needs real unheld, unallotted physical backing before reconciliation.")
					break
				}
				free[day] -= qty
				releases = append(releases, release{room: room.ID, day: day, quantity: qty})
			}
			rowErr := rows.Err()
			rows.Close()
			if e != nil {
				return 0, nil, e
			}
			if rowErr != nil {
				return 0, nil, rowErr
			}
		}
		if len(releases) == 0 {
			return 0, nil, apperrors.Conflict("OVERBOOKING_ALREADY_BACKED", "No outstanding excess demand remains on these room nights.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_OVERBOOKING", target.Scope, in.ApprovalID, map[string]any{"action": "RECONCILE", "reservation_id": v.ID, "command": op}, v.Channel, &v.AmountMinor, v.Currency); e != nil {
			return 0, nil, e
		}
		for _, d := range releases {
			if _, e = tx.Exec(ctx, `INSERT INTO reservation_overbooking_releases(id,organisation_id,property_id,room_id,pool_id,day,quantity,kind,reason,created_by) VALUES($1,$2,$3,$4,$5,$6::date,$7,'CONVERTED',$8,$9)`, ids.New(), t.Organisation, v.PropertyID, d.room, pool.ID, d.day, d.quantity, in.Reason, t.Account); e != nil {
				return 0, nil, e
			}
			if _, e = tx.Exec(ctx, `UPDATE inventory_calendar SET overbooked=overbooked-$4,version=version+1,updated_by=$5,updated_by_service='',updated_at=now() WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date AND overbooked>=$4`, t.Organisation, pool.ID, d.day, d.quantity, t.Account); e != nil {
				return 0, nil, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET version=version+1,reason=$3,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, in.Reason); e != nil {
			return 0, nil, e
		}
		v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		if e = h.reservationCommit(ctx, tx, t, v, "OVERBOOKING_PHYSICALLY_RECONCILED", "reservation.modified.v1", in.Reason, map[string]any{"policy_id": policy.ID, "policy_version": policy.Version, "day_adjustments": len(releases), "physical_capacity_changed": false, "customer_recovery_completed": false, "recovery_reference": in.RecoveryReference}); e != nil {
			return 0, nil, e
		}
		out, e := h.reservationDetail(ctx, tx, t.Organisation, v.ID)
		return 200, out, e
	})
}
