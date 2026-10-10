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

type RefundRequest struct {
	LineID       ids.ID   `json:"line_id"`
	CaseID       ids.ID   `json:"case_id"`
	AmountMinor  int64    `json:"amount_minor"`
	Strategy     string   `json:"destination_strategy"`
	AuthorityRef string   `json:"authority_ref"`
	EvidenceIDs  []ids.ID `json:"evidence_ids"`
	Approvers    []string `json:"approvers"`
}
type RefundAttempt struct {
	ID ids.ID `json:"refund_id"`
	RefundRequest
	IntentID          ids.ID    `json:"intent_id"`
	WalletID          ids.ID    `json:"payer_wallet_id"`
	ProviderID        ids.ID    `json:"provider_account_id"`
	PoolID            ids.ID    `json:"pool_id"`
	Currency          string    `json:"currency"`
	OriginalReference string    `json:"original_reference"`
	AttemptReference  string    `json:"attempt_reference"`
	ProviderRefundID  *string   `json:"provider_refund_id,omitempty"`
	State             string    `json:"state"`
	Deadline          time.Time `json:"refund_deadline"`
}

func refundProviderRequest(r RefundAttempt) providers.Refund {
	in := providers.Refund{Reference: r.AttemptReference, ChargeReference: r.OriginalReference, AmountMinor: r.AmountMinor, Currency: r.Currency}
	if r.ProviderRefundID != nil {
		in.ID = *r.ProviderRefundID
	}
	return in
}

