package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// reservationFinancialEligibility follows immutable amendment credits back to
// their original Payment receipts. A local credit never invents payment truth,
// duplicates spendable money, or conceals an expired/reversed source receipt.
func (h *Handler) reservationFinancialEligibility(ctx context.Context, q database.Querier, org, group ids.ID) error {
	rows, e := commercialJSONRows(ctx, q, `WITH RECURSIVE backing(id,path,cycle) AS (SELECT $2::uuid,ARRAY[$2::uuid],false UNION ALL SELECT c.source_group_id,b.path||c.source_group_id,c.source_group_id=ANY(b.path) FROM backing b JOIN reservation_financial_credits c ON c.organisation_id=$1 AND c.target_group_id=b.id WHERE NOT b.cycle AND cardinality(b.path)<=21) SELECT jsonb_build_object('group',to_jsonb(f),'cycle',b.cycle,'depth',cardinality(b.path),'credits',coalesce((SELECT sum(c.amount_minor) FROM reservation_financial_credits c WHERE c.organisation_id=$1 AND c.target_group_id=f.id),0),'fresh',EXISTS(SELECT 1 FROM reservation_payment_projections p JOIN reservation_payment_bindings bind ON bind.organisation_id=p.organisation_id AND bind.intent_id=p.intent_id AND bind.financial_group_id=p.financial_group_id WHERE p.organisation_id=$1 AND p.financial_group_id=f.id AND p.id=(SELECT latest.id FROM reservation_payment_projections latest WHERE latest.organisation_id=$1 AND latest.financial_group_id=f.id ORDER BY latest.observed_at DESC,latest.id DESC LIMIT 1) AND p.provider_state='VERIFIED_SUCCESS' AND p.reversed_minor=0 AND p.remedy_version=1 AND NOT p.remedy_review_required AND p.amount_minor=f.required_amount_minor AND p.currency=f.currency AND p.expires_at>$3)) FROM backing b JOIN reservation_financial_groups f ON f.organisation_id=$1 AND f.id=b.id LIMIT 201`, org, group, h.Auth.Service.Now().UTC())
	if e != nil {
		return e
	}
	if len(rows) == 0 || len(rows) > 200 {
		return apperrors.Conflict("FINANCIAL_LINEAGE_REVIEW_REQUIRED", "Financial backing is missing or exceeds the bounded review graph.")
	}
	for _, raw := range rows {
		var v struct {
			Group   reservationFinancialGroup `json:"group"`
			Cycle   bool                      `json:"cycle"`
			Depth   int                       `json:"depth"`
			Credits int64                     `json:"credits"`
			Fresh   bool                      `json:"fresh"`
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			return e
		}
		if v.Cycle || v.Depth > 20 {
			return apperrors.Conflict("FINANCIAL_LINEAGE_REVIEW_REQUIRED", "Financial backing cannot cycle or exceed its review depth.")
		}
		if v.Group.State == "APPROVED_GUARANTEE" {
			continue
		}
		if v.Group.State != "VERIFIED" || v.Credits < 0 || v.Credits > v.Group.AmountMinor || v.Group.RequiredAmountMinor != v.Group.AmountMinor-v.Credits || v.Group.RequiredAmountMinor > 0 && !v.Fresh {
			return apperrors.Conflict("PAYMENT_READBACK_REQUIRED", "Every accepted obligation and amendment credit needs fresh, unreversed Payment evidence or its independent guarantee.")
		}
	}
	return nil
}

func isReservationFinancialPending(err error) bool {
	var app *apperrors.Error
	return errors.As(err, &app) && app.Status == 409
}
