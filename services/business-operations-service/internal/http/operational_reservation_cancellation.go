package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/reservations"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

// cancellationObligation records a contractual fee/refund requirement. A local
// obligation is never a Payment-owned executed refund, settlement or collection.
func (h *Handler) cancellationObligation(ctx context.Context, tx pgx.Tx, t tenant, v reservations.Reservation, rooms []reservations.Room, kind, reason string, version int64) error {
	zone := v.PolicySnapshot.AcceptanceTimezone
	if zone == "" {
		if e := tx.QueryRow(ctx, `SELECT timezone FROM inventory_nodes WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.PropertyID).Scan(&zone); e != nil {
			return e
		}
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return e
	}
	now := h.Auth.Service.Now().UTC()
	total, penalty, collected, refund := int64(0), int64(0), int64(0), int64(0)
	known := true
	effects := []map[string]any{}
	type groupRefund struct {
		amount int64
		known  bool
	}
	refunds := map[ids.ID]groupRefund{}
	for _, room := range rooms {
		fee := int64(0)
		if kind == "NO_SHOW" {
			base, e := money.New(room.AmountMinor, room.Currency)
			if e != nil {
				return e
			}
			ratios := []int64{}
			for _, night := range room.Snapshot.Nights {
				ratios = append(ratios, night.AmountMinor)
			}
			zero := true
			for _, weight := range ratios {
				if weight > 0 {
					zero = false
				}
			}
			if zero {
				for i := range ratios {
					ratios[i] = 1
				}
			}
			parts, e := base.Allocate(ratios...)
			if e != nil {
				return e
			}
			for _, part := range parts[:min(len(parts), v.PolicySnapshot.NoShowRetainNights)] {
				fee, e = addMinor(fee, part.AmountMinor, room.Currency)
				if e != nil {
					return e
				}
			}
		} else if kind != "REJECTED" && kind != "EXPIRED" {
			arrival, e := time.ParseInLocation("2006-01-02 15:04", room.DateFrom+" "+v.PolicySnapshot.CheckInLocal, loc)
			if e != nil {
				return e
			}
			if !now.Before(arrival.Add(-time.Duration(v.PolicySnapshot.CancellationHours) * time.Hour)) {
				fee, e = revenue.Ratio(room.AmountMinor, v.PolicySnapshot.CancellationPenaltyBPS, 10000)
				if e != nil {
					return e
				}
			}
		}
		total, e = addMinor(total, room.AmountMinor, v.Currency)
		if e != nil {
			return e
		}
		penalty, e = addMinor(penalty, fee, v.Currency)
		if e != nil {
			return e
		}
		group, e := typedJSON[reservationFinancialGroup](ctx, tx, `SELECT to_jsonb(f) FROM reservation_financial_groups f WHERE organisation_id=$1 AND id=$2`, t.Organisation, room.FinancialGroupID)
		if e != nil {
			return e
		}
		var paid, eligible *int64
		backed := false
		if group.State == "VERIFIED" {
			if readErr := h.reservationFinancialEligibility(ctx, tx, t.Organisation, group.ID); readErr == nil {
				backed = true
			} else if !isReservationFinancialPending(readErr) {
				return readErr
			}
		}
		if backed {
			value := room.AmountMinor
			paid = &value
			valueRefund := max(int64(0), value-fee)
			eligible = &valueRefund
		} else if v.PaymentResponsibility == "PLATFORM_COLLECT" {
			var started bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reservation_financial_requests WHERE organisation_id=$1 AND financial_group_id=$2 AND kind='COLLECTION') OR EXISTS(SELECT 1 FROM reservation_financial_credits WHERE organisation_id=$1 AND target_group_id=$2)`, t.Organisation, group.ID).Scan(&started); e != nil {
				return e
			}
			if !started {
				zero := int64(0)
				paid = &zero
				eligible = &zero
			}
		}
		g, exists := refunds[group.ID]
		if !exists {
			g.known = true
		}
		if paid == nil || eligible == nil {
			known = false
			g.known = false
		} else {
			collected, e = addMinor(collected, *paid, v.Currency)
			if e != nil {
				return e
			}
			refund, e = addMinor(refund, *eligible, v.Currency)
			if e != nil {
				return e
			}
			g.amount, e = addMinor(g.amount, *eligible, v.Currency)
			if e != nil {
				return e
			}
		}
		refunds[group.ID] = g
		effects = append(effects, map[string]any{"room_id": room.ID, "financial_group_id": group.ID, "accepted_amount_minor": room.AmountMinor, "penalty_minor": fee, "platform_collected_minor": paid, "refundable_minor": eligible, "actual_refund_executed": false})
	}
	var collectedValue, refundValue any
	state := "READBACK_REQUIRED"
	if v.PaymentResponsibility != "PLATFORM_COLLECT" {
		state = "EXTERNAL_COLLECTION_UNKNOWN"
	}
	if known {
		collectedValue = collected
		refundValue = refund
		state = "KNOWN_PLATFORM_REFERENCE"
	}
	id := ids.New()
	if _, e = tx.Exec(ctx, `INSERT INTO reservation_cancellation_obligations(id,organisation_id,property_id,reservation_id,reservation_version,kind,currency,accepted_amount_minor,penalty_minor,platform_collected_minor,refundable_minor,financial_state,policy_snapshot,room_effects,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, id, t.Organisation, v.PropertyID, v.ID, version, kind, v.Currency, total, penalty, collectedValue, refundValue, state, marshalCommercial(v.PolicySnapshot), marshalCommercial(effects), reason, t.Account); e != nil {
		return e
	}
	for group, g := range refunds {
		if !g.known || g.amount == 0 {
			continue
		}
		request := marshalCommercial(map[string]any{"cancellation_obligation_id": id, "financial_group_id": group, "amount_minor": g.amount, "currency": v.Currency, "approval_state": "REQUIRES_INDEPENDENT_FINANCIAL_APPROVAL", "submission_contract": "BUSINESS_AUTHORISED_REFUND_OPERATION_UNAVAILABLE", "actual_refund_executed": false})
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,kind,state,request,request_hash,idempotency_key,last_error,created_by) VALUES($1,$2,$3,$4,$5,'REFUND','DEPENDENCY_BLOCKED',$6,$7,$8,'PAYMENT_REFUND_REQUIRES_AUTHORISED_ADMIN_HANDOFF',$9)`, ids.New(), t.Organisation, v.PropertyID, v.ID, group, request, hashCommercialPayload(request), "cancellation-refund:"+id.String()+":"+group.String(), t.Account); e != nil {
			return e
		}
	}
	return nil
}
func (h *Handler) reservationCancellationObligations(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.reservationTarget(r, "reservations:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	rows, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(o) FROM reservation_cancellation_obligations o WHERE organisation_id=$1 AND reservation_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, v.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": rows, "limit": limit, "offset": offset}, e)
}
