package businesshttp

import (
	"context"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/security"
	"github.com/jackc/pgx/v5"
)

// Existing gross postings are retained. Backfill appends detail from immutable
// accepted sources, never a replacement charge or guessed historical quantity.
// Unreconciled sources become visible durable exceptions instead of starving
// other folios or repeatedly overwriting an operator's reviewed allocation.
func (h *Handler) ProcessFolioFiscalMaintenance(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	ctx = requestcontext.WithActor(ctx, requestcontext.Actor{})
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT i.organisation_id,i.folio_id,i.id FROM folio_items i WHERE i.kind='ACCOMMODATION' AND i.policy_snapshot->'accepted_quote' IS NOT NULL AND (i.amount_minor=0 OR i.amount_minor+coalesce((SELECT sum(a.amount_minor) FROM folio_items a WHERE a.organisation_id=i.organisation_id AND a.adjusts_item_id=i.id),0)>0) AND NOT EXISTS(SELECT 1 FROM folio_fiscal_reviews r WHERE r.organisation_id=i.organisation_id AND r.folio_item_id=i.id) AND NOT EXISTS(SELECT 1 FROM hospitality_operation_exceptions x WHERE x.organisation_id=i.organisation_id AND x.source_type='folio' AND x.source_id=i.folio_id AND x.state='OPEN' AND x.code='FISCAL_SOURCE_REVIEW_REQUIRED') ORDER BY i.created_at,i.id LIMIT $1`, limit)
	if e != nil {
		return e
	}
	type ref struct{ org, folio, item ids.ID }
	var refs []ref
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.org, &v.folio, &v.item); e != nil {
			break
		}
		refs = append(refs, v)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if re != nil {
		return re
	}
	for _, v := range refs {
		e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `SELECT set_config('business.actor_service','business-operations-service',true)`); e != nil {
				return e
			}
			var done bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM folio_fiscal_reviews WHERE organisation_id=$1 AND folio_item_id=$2)`, v.org, v.item).Scan(&done); e != nil {
				return e
			}
			if done {
				return nil
			}
			f, e := hospitality.GetFolio(ctx, tx, v.org, v.folio)
			if e != nil {
				return e
			}
			item, e := hospitality.Load[hospitality.Item](ctx, tx, `SELECT to_jsonb(i) FROM folio_items i WHERE organisation_id=$1 AND folio_id=$2 AND id=$3`, v.org, v.folio, v.item)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `SAVEPOINT fiscal_source`); e != nil {
				return e
			}
			detail, _, sourceError := h.acceptedFolioFiscal(ctx, tx, f, item)
			if sourceError == nil {
				_, sourceError = h.captureFolioFiscal(ctx, tx, tenant{Organisation: v.org}, f, item, detail, "ACCEPTED_QUOTE", ids.Nil, "", "Reconcile retained accepted fiscal source without changing any charge", true)
			}
			if sourceError == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, e = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT fiscal_source`); e != nil {
				return e
			}
			return h.pmsOperationException(ctx, tx, tenant{Organisation: v.org}, f, security.SHA256Hex(item.PolicySnapshot), "FISCAL_SOURCE_REVIEW_REQUIRED", map[string]any{"item_id": v.item, "reason": "Original accepted fiscal source could not be reconciled; operator review required"})
		})
		if e != nil {
			return e
		}
	}
	return nil
}
