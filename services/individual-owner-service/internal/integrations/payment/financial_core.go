package payment

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
)

// Published Core DTOs, deliberately separate from the legacy payment.API.
// Owner never calls a provider, invents an approval or stores ledger balances.
type CoreComponent struct {
	Ref                string `json:"component_ref"`
	Kind               string `json:"kind"`
	AmountMinor        int64  `json:"amount_minor"`
	PayerRef           string `json:"payer_ref"`
	Recurrence         string `json:"recurrence"`
	ServiceEvidenceRef string `json:"service_evidence_ref,omitempty"`
}
type CoreAuthority struct {
	BeneficiaryDomain    string    `json:"beneficiary_source_domain"`
	BeneficiaryAccountID ids.ID    `json:"beneficiary_source_account_id"`
	ID                   ids.ID    `json:"authority_id"`
	Domain               string    `json:"domain"`
	BusinessRef          string    `json:"business_ref"`
	WalletID             *ids.ID   `json:"wallet_id,omitempty"`
	LegalPartyRef        string    `json:"legal_party_ref"`
	Role                 string    `json:"role"`
	Currency             string    `json:"currency"`
	LegalEntity          string    `json:"legal_entity"`
	Components           []string  `json:"components"`
	EvidenceRef          string    `json:"evidence_ref"`
	MandateRef           string    `json:"mandate_ref"`
	OwnershipBPS         int       `json:"ownership_bps"`
	MaxMinor             int64     `json:"max_minor"`
	ExpiresAt            time.Time `json:"expires_at"`
	Active               bool      `json:"active"`
}
type CoreLineInput struct {
	AuthorityID  ids.ID `json:"authority_id" v:"required"`
	ComponentRef string `json:"component_ref" v:"required,max=128"`
	Kind         string `json:"kind" v:"required,enum=BENEFICIARY|PLATFORM|DEPOSIT|RESERVE"`
	AmountMinor  int64  `json:"amount_minor" v:"required,min=1"`
	Currency     string `json:"currency" v:"required,format=currency"`
}
type CoreLine struct {
	ID ids.ID `json:"line_id"`
	CoreLineInput
	WalletID      *ids.ID `json:"wallet_id,omitempty"`
	State         string  `json:"state"`
	ReleasedMinor int64   `json:"released_minor"`
	ReservedMinor int64   `json:"reserved_minor"`
	PaidMinor     int64   `json:"paid_minor"`
}
type CoreAllocation struct {
	ID          ids.ID     `json:"allocation_id"`
	IntentID    ids.ID     `json:"intent_id"`
	Version     int        `json:"version"`
	QuoteID     ids.ID     `json:"quote_id"`
	PolicyID    ids.ID     `json:"policy_id"`
	PoolID      ids.ID     `json:"pool_id"`
	Currency    string     `json:"currency"`
	LegalEntity string     `json:"legal_entity"`
	Lines       []CoreLine `json:"lines"`
}
type CoreReceipt struct {
	Quote            AcceptedQuote    `json:"quote"`
	IntentID         *ids.ID          `json:"intent_id,omitempty"`
	CollectionState  string           `json:"collection_state"`
	SettlementState  string           `json:"settlement_state"`
	Allocations      []CoreAllocation `json:"allocations"`
	FundingConfirmed bool             `json:"funding_confirmed"`
	ReversedMinor    int64            `json:"reversed_minor"`
	AwardAllocations []CoreAllocation `json:"award_allocations"`
	PayerContext     CorePayerContext `json:"payer_context"`
}
type CorePayerContext struct {
	WalletID          ids.ID `json:"wallet_id"`
	Domain            string `json:"source_domain"`
	AccountID         ids.ID `json:"source_account_id"`
	CanonicalPartyRef string `json:"canonical_party_ref"`
}
type CoreDeposit struct {
	ID            ids.ID    `json:"deposit_id"`
	LineID        ids.ID    `json:"line_id"`
	Type          string    `json:"deposit_type"`
	CustodyRef    string    `json:"custody_ref"`
	IngressRef    string    `json:"ingress_ref"`
	InspectionIDs []ids.ID  `json:"inspection_evidence_ids"`
	IntentID      ids.ID    `json:"intent_id"`
	QuoteID       ids.ID    `json:"quote_id"`
	PayerWalletID ids.ID    `json:"payer_wallet_id"`
	BusinessRef   string    `json:"business_ref"`
	PolicyID      ids.ID    `json:"policy_id"`
	Jurisdiction  string    `json:"jurisdiction"`
	Currency      string    `json:"currency"`
	AmountMinor   int64     `json:"amount_minor"`
	State         string    `json:"state"`
	ClaimDeadline time.Time `json:"claim_deadline"`
}
type CoreCaseRequest struct {
	ID            ids.ID   `json:"case_id" v:"required"`
	LineID        ids.ID   `json:"line_id" v:"required"`
	DepositID     *ids.ID  `json:"deposit_id,omitempty"`
	Kind          string   `json:"kind" v:"required,enum=DEPOSIT_CLAIM|DISPUTE|CANCELLATION|REFUND_REVIEW"`
	AmountMinor   int64    `json:"contested_minor" v:"required,min=1"`
	ClaimantRef   string   `json:"claimant_ref"`
	RespondentRef string   `json:"respondent_ref"`
	AuthorityRef  string   `json:"authority_ref"`
	ReasonRef     string   `json:"reason_ref" v:"required,max=500"`
	EvidenceIDs   []ids.ID `json:"evidence_ids" v:"required,max=30"`
}
type CoreCase struct {
	CoreCaseRequest
	IntentID    ids.ID `json:"intent_id"`
	QuoteID     ids.ID `json:"quote_id"`
	BusinessRef string `json:"business_ref"`
	Currency    string `json:"currency"`
	State       string `json:"state"`
}
type CoreRefund struct {
	ID                ids.ID `json:"refund_id"`
	LineID            ids.ID `json:"line_id"`
	CaseID            ids.ID `json:"case_id"`
	AmountMinor       int64  `json:"amount_minor"`
	IntentID          ids.ID `json:"intent_id"`
	WalletID          ids.ID `json:"payer_wallet_id"`
	ProviderID        ids.ID `json:"provider_account_id"`
	PoolID            ids.ID `json:"pool_id"`
	Currency          string `json:"currency"`
	State             string `json:"state"`
	OriginalReference string `json:"original_reference"`
}
type CoreRelease struct {
	LineID        ids.ID   `json:"line_id" v:"required"`
	AmountMinor   int64    `json:"amount_minor" v:"required,min=1"`
	PolicyVersion string   `json:"policy_version" v:"required,max=128"`
	EvidenceIDs   []ids.ID `json:"evidence_ids" v:"required,max=30"`
}
type CoreEvidence struct {
	ID               ids.ID    `json:"evidence_id" v:"required"`
	LineID           ids.ID    `json:"line_id" v:"required"`
	Condition        string    `json:"condition" v:"required,max=128"`
	Reference        string    `json:"evidence_ref"`
	Version          string    `json:"evidence_version"`
	PolicyVersion    string    `json:"policy_version"`
	ExpiresAt        time.Time `json:"expires_at" v:"required"`
	HumanApprovalRef string    `json:"human_approval_ref"`
}
type CorePayoutReference struct {
	ID              ids.ID    `json:"payout_account_id"`
	WalletID        ids.ID    `json:"wallet_id"`
	BeneficiaryRef  string    `json:"beneficiary_ref"`
	VerificationRef string    `json:"verification_ref"`
	Jurisdiction    string    `json:"jurisdiction"`
	ExpiresAt       time.Time `json:"expires_at"`
	Eligible        bool      `json:"eligible"`
}
type CoreHistoryEntry struct {
	EntryID      ids.ID    `json:"entry_id"`
	JournalID    ids.ID    `json:"journal_id"`
	OperationRef string    `json:"operation_ref"`
	Kind         string    `json:"kind"`
	Side         string    `json:"side"`
	AmountMinor  int64     `json:"amount_minor"`
	Currency     string    `json:"currency"`
	CreatedAt    time.Time `json:"created_at"`
}
type CoreAvailability struct {
	WalletID               ids.ID   `json:"wallet_id"`
	PoolID                 ids.ID   `json:"pool_id"`
	Currency               string   `json:"currency"`
	AvailableMinor         string   `json:"available_minor"`
	ProtectedMinor         string   `json:"protected_minor"`
	PayoutPendingMinor     string   `json:"payout_pending_minor"`
	SettlementPendingMinor string   `json:"settlement_pending_minor"`
	ReservedMinor          string   `json:"reserved_minor"`
	WithdrawableNow        bool     `json:"withdrawable_now"`
	Status                 []string `json:"status"`
}
type CoreWithdrawal struct {
	ApprovalID      ids.ID `json:"approval_id" v:"required"`
	LineID          ids.ID `json:"line_id" v:"required"`
	WalletID        ids.ID `json:"wallet_id"`
	PayoutAccountID ids.ID `json:"payout_account_id" v:"required"`
	AmountMinor     int64  `json:"amount_minor" v:"required,min=1"`
}
type CorePayout struct {
	ID          ids.ID `json:"payout_id"`
	LineID      ids.ID `json:"line_id"`
	WalletID    ids.ID `json:"wallet_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	ProviderID  ids.ID `json:"provider_account_id"`
	PoolID      ids.ID `json:"pool_id"`
	Reference   string `json:"provider_reference"`
	State       string `json:"state"`
}
type CoreCancellation struct {
	IntentID     ids.ID   `json:"intent_id"`
	AuthorityRef string   `json:"authority_ref"`
	ReasonRef    string   `json:"reason_ref"`
	EvidenceIDs  []ids.ID `json:"evidence_ids"`
}

func coreActor(ctx context.Context, account ids.ID) context.Context {
	return requestcontext.WithActor(ctx, requestcontext.Actor{Domain: "individual-owner", ID: account})
}
func coreCall[T any](p *WalletProvisioner, ctx context.Context, method, path, key string, body any) (T, error) {
	var envelope struct {
		Data T `json:"data"`
	}
	e := p.Client.Do(ctx, httpx.Request{Method: method, Path: "/internal/v1" + path, IdempotencyKey: key, Body: body}, &envelope)
	return envelope.Data, e
}
func (p *WalletProvisioner) Policy(ctx context.Context, id ids.ID) (CollectionPolicy, error) {
	return coreCall[CollectionPolicy](p, ctx, "GET", "/financial-policies/"+id.String(), "", nil)
}
func (p *WalletProvisioner) Receipt(ctx context.Context, id ids.ID) (CoreReceipt, error) {
	v, e := coreCall[CoreReceipt](p, ctx, "GET", "/locked-quotes/"+id.String()+"/receipt", "", nil)
	if e == nil && v.Quote.ID != id {
		e = fmt.Errorf("receipt quote context mismatch")
	}
	return v, e
}
func (p *WalletProvisioner) Authority(ctx context.Context, id ids.ID) (CoreAuthority, error) {
	v, e := coreCall[CoreAuthority](p, ctx, "GET", "/beneficiary-authorities/"+id.String(), "", nil)
	if e == nil && v.ID != id {
		e = fmt.Errorf("authority context mismatch")
	}
	return v, e
}
func (p *WalletProvisioner) Allocate(ctx context.Context, intent ids.ID, lines []CoreLineInput, key string) (CoreAllocation, error) {
	return coreCall[CoreAllocation](p, ctx, "POST", "/allocations", key, struct {
		IntentID ids.ID          `json:"intent_id"`
		Version  int             `json:"version"`
		Lines    []CoreLineInput `json:"lines"`
	}{intent, 1, lines})
}
func (p *WalletProvisioner) SubmitEvidence(ctx context.Context, in CoreEvidence) error {
	_, e := coreCall[map[string]bool](p, ctx, "POST", "/release-evidence", "", in)
	return e
}
func (p *WalletProvisioner) Release(ctx context.Context, in CoreRelease, key string) (ids.ID, error) {
	v, e := coreCall[struct {
		ID ids.ID `json:"journal_id"`
	}](p, ctx, "POST", "/allocation-releases", key, in)
	return v.ID, e
}
func (p *WalletProvisioner) RegisterDeposit(ctx context.Context, in CoreDeposit) (CoreDeposit, error) {
	return coreCall[CoreDeposit](p, ctx, "POST", "/refundable-deposits", "", in)
}
func (p *WalletProvisioner) Deposit(ctx context.Context, id ids.ID) (CoreDeposit, error) {
	v, e := coreCall[CoreDeposit](p, ctx, "GET", "/refundable-deposits/"+id.String(), "", nil)
	if e == nil && v.ID != id {
		e = fmt.Errorf("deposit context mismatch")
	}
	return v, e
}
func (p *WalletProvisioner) OpenCase(ctx context.Context, in CoreCaseRequest, key string) (CoreCase, error) {
	return coreCall[CoreCase](p, ctx, "POST", "/financial-cases", key, in)
}
func (p *WalletProvisioner) Case(ctx context.Context, id ids.ID) (CoreCase, error) {
	v, e := coreCall[CoreCase](p, ctx, "GET", "/financial-cases/"+id.String(), "", nil)
	if e == nil && v.ID != id {
		e = fmt.Errorf("case context mismatch")
	}
	return v, e
}
func (p *WalletProvisioner) Refund(ctx context.Context, id ids.ID) (CoreRefund, error) {
	v, e := coreCall[CoreRefund](p, ctx, "GET", "/refunds/"+id.String(), "", nil)
	if e == nil && v.ID != id {
		e = fmt.Errorf("refund context mismatch")
	}
	return v, e
}
func (p *WalletProvisioner) Cancel(ctx context.Context, in CoreCancellation, key string) (CollectionIntent, error) {
	v, e := coreCall[CollectionIntent](p, ctx, "POST", "/intent-cancellations", key, in)
	if e == nil && v.ID != in.IntentID {
		e = fmt.Errorf("cancelled intent context mismatch")
	}
	return v, e
}
func (p *WalletProvisioner) PayoutReferences(ctx context.Context, account, wallet ids.ID) ([]CorePayoutReference, error) {
	vs, e := coreCall[[]CorePayoutReference](p, coreActor(ctx, account), "GET", "/wallets/"+wallet.String()+"/payout-account-references", "", nil)
	for _, v := range vs {
		if v.WalletID != wallet {
			return nil, fmt.Errorf("payout reference context mismatch")
		}
	}
	return vs, e
}
func (p *WalletProvisioner) History(ctx context.Context, account, wallet, after ids.ID, limit int) ([]CoreHistoryEntry, error) {
	query := url.Values{"limit": []string{strconv.Itoa(limit)}}
	if after != ids.Nil {
		query.Set("after", after.String())
	}
	var out struct {
		Data []CoreHistoryEntry `json:"data"`
	}
	e := p.Client.Do(coreActor(ctx, account), httpx.Request{Method: "GET", Path: "/internal/v1/wallets/" + wallet.String() + "/transactions", Query: query}, &out)
	return out.Data, e
}
func (p *WalletProvisioner) Availability(ctx context.Context, account, wallet, pool ids.ID) (CoreAvailability, error) {
	v, e := coreCall[CoreAvailability](p, coreActor(ctx, account), "GET", "/wallets/"+wallet.String()+"/availability/"+pool.String(), "", nil)
	if e == nil && (v.WalletID != wallet || v.PoolID != pool) {
		e = fmt.Errorf("availability context mismatch")
	}
	return v, e
}
func (p *WalletProvisioner) Withdraw(ctx context.Context, account ids.ID, in CoreWithdrawal, key string) (CorePayout, error) {
	v, e := coreCall[CorePayout](p, coreActor(ctx, account), "POST", "/withdrawals", key, in)
	if e == nil && (v.WalletID != in.WalletID || v.LineID != in.LineID || v.AmountMinor != in.AmountMinor) {
		e = fmt.Errorf("payout context mismatch")
	}
	return v, e
}
func (p *WalletProvisioner) Payout(ctx context.Context, account, id ids.ID, resolve bool) (CorePayout, error) {
	method, path := "GET", "/withdrawals/"+id.String()
	if resolve {
		method, path = "POST", path+"/resolve"
	}
	v, e := coreCall[CorePayout](p, coreActor(ctx, account), method, path, "", nil)
	if e == nil && v.ID != id {
		e = fmt.Errorf("payout readback context mismatch")
	}
	return v, e
}
