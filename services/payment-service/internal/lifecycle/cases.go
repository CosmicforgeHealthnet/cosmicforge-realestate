package lifecycle

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"time"
)

type CaseRequest struct {
	ID            ids.ID   `json:"case_id"`
	LineID        ids.ID   `json:"line_id"`
	DepositID     *ids.ID  `json:"deposit_id,omitempty"`
	RefundID      *ids.ID  `json:"refund_id,omitempty"`
	Kind          string   `json:"kind"`
	AmountMinor   int64    `json:"contested_minor"`
	ClaimantRef   string   `json:"claimant_ref"`
	RespondentRef string   `json:"respondent_ref"`
	AuthorityRef  string   `json:"authority_ref"`
	ReasonRef     string   `json:"reason_ref"`
	EvidenceIDs   []ids.ID `json:"evidence_ids"`
}
type FinancialCase struct {
	CaseRequest
	IntentID           ids.ID    `json:"intent_id"`
	QuoteID            ids.ID    `json:"quote_id"`
	PolicyID           ids.ID    `json:"policy_id"`
	BusinessRef        string    `json:"business_ref"`
	Currency           string    `json:"currency"`
	Jurisdiction       string    `json:"jurisdiction"`
	SourceService      string    `json:"source_service"`
	State              string    `json:"state"`
	ClaimDeadline      time.Time `json:"claim_deadline"`
	ChallengeDeadline  time.Time `json:"challenge_deadline"`
	ReviewDeadline     time.Time `json:"review_deadline"`
	EscalationDeadline time.Time `json:"escalation_deadline"`
	NoticeDeadline     time.Time `json:"notice_deadline"`
}
type CaseFact struct {
	ID           ids.ID   `json:"fact_id"`
	CaseID       ids.ID   `json:"case_id"`
	Kind         string   `json:"kind"`
	ActorRef     string   `json:"actor_ref"`
	AuthorityRef string   `json:"authority_ref"`
	ReasonRef    string   `json:"reason_ref"`
	EvidenceIDs  []ids.ID `json:"evidence_ids"`
}
type CaseDecision struct {
	CaseFact
	Outcome          string     `json:"outcome"`
	AwardMinor       int64      `json:"award_minor"`
	AwardAuthorityID *ids.ID    `json:"award_authority_id,omitempty"`
	Approvers        []string   `json:"approvers"`
	ReviewerRef      string     `json:"reviewer_ref"`
	ExecutorRef      string     `json:"executor_ref"`
	ExtensionUntil   *time.Time `json:"extension_until,omitempty"`
}

