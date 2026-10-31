package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Context is deduplicated across civil days. Consumers receive the immutable
// approved policies behind every rate/tax reference, without cross-service SQL.
func (h *Handler) channelPricingContext(ctx context.Context, q database.Querier, org ids.ID, days []CommercialARIDay) (any, error) {
	rates := map[string]revenue.RateVersion{}
	taxes := map[string]CommercialTaxProfile{}
	for _, day := range days {
		id := day.RateVersionID
		for depth := 0; depth < 8; depth++ {
			if _, ok := rates[id.String()]; ok {
				break
			}
			v, e := revenue.GetVersion(ctx, q, org, id)
			if e != nil {
				return nil, e
			}
			rates[id.String()] = v
			if v.Definition.TaxProfileID != nil {
				taxID := *v.Definition.TaxProfileID
				if _, ok := taxes[taxID.String()]; !ok {
					fiscal, e := typedJSON[CommercialTaxProfile](ctx, q, `SELECT to_jsonb(t) FROM tax_profiles t WHERE organisation_id=$1 AND id=$2`, org, taxID)
					if e != nil {
						return nil, e
					}
					taxes[taxID.String()] = fiscal
				}
			}
			if v.Definition.ParentVersionID == nil {
				break
			}
			id = *v.Definition.ParentVersionID
		}
	}
	return map[string]any{"rate_versions": rates, "tax_profiles": taxes, "rounding": "HALF_AWAY_FROM_ZERO", "money_representation": "AMOUNT_MINOR_AND_ISO_CURRENCY", "daily_amount_basis": "CANONICAL_MINIMUM_OCCUPANCY_BEFORE_ITEMIZED_TAX_AND_FEES", "reservation_acceptance": "SECTION9_AUTHORITY_REQUIRED"}, nil
}
