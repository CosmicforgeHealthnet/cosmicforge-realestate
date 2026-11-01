package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
	"net/http"
	"slices"
)

func (h *Handler) reservationOverbook(w http.ResponseWriter, r *http.Request) {
	var in ReservationOverbookingCommand
	if !decode(w, r, &in) {
		return
	}
	if !validOverbookingCommand(in) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current reservation/policy versions, independent approval and recovery reference required."))
		return
	}
	_, pre, e := h.reservationTarget(r, "reservations:overbook", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "reservation:overbook:"+pre.ID.String(), in, "reservations:overbook", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:overbook", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := reservations.Get(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		policy, e := h.currentOverbookingPolicy(ctx, tx, t, in.PolicyID, in.PolicyVersion)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.State != "INQUIRY" || v.AllocationState != "ACTIVE" || v.Channel != "" || v.PropertyID != policy.PropertyID || !slices.Contains(policy.Definition.AllowedSources, v.Source) {
			return 0, nil, apperrors.Conflict("OVERBOOKING_RESERVATION_STATE", "Only a current unallocated staff inquiry under its exact approved pool policy can become an overbooked obligation.")
		}
		if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
			return 0, nil, e
		}
		if e = h.reservationRiskGate(ctx, tx, t.Organisation, v); e != nil {
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
		if len(rooms) == 0 {
			return 0, nil, apperrors.Conflict("RESERVATION_ROOMS_REQUIRED", "Accepted priced rooms required.")
		}
		type demand struct {
			stock     inventory.Day
			quantity  int
			available int
		}
		days := map[string]*demand{}
		roomDates := map[ids.ID][]string{}
		now := h.Auth.Service.Now().UTC()
		for _, room := range rooms {
			if room.PoolID != pool.ID || room.HoldID != nil {
				return 0, nil, apperrors.Conflict("OVERBOOKING_POOL_MISMATCH", "Every inquiry room must be unallocated and use the same approved physical pool.")
			}
			if e = h.reservationFinancialEligibility(ctx, tx, t.Organisation, room.FinancialGroupID); e != nil {
				return 0, nil, e
			}
			if e = h.validateHoldQuote(ctx, tx, t, inventory.HoldCommand{ProductID: room.ProductID, ProductVersion: room.Snapshot.ProductVersion, PoolVersion: room.Snapshot.PoolVersion, QuoteID: room.QuoteID, DateFrom: room.DateFrom, DateTo: room.DateTo, Quantity: room.Quantity, Channel: v.Channel, DurationSeconds: 60}); e != nil {
				return 0, nil, e
			}
			stock, e := inventory.Calendar(ctx, tx, t.Organisation, pool.ID, inventory.DateRange{DateFrom: room.DateFrom, DateTo: room.DateTo}, v.PolicySnapshot.AcceptanceTimezone, "", now, true)
			if e != nil {
				return 0, nil, e
			}
			for _, day := range stock {
				d := days[day.Day]
				if d == nil {
					d = &demand{stock: day, available: day.Available}
					days[day.Day] = d
				}
				d.quantity += room.Quantity
				roomDates[room.ID] = append(roomDates[room.ID], day.Day)
			}
		}
		maximum := 0
		for _, day := range days {
			excess := max(0, day.quantity-day.available)
			if day.stock.Overbooked+excess > policy.Definition.Limit(day.stock.Capacity) {
				return 0, nil, apperrors.Conflict("OVERBOOKING_LIMIT_EXCEEDED", "Existing and proposed excess demand exceed the approved absolute or percentage bound.")
			}
			maximum = max(maximum, excess)
		}
		if maximum == 0 {
			return 0, nil, apperrors.Conflict("OVERBOOKING_NOT_REQUIRED", "Physical stock is available; use the ordinary approved reservation hold workflow.")
		}
		op := in
		op.ApprovalID = ids.Nil
		if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RESERVATION_OVERBOOKING", target.Scope, in.ApprovalID, map[string]any{"action": "ACCEPT", "reservation_id": v.ID, "command": op}, v.Channel, &v.AmountMinor, v.Currency); e != nil {
			return 0, nil, e
		}
		permit := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_overbooking_permits(id,organisation_id,property_id,reservation_id,policy_id,policy_version,policy_snapshot,quantity,approval_id,reason,recovery_reference,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, permit, t.Organisation, v.PropertyID, v.ID, policy.ID, policy.Version, marshalCommercial(policy.Definition), maximum, in.ApprovalID, in.Reason, in.RecoveryReference, overbookingPermitDeadline(policy, now), t.Account); e != nil {
			return 0, nil, e
		}
		for _, room := range rooms {
			for _, day := range roomDates[room.ID] {
				d := days[day]
				excess := max(0, room.Quantity-d.available)
				d.available = max(0, d.available-room.Quantity)
				if _, e = tx.Exec(ctx, `UPDATE inventory_calendar SET committed=committed+$4,overbooked=overbooked+$5,version=version+1,updated_by=$6,updated_by_service='',updated_at=$7 WHERE organisation_id=$1 AND pool_id=$2 AND day=$3::date`, t.Organisation, pool.ID, day, room.Quantity, excess, t.Account, now); e != nil {
					return 0, nil, e
				}
				if _, e = tx.Exec(ctx, `INSERT INTO reservation_inventory_days(organisation_id,property_id,room_id,pool_id,day,quantity,overbooked_quantity,overbooking_permit_id) VALUES($1,$2,$3,$4,$5::date,$6,$7,$8)`, t.Organisation, v.PropertyID, room.ID, pool.ID, day, room.Quantity, excess, permit); e != nil {
					return 0, nil, e
				}
			}
		}
		caseID := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_conflicts(id,organisation_id,property_id,reservation_id,source_reference,classification,evidence,reason,created_by) VALUES($1,$2,$3,$4,$5,'APPROVED_OVERBOOKING_RECOVERY',$6,$7,$8)`, caseID, t.Organisation, v.PropertyID, v.ID, "OVERBOOKING:"+permit.String(), marshalCommercial(map[string]any{"permit_id": permit, "pool_id": pool.ID, "recovery_reference": in.RecoveryReference, "customer_recovery_completed": false}), in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE reservations SET state='CONFIRMED',confirmed_at=$3,hold_expires_at=NULL,version=version+1,reason=$4,updated_at=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, now, in.Reason); e != nil {
			return 0, nil, e
		}
		v, e = reservations.Get(ctx, tx, t.Organisation, v.ID)
		if e != nil {
			return 0, nil, e
		}
		if e = h.reservationCommit(ctx, tx, t, v, "OVERBOOKING_CONFIRMED", "reservation.modified.v1", in.Reason, map[string]any{"permit_id": permit, "policy_id": policy.ID, "policy_version": policy.Version, "maximum_excess_quantity": maximum, "physical_capacity_changed": false, "reconciliation_case_id": caseID, "recovery_reference": in.RecoveryReference, "physical_room_assignment_allowed": false}); e != nil {
			return 0, nil, e
		}
		if e = h.pauseOverbookedPoolChannels(ctx, tx, t, pool.ID, v.PropertyID); e != nil {
			return 0, nil, e
		}
		out, e := h.reservationDetail(ctx, tx, t.Organisation, v.ID)
		return 200, out, e
	})
}
func (h *Handler) pauseOverbookedPoolChannels(ctx context.Context, tx pgx.Tx, t tenant, pool, property ids.ID) error {
	rows, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(p) FROM listing_publication p JOIN listings l ON l.organisation_id=p.organisation_id AND l.id=p.listing_id JOIN sellable_products s ON s.organisation_id=l.organisation_id AND s.id=l.product_id WHERE p.organisation_id=$1 AND s.pool_id=$2 AND p.desired_state='PUBLISHED' ORDER BY p.id`, t.Organisation, pool)
	if e != nil {
		return e
	}
	for _, raw := range rows {
		var p CommercialPublication
		if e = json.Unmarshal(raw, &p); e != nil {
			return e
		}
		if e = h.queueSafetyPause(ctx, tx, CommercialSyncJob{OrganisationID: t.Organisation, PropertyID: property, ConnectionID: p.ConnectionID, PublicationID: &p.ID, ActorID: t.Account}, "APPROVED_OVERBOOKING_REQUIRES_RECONCILIATION"); e != nil {
			return e
		}
	}
	return nil
}
