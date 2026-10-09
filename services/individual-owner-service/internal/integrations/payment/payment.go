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

// Payment purposes the owner domain requests.
const (
	PurposeRent    = "RENT"
	PurposeLateFee = "LATE_FEE"
	PurposeDeposit = "DEPOSIT"
)

// IntentRequest asks payment-service to collect a business amount. The owner
// domain supplies the business context only; payment-service owns methods,
// providers, ledger and settlement.
type IntentRequest struct {
	Purpose             string
	ReferenceType       string // rent_obligation | deposit_obligation
	ReferenceID         ids.ID
	PayeeOwnerAccountID ids.ID
	PayerDomain         string
	PayerRefID          *ids.ID
	AmountMinor         int64
	Currency            string
	DueDate             time.Time
	Escrow              bool // deposits are held, not paid out
}

// Intent statuses (payment-service vocabulary as consumed here).
const (
	IntentRequiresPayment = "REQUIRES_PAYMENT"
	IntentProcessing      = "PROCESSING"
	IntentPartiallyPaid   = "PARTIALLY_PAID"
	IntentSucceeded       = "SUCCEEDED"
	IntentHeld            = "HELD"
	IntentFailed          = "FAILED"
	IntentCancelled       = "CANCELLED"
	IntentRefundPending   = "REFUND_PENDING"
	IntentRefunded        = "REFUNDED"
)

// Intent is payment-service's authoritative view of a collection.
type Intent struct {
	Ref           string
	Status        string
	AmountMinor   int64
	PaidMinor     int64
	RefundedMinor int64
	UpdatedAt     time.Time
}

// DepositReleaseRequest settles a held deposit: the tenant refund and the
// owner's agreed deductions. payment-service performs the money movement.
type DepositReleaseRequest struct {
	DepositID        ids.ID
	IntentRef        string
	RefundMinor      int64
	DeductionMinor   int64
	WithheldMinor    int64 // disputed amount frozen pending resolution
	Currency         string
	OwnerAccountID   ids.ID
	DeductionSummary string
}

// Release statuses.
const (
	ReleasePending   = "PENDING"
	ReleaseCompleted = "COMPLETED"
	ReleasePartial   = "PARTIAL" // released except the withheld amount
	ReleaseFailed    = "FAILED"
)

// Release is payment-service's view of a deposit settlement.
type Release struct {
	Ref           string
	Status        string
	ReleasedMinor int64
}

// API is the expected payment-service contract.
type API interface {
	RegisterBeneficiary(ctx context.Context, req BeneficiaryRequest, idemKey string) (Beneficiary, error)
	GetBeneficiary(ctx context.Context, ref string) (Beneficiary, error)
	ListStatements(ctx context.Context, ownerAccountID ids.ID, cursor string, limit int) ([]Statement, string, error)
	// CreateIntent is idempotent per idemKey.
	CreateIntent(ctx context.Context, req IntentRequest, idemKey string) (Intent, error)
	GetIntent(ctx context.Context, ref string) (Intent, error)
	// CancelIntent stops collection of an intent that is no longer owed
	// (waived obligation, tenancy cancelled before move-in). Idempotent;
	// returns the authoritative intent (it may already have succeeded).
	CancelIntent(ctx context.Context, ref, reason string) (Intent, error)
	// RequestDepositRelease is idempotent per idemKey.
	RequestDepositRelease(ctx context.Context, req DepositReleaseRequest, idemKey string) (Release, error)
	GetRelease(ctx context.Context, ref string) (Release, error)
}