func (s *Service) caseRow(ctx context.Context, q query, id ids.ID) (c FinancialCase, e error) {
	e = load(ctx, q, `SELECT snapshot||jsonb_build_object('state',state) FROM financial_cases WHERE case_id=$1`, id, &c)
	return
}
func (s *Service) Case(ctx context.Context, id ids.ID) (FinancialCase, error) {
	c, e := s.caseRow(ctx, s.DB, id)
	if e != nil {
		return c, e
	}
	a, e := s.lineAllocation(ctx, c.LineID)
	if e != nil {
		return FinancialCase{}, e
	}
	if e = s.caseAccess(ctx, a, c.LineID); e != nil {
		return FinancialCase{}, e
	}
	return c, nil
}
func (s *Service) caseAccess(ctx context.Context, a Allocation, line ids.ID) error {
	if e := s.sourceFor(ctx, a); e == nil {
		return nil
	}
	var payer ids.ID
	if e := s.DB.QueryRow(ctx, `SELECT payer_wallet_id FROM locked_quotes WHERE quote_id=$1`, a.QuoteID).Scan(&payer); e != nil {
		return e
	}
	if _, e := s.OwnWallet(ctx, payer); e == nil {
		return nil
	}
	for _, l := range a.Lines {
		if l.ID == line && l.WalletID != nil {
			if _, e := s.OwnWallet(ctx, *l.WalletID); e == nil {
				return nil
			}
		}
	}
	return core.Fail(403, "CASE_PARTY_AUTHORITY_REQUIRED")
}
func (s *Service) OpenCase(ctx context.Context, in CaseRequest, key string) (FinancialCase, error) {
	a, e := s.lineAllocation(ctx, in.LineID)
	if e != nil {
		return FinancialCase{}, e
	}
	if in.Kind == "DEPOSIT_CLAIM" {
		e = s.sourceFor(ctx, a)
	} else {
		e = s.caseAccess(ctx, a, in.LineID)
	}
	if e != nil {
		return FinancialCase{}, e
	}
	if p, err := core.Caller(ctx); err == nil && p.Service == platform.APIGateway {
		var payer ids.ID
		if e = s.DB.QueryRow(ctx, `SELECT payer_wallet_id FROM locked_quotes WHERE quote_id=$1`, a.QuoteID).Scan(&payer); e != nil {
			return FinancialCase{}, e
		}
		var party string
		var authorized bool
		for _, w := range append([]ids.ID{payer}, func() []ids.ID {
			for _, l := range a.Lines {
				if l.ID == in.LineID && l.WalletID != nil {
					return []ids.ID{*l.WalletID}
				}
			}
			return nil
		}()...) {
			if _, err = s.OwnWallet(ctx, w); err == nil {
				e = s.DB.QueryRow(ctx, `SELECT i.legal_party_ref FROM wallets w JOIN financial_identities i USING(identity_id) WHERE w.wallet_id=$1`, w).Scan(&party)
				if e != nil {
					return FinancialCase{}, e
				}
				authorized = true
				break
			}
		}
		if !authorized || in.ClaimantRef != party {
			return FinancialCase{}, core.Fail(403, "CASE_CLAIMANT_DELEGATION_MISMATCH")
		}
	}
	if key == "" || len(key) > 128 || in.ID.ValidateV7() != nil || in.AmountMinor <= 0 || in.ClaimantRef == "" || in.RespondentRef == "" || in.AuthorityRef == "" || in.ReasonRef == "" || !has([]string{"DEPOSIT_CLAIM", "DISPUTE", "CANCELLATION", "REFUND_REVIEW"}, in.Kind) {
		return FinancialCase{}, fail("CASE_AUTHORITY_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return FinancialCase{}, e
	}
	defer tx.Rollback(ctx)
	op := "case:" + in.LineID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return FinancialCase{}, e
	}
	var old ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT case_id,fingerprint FROM financial_cases WHERE operation_key=$1`, op).Scan(&old, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return FinancialCase{}, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return s.caseRow(ctx, tx, old)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return FinancialCase{}, e
	}
	var protected ids.ID
	var available, wallet *ids.ID
	var state string
	e = tx.QueryRow(ctx, `SELECT protected_account_id,available_account_id,wallet_id,state FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, in.LineID).Scan(&protected, &available, &wallet, &state)
	if e != nil {
		return FinancialCase{}, e
	}
	if has([]string{"CORRECTED", "REVERSED", "HELD", "DISPUTED", "REVIEW_REQUIRED"}, state) {
		return FinancialCase{}, core.Fail(409, "CASE_REQUIRES_REVIEW")
	}
	rp, e := s.remedyPolicy(ctx, tx, a.PolicyID)
	if e != nil {
		return FinancialCase{}, e
	}
	var q Quote
	if e = load(ctx, tx, `SELECT snapshot FROM locked_quotes WHERE quote_id=$1`, a.QuoteID, &q); e != nil {
		return FinancialCase{}, e
	}
	if e = s.checkEvidence(ctx, tx, rp, q.BusinessRef, in.EvidenceIDs); e != nil {
		return FinancialCase{}, e
	}
	if in.DepositID != nil {
		var matches bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM refundable_deposits WHERE deposit_id=$1 AND line_id=$2)`, *in.DepositID, in.LineID).Scan(&matches)
		if e != nil {
			return FinancialCase{}, e
		}
		if !matches {
			return FinancialCase{}, fail("DEPOSIT_CONTEXT_MISMATCH")
		}
		var deposit Deposit
		if e = load(ctx, tx, `SELECT snapshot FROM refundable_deposits WHERE deposit_id=$1`, *in.DepositID, &deposit); e != nil {
			return FinancialCase{}, e
		}
		if !deposit.ClaimDeadline.After(time.Now()) {
			return FinancialCase{}, core.Fail(403, "DEPOSIT_CLAIM_DEADLINE_EXPIRED")
		}
	}
	if in.Kind == "DEPOSIT_CLAIM" && in.DepositID == nil {
		return FinancialCase{}, fail("DEPOSIT_CONTEXT_MISMATCH")
	}
	if in.RefundID != nil {
		var matches bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM refund_attempts WHERE refund_id=$1 AND line_id=$2 AND state IN ('RESERVED','SUBMITTED','PROCESSING','UNKNOWN_OUTCOME'))`, *in.RefundID, in.LineID).Scan(&matches)
		if e != nil {
			return FinancialCase{}, e
		}
		if !matches {
			return FinancialCase{}, fail("REFUND_CONTEXT_MISMATCH")
		}
		return FinancialCase{}, core.Fail(409, "IN_FLIGHT_REFUND_REQUIRES_PROVIDER_REVIEW")
	}
	// Locking the line serializes competing claims, releases and reservations.
	pn, e := accountBalance(ctx, tx, protected)
	if e != nil {
		return FinancialCase{}, e
	}
	var an int64
	if available != nil {
		an, e = accountBalance(ctx, tx, *available)
		if e != nil {
			return FinancialCase{}, e
		}
	}
	if in.AmountMinor > pn+an {
		return FinancialCase{}, core.Fail(409, "CONTESTED_FUNDS_UNAVAILABLE")
	}
	if !rp.AllowUncontested && in.AmountMinor != pn+an {
		return FinancialCase{}, core.Fail(403, "PARTIAL_FREEZE_POLICY_DISABLED")
	}
	takeP := min(pn, in.AmountMinor)
	takeA := in.AmountMinor - takeP
	// Deposit and internal reserves are frozen as a payer liability, never earnings.
	if wallet == nil {
		wallet = &q.PayerWalletID
	}
	frozen, e := s.Account(trusted(ctx), tx, wallet, a.PoolID, "FROZEN", a.LegalEntity, a.Currency, "case:"+in.ID.String())
	if e != nil {
		return FinancialCase{}, e
	}
	entries := []core.Entry{{AccountID: frozen, Side: "CREDIT", AmountMinor: in.AmountMinor}}
	if takeP > 0 {
		entries = append(entries, core.Entry{AccountID: protected, Side: "DEBIT", AmountMinor: takeP})
	}
	if takeA > 0 {
		entries = append(entries, core.Entry{AccountID: *available, Side: "DEBIT", AmountMinor: takeA})
	}
	journal, e := s.remedyPost(ctx, tx, a, op, in.AuthorityRef, entries)
	if e != nil {
		return FinancialCase{}, e
	}
	now := time.Now()
	deadline := func(n int64) time.Time { return now.Add(time.Duration(n) * time.Second) }
	var source string
	if e = tx.QueryRow(ctx, `SELECT source_service FROM payment_intents WHERE intent_id=$1`, a.IntentID).Scan(&source); e != nil {
		return FinancialCase{}, e
	}
	c := FinancialCase{CaseRequest: in, IntentID: a.IntentID, QuoteID: a.QuoteID, PolicyID: a.PolicyID, BusinessRef: q.BusinessRef, Currency: a.Currency, Jurisdiction: q.Jurisdiction, SourceService: source, State: "OPEN", ClaimDeadline: deadline(rp.ClaimSeconds), ChallengeDeadline: deadline(rp.ChallengeSeconds), ReviewDeadline: deadline(rp.ReviewSeconds), EscalationDeadline: deadline(rp.EscalationSeconds), NoticeDeadline: deadline(rp.NoticeSeconds)}
	_, e = tx.Exec(ctx, `INSERT INTO financial_cases(case_id,line_id,operation_key,fingerprint,snapshot,contested_minor,protected_minor,available_minor,frozen_account_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'OPEN')`, in.ID, in.LineID, op, core.Fingerprint(in), raw(c), in.AmountMinor, takeP, takeA, frozen)
	if e != nil {
		return c, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO financial_case_facts(fact_id,case_id,operation_key,fingerprint,snapshot,kind,journal_id) VALUES($1,$2,$3,$4,$5,'CLAIM',$6)`, ids.New(), in.ID, op, core.Fingerprint(in), raw(c), journal)
	if e != nil {
		return c, e
	}
	if e = s.Audit(ctx, tx, "case.opened", in.ID.String(), c); e != nil {
		return c, e
	}
	if e = s.Emit(ctx, tx, "finance.case_opened.v1", "financial_case", in.ID, c); e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}
func (s *Service) AppendCaseFact(ctx context.Context, in CaseFact, key string) error {
	c, e := s.Case(ctx, in.CaseID)
	if e != nil {
		return e
	}
	if key == "" || in.ID.ValidateV7() != nil || in.ActorRef == "" || in.AuthorityRef == "" || in.ReasonRef == "" || !has([]string{"CHALLENGE", "REVIEW", "NOTICE"}, in.Kind) {
		return fail("CASE_FACT_REQUIRED")
	}
	if in.Kind == "REVIEW" || in.Kind == "NOTICE" {
		if e = core.Require(ctx, platform.AdminService); e != nil {
			return e
		}
	} else {
		var payer ids.ID
		if e = s.DB.QueryRow(ctx, `SELECT payer_wallet_id FROM locked_quotes WHERE quote_id=$1`, c.QuoteID).Scan(&payer); e != nil {
			return e
		}
		if _, e = s.OwnWallet(ctx, payer); e != nil {
			return e
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	op := "case-fact:" + in.CaseID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM financial_case_facts WHERE operation_key=$1`, op).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	var state string
	if e = tx.QueryRow(ctx, `SELECT state FROM financial_cases WHERE case_id=$1 FOR UPDATE`, c.ID).Scan(&state); e != nil {
		return e
	}
	if state == "CLOSED" {
		return core.Fail(409, "CASE_CLOSED")
	}
	if in.Kind == "CHALLENGE" && !c.ChallengeDeadline.After(time.Now()) {
		return core.Fail(403, "CHALLENGE_DEADLINE_EXPIRED")
	}
	rp, e := s.remedyPolicy(ctx, tx, c.PolicyID)
	if e != nil {
		return e
	}
	if e = s.checkEvidence(ctx, tx, rp, c.BusinessRef, in.EvidenceIDs); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO financial_case_facts(fact_id,case_id,operation_key,fingerprint,snapshot,kind) VALUES($1,$2,$3,$4,$5,$6)`, in.ID, c.ID, op, core.Fingerprint(in), raw(in), in.Kind)
	if e != nil {
		return e
	}
	if in.Kind == "CHALLENGE" {
		state = "EVIDENCE_PENDING"
	}
	if in.Kind == "REVIEW" {
		state = "UNDER_REVIEW"
	}
	if _, e = tx.Exec(ctx, `UPDATE financial_cases SET state=$2,updated_at=now() WHERE case_id=$1`, c.ID, state); e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "case.fact_recorded", c.ID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) DecideCase(ctx context.Context, in CaseDecision, key string) error {
	in.Kind = "DECISION"
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	c, e := s.caseRow(ctx, s.DB, in.CaseID)
	if e != nil {
		return e
	}
	a, e := s.lineAllocation(ctx, c.LineID)
	if e != nil {
		return e
	}
	if key == "" || in.ID.ValidateV7() != nil || in.ActorRef == "" || in.ReviewerRef == "" || in.ExecutorRef == "" || in.AuthorityRef == "" || in.ReasonRef == "" || in.AwardMinor < 0 || in.AwardMinor > c.AmountMinor || !has([]string{"RELEASE_TO_BENEFICIARY", "RETURN_TO_PAYER", "PARTIAL_AWARD", "RESERVE_EXTENDED", "REWORK_REQUIRED", "LEGAL_ESCALATION"}, in.Outcome) {
		return fail("CASE_DECISION_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	op := "case-decision:" + c.ID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM financial_case_facts WHERE operation_key=$1`, op).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	var state string
	var frozen ids.ID
	var pn, an int64
	if e = tx.QueryRow(ctx, `SELECT state,frozen_account_id,protected_minor,available_minor FROM financial_cases WHERE case_id=$1 FOR UPDATE`, c.ID).Scan(&state, &frozen, &pn, &an); e != nil {
		return e
	}
	if !has([]string{"UNDER_REVIEW", "MANUAL_APPROVAL_REQUIRED", "RESERVE_EXTENDED", "REWORK_REQUIRED", "LEGAL_ESCALATION"}, state) {
		return core.Fail(409, "INDEPENDENT_CASE_REVIEW_REQUIRED")
	}
	var reviewer bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_case_facts WHERE case_id=$1 AND kind='REVIEW' AND snapshot->>'actor_ref'=$2)`, c.ID, in.ReviewerRef).Scan(&reviewer); e != nil {
		return e
	}
	if !reviewer {
		return core.Fail(403, "INDEPENDENT_CASE_REVIEW_REQUIRED")
	}
	rp, e := s.remedyPolicy(ctx, tx, c.PolicyID)
	if e != nil {
		return e
	}
	if !c.ReviewDeadline.After(time.Now()) && !has([]string{"LEGAL_ESCALATION", "REWORK_REQUIRED", "RESERVE_EXTENDED"}, in.Outcome) {
		var overridden bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_case_facts WHERE case_id=$1 AND kind='OVERRIDE' AND (snapshot->>'expires_at')::timestamptz>now())`, c.ID).Scan(&overridden); e != nil {
			return e
		}
		if !overridden {
			return core.Fail(403, "REVIEW_DEADLINE_OVERRIDE_REQUIRED")
		}
	}
	if !dual(in.Approvers) || in.ReviewerRef == c.ClaimantRef || in.ExecutorRef == c.ClaimantRef {
		return core.Fail(403, "SEPARATION_OF_DUTIES_REQUIRED")
	}
	if c.AmountMinor >= rp.DualAboveMinor {
		seen := map[string]bool{c.ClaimantRef: true, in.ReviewerRef: true, in.ExecutorRef: true}
		if in.ReviewerRef == in.ExecutorRef {
			return core.Fail(403, "SEPARATION_OF_DUTIES_REQUIRED")
		}
		for _, v := range in.Approvers {
			if seen[v] {
				return core.Fail(403, "SEPARATION_OF_DUTIES_REQUIRED")
			}
			seen[v] = true
		}
	}
	if e = s.checkEvidence(ctx, tx, rp, c.BusinessRef, in.EvidenceIDs); e != nil {
		return e
	}
	var protected ids.ID
	var available *ids.ID
	if e = tx.QueryRow(ctx, `SELECT protected_account_id,available_account_id FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, c.LineID).Scan(&protected, &available); e != nil {
		return e
	}
	var journal *ids.ID
	var award *derivedAward
	next := in.Outcome
	if has([]string{"RESERVE_EXTENDED", "REWORK_REQUIRED", "LEGAL_ESCALATION"}, in.Outcome) {
		if in.AwardMinor != 0 {
			return fail("NONFINANCIAL_DECISION_AMOUNT_FORBIDDEN")
		}
		if in.Outcome == "RESERVE_EXTENDED" && (in.ExtensionUntil == nil || !in.ExtensionUntil.After(time.Now()) || in.ExtensionUntil.After(time.Now().Add(time.Duration(rp.ReserveExtensionSeconds)*time.Second))) {
			return fail("RESERVE_EXTENSION_POLICY_REQUIRED")
		}
	} else {
		if in.Outcome == "RETURN_TO_PAYER" && in.AwardMinor != 0 {
			return fail("AWARD_OUTCOME_MISMATCH")
		}
		entries := []core.Entry{{AccountID: frozen, Side: "DEBIT", AmountMinor: c.AmountMinor}}
		if in.AwardMinor > 0 {
			if in.AwardAuthorityID == nil {
				return fail("AWARD_AUTHORITY_REQUIRED")
			}
			var auth Authority
			if e = load(ctx, tx, `SELECT snapshot FROM beneficiary_authorities WHERE authority_id=$1 AND NOT revoked AND expires_at>now() FOR SHARE`, *in.AwardAuthorityID, &auth); e != nil {
				return core.Fail(403, "AWARD_AUTHORITY_REQUIRED")
			}
			if auth.WalletID == nil || auth.BusinessRef != c.BusinessRef || auth.Currency != a.Currency || auth.LegalEntity != a.LegalEntity || in.AwardMinor > auth.MaxMinor || auth.Role == "CUSTOMER" || !has(auth.Components, "DEPOSIT_CLAIM") {
				return core.Fail(403, "AWARD_AUTHORITY_REQUIRED")
			}
			if e = s.currentWallet(ctx, tx, *auth.WalletID, auth.LegalPartyRef); e != nil {
				return e
			}
			var funded bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transaction_funding WHERE intent_id=$1)`, a.IntentID).Scan(&funded); e != nil {
				return e
			}
			if !funded {
				return core.Fail(409, "UNFUNDED_RELEASE")
			}
			v, e := s.prepareAward(ctx, tx, c, a, auth, in.AwardMinor)
			if e != nil {
				return e
			}
			award = &v
			entries = append(entries, core.Entry{AccountID: v.ProtectedID, Side: "CREDIT", AmountMinor: in.AwardMinor})
		}
		left := c.AmountMinor - in.AwardMinor
		if left > 0 && has([]string{"RETURN_TO_PAYER", "PARTIAL_AWARD"}, in.Outcome) {
			var payer ids.ID
			if e = tx.QueryRow(ctx, `SELECT payer_wallet_id FROM locked_quotes WHERE quote_id=$1`, c.QuoteID).Scan(&payer); e != nil {
				return e
			}
			refundable, e := s.Account(trusted(ctx), tx, &payer, a.PoolID, "REFUND_PENDING", a.LegalEntity, a.Currency, "case-refundable:"+c.ID.String())
			if e != nil {
				return e
			}
			entries = append(entries, core.Entry{AccountID: refundable, Side: "CREDIT", AmountMinor: left})
		} else {
			restoreA := min(an, left)
			restoreP := left - restoreA
			if restoreP > 0 {
				entries = append(entries, core.Entry{AccountID: protected, Side: "CREDIT", AmountMinor: restoreP})
			}
			if restoreA > 0 {
				entries = append(entries, core.Entry{AccountID: *available, Side: "CREDIT", AmountMinor: restoreA})
			}
		}
		j, e := s.remedyPost(ctx, tx, a, op, in.AuthorityRef, entries)
		if e != nil {
			return e
		}
		journal = &j
		if award != nil {
			if e = s.sealAward(ctx, tx, *award, c, j); e != nil {
				return e
			}
		}
		next = "CLOSED"
	}
	in.Kind = "DECISION"
	_, e = tx.Exec(ctx, `INSERT INTO financial_case_facts(fact_id,case_id,operation_key,fingerprint,snapshot,kind,journal_id) VALUES($1,$2,$3,$4,$5,'DECISION',$6)`, in.ID, c.ID, op, core.Fingerprint(in), raw(in), journal)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE financial_cases SET state=$2,updated_at=now() WHERE case_id=$1`, c.ID, next); e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "case.decided", c.ID.String(), in); e != nil {
		return e
	}
	if e = s.Emit(ctx, tx, "finance.case_decided.v1", "financial_case", c.ID, in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
