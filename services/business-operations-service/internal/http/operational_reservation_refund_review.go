package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// Late financial success does not revive a cancelled/expired allocation. It
// creates a durable review obligation using the fee frozen at the actual exit,
// including credit descendants of an amended reservation. This never submits a
// refund, impersonates Admin, or recalculates fees at today's later deadline.
func (h *Handler) reconcileReservationRefundReviews(ctx context.Context, tx pgx.Tx, job reservationFinancialJob) error {
	rows, e := commercialJSONRows(ctx, tx, `WITH RECURSIVE affected(id) AS (SELECT $2::uuid UNION SELECT c.target_group_id FROM affected a JOIN reservation_financial_credits c ON c.organisation_id=$1 AND c.source_group_id=a.id) SELECT jsonb_build_object('obligation_id',o.id,'reservation_id',o.reservation_id,'property_id',o.property_id,'currency',o.currency,'group_id',a.id,'amount_minor',sum((effect->>'accepted_amount_minor')::bigint-(effect->>'penalty_minor')::bigint)) FROM reservation_cancellation_obligations o CROSS JOIN LATERAL jsonb_array_elements(o.room_effects) effect JOIN affected a ON a.id=(effect->>'financial_group_id')::uuid WHERE o.organisation_id=$1 AND NOT EXISTS(SELECT 1 FROM reservation_financial_requests j WHERE j.organisation_id=$1 AND j.idempotency_key='cancellation-refund:'||o.id::text||':'||a.id::text) GROUP BY o.id,a.id ORDER BY o.id,a.id`, job.OrganisationID, job.FinancialGroupID)
	if e != nil {
		return e
	}
	for _, raw := range rows {
		var v struct {
			ObligationID  ids.ID `json:"obligation_id"`
			ReservationID ids.ID `json:"reservation_id"`
			PropertyID    ids.ID `json:"property_id"`
			GroupID       ids.ID `json:"group_id"`
			Currency      string `json:"currency"`
			AmountMinor   int64  `json:"amount_minor"`
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			return e
		}
		if v.AmountMinor <= 0 {
			continue
		}
		if e = h.reservationFinancialEligibility(ctx, tx, job.OrganisationID, v.GroupID); e != nil {
			// Eligibility denial preserves the original READBACK_REQUIRED evidence;
			// database/transport failures must still abort, never silently disappear.
			if !isReservationFinancialPending(e) {
				return e
			}
			continue
		}
		request := marshalCommercial(map[string]any{"cancellation_obligation_id": v.ObligationID, "financial_group_id": v.GroupID, "amount_minor": v.AmountMinor, "currency": v.Currency, "approval_state": "REQUIRES_INDEPENDENT_FINANCIAL_APPROVAL", "submission_contract": "BUSINESS_AUTHORISED_REFUND_OPERATION_UNAVAILABLE", "actual_refund_executed": false, "late_collection_readback": job.ID})
		id := ids.New()
		tag, e := tx.Exec(ctx, `INSERT INTO reservation_financial_requests(id,organisation_id,property_id,reservation_id,financial_group_id,kind,state,request,request_hash,idempotency_key,last_error,created_by) VALUES($1,$2,$3,$4,$5,'REFUND','DEPENDENCY_BLOCKED',$6,$7,$8,'PAYMENT_REFUND_REQUIRES_AUTHORISED_ADMIN_HANDOFF',$9) ON CONFLICT(organisation_id,idempotency_key) DO NOTHING`, id, job.OrganisationID, v.PropertyID, v.ReservationID, v.GroupID, request, hashCommercialPayload(request), "cancellation-refund:"+v.ObligationID.String()+":"+v.GroupID.String(), job.CreatedBy)
		if e != nil {
			return e
		}
		if tag.RowsAffected() > 0 {
			if e = audit(ctx, tx, tenant{Organisation: job.OrganisationID, Account: job.CreatedBy}, "RESERVATION_LATE_COLLECTION_REFUND_REVIEW", "reservation", v.ReservationID, map[string]any{"obligation_id": v.ObligationID, "financial_group_id": v.GroupID, "request_id": id, "amount_minor": v.AmountMinor, "currency": v.Currency, "actual_refund_executed": false}); e != nil {
				return e
			}
		}
	}
	return nil
}
