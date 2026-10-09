// Package insurance is the provider-neutral insurance partner contract
// (spec §12, §13.29). Product and policy truth stay with the partner; the
// owner domain stores policy/claim references and business requirements.
//
// No partner adapter is implemented: partner options answer 503
// FEATURE_UNAVAILABLE; owners can still record policies they hold and track
// claims with evidence. Partner policy purchase and partner claim
// submission are added to this contract when a partner is selected.
package insurance

import (
	"context"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Product is an insurance product a partner offers for a property.
type Product struct {
	PartnerRef    string
	ProductRef    string
	ProductType   string
	CoverageMinor int64
	PremiumMinor  int64
	Currency      string
	Terms         map[string]any
}

// API is the adapter contract.
type API interface {
	Options(ctx context.Context, propertyID ids.ID, propertyType, countryCode string) ([]Product, error)
}
