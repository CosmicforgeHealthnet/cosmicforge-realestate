package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// ProcessRMSMaintenance uses the established scheduler. Expiry only withdraws
// unapplied authority; it never changes rates or borrows a human identity.
func (h *Handler) ProcessRMSMaintenance(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	now := h.Auth.Service.Now().UTC()
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM pricing_recommendations WHERE state IN ('DRAFT','PENDING_APPROVAL','APPROVED') AND expires_at<=$1 ORDER BY expires_at,id LIMIT $2`, now, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, id ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.id); e != nil {
			break
		}
		refs = append(refs, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	for _, v := range refs {
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			r, e := loadRMSRecommendation(ctx, tx, v.org, v.id)
			if e != nil {
				return e
			}
			if now.Before(r.ExpiresAt) || r.State != "DRAFT" && r.State != "PENDING_APPROVAL" && r.State != "APPROVED" {
				return nil
			}
			return h.rmsRecordDecision(ctx, tx, tenant{Organisation: v.org}, r, "EXPIRED", "EXPIRE", r.ApprovalID, r.AppliedVersions, "Recommendation authorization deadline elapsed", map[string]any{"deadline": r.ExpiresAt, "actor_service": "business-operations-service"})
		})
		if e != nil {
			return e
		}
	}
	return nil
}
