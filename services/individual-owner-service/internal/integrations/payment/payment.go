// Package payment is the consumer-side contract of payment-service as the
// owner domain needs it (spec §9, §12). payment-service is the sole
// financial authority: beneficiary (payout account) verification, payment
// intents, obligations' money state, refunds, payouts and statements.
//
// payment-service is not implemented in this repository yet, so there is no
// HTTP adapter: the owner service is built with a nil API and every
// dependent feature answers 503 FEATURE_UNAVAILABLE. Financial state in
// individual_owner_db changes only from payment.* / payout.* / refund.*
// events (never synthesized locally).
package payment

import (
	"context"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// BeneficiaryRequest registers a payout destination. The full account
// number is forwarded to payment-service and never stored by the owner domain.
type BeneficiaryRequest struct {
	OwnerAccountID ids.ID
	BankCode       string
	AccountNumber  string
	AccountName    string
	Currency       string
}

// Beneficiary is payment-service's verification result.
type Beneficiary struct {
	Ref        string
	Status     string // PENDING | VERIFIED | REJECTED
	NameMatch  string // MATCH | PARTIAL | MISMATCH | UNKNOWN
	VerifiedAt *time.Time
}

// Statement is an owner statement period summary owned by payment-service.
type Statement struct {
	StatementID ids.ID    `json:"statement_id"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Currency    string    `json:"currency"`
	DownloadRef string    `json:"download_ref"`
}

// API is the expected payment-service contract.
type API interface {
	RegisterBeneficiary(ctx context.Context, req BeneficiaryRequest, idemKey string) (Beneficiary, error)
	GetBeneficiary(ctx context.Context, ref string) (Beneficiary, error)
	ListStatements(ctx context.Context, ownerAccountID ids.ID, cursor string, limit int) ([]Statement, string, error)
}
