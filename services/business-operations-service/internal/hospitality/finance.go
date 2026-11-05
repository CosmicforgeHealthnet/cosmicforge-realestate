package hospitality

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"time"
)

// Balance uses the newest authoritative readback, not the historic observation
// attached when money was allocated. Expiry, reversal or remedy review removes
// usable backing immediately without mutating financial history.
func Balance(ctx context.Context, q database.Querier, org, folio ids.ID, now time.Time) (balance int64, e error) {
	e = q.QueryRow(ctx, `SELECT (coalesce((SELECT sum(amount_minor) FROM folio_items WHERE organisation_id=$1 AND folio_id=$2),0)-coalesce((SELECT sum(a.amount_minor) FROM folio_payment_references a JOIN LATERAL (SELECT p.* FROM reservation_payment_projections p WHERE p.organisation_id=a.organisation_id AND p.financial_group_id=a.financial_group_id AND p.intent_id=a.intent_id ORDER BY p.observed_at DESC,p.id DESC LIMIT 1) p ON true WHERE a.organisation_id=$1 AND a.folio_id=$2 AND p.provider_state='VERIFIED_SUCCESS' AND p.settlement_state='SETTLED' AND p.funding_confirmed AND p.reversed_minor=0 AND p.remedy_version=1 AND NOT p.remedy_review_required AND p.expires_at>$3 AND p.currency=a.currency AND p.amount_minor>=(SELECT sum(total.amount_minor) FROM folio_payment_references total WHERE total.organisation_id=a.organisation_id AND total.financial_group_id=a.financial_group_id)),0))::bigint`, org, folio, now).Scan(&balance)
	return
}
