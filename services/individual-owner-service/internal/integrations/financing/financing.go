// Package financing is the provider-neutral rent-financing / mortgage /
// payment-plan partner contract (spec §12 "Financing / insurance partners",
// §13.28). Partners own underwriting and applicant financial data; the owner
// domain sends only listing-level business context after the owner's
// PARTNER_FINANCING consent and keeps offer references and disclosures.
//
// No partner adapter is implemented: the service is built with a nil API and
// financing features answer 503 FEATURE_UNAVAILABLE.
package financing

import (
	"context"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// OptionsRequest is the listing context shared with partners.
type OptionsRequest struct {
	ListingID       ids.ID
	TransactionType string
	Currency        string
	PriceMinor      int64
	BasePeriod      string
	CountryCode     string
	City            string
}

// Offer is a partner product offered for the listing.
type Offer struct {
	PartnerRef       string
	ExternalOfferRef string
	ProductType      string // RENT_INSTALMENT | RENT_ADVANCE | MORTGAGE_BUYER | SELLER_PLAN
	// Disclosure is the total-cost disclosure the partner must provide
	// (total cost, fees, term, rate) — shown to the owner as given.
	Disclosure map[string]any
	ExpiresAt  *time.Time
}

// API is the adapter contract.
type API interface {
	Options(ctx context.Context, req OptionsRequest, idemKey string) ([]Offer, error)
	// Decide records the owner's acceptance (with payout preference) or decline.
	Decide(ctx context.Context, partnerRef, externalOfferRef, decision, payoutPreference, idemKey string) error
}
