package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Invoice projects immutable accepted terms, including explicitly accepted tax
// lines. It does not calculate statutory tax or assert receipt/settlement.
// Quote/version identify a revision; original terms cannot be overwritten.
type Invoice struct {
	ID               ids.ID      `json:"invoice_id"`
	Kind             string      `json:"kind"`
	Revision         string      `json:"revision"`
	AgreementRef     string      `json:"agreement_ref"`
	AgreementVersion string      `json:"agreement_version"`
	BusinessRef      string      `json:"business_ref"`
	PayerRef         string      `json:"payer_ref"`
	Currency         string      `json:"currency"`
	AmountMinor      int64       `json:"amount_minor"`
	PolicyID         ids.ID      `json:"policy_id"`
	Components       []Component `json:"components"`
}

func (s *Service) Invoice(ctx context.Context, id ids.ID) (Invoice, error) {
	q, e := s.Quote(ctx, id)
	if e != nil {
		return Invoice{}, e
	}
	return Invoice{ID: q.ID, Kind: "ACCEPTED_TERMS", Revision: q.QuoteVersion, AgreementRef: q.AgreementRef, AgreementVersion: q.AgreementVersion, BusinessRef: q.BusinessRef, PayerRef: q.LegalPartyRef, Currency: q.Currency, AmountMinor: q.AmountMinor, PolicyID: q.PolicyID, Components: q.Components}, nil
}
