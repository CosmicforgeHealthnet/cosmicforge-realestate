// Package providers contains provider-neutral roles. Unsupported regulated
// operations return ErrUnsupported; a merchant balance is never custody proof.
package providers

import (
	"context"
	"errors"
	"time"
)

var ErrUnsupported = errors.New("provider capability unavailable")
var ErrUnknown = errors.New("provider outcome unknown; read-back required")
var ErrMismatch = errors.New("provider response context mismatch")
var ErrSignature = errors.New("invalid provider signature")

type ChargeRequest struct {
	Reference, Email, Currency, IntentID string
	AmountMinor                          int64
	Channels                             []string
}
type Initialized struct{ Reference, CheckoutURL string }
type Charge struct {
	Reference, State, Currency, Mode, IntentID string
	AmountMinor                                int64
	MerchantID                                 int64
	CustomerRef                                string
	FeeMinor                                   int64
}
type Callback struct{ EventType, Reference string }
type Collection interface {
	InitializeCharge(context.Context, ChargeRequest) (Initialized, error)
	VerifyCharge(context.Context, string) (Charge, error)
	CancelCharge(context.Context, string) error
	ReceiveWebhook([]byte, string) (Callback, error)
}
type Customer struct {
	Code       string
	Identified bool
	MerchantID int64
	Mode       string
}
type CustomerRequest struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Phone     string `json:"phone"`
}
type Validation struct {
	Country       string `json:"country"`
	Type          string `json:"type"`
	AccountNumber string `json:"account_number"`
	BVN           string `json:"bvn"`
	BankCode      string `json:"bank_code"`
	FirstName     string `json:"first_name"`
	LastName      string `json:"last_name"`
}
type VirtualAccount struct {
	ID                      string
	CustomerRef             string
	AccountNumber, BankCode string
	Active                  bool
	MerchantID              int64
	Mode                    string
}
type DedicatedAccounts interface {
	CreateCustomer(context.Context, CustomerRequest) (Customer, error)
	FetchCustomer(context.Context, string) (Customer, error)
	ValidateCustomer(context.Context, string, Validation) error
	AssignDVA(context.Context, string) (VirtualAccount, error)
	FetchDVA(context.Context, string) (VirtualAccount, error)
	FindDVA(context.Context, string) (VirtualAccount, error)
	RequeryDVA(context.Context, string, string) error
	DeactivateDVA(context.Context, string) error
}
type Settlement struct {
	Reference             string
	AmountMinor, FeeMinor int64
	Currency, State       string
	SettledAt             *time.Time
}
type MerchantBalance struct {
	Currency    string
	AmountMinor int64
}
type SettlementProvider interface {
	FetchSettlement(context.Context, string) (Settlement, error)
	FetchBalance(context.Context) ([]MerchantBalance, error)
	ListTransactions(context.Context, string) ([]Charge, error)
}
type SettlementTransactions interface {
	SettlementTransactions(context.Context, string) ([]Charge, error)
}
type CustodyProof struct {
	Reference, Currency, LegalEntity string
	AmountMinor                      int64
	ObservedAt                       time.Time
}
type Custody interface {
	Reserve(context.Context, string, int64) error
	Release(context.Context, string, int64) error
	Refund(context.Context, string, int64) error
	Proof(context.Context, string) (CustodyProof, error)
}
type Bank struct{ BankCode, AccountNumber, Name string }
type Transfer struct {
	Reference, State   string
	RecipientRef, Mode string
	MerchantID         int64
	NoEffectConfirmed  bool
	Reversed           bool
	FeeMinor           int64
	FeeKnown           bool
	AmountMinor        int64
	Currency           string
}
type Payout interface {
	VerifyBank(context.Context, Bank) (Bank, error)
	CreateRecipient(context.Context, Bank) (string, error)
	InitiateTransfer(context.Context, Transfer) (Transfer, error)
	VerifyTransfer(context.Context, string) (Transfer, error)
	FinalizeOTP(context.Context, string, string) error
}
type Recipient struct {
	Reference, Currency, Mode string
	MerchantID                int64
	Active                    bool
}
type RecipientLookup interface {
	FetchRecipient(context.Context, string) (Recipient, error)
}
type Refund struct {
	Reference, ChargeReference, State string
	AmountMinor                       int64
	ID, Currency, Mode                string
	MerchantID                        int64
	NoEffectConfirmed                 bool
}
type RefundProvider interface {
	InitiateRefund(context.Context, Refund) (Refund, error)
	VerifyRefund(context.Context, Refund) (Refund, error)
}
type Reversal struct {
	Reference, EconomicReference, ChargeReference, Currency, Mode string
	AmountMinor                                                   int64
	MerchantID                                                    int64
	Confirmed                                                     bool
}
type ReversalProvider interface {
	VerifyReversal(context.Context, string) (Reversal, error)
}

// Mandates are a reserved contract only. No implementation is registered.
type Mandates interface {
	CreateConsent(context.Context, string) (string, error)
	AuthorizeMandate(context.Context, string) error
	SuspendMandate(context.Context, string) error
	AttemptDebit(context.Context, string, int64) error
	RevokeMandate(context.Context, string) error
}

type DisabledCustody struct{}

func (DisabledCustody) Reserve(context.Context, string, int64) error { return ErrUnsupported }
func (DisabledCustody) Release(context.Context, string, int64) error { return ErrUnsupported }
func (DisabledCustody) Refund(context.Context, string, int64) error  { return ErrUnsupported }
func (DisabledCustody) Proof(context.Context, string) (CustodyProof, error) {
	return CustodyProof{}, ErrUnsupported
}
