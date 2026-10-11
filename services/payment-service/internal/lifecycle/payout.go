package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/finance"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"time"
)

type Destination struct {
	ID              ids.ID    `json:"binding_id"`
	PayoutAccountID ids.ID    `json:"payout_account_id"`
	ProviderID      ids.ID    `json:"provider_account_id"`
	RecipientRef    string    `json:"recipient_ref"`
	EvidenceRef     string    `json:"evidence_ref"`
	ExpiresAt       time.Time `json:"expires_at"`
}

func (s *Service) BindDestination(ctx context.Context, in Destination) error {
	if e := core.Require(ctx, platform.VerificationService); e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || in.EvidenceRef == "" || in.RecipientRef == "" || !in.ExpiresAt.After(time.Now()) {
		return fail("VERIFIED_DESTINATION_REQUIRED")
	}
	a, e := s.Adapter(in.ProviderID)
	if e != nil {
		return e
	}
	lookup, ok := a.Payout.(providers.RecipientLookup)
	if !ok {
		return core.Fail(503, "RECIPIENT_VERIFICATION_UNAVAILABLE")
	}
	r, e := lookup.FetchRecipient(ctx, in.RecipientRef)
	if e != nil {
		return core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	var currency, merchant, mode string
	var valid bool
	e = s.DB.QueryRow(ctx, `SELECT w.currency,p.merchant_ref,p.mode,(b.expires_at>=$3 AND i.state='ACTIVE' AND v.state='VERIFIED' AND v.expires_at>=$3 AND v.verification_ref=b.verification_ref AND w.legal_entity=p.legal_entity) FROM payout_account_refs b JOIN wallets w USING(wallet_id) JOIN financial_identities i USING(identity_id) JOIN verification_refs v USING(identity_id) CROSS JOIN provider_accounts p WHERE b.payout_account_id=$1 AND p.provider_account_id=$2`, in.PayoutAccountID, in.ProviderID, in.ExpiresAt).Scan(&currency, &merchant, &mode, &valid)
	if e != nil {
		return e
	}
	if !valid || !r.Active || r.Reference != in.RecipientRef || r.Currency != currency || r.Mode != mode || formatID(r.MerchantID) != merchant {
		return core.Fail(403, "VERIFIED_DESTINATION_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "destination:"+in.PayoutAccountID.String()+in.ProviderID.String()); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM payout_destination_bindings WHERE payout_account_id=$1 AND provider_account_id=$2`, in.PayoutAccountID, in.ProviderID).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "DESTINATION_IMMUTABLE")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO payout_destination_bindings(binding_id,payout_account_id,provider_account_id,recipient_ref,evidence_ref,fingerprint,snapshot,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, in.ID, in.PayoutAccountID, in.ProviderID, in.RecipientRef, in.EvidenceRef, core.Fingerprint(in), raw(in), in.ExpiresAt)
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "payout.destination_bound", in.ID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type WithdrawalApproval struct {
	ID               ids.ID    `json:"approval_id"`
	LineID           ids.ID    `json:"line_id"`
	WalletID         ids.ID    `json:"wallet_id"`
	PayoutAccountID  ids.ID    `json:"payout_account_id"`
	AmountMinor      int64     `json:"amount_minor"`
	ApprovalChainRef string    `json:"approval_chain_ref,omitempty"`
	BranchProjectRef string    `json:"branch_project_ref,omitempty"`
	ConsentRef       string    `json:"consent_ref,omitempty"`
	WorkMandateRef   string    `json:"work_mandate_ref,omitempty"`
	StepUpRef        string    `json:"step_up_ref"`
	RiskRef          string    `json:"risk_ref"`
	Approvers        []string  `json:"approvers"`
	ExpiresAt        time.Time `json:"expires_at"`
}

func (s *Service) ApproveWithdrawal(ctx context.Context, in WithdrawalApproval) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	a, e := s.lineAllocation(ctx, in.LineID)
	if e != nil {
		return e
	}
	p, e := s.Policy(ctx, a.PolicyID)
	if e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || in.AmountMinor <= 0 || p.WithdrawalLimitMinor == 0 || in.AmountMinor > p.WithdrawalLimitMinor || in.StepUpRef == "" || in.RiskRef == "" || !in.ExpiresAt.After(time.Now()) || (p.HumanReview || (p.DualApprovalAboveMinor > 0 && in.AmountMinor >= p.DualApprovalAboveMinor)) && !dual(in.Approvers) {
		return core.Fail(403, "WITHDRAWAL_APPROVAL_REQUIRED")
	}
	if e = s.payoutPartyControl(ctx, s.DB, in); e != nil {
		return e
	}
	var owns bool
	e = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM allocation_lines l JOIN payout_account_refs b USING(wallet_id) WHERE l.line_id=$1 AND l.wallet_id=$2 AND b.payout_account_id=$3 AND b.expires_at>=$4)`, in.LineID, in.WalletID, in.PayoutAccountID, in.ExpiresAt).Scan(&owns)
	if e != nil {
		return e
	}
	if !owns {
		return core.Fail(403, "PAYOUT_DESTINATION_OWNERSHIP_MISMATCH")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	op := "approval:" + in.ID.String()
	if e = lock(ctx, tx, op); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM lifecycle_decisions WHERE operation_key=$1`, op).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO lifecycle_decisions(decision_id,aggregate_id,operation_key,action,fingerprint,snapshot) VALUES($1,$2,$3,'WITHDRAWAL_APPROVAL',$4,$5)`, in.ID, in.LineID, op, core.Fingerprint(in), raw(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "withdrawal.approved", in.LineID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type Withdrawal struct {
	ApprovalID      ids.ID `json:"approval_id"`
	LineID          ids.ID `json:"line_id"`
	WalletID        ids.ID `json:"wallet_id"`
	PayoutAccountID ids.ID `json:"payout_account_id"`
	AmountMinor     int64  `json:"amount_minor"`
}
type Payout struct {
	ID              ids.ID `json:"payout_id"`
	LineID          ids.ID `json:"line_id"`
	WalletID        ids.ID `json:"wallet_id"`
	PayoutAccountID ids.ID `json:"payout_account_id"`
	ProviderID      ids.ID `json:"provider_account_id"`
	PoolID          ids.ID `json:"pool_id"`
	AmountMinor     int64  `json:"amount_minor"`
	Currency        string `json:"currency"`
	Reference       string `json:"provider_reference"`
	RecipientRef    string `json:"recipient_ref"`
	State           string `json:"state"`
}

func (s *Service) payout(ctx context.Context, q query, id ids.ID) (p Payout, e error) {
	e = q.QueryRow(ctx, `SELECT payout_id,line_id,wallet_id,payout_account_id,provider_account_id,pool_id,amount_minor,currency,provider_reference,recipient_ref,state FROM payout_attempts WHERE payout_id=$1`, id).Scan(&p.ID, &p.LineID, &p.WalletID, &p.PayoutAccountID, &p.ProviderID, &p.PoolID, &p.AmountMinor, &p.Currency, &p.Reference, &p.RecipientRef, &p.State)
	return
}
func (s *Service) Payout(ctx context.Context, id ids.ID) (Payout, error) {
	p, e := s.payout(ctx, s.DB, id)
	if e != nil {
		return p, core.Fail(404, "PAYOUT_NOT_FOUND")
	}
	caller, e := core.Caller(ctx)
	if e != nil {
		return Payout{}, e
	}
	if caller.Service != platform.PaymentService && caller.Service != platform.AdminService {
		if _, e = s.OwnWallet(ctx, p.WalletID); e != nil {
			return Payout{}, e
		}
	}
	var reversed bool
	if e = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_reversals WHERE payout_id=$1)`, id).Scan(&reversed); e != nil {
		return Payout{}, e
	}
	if reversed {
		p.State = "REVERSED"
	}
	return p, nil
}
func (s *Service) Withdraw(ctx context.Context, in Withdrawal, key string) (Payout, error) {
	ctx = beneficiaryContext(ctx)
	if s.CheckEnvironmentCapability("WITHDRAWAL") != nil || s.CheckEnvironmentCapability("PROVIDER_PAYOUT") != nil {
		return Payout{}, core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	w, e := s.OwnWallet(ctx, in.WalletID)
	if e != nil {
		return Payout{}, e
	}
	if e = core.DomainCaller(ctx, w.Domain); e != nil {
		return Payout{}, e
	}
	if w.State != "ACTIVE" || key == "" || len(key) > 128 || in.AmountMinor <= 0 {
		return Payout{}, fail("INVALID_WITHDRAWAL")
	}
	a, e := s.lineAllocation(ctx, in.LineID)
	if e != nil {
		return Payout{}, e
	}
	policy, e := s.Policy(ctx, a.PolicyID)
	if e != nil {
		return Payout{}, e
	}
	if in.AmountMinor > policy.WithdrawalLimitMinor || policy.WithdrawalLimitMinor == 0 {
		return Payout{}, core.Fail(403, "WITHDRAWAL_POLICY_DISABLED")
	}
	var approval WithdrawalApproval
	if e = load(ctx, s.DB, `SELECT snapshot FROM lifecycle_decisions WHERE decision_id=$1 AND action='WITHDRAWAL_APPROVAL'`, in.ApprovalID, &approval); e != nil {
		return Payout{}, e
	}
	if approval.LineID != in.LineID || approval.WalletID != in.WalletID || approval.PayoutAccountID != in.PayoutAccountID || approval.AmountMinor != in.AmountMinor || !approval.ExpiresAt.After(time.Now()) {
		return Payout{}, core.Fail(403, "WITHDRAWAL_APPROVAL_REQUIRED")
	}
	var config finance.ProviderConfig
	var cfgRaw []byte
	e = s.DB.QueryRow(ctx, `SELECT p.config FROM custody_pools c JOIN provider_accounts p USING(provider_account_id) WHERE pool_id=$1`, a.PoolID).Scan(&cfgRaw)
	if e != nil {
		return Payout{}, e
	}
	if e = decode(cfgRaw, &config); e != nil {
		return Payout{}, e
	}
	adapter, e := s.Adapter(config.ID)
	if e != nil {
		return Payout{}, e
	}
	if !config.Approved || !config.Operational || !config.PayoutApproved || config.PayoutApprovalRef == "" || !config.PayoutFeeApproved || config.PayoutFeeMinor < 0 || config.Mode != "test" || !has(config.Capabilities, "TRANSFER") || adapter.Payout == nil {
		return Payout{}, core.Fail(403, "PAYOUT_CAPABILITY_DISABLED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Payout{}, e
	}
	defer tx.Rollback(ctx)
	op := "withdraw:" + in.WalletID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return Payout{}, e
	}
	var previous ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT payout_id,fingerprint FROM payout_attempts WHERE operation_key=$1`, op).Scan(&previous, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return Payout{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return s.payout(ctx, tx, previous)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Payout{}, e
	}
	if e = lock(ctx, tx, "withdrawal-approval:"+in.ApprovalID.String()); e != nil {
		return Payout{}, e
	}
	var used bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payout_attempts WHERE authorization_snapshot->>'approval_id'=$1)`, in.ApprovalID.String()).Scan(&used)
	if e != nil {
		return Payout{}, e
	}
	if used {
		return Payout{}, core.Fail(409, "WITHDRAWAL_APPROVAL_ALREADY_USED")
	}
	var wallet *ids.ID
	var state string
	var released, reserved int64
	var available *ids.ID
	var authority ids.ID
	e = tx.QueryRow(ctx, `SELECT wallet_id,state,released_minor,reserved_minor,available_account_id,authority_id FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, in.LineID).Scan(&wallet, &state, &released, &reserved, &available, &authority)
	if e != nil {
		return Payout{}, e
	}
	if wallet == nil || *wallet != in.WalletID || available == nil || !has([]string{"AVAILABLE", "WITHDRAWAL_RESERVED"}, state) || in.AmountMinor > released-reserved {
		return Payout{}, core.Fail(409, "AVAILABLE_FUNDS_REQUIRED")
	}
	if e = s.outstandingRecovery(ctx, tx, in.WalletID); e != nil {
		return Payout{}, e
	}
	if e = s.payoutPartyControl(ctx, tx, approval); e != nil {
		return Payout{}, e
	}
	if e = s.currentDestination(ctx, tx, in.WalletID, in.PayoutAccountID); e != nil {
		return Payout{}, e
	}
	var current bool
	e = tx.QueryRow(ctx, `SELECT true FROM beneficiary_authorities WHERE authority_id=$1 AND NOT revoked AND expires_at>now() FOR SHARE`, authority).Scan(&current)
	if errors.Is(e, pgx.ErrNoRows) {
		return Payout{}, core.Fail(403, "CURRENT_BENEFICIARY_AUTHORITY_REQUIRED")
	}
	if e != nil {
		return Payout{}, e
	}
	if !current {
		return Payout{}, core.Fail(403, "CURRENT_BENEFICIARY_AUTHORITY_REQUIRED")
	}
	var recipient string
	e = tx.QueryRow(ctx, `SELECT d.recipient_ref FROM payout_destination_bindings d JOIN payout_account_refs b USING(payout_account_id) JOIN wallets w USING(wallet_id) JOIN verification_refs v USING(identity_id) WHERE d.payout_account_id=$1 AND d.provider_account_id=$2 AND b.wallet_id=$3 AND d.expires_at>now() AND b.expires_at>now() AND v.state='VERIFIED' AND v.verification_ref=b.verification_ref AND v.expires_at>now() AND b.jurisdiction=$4 FOR SHARE OF v`, in.PayoutAccountID, config.ID, in.WalletID, policy.Jurisdiction).Scan(&recipient)
	if e != nil {
		return Payout{}, core.Fail(403, "VERIFIED_DESTINATION_REQUIRED")
	}
	pending, e := s.Account(trusted(ctx), tx, wallet, a.PoolID, "WITHDRAWAL_PENDING", a.LegalEntity, a.Currency, "allocation:"+in.LineID.String())
	if e != nil {
		return Payout{}, e
	}
	journal, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: op, PoolID: a.PoolID, LegalEntity: a.LegalEntity, Currency: a.Currency, PolicyVersion: policy.Version, Authorization: map[string]string{"evidence_ref": approval.RiskRef, "approved_by": approval.StepUpRef}, Entries: []core.Entry{{AccountID: *available, Side: "DEBIT", AmountMinor: in.AmountMinor}, {AccountID: pending, Side: "CREDIT", AmountMinor: in.AmountMinor}}})
	if e != nil {
		return Payout{}, e
	}
	var feeCovered bool
	e = tx.QueryRow(ctx, `SELECT COALESCE(sum(balance_minor) FILTER(WHERE kind='SETTLED_CASH'),0)-p.reserve_minor-COALESCE(sum(balance_minor) FILTER(WHERE kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','SETTLEMENT_CONTROL','RESERVE','PROVIDER_PAYABLE')),0) >= $2 + COALESCE((SELECT sum((route_snapshot->>'payout_fee_minor')::numeric) FROM payout_attempts WHERE pool_id=$1 AND state NOT IN ('CONFIRMED_PAID','CONFIRMED_FAILED')),0) FROM account_balances JOIN custody_pools p USING(pool_id) WHERE pool_id=$1 GROUP BY p.reserve_minor`, a.PoolID, config.PayoutFeeMinor).Scan(&feeCovered)
	if e != nil {
		return Payout{}, e
	}
	if !feeCovered {
		return Payout{}, core.Fail(409, "PAYOUT_FEE_LIQUIDITY_REQUIRED")
	}
	out := Payout{ID: ids.New(), LineID: in.LineID, WalletID: in.WalletID, PayoutAccountID: in.PayoutAccountID, ProviderID: config.ID, PoolID: a.PoolID, AmountMinor: in.AmountMinor, Currency: a.Currency, RecipientRef: recipient, State: "RESERVED"}
	out.Reference = "cfp-" + out.ID.String()
	_, e = tx.Exec(ctx, `INSERT INTO payout_attempts(payout_id,line_id,wallet_id,payout_account_id,provider_account_id,pool_id,operation_key,fingerprint,amount_minor,currency,provider_reference,recipient_ref,route_snapshot,authorization_snapshot,state,reservation_journal_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'RESERVED',$15)`, out.ID, in.LineID, in.WalletID, in.PayoutAccountID, config.ID, a.PoolID, op, core.Fingerprint(in), in.AmountMinor, a.Currency, out.Reference, recipient, raw(config), raw(approval), journal)
	if e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `UPDATE allocation_lines SET reserved_minor=reserved_minor+$2,state='WITHDRAWAL_RESERVED' WHERE line_id=$1`, in.LineID, in.AmountMinor)
	if e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.withdrawal_reserved.v1", "payout", out.ID, out); e != nil {
		return out, e
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	// One CAS and one external submission. Repeated commands/recovery only read back.
	tag, e := s.ChangeProviderState(trusted(ctx), "PAYOUT", out.ID, "RESERVED", "SUBMITTING", "")
	if e != nil {
		return out, e
	}
	if tag.RowsAffected() != 1 {
		return s.Payout(ctx, out.ID)
	}
	transfer, callErr := adapter.Payout.InitiateTransfer(ctx, providers.Transfer{Reference: out.Reference, RecipientRef: recipient, AmountMinor: in.AmountMinor, Currency: a.Currency})
	state = "PROCESSING"
	if callErr != nil || transfer.Reference != out.Reference {
		state = "UNKNOWN_OUTCOME"
	}
	_, e = s.ChangeProviderState(trusted(ctx), "PAYOUT", out.ID, "SUBMITTING", state, "")
	if e != nil {
		return out, e
	}
	out.State = state
	// Durable reservation already publishes initiation intent; provider observation is redacted.
	tx, e = s.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.Audit(ctx, tx, "payout.submission_observed", out.ID.String(), map[string]string{"state": state, "provider_reference": out.Reference}); e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.payout_initiated.v1", "payout", out.ID, out); e != nil {
		return out, e
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	return s.Payout(ctx, out.ID)
}
func (s *Service) ResolvePayout(ctx context.Context, id ids.ID) (Payout, error) {
	out, e := s.Payout(ctx, id)
	if e != nil {
		return out, e
	}
	if has([]string{"REVERSED", "CONFIRMED_FAILED"}, out.State) {
		return out, nil
	}
	if out.State == "CONFIRMED_PAID" {
		a, e := s.Adapter(out.ProviderID)
		if e != nil {
			return out, e
		}
		if a.Payout == nil {
			return out, core.Fail(503, "PAYOUT_PROVIDER_UNAVAILABLE")
		}
		r, e := a.Payout.VerifyTransfer(ctx, out.Reference)
		if e != nil {
			return out, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
		}
		if r.State == "REVERSED" || r.Reversed {
			return s.ReversePaidPayout(trusted(ctx), id)
		}
		return out, nil
	}
	a, e := s.Adapter(out.ProviderID)
	if e != nil {
		return out, e
	}
	if a.Payout == nil {
		return out, core.Fail(503, "PAYOUT_PROVIDER_UNAVAILABLE")
	}
	result, callErr := a.Payout.VerifyTransfer(ctx, out.Reference)
	var route finance.ProviderConfig
	var b []byte
	e = s.DB.QueryRow(ctx, `SELECT route_snapshot FROM payout_attempts WHERE payout_id=$1`, id).Scan(&b)
	if e != nil {
		return out, e
	}
	if e = decode(b, &route); e != nil {
		return out, e
	}
	valid := callErr == nil && result.Reference == out.Reference && result.AmountMinor == out.AmountMinor && result.Currency == out.Currency && result.RecipientRef == out.RecipientRef && result.Mode == route.Mode && formatID(result.MerchantID) == route.MerchantRef
	if valid && has([]string{"CONFIRMED_PAID", "CONFIRMED_FAILED"}, result.State) && (!result.FeeKnown || result.FeeMinor < 0 || result.FeeMinor > route.PayoutFeeMinor) {
		valid = false
	}
	next := "UNKNOWN_OUTCOME"
	if valid {
		switch result.State {
		case "CONFIRMED_PAID":
			next = result.State
		case "CONFIRMED_FAILED":
			if result.NoEffectConfirmed {
				next = result.State
			}
		case "PROCESSING", "OTP_REQUIRED", "REVIEW_REQUIRED":
			next = result.State
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	var current string
	e = tx.QueryRow(ctx, `SELECT state FROM payout_attempts WHERE payout_id=$1 FOR UPDATE`, id).Scan(&current)
	if e != nil {
		return out, e
	}
	if has([]string{"CONFIRMED_PAID", "CONFIRMED_FAILED"}, current) {
		return s.payout(ctx, tx, id)
	}
	var allocationID ids.ID
	e = tx.QueryRow(ctx, `SELECT allocation_id FROM allocation_lines WHERE line_id=$1`, out.LineID).Scan(&allocationID)
	if e != nil {
		return out, e
	}
	allocation, e := s.allocation(ctx, tx, allocationID)
	if e != nil {
		return out, e
	}
	var terminal *ids.ID
	var state string
	e = tx.QueryRow(ctx, `SELECT state FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, out.LineID).Scan(&state)
	if e != nil {
		return out, e
	}
	if next == "CONFIRMED_PAID" || next == "CONFIRMED_FAILED" {
		pending, e := s.Account(trusted(ctx), tx, &out.WalletID, out.PoolID, "WITHDRAWAL_PENDING", allocation.LegalEntity, out.Currency, "allocation:"+out.LineID.String())
		if e != nil {
			return out, e
		}
		var destination ids.ID
		if next == "CONFIRMED_PAID" {
			destination, e = s.Account(trusted(ctx), tx, nil, out.PoolID, "SETTLED_CASH", allocation.LegalEntity, out.Currency, "")
		} else {
			var active bool
			e = tx.QueryRow(ctx, `SELECT i.state='ACTIVE' AND v.state='VERIFIED' AND v.expires_at>now() AND NOT p.restricted FROM wallets w JOIN financial_identities i USING(identity_id) JOIN verification_refs v USING(identity_id) CROSS JOIN custody_pools p WHERE w.wallet_id=$1 AND p.pool_id=$2 FOR SHARE OF v`, out.WalletID, out.PoolID).Scan(&active)
			if e != nil {
				return out, e
			}
			kind := "AVAILABLE"
			dimension := "allocation:" + out.LineID.String()
			if !active || has([]string{"HELD", "DISPUTED", "REVIEW_REQUIRED"}, state) {
				kind = "FROZEN"
				dimension += ":held-available"
				if !has([]string{"HELD", "DISPUTED"}, state) {
					state = "REVIEW_REQUIRED"
				}
			}
			destination, e = s.Account(trusted(ctx), tx, &out.WalletID, out.PoolID, kind, allocation.LegalEntity, out.Currency, dimension)
		}
		if e != nil {
			return out, e
		}
		entries := []core.Entry{{AccountID: pending, Side: "DEBIT", AmountMinor: out.AmountMinor}, {AccountID: destination, Side: "CREDIT", AmountMinor: out.AmountMinor}}
		if result.FeeMinor > 0 {
			expense, e := s.Account(trusted(ctx), tx, nil, out.PoolID, "PROCESSING_EXPENSE", allocation.LegalEntity, out.Currency, "")
			if e != nil {
				return out, e
			}
			cash, e := s.Account(trusted(ctx), tx, nil, out.PoolID, "SETTLED_CASH", allocation.LegalEntity, out.Currency, "")
			if e != nil {
				return out, e
			}
			entries = append(entries, core.Entry{AccountID: expense, Side: "DEBIT", AmountMinor: result.FeeMinor}, core.Entry{AccountID: cash, Side: "CREDIT", AmountMinor: result.FeeMinor})
		}
		journal, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: "payout-terminal:" + id.String(), PoolID: out.PoolID, LegalEntity: allocation.LegalEntity, Currency: out.Currency, PolicyVersion: route.Version, Authorization: map[string]string{"evidence_ref": out.Reference, "approved_by": "authoritative-provider-readback"}, Entries: entries})
		if e != nil {
			return out, e
		}
		terminal = &journal
		if next == "CONFIRMED_PAID" {
			_, e = tx.Exec(ctx, `UPDATE allocation_lines SET paid_minor=paid_minor+$2,state=CASE WHEN paid_minor+$2=released_minor AND released_minor=amount_minor THEN 'PAID_OUT' ELSE state END WHERE line_id=$1`, out.LineID, out.AmountMinor)
		} else {
			_, e = tx.Exec(ctx, `UPDATE allocation_lines SET reserved_minor=reserved_minor-$2,state=CASE WHEN $3='WITHDRAWAL_RESERVED' THEN 'AVAILABLE' ELSE $3 END WHERE line_id=$1`, out.LineID, out.AmountMinor, state)
		}
		if e != nil {
			return out, e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE payout_attempts SET state=$2,terminal_journal_id=$3,updated_at=now() WHERE payout_id=$1`, id, next, terminal)
	if e != nil {
		return out, e
	}
	out.State = next
	event := "finance.payout_resolution_required.v1"
	if next == "CONFIRMED_PAID" {
		event = "finance.payout_confirmed.v1"
	}
	if next == "CONFIRMED_FAILED" {
		event = "finance.payout_failed.v1"
	}
	if e = s.Emit(ctx, tx, event, "payout", id, out); e != nil {
		return out, e
	}
	if e = s.Audit(ctx, tx, "payout.readback", id.String(), map[string]any{"state": next, "reference": out.Reference, "context_valid": valid}); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) RecoverPayouts(ctx context.Context) error {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		return e
	}
	rows, e := s.DB.Query(ctx, `SELECT payout_id FROM payout_attempts WHERE state NOT IN ('CONFIRMED_PAID','CONFIRMED_FAILED') AND NOT EXISTS(SELECT 1 FROM original_readback_jobs j WHERE j.kind='PAYOUT' AND j.aggregate_id=payout_id AND (j.state IN ('DEAD','COMPLETED') OR j.next_attempt_at>now() OR j.lease_until>now())) AND updated_at<now()-interval '15 seconds' ORDER BY updated_at LIMIT 20`)
	if e != nil {
		return e
	}
	xs := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		xs = append(xs, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range xs {
		token, claimed, e := s.ClaimReadback(ctx, "PAYOUT", id)
		if e != nil {
			return e
		}
		if !claimed {
			continue
		}
		out, readErr := s.ResolvePayout(ctx, id)
		if e = s.FinishReadback(ctx, "PAYOUT", id, token, readErr == nil && has([]string{"CONFIRMED_PAID", "CONFIRMED_FAILED", "CANCELLED", "REVERSED"}, out.State)); e != nil {
			return e
		}
	}
	return nil
}