func (s *Service) refundRow(ctx context.Context, q query, id ids.ID) (r RefundAttempt, e error) {
	e = load(ctx, q, `SELECT snapshot||jsonb_build_object('state',CASE WHEN EXISTS(SELECT 1 FROM financial_reversals v WHERE v.refund_id=refund_attempts.refund_id) THEN 'REVERSED' ELSE state END)||CASE WHEN provider_refund_id IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('provider_refund_id',provider_refund_id) END FROM refund_attempts WHERE refund_id=$1`, id, &r)
	return
}
func (s *Service) Refund(ctx context.Context, id ids.ID) (RefundAttempt, error) {
	r, e := s.refundRow(ctx, s.DB, id)
	if e != nil {
		return r, e
	}
	a, e := s.lineAllocation(ctx, r.LineID)
	if e != nil {
		return RefundAttempt{}, e
	}
	if e = s.caseAccess(ctx, a, r.LineID); e != nil {
		if e = core.Require(ctx, platform.PaymentService); e != nil {
			return RefundAttempt{}, e
		}
	}
	return r, nil
}
func (s *Service) RequestRefund(ctx context.Context, in RefundRequest, key string) (RefundAttempt, error) {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return RefundAttempt{}, e
	}
	if s.Environment == "production" {
		return RefundAttempt{}, core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	a, e := s.lineAllocation(ctx, in.LineID)
	if e != nil {
		return RefundAttempt{}, e
	}
	if key == "" || len(key) > 128 || in.AmountMinor <= 0 || in.AuthorityRef == "" || !dual(in.Approvers) {
		return RefundAttempt{}, fail("APPROVED_REFUND_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return RefundAttempt{}, e
	}
	defer tx.Rollback(ctx)
	op := "refund:" + a.IntentID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return RefundAttempt{}, e
	}
	var old ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT refund_id,fingerprint FROM refund_attempts WHERE operation_key=$1`, op).Scan(&old, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return RefundAttempt{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return s.refundRow(ctx, tx, old)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return RefundAttempt{}, e
	}
	// Original-payment serialization prevents refund/reversal races across shares.
	var original string
	var payer, provider ids.ID
	var paymentAmount int64
	var routeRaw []byte
	e = tx.QueryRow(ctx, `SELECT provider_reference,wallet_id,provider_account_id,amount_minor,route_snapshot FROM payment_intents WHERE intent_id=$1 AND state='VERIFIED_SUCCESS' FOR UPDATE`, a.IntentID).Scan(&original, &payer, &provider, &paymentAmount, &routeRaw)
	if e != nil {
		return RefundAttempt{}, core.Fail(409, "REFUND_REQUIRES_VERIFIED_COLLECTION")
	}
	rp, e := s.remedyPolicy(ctx, tx, a.PolicyID)
	if e != nil {
		return RefundAttempt{}, e
	}
	if !has(rp.RefundStrategies, in.Strategy) {
		return RefundAttempt{}, core.Fail(403, "REFUND_STRATEGY_DISABLED")
	}
	// Unsupported rails are explicit policy reservations; never fake a bank refund.
	if !has([]string{"ORIGINAL_INSTRUMENT", "WALLET_CREDIT"}, in.Strategy) {
		return RefundAttempt{}, core.Fail(403, "REFUND_RAIL_NOT_APPROVED")
	}
	c, e := s.caseRow(ctx, tx, in.CaseID)
	if e != nil {
		return RefundAttempt{}, e
	}
	if c.LineID != in.LineID || c.State != "CLOSED" {
		return RefundAttempt{}, core.Fail(403, "REFUND_DECISION_REQUIRED")
	}
	var decision CaseDecision
	if e = load(ctx, tx, `SELECT snapshot FROM financial_case_facts WHERE case_id=$1 AND kind='DECISION' AND journal_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`, c.ID, &decision); e != nil {
		return RefundAttempt{}, e
	}
	if !has([]string{"RETURN_TO_PAYER", "PARTIAL_AWARD"}, decision.Outcome) || in.AmountMinor > c.AmountMinor-decision.AwardMinor {
		return RefundAttempt{}, core.Fail(403, "REFUND_DECISION_REQUIRED")
	}
	if e = s.checkEvidence(ctx, tx, rp, c.BusinessRef, in.EvidenceIDs); e != nil {
		return RefundAttempt{}, e
	}
	var used int64
	e = tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor),0)::bigint FROM refund_attempts WHERE case_id=$1 AND state NOT IN ('CONFIRMED_FAILED','CANCELLED')`, c.ID).Scan(&used)
	if e != nil {
		return RefundAttempt{}, e
	}
	if in.AmountMinor > c.AmountMinor-decision.AwardMinor-used {
		return RefundAttempt{}, core.Fail(409, "REFUND_CAP_EXCEEDED")
	}
	var reversal, refunded int64
	e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT sum(amount_minor) FROM financial_reversals WHERE intent_id=$1),0)::bigint,COALESCE((SELECT sum(amount_minor) FROM refund_attempts WHERE intent_id=$1 AND state NOT IN ('CONFIRMED_FAILED','CANCELLED')),0)::bigint`, a.IntentID).Scan(&reversal, &refunded)
	if e != nil {
		return RefundAttempt{}, e
	}
	if in.AmountMinor > paymentAmount-reversal-refunded {
		return RefundAttempt{}, core.Fail(409, "REFUND_CAP_EXCEEDED")
	}
	var src ids.ID
	var avail *ids.ID
	var state string
	e = tx.QueryRow(ctx, `SELECT protected_account_id,available_account_id,state FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, in.LineID).Scan(&src, &avail, &state)
	if e != nil {
		return RefundAttempt{}, e
	}
	if has([]string{"CORRECTED", "REVERSED", "HELD", "DISPUTED", "REVIEW_REQUIRED"}, state) {
		return RefundAttempt{}, core.Fail(409, "REFUND_FUNDS_UNAVAILABLE")
	}
	src, e = s.Account(trusted(ctx), tx, &payer, a.PoolID, "REFUND_PENDING", a.LegalEntity, a.Currency, "case-refundable:"+c.ID.String())
	if e != nil {
		return RefundAttempt{}, e
	}
	pn, e := accountBalance(ctx, tx, src)
	if e != nil {
		return RefundAttempt{}, e
	}
	if pn < in.AmountMinor {
		return RefundAttempt{}, core.Fail(409, "REFUND_FUNDS_UNAVAILABLE")
	}
	if in.Strategy == "ORIGINAL_INSTRUMENT" {
		var internalFunding bool
		if e = tx.QueryRow(ctx, `SELECT route_snapshot ? 'funding_source' FROM payment_intents WHERE intent_id=$1`, a.IntentID).Scan(&internalFunding); e != nil {
			return RefundAttempt{}, e
		}
		if internalFunding {
			return RefundAttempt{}, core.Fail(403, "INTERNAL_FUNDING_REQUIRES_WALLET_REMEDY")
		}
		var cfg finance.ProviderConfig
		if e = decode(routeRaw, &cfg); e != nil {
			return RefundAttempt{}, e
		}
		adapter, e := s.Adapter(provider)
		if e != nil {
			return RefundAttempt{}, e
		}
		if !cfg.RefundApproved || cfg.RefundApprovalRef == "" || adapter.Refund == nil {
			return RefundAttempt{}, core.Fail(403, "REFUND_CAPABILITY_DISABLED")
		}
	}
	if in.Strategy == "WALLET_CREDIT" {
		var ambiguous bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM refund_attempts WHERE intent_id=$1 AND snapshot->>'destination_strategy'='ORIGINAL_INSTRUMENT' AND state IN ('SUBMITTED','PROCESSING','UNKNOWN_OUTCOME','REVIEW_REQUIRED'))`, a.IntentID).Scan(&ambiguous); e != nil {
			return RefundAttempt{}, e
		}
		if ambiguous {
			return RefundAttempt{}, core.Fail(409, "UNKNOWN_PROVIDER_REFUND_PREVENTS_WALLET_CREDIT")
		}
		var customer bool
		e = tx.QueryRow(ctx, `SELECT w.role='CUSTOMER' AND w.domain='marketplace' AND EXISTS(SELECT 1 FROM transaction_funding WHERE intent_id=$2) FROM wallets w WHERE wallet_id=$1`, payer, a.IntentID).Scan(&customer)
		if e != nil {
			return RefundAttempt{}, e
		}
		if !customer {
			return RefundAttempt{}, core.Fail(403, "CUSTOMER_FUNDED_WALLET_REQUIRED")
		}
	}
	out := RefundAttempt{ID: ids.New(), RefundRequest: in, IntentID: a.IntentID, WalletID: payer, ProviderID: provider, PoolID: a.PoolID, Currency: a.Currency, OriginalReference: original, State: "RESERVED", Deadline: time.Now().Add(time.Duration(rp.RefundSeconds) * time.Second)}
	out.AttemptReference = "cfr-" + out.ID.String()
	pending, e := s.Account(trusted(ctx), tx, &payer, a.PoolID, "REFUND_PENDING", a.LegalEntity, a.Currency, "refund:"+out.ID.String())
	if e != nil {
		return out, e
	}
	j, e := s.remedyPost(ctx, tx, a, op, in.AuthorityRef, []core.Entry{{AccountID: src, Side: "DEBIT", AmountMinor: in.AmountMinor}, {AccountID: pending, Side: "CREDIT", AmountMinor: in.AmountMinor}})
	if e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO refund_attempts(refund_id,intent_id,line_id,case_id,payer_wallet_id,provider_account_id,pool_id,amount_minor,currency,original_reference,attempt_reference,operation_key,fingerprint,snapshot,source_account_id,pending_account_id,reservation_journal_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'RESERVED')`, out.ID, a.IntentID, in.LineID, in.CaseID, payer, provider, a.PoolID, in.AmountMinor, a.Currency, original, out.AttemptReference, op, core.Fingerprint(in), raw(out), src, pending, j)
	if e != nil {
		return out, e
	}
	if e = s.Audit(ctx, tx, "refund.reserved", out.ID.String(), out); e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.refund_reserved.v1", "refund", out.ID, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) SubmitRefund(ctx context.Context, id ids.ID) (RefundAttempt, error) {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return RefundAttempt{}, e
	}
	if s.Environment == "production" {
		return RefundAttempt{}, core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	out, e := s.refundRow(ctx, s.DB, id)
	if e != nil {
		return out, e
	}
	if out.State == "RESERVED" && !out.Deadline.After(time.Now()) {
		return out, core.Fail(403, "REFUND_DEADLINE_REVIEW_REQUIRED")
	}
	if out.Strategy == "WALLET_CREDIT" {
		return s.completeRefund(ctx, out, providers.Refund{State: "CONFIRMED_REFUNDED"})
	}
	adapter, e := s.Adapter(out.ProviderID)
	if e != nil {
		return out, e
	}
	if adapter.Refund == nil {
		return out, core.Fail(503, "REFUND_PROVIDER_UNAVAILABLE")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE refund_attempts SET state='SUBMITTED',updated_at=now() WHERE refund_id=$1 AND state='RESERVED' AND (snapshot->>'refund_deadline')::timestamptz>now()`, id)
	if e != nil {
		return out, e
	}
	if tag.RowsAffected() != 1 {
		current, e := s.refundRow(ctx, tx, id)
		if e != nil {
			return current, e
		}
		if current.State == "RESERVED" && !current.Deadline.After(time.Now()) {
			return current, core.Fail(403, "REFUND_DEADLINE_REVIEW_REQUIRED")
		}
		return s.refundRow(ctx, tx, id)
	}
	if e = s.Audit(ctx, tx, "refund.submission_committed", id.String(), map[string]string{"attempt_reference": out.AttemptReference}); e != nil {
		return out, e
	}
	out.State = "SUBMITTED"
	if e = s.Emit(ctx, tx, "finance.refund_state_changed.v1", "refund", out.ID, out); e != nil {
		return out, e
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	r, err := adapter.Refund.InitiateRefund(ctx, providers.Refund{Reference: out.AttemptReference, ChargeReference: out.OriginalReference, AmountMinor: out.AmountMinor, Currency: out.Currency})
	// Acceptance is not proof of completion. Even a synchronous success must be
	// read back against the original durable reference before money leaves.
	state := "UNKNOWN_OUTCOME"
	var providerID *string
	if err == nil && r.Reference == out.AttemptReference && r.ChargeReference == out.OriginalReference && r.AmountMinor == out.AmountMinor && r.Currency == out.Currency && r.ID != "" {
		state = "PROCESSING"
		providerID = &r.ID
	}
	tx, e = s.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE refund_attempts SET state=$2,provider_refund_id=$3,updated_at=now() WHERE refund_id=$1 AND state='SUBMITTED'`, id, state, providerID)
	if e != nil {
		return out, e
	}
	out.State = state
	out.ProviderRefundID = providerID
	if e = s.Emit(ctx, tx, "finance.refund_state_changed.v1", "refund", out.ID, out); e != nil {
		return out, e
	}
	if e = s.Audit(ctx, tx, "refund.submission_observed", id.String(), map[string]string{"state": state}); e != nil {
		return out, e
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	return s.refundRow(ctx, s.DB, id)
}
func (s *Service) ResolveRefund(ctx context.Context, id ids.ID) (RefundAttempt, error) {
	if e := core.Require(ctx, platform.PaymentService); e != nil {
		if e = core.Require(ctx, platform.AdminService); e != nil {
			return RefundAttempt{}, e
		}
	}
	out, e := s.refundRow(ctx, s.DB, id)
	if e != nil {
		return out, e
	}
	if has([]string{"REVERSED", "CONFIRMED_FAILED", "CANCELLED"}, out.State) {
		return out, nil
	}
	if out.State == "CONFIRMED_REFUNDED" {
		if out.Strategy != "ORIGINAL_INSTRUMENT" {
			return out, nil
		}
		adapter, e := s.Adapter(out.ProviderID)
		if e != nil {
			return out, e
		}
		if adapter.Refund == nil {
			return out, core.Fail(503, "REFUND_PROVIDER_UNAVAILABLE")
		}
		r := providers.Refund{Reference: out.AttemptReference, ChargeReference: out.OriginalReference, AmountMinor: out.AmountMinor, Currency: out.Currency}
		if out.ProviderRefundID != nil {
			r.ID = *out.ProviderRefundID
		}
		obs, e := adapter.Refund.VerifyRefund(ctx, r)
		if e != nil {
			return out, core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
		}
		if obs.State == "REVERSED" {
			return s.ReverseRefund(ctx, id)
		}
		return out, nil
	}
	if out.State == "RESERVED" {
		return out, nil
	}
	if out.Strategy == "WALLET_CREDIT" {
		return out, core.Fail(409, "WALLET_REFUND_NOT_SUBMITTED")
	}
	adapter, e := s.Adapter(out.ProviderID)
	if e != nil {
		return out, e
	}
	if adapter.Refund == nil {
		return out, core.Fail(503, "REFUND_PROVIDER_UNAVAILABLE")
	}
	in := providers.Refund{Reference: out.AttemptReference, ChargeReference: out.OriginalReference, AmountMinor: out.AmountMinor, Currency: out.Currency}
	if out.ProviderRefundID != nil {
		in.ID = *out.ProviderRefundID
	}
	r, err := adapter.Refund.VerifyRefund(ctx, in)
	var route finance.ProviderConfig
	var b []byte
	if e = s.DB.QueryRow(ctx, `SELECT route_snapshot FROM payment_intents WHERE intent_id=$1`, out.IntentID).Scan(&b); e != nil {
		return out, e
	}
	if e = decode(b, &route); e != nil {
		return out, e
	}
	valid := err == nil && r.Reference == out.AttemptReference && r.ChargeReference == out.OriginalReference && r.AmountMinor == out.AmountMinor && r.Currency == out.Currency && r.Mode == route.Mode && formatID(r.MerchantID) == route.MerchantRef && r.ID != "" && (out.ProviderRefundID == nil || r.ID == *out.ProviderRefundID)
	if !valid {
		r.State = "UNKNOWN_OUTCOME"
		r.ID = ""
	} else if r.State == "CONFIRMED_FAILED" && !r.NoEffectConfirmed {
		r.State = "UNKNOWN_OUTCOME"
	}
	return s.completeRefund(ctx, out, r)
}
func (s *Service) completeRefund(ctx context.Context, out RefundAttempt, r providers.Refund) (RefundAttempt, error) {
	a, e := s.lineAllocation(ctx, out.LineID)
	if e != nil {
		return out, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	// Lock order is original intent, then line, then attempt for all remedies.
	var intent ids.ID
	if e = tx.QueryRow(ctx, `SELECT intent_id FROM payment_intents WHERE intent_id=$1 FOR UPDATE`, out.IntentID).Scan(&intent); e != nil {
		return out, e
	}
	var line ids.ID
	if e = tx.QueryRow(ctx, `SELECT line_id FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, out.LineID).Scan(&line); e != nil {
		return out, e
	}
	var state string
	var src, pending ids.ID
	if e = tx.QueryRow(ctx, `SELECT state,source_account_id,pending_account_id FROM refund_attempts WHERE refund_id=$1 FOR UPDATE`, out.ID).Scan(&state, &src, &pending); e != nil {
		return out, e
	}
	if has([]string{"CONFIRMED_REFUNDED", "CONFIRMED_FAILED", "CANCELLED"}, state) {
		return s.refundRow(ctx, tx, out.ID)
	}
	if r.State == "CANCELLED" && state != "RESERVED" {
		return out, core.Fail(409, "SUBMITTED_REFUND_CANNOT_BE_CANCELLED")
	}
	if state == "RESERVED" && r.State == "CONFIRMED_REFUNDED" && !out.Deadline.After(time.Now()) {
		return out, core.Fail(403, "REFUND_DEADLINE_REVIEW_REQUIRED")
	}
	next := r.State
	if !has([]string{"CONFIRMED_REFUNDED", "CONFIRMED_FAILED", "CANCELLED", "PROCESSING", "REVIEW_REQUIRED", "UNKNOWN_OUTCOME"}, next) {
		next = "UNKNOWN_OUTCOME"
	}
	var journal *ids.ID
	if has([]string{"CONFIRMED_REFUNDED", "CONFIRMED_FAILED", "CANCELLED"}, next) {
		dest := src
		if next == "CONFIRMED_REFUNDED" {
			kind := "SETTLED_CASH"
			var settled bool
			if e = tx.QueryRow(ctx, `SELECT settlement_state='SETTLED' FROM payment_intents WHERE intent_id=$1`, out.IntentID).Scan(&settled); e != nil {
				return out, e
			}
			if !settled {
				kind = "PROVIDER_RECEIVABLE"
			}
			var wallet *ids.ID
			if out.Strategy == "WALLET_CREDIT" {
				kind = "AVAILABLE"
				wallet = &out.WalletID
			}
			dest, e = s.Account(trusted(ctx), tx, wallet, out.PoolID, kind, a.LegalEntity, out.Currency, "")
			if e != nil {
				return out, e
			}
		}
		// Failed/cancelled reservations return to protection. Released funds do not
		// become withdrawable again without a fresh authority/risk review.
		if next != "CONFIRMED_REFUNDED" {
			var kind string
			if e = tx.QueryRow(ctx, `SELECT kind FROM ledger_accounts WHERE account_id=$1`, src).Scan(&kind); e != nil {
				return out, e
			}
			if kind == "AVAILABLE" {
				dest, e = s.Account(trusted(ctx), tx, &out.WalletID, out.PoolID, "FROZEN", a.LegalEntity, out.Currency, "refund-review:"+out.ID.String())
				if e != nil {
					return out, e
				}
			}
		}
		j, e := s.remedyPost(ctx, tx, a, "refund-terminal:"+out.ID.String(), out.AttemptReference, []core.Entry{{AccountID: pending, Side: "DEBIT", AmountMinor: out.AmountMinor}, {AccountID: dest, Side: "CREDIT", AmountMinor: out.AmountMinor}})
		if e != nil {
			return out, e
		}
		journal = &j
	}
	_, e = tx.Exec(ctx, `UPDATE refund_attempts SET state=$2,provider_refund_id=COALESCE(provider_refund_id,NULLIF($3,'')),terminal_journal_id=$4,updated_at=now() WHERE refund_id=$1`, out.ID, next, r.ID, journal)
	if e != nil {
		return out, e
	}
	out.State = next
	if e = s.Audit(ctx, tx, "refund.readback", out.ID.String(), map[string]string{"state": next, "attempt_reference": out.AttemptReference}); e != nil {
		return out, e
	}
	if e = s.Emit(ctx, tx, "finance.refund_state_changed.v1", "refund", out.ID, out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) CancelRefund(ctx context.Context, id ids.ID, ref string) (RefundAttempt, error) {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return RefundAttempt{}, e
	}
	r, e := s.refundRow(ctx, s.DB, id)
	if e != nil {
		return r, e
	}
	if ref == "" || r.State != "RESERVED" {
		return r, core.Fail(409, "SUBMITTED_REFUND_CANNOT_BE_CANCELLED")
	}
	return s.completeRefund(ctx, r, providers.Refund{State: "CANCELLED"})
}
