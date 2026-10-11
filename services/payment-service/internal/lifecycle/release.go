package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/providers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"math/big"
	"strconv"
	"strings"
	"time"
)

func formatID(n int64) string { return strconv.FormatInt(n, 10) }

type FundingInput struct {
	IntentID           ids.ID   `json:"intent_id"`
	SettlementRef      string   `json:"settlement_ref"`
	CashEvidenceRef    string   `json:"cash_evidence_ref"`
	CustodyEvidenceRef string   `json:"custody_evidence_ref"`
	Approvers          []string `json:"approvers"`
}

func dual(xs []string) bool { return len(xs) == 2 && xs[0] != "" && xs[1] != "" && xs[0] != xs[1] }

// ConfirmFunding reads the settlement's own transaction membership, not a general
// transaction list or a frontend success flag. Cash attestation is distinct from custody.
func (s *Service) ConfirmFunding(ctx context.Context, in FundingInput) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if s.CheckEnvironmentCapability("CUSTODY_RELEASE") != nil {
		return core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	if in.SettlementRef == "" || in.CashEvidenceRef == "" || !dual(in.Approvers) {
		return fail("DUAL_SETTLEMENT_ATTESTATION_REQUIRED")
	}
	i, e := s.GetIntent(trusted(ctx), in.IntentID)
	if e != nil {
		return e
	}
	if i.State != "VERIFIED_SUCCESS" {
		return core.Fail(409, "PAYMENT_NOT_VERIFIED")
	}
	a, e := s.Adapter(i.ProviderAccountID)
	if e != nil {
		return e
	}
	membership, ok := a.Settlement.(providers.SettlementTransactions)
	if !ok || a.Settlement == nil {
		return core.Fail(503, "MATCHED_SETTLEMENT_UNAVAILABLE")
	}
	settled, e := a.Settlement.FetchSettlement(ctx, in.SettlementRef)
	if e != nil {
		return core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	if settled.Reference != in.SettlementRef || settled.Currency != i.Currency || !has([]string{"settled", "success", "processed"}, strings.ToLower(settled.State)) || settled.SettledAt == nil || settled.SettledAt.After(time.Now()) {
		return core.Fail(409, "SETTLEMENT_CONTEXT_MISMATCH")
	}
	charges, e := membership.SettlementTransactions(ctx, in.SettlementRef)
	if e != nil {
		return core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	var match providers.Charge
	var gross, fees int64
	seen := map[string]bool{}
	for _, c := range charges {
		if c.AmountMinor <= 0 || c.FeeMinor < 0 || c.FeeMinor >= c.AmountMinor || c.Currency != i.Currency || c.State != "VERIFIED_SUCCESS" || seen[c.Reference] || c.AmountMinor > settled.AmountMinor-gross || c.FeeMinor > settled.FeeMinor-fees {
			return core.Fail(409, "SETTLEMENT_CONTEXT_MISMATCH")
		}
		seen[c.Reference] = true
		gross += c.AmountMinor
		fees += c.FeeMinor
		if c.Reference == i.Reference {
			match = c
		}
	}
	var route struct {
		MerchantRef string `json:"merchant_ref"`
		Mode        string `json:"mode"`
	}
	var b []byte
	e = s.DB.QueryRow(ctx, `SELECT route_snapshot FROM payment_intents WHERE intent_id=$1`, i.ID).Scan(&b)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &route); e != nil {
		return e
	}
	if gross != settled.AmountMinor || fees != settled.FeeMinor || match.AmountMinor != i.AmountMinor || match.IntentID != i.ID.String() || match.Mode != route.Mode || formatID(match.MerchantID) != route.MerchantRef {
		return core.Fail(409, "SETTLEMENT_CONTEXT_MISMATCH")
	}
	balances, e := a.Settlement.FetchBalance(ctx)
	if e != nil {
		return core.Fail(503, "PROVIDER_CONFIRMATION_PENDING")
	}
	var observed int64 = -1
	for _, v := range balances {
		if v.Currency == i.Currency {
			if observed != -1 || v.AmountMinor < 0 {
				return fail("INVALID_LIQUIDITY_OBSERVATION")
			}
			observed = v.AmountMinor
		}
	}
	if observed < 0 {
		return core.Fail(503, "LIQUIDITY_CONFIRMATION_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "funding:"+i.ID.String()); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM transaction_funding WHERE intent_id=$1`, i.ID).Scan(&fp)
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
	e = tx.QueryRow(ctx, `SELECT state FROM payment_intents WHERE intent_id=$1 FOR UPDATE`, i.ID).Scan(&state)
	if e != nil {
		return e
	}
	if state != "VERIFIED_SUCCESS" {
		return core.Fail(409, "PAYMENT_NOT_VERIFIED")
	}
	var cash string
	var exception bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reconciliation_exceptions WHERE pool_id=$1 AND resolved_at IS NULL)`, i.PoolID).Scan(&exception)
	if e != nil {
		return e
	}
	if exception {
		return core.Fail(409, "POOL_RECONCILIATION_REQUIRED")
	}
	_, e = tx.Exec(ctx, `SELECT pool_id FROM custody_pools WHERE pool_id=$1 FOR UPDATE`, i.PoolID)
	if e != nil {
		return e
	}
	e = tx.QueryRow(ctx, `SELECT COALESCE(sum(balance_minor),0)::text FROM account_balances WHERE pool_id=$1 AND kind='SETTLED_CASH'`, i.PoolID).Scan(&cash)
	if e != nil {
		return e
	}
	existingCash, ok := new(big.Int).SetString(cash, 10)
	if !ok {
		return fail("INVALID_CASH_AGGREGATE")
	}
	required := new(big.Int).Add(existingCash, big.NewInt(i.AmountMinor-match.FeeMinor))
	if required.Cmp(big.NewInt(observed)) > 0 {
		return core.Fail(409, "UNBACKED_SETTLEMENT")
	}
	receivable, e := s.Account(trusted(ctx), tx, nil, i.PoolID, "PROVIDER_RECEIVABLE", i.LegalEntity, i.Currency, "")
	if e != nil {
		return e
	}
	asset, e := s.Account(trusted(ctx), tx, nil, i.PoolID, "SETTLED_CASH", i.LegalEntity, i.Currency, "")
	if e != nil {
		return e
	}
	entries := []core.Entry{{AccountID: receivable, Side: "CREDIT", AmountMinor: i.AmountMinor}, {AccountID: asset, Side: "DEBIT", AmountMinor: i.AmountMinor - match.FeeMinor}}
	if match.FeeMinor > 0 {
		expense, e := s.Account(trusted(ctx), tx, nil, i.PoolID, "PROCESSING_EXPENSE", i.LegalEntity, i.Currency, "")
		if e != nil {
			return e
		}
		entries = append(entries, core.Entry{AccountID: expense, Side: "DEBIT", AmountMinor: match.FeeMinor})
	}
	journal, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: "funding:" + i.ID.String(), PoolID: i.PoolID, LegalEntity: i.LegalEntity, Currency: i.Currency, PolicyVersion: "matched-settlement-v1", Authorization: map[string]string{"evidence_ref": in.CashEvidenceRef, "approved_by": strings.Join(in.Approvers, ",")}, Entries: entries})
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO transaction_funding(funding_id,intent_id,journal_id,snapshot,fingerprint) VALUES($1,$2,$3,$4,$5)`, ids.New(), i.ID, journal, raw(in), core.Fingerprint(in))
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE payment_intents SET settlement_state='SETTLED' WHERE intent_id=$1`, i.ID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE custody_pools SET restricted=false WHERE pool_id=$1`, i.PoolID)
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "funding.reconciled", i.ID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type Evidence struct {
	ID               ids.ID    `json:"evidence_id"`
	LineID           ids.ID    `json:"line_id"`
	Condition        string    `json:"condition"`
	Reference        string    `json:"evidence_ref"`
	Version          string    `json:"evidence_version"`
	PolicyVersion    string    `json:"policy_version"`
	ExpiresAt        time.Time `json:"expires_at"`
	HumanApprovalRef string    `json:"human_approval_ref"`
}

func (s *Service) SubmitEvidence(ctx context.Context, in Evidence) error {
	p, e := core.Caller(ctx)
	if e != nil {
		return e
	}
	a, e := s.lineAllocation(ctx, in.LineID)
	if e != nil {
		return e
	}
	policy, e := s.Policy(ctx, a.PolicyID)
	if e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || in.Reference == "" || in.Version == "" || in.PolicyVersion != policy.Version || !in.ExpiresAt.After(time.Now()) || !has(policy.Conditions, in.Condition) || !has(policy.EvidenceServices, string(p.Service)) || (policy.HumanReview && in.HumanApprovalRef == "") {
		return core.Fail(403, "RELEASE_EVIDENCE_AUTHORITY_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "evidence:"+in.ID.String()); e != nil {
		return e
	}
	key := "evidence:" + in.ID.String()
	fp := core.Fingerprint(in)
	var old string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM lifecycle_decisions WHERE operation_key=$1`, key).Scan(&old)
	if e == nil {
		if old != fp {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO lifecycle_decisions(decision_id,aggregate_id,operation_key,action,fingerprint,snapshot) VALUES($1,$2,$3,'EVIDENCE',$4,$5)`, in.ID, in.LineID, key, fp, raw(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "release.evidence_received", in.LineID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) lineAllocation(ctx context.Context, line ids.ID) (Allocation, error) {
	var id ids.ID
	e := s.DB.QueryRow(ctx, `SELECT allocation_id FROM allocation_lines WHERE line_id=$1`, line).Scan(&id)
	if e != nil {
		return Allocation{}, core.Fail(404, "ALLOCATION_LINE_NOT_FOUND")
	}
	return s.allocation(ctx, s.DB, id)
}
func (s *Service) sourceFor(ctx context.Context, a Allocation) error {
	p, e := core.Caller(ctx)
	if e != nil {
		return e
	}
	var source string
	e = s.DB.QueryRow(ctx, `SELECT source_service FROM payment_intents WHERE intent_id=$1`, a.IntentID).Scan(&source)
	if e != nil {
		return e
	}
	if string(p.Service) != source && p.Service != platform.AdminService {
		return core.Fail(403, "SOURCE_NOT_AUTHORIZED")
	}
	return nil
}

type Release struct {
	LineID        ids.ID   `json:"line_id"`
	AmountMinor   int64    `json:"amount_minor"`
	PolicyVersion string   `json:"policy_version"`
	EvidenceIDs   []ids.ID `json:"evidence_ids"`
}

func (s *Service) Release(ctx context.Context, in Release, key string) (ids.ID, error) {
	if e := s.CheckEnvironmentCapability("CUSTODY_RELEASE"); e != nil {
		return ids.Nil, e
	}
	a, e := s.lineAllocation(ctx, in.LineID)
	if e != nil {
		return ids.Nil, e
	}
	if e = s.sourceFor(ctx, a); e != nil {
		return ids.Nil, e
	}
	policy, e := s.Policy(ctx, a.PolicyID)
	if e != nil {
		return ids.Nil, e
	}
	if key == "" || len(key) > 128 || in.AmountMinor <= 0 || in.PolicyVersion != policy.Version {
		return ids.Nil, fail("INVALID_RELEASE")
	}
	p, _ := core.Caller(ctx)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return ids.Nil, e
	}
	defer tx.Rollback(ctx)
	op := "release:" + in.LineID.String() + ":" + key
	if e = lock(ctx, tx, op); e != nil {
		return ids.Nil, e
	}
	var prior ids.ID
	var fp string
	e = tx.QueryRow(ctx, `SELECT journal_id,fingerprint FROM lifecycle_decisions WHERE operation_key=$1`, op).Scan(&prior, &fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return ids.Nil, core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return prior, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return ids.Nil, e
	}
	var state string
	var amount, released int64
	var protected ids.ID
	var available, authority *ids.ID
	e = tx.QueryRow(ctx, `SELECT state,amount_minor,released_minor,protected_account_id,available_account_id,authority_id FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, in.LineID).Scan(&state, &amount, &released, &protected, &available, &authority)
	if e != nil {
		return ids.Nil, e
	}
	if !has([]string{"PROTECTED", "RELEASE_ELIGIBLE", "AVAILABLE", "RELEASED", "WITHDRAWAL_RESERVED"}, state) || available == nil || in.AmountMinor > amount-released {
		return ids.Nil, core.Fail(409, "ALLOCATION_NOT_RELEASE_ELIGIBLE")
	}
	var auth Authority
	var b []byte
	e = tx.QueryRow(ctx, `SELECT snapshot FROM beneficiary_authorities WHERE authority_id=$1 AND NOT revoked AND expires_at>now() FOR SHARE`, authority).Scan(&b)
	if e != nil {
		return ids.Nil, core.Fail(403, "CURRENT_BENEFICIARY_AUTHORITY_REQUIRED")
	}
	if e = decode(b, &auth); e != nil {
		return ids.Nil, e
	}
	if auth.WalletID != nil {
		if e = s.outstandingRecovery(ctx, tx, *auth.WalletID); e != nil {
			return ids.Nil, e
		}
		if e = s.currentWallet(ctx, tx, *auth.WalletID, auth.LegalPartyRef); e != nil {
			return ids.Nil, e
		}
	}
	conditions := map[string]bool{}
	for _, id := range in.EvidenceIDs {
		var ev Evidence
		if e = load(ctx, tx, `SELECT snapshot FROM lifecycle_decisions WHERE decision_id=$1 AND action='EVIDENCE'`, id, &ev); e != nil {
			return ids.Nil, e
		}
		if ev.LineID != in.LineID || ev.PolicyVersion != policy.Version || !ev.ExpiresAt.After(time.Now()) {
			return ids.Nil, core.Fail(403, "STALE_RELEASE_EVIDENCE")
		}
		conditions[ev.Condition] = true
	}
	for _, c := range policy.Conditions {
		if !conditions[c] {
			return ids.Nil, core.Fail(403, "REQUIRED_RELEASE_EVIDENCE_MISSING")
		}
	}
	var funded bool
	var custody string
	e = tx.QueryRow(ctx, `SELECT p.state='VERIFIED_SUCCESS' AND p.settlement_state='SETTLED',f.snapshot->>'custody_evidence_ref' FROM payment_intents p JOIN transaction_funding f USING(intent_id) WHERE p.intent_id=$1 FOR SHARE OF p`, a.IntentID).Scan(&funded, &custody)
	if e != nil || !funded {
		return ids.Nil, core.Fail(409, "UNFUNDED_RELEASE")
	}
	if policy.RequiresCustody {
		var approved bool
		e = tx.QueryRow(ctx, `SELECT custody_approved FROM custody_pools WHERE pool_id=$1`, a.PoolID).Scan(&approved)
		if e != nil {
			return ids.Nil, e
		}
		if !approved || custody == "" {
			return ids.Nil, core.Fail(403, "APPROVED_CUSTODY_REQUIRED")
		}
	}
	journal, e := s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: op, PoolID: a.PoolID, LegalEntity: a.LegalEntity, Currency: a.Currency, PolicyVersion: policy.Version, Authorization: map[string]string{"evidence_ref": raw(in.EvidenceIDs), "approved_by": string(p.Service)}, Entries: []core.Entry{{AccountID: protected, Side: "DEBIT", AmountMinor: in.AmountMinor}, {AccountID: *available, Side: "CREDIT", AmountMinor: in.AmountMinor}}})
	if e != nil {
		return ids.Nil, e
	}
	_, e = tx.Exec(ctx, `UPDATE allocation_lines SET released_minor=released_minor+$2,state=CASE WHEN reserved_minor>paid_minor THEN 'WITHDRAWAL_RESERVED' ELSE 'AVAILABLE' END WHERE line_id=$1`, in.LineID, in.AmountMinor)
	if e != nil {
		return ids.Nil, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO lifecycle_decisions(decision_id,aggregate_id,operation_key,action,fingerprint,snapshot,journal_id) VALUES($1,$2,$3,'RELEASE',$4,$5,$6)`, ids.New(), in.LineID, op, core.Fingerprint(in), raw(in), journal)
	if e != nil {
		return ids.Nil, e
	}
	if e = s.Emit(ctx, tx, "finance.allocation_released.v1", "allocation_line", in.LineID, map[string]any{"line_id": in.LineID, "amount_minor": in.AmountMinor, "currency": a.Currency, "journal_id": journal}); e != nil {
		return ids.Nil, e
	}
	if auth.Role == "PLATFORM" {
		if e = s.Emit(ctx, tx, "finance.platform_commission_allocated.v1", "allocation_line", in.LineID, map[string]any{"line_id": in.LineID, "amount_minor": in.AmountMinor, "currency": a.Currency, "journal_id": journal}); e != nil {
			return ids.Nil, e
		}
	}
	if e = s.Audit(ctx, tx, "allocation.released", in.LineID.String(), in); e != nil {
		return ids.Nil, e
	}
	return journal, tx.Commit(ctx)
}

type Hold struct {
	State       string   `json:"state"`
	EvidenceRef string   `json:"evidence_ref"`
	Approvers   []string `json:"approvers"`
}

func (s *Service) Hold(ctx context.Context, line ids.ID, in Hold, key string) error {
	a, e := s.lineAllocation(ctx, line)
	if e != nil {
		return e
	}
	if e = s.sourceFor(ctx, a); e != nil {
		return e
	}
	if in.EvidenceRef == "" || key == "" || !has([]string{"HELD", "DISPUTED", "PROTECTED"}, in.State) || (in.State == "PROTECTED" && !dual(in.Approvers)) {
		return fail("HOLD_DECISION_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	op := "hold:" + line.String() + key
	if e = lock(ctx, tx, op); e != nil {
		return e
	}
	var old string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM lifecycle_decisions WHERE operation_key=$1`, op).Scan(&old)
	if e == nil {
		if old != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	var state string
	e = tx.QueryRow(ctx, `SELECT state FROM allocation_lines WHERE line_id=$1 FOR UPDATE`, line).Scan(&state)
	if e != nil {
		return e
	}
	if has([]string{"CORRECTED", "REVERSED", "PAID_OUT"}, state) {
		return core.Fail(409, "TERMINAL_ALLOCATION")
	}
	var remedy bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM financial_cases WHERE line_id=$1) OR EXISTS(SELECT 1 FROM refund_attempts WHERE line_id=$1)`, line).Scan(&remedy); e != nil {
		return e
	}
	if remedy {
		return core.Fail(409, "AMOUNT_SPECIFIC_REMEDY_WORKFLOW_REQUIRED")
	}
	next, e := s.moveHold(ctx, tx, a, line, state, in.State, op, in.EvidenceRef)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE allocation_lines SET state=$2 WHERE line_id=$1`, line, next)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO lifecycle_decisions(decision_id,aggregate_id,operation_key,action,fingerprint,snapshot) VALUES($1,$2,$3,'HOLD',$4,$5)`, ids.New(), line, op, core.Fingerprint(in), raw(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "allocation.hold_decided", line.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
