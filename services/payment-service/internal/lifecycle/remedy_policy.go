package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
)

// RemedyPolicy supplements an immutable commercial policy. Durations are supplied
// by an approved jurisdiction pack, never inferred from a property category.
type RemedyPolicy struct {
	PolicyID                  ids.ID   `json:"policy_id"`
	ApprovalRef               string   `json:"approval_ref"`
	ClaimSeconds              int64    `json:"claim_seconds"`
	ChallengeSeconds          int64    `json:"challenge_seconds"`
	ReviewSeconds             int64    `json:"review_seconds"`
	RefundSeconds             int64    `json:"refund_seconds"`
	EscalationSeconds         int64    `json:"escalation_seconds"`
	ReserveExtensionSeconds   int64    `json:"reserve_extension_seconds"`
	NoticeSeconds             int64    `json:"notice_seconds"`
	CoolingSeconds            int64    `json:"cooling_seconds"`
	DualAboveMinor            int64    `json:"dual_above_minor"`
	RefundStrategies          []string `json:"refund_strategies"`
	EvidenceServices          []string `json:"evidence_services"`
	AllowUncontested          bool     `json:"allow_uncontested"`
	WalletTopup               bool     `json:"wallet_topup"`
	WalletSpend               bool     `json:"wallet_spend"`
	TopupFeeTreatment         string   `json:"topup_fee_treatment"`
	RecoverFromProceeds       bool     `json:"recover_from_proceeds"`
	ReserveRecovery           bool     `json:"reserve_recovery"`
	CorporateApprovalRequired bool     `json:"corporate_approval_required"`
	WorkMandateRequired       bool     `json:"work_mandate_required"`
	ConsentRequired           bool     `json:"payout_consent_required"`
	DepositTypes              []string `json:"deposit_types"`
}

func (s *Service) PutRemedyPolicy(ctx context.Context, in RemedyPolicy) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	p, e := s.Policy(ctx, in.PolicyID)
	if e != nil {
		return e
	}
	if !p.Active || in.ApprovalRef == "" || in.DualAboveMinor <= 0 || len(in.RefundStrategies) == 0 || len(in.EvidenceServices) == 0 {
		return fail("APPROVED_REMEDY_POLICY_REQUIRED")
	}
	if in.WalletTopup && !has([]string{"CUSTOMER_NET", "PLATFORM_FUNDED"}, in.TopupFeeTreatment) {
		return fail("TOPUP_FEE_POLICY_REQUIRED")
	}
	for _, n := range []int64{in.ClaimSeconds, in.ChallengeSeconds, in.ReviewSeconds, in.RefundSeconds, in.EscalationSeconds, in.ReserveExtensionSeconds, in.NoticeSeconds, in.CoolingSeconds} {
		if n <= 0 || n > 315360000 {
			return fail("JURISDICTION_DEADLINES_REQUIRED")
		}
	}
	for _, v := range in.DepositTypes {
		if !has([]string{"RESIDENTIAL_SECURITY", "HOSPITALITY_DAMAGE", "EVENT_CLEANING_DAMAGE", "LAND_RESTORATION", "POLICY_REFUNDABLE_RESERVE"}, v) {
			return fail("INVALID_DEPOSIT_TYPE")
		}
	}
	for _, v := range in.RefundStrategies {
		if !has([]string{"ORIGINAL_INSTRUMENT", "PROVIDER_REVERSAL", "VERIFIED_BANK", "WALLET_CREDIT", "MANUAL_REGULATED_REMEDY"}, v) {
			return fail("INVALID_REFUND_STRATEGY")
		}
	}
	for _, v := range in.EvidenceServices {
		if !platform.KnownService(v) || has([]string{string(platform.APIGateway), string(platform.PaymentService), string(platform.NotificationService)}, v) {
			return fail("BUSINESS_EVIDENCE_SERVICE_REQUIRED")
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "remedy-policy:"+in.PolicyID.String()); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM remedy_policies WHERE policy_id=$1`, in.PolicyID).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "REMEDY_POLICY_IMMUTABLE")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO remedy_policies(policy_id,snapshot,fingerprint) VALUES($1,$2,$3)`, in.PolicyID, raw(in), core.Fingerprint(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "remedy.policy_recorded", in.PolicyID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) remedyPolicy(ctx context.Context, q query, id ids.ID) (p RemedyPolicy, e error) {
	e = load(ctx, q, `SELECT snapshot FROM remedy_policies WHERE policy_id=$1`, id, &p)
	return
}

type RemedyEvidence struct {
	ID          ids.ID    `json:"evidence_id"`
	BusinessRef string    `json:"business_ref"`
	Reference   string    `json:"reference"`
	Version     string    `json:"version"`
	ActorRef    string    `json:"actor_ref"`
	HashRef     string    `json:"hash_ref"`
	ObservedAt  time.Time `json:"observed_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (s *Service) PutRemedyEvidence(ctx context.Context, in RemedyEvidence) error {
	p, e := core.Caller(ctx)
	if e != nil {
		return e
	}
	if has([]string{string(platform.APIGateway), string(platform.PaymentService), string(platform.NotificationService)}, string(p.Service)) || in.ID.ValidateV7() != nil || in.BusinessRef == "" || in.Reference == "" || in.Version == "" || in.ActorRef == "" || in.HashRef == "" || in.ObservedAt.IsZero() || in.ObservedAt.After(time.Now()) || !in.ExpiresAt.After(time.Now()) {
		return fail("AUTHORITATIVE_EVIDENCE_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "remedy-evidence:"+in.ID.String()); e != nil {
		return e
	}
	var fp, source string
	e = tx.QueryRow(ctx, `SELECT fingerprint,source_service FROM remedy_evidence WHERE evidence_id=$1`, in.ID).Scan(&fp, &source)
	if e == nil {
		if fp != core.Fingerprint(in) || source != string(p.Service) {
			return core.Fail(409, "EVIDENCE_IMMUTABLE")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO remedy_evidence(evidence_id,source_service,snapshot,fingerprint,expires_at) VALUES($1,$2,$3,$4,$5)`, in.ID, p.Service, raw(in), core.Fingerprint(in), in.ExpiresAt)
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "remedy.evidence_recorded", in.ID.String(), map[string]any{"evidence_id": in.ID, "source_service": p.Service}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) RevokeRemedyEvidence(ctx context.Context, id ids.ID, ref string) error {
	p, e := core.Caller(ctx)
	if e != nil {
		return e
	}
	if ref == "" {
		return fail("REVOCATION_EVIDENCE_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var source string
	if e = tx.QueryRow(ctx, `SELECT source_service FROM remedy_evidence WHERE evidence_id=$1 FOR UPDATE`, id).Scan(&source); e != nil {
		return e
	}
	if string(p.Service) != source && p.Service != platform.AdminService {
		return core.Fail(403, "SOURCE_NOT_AUTHORIZED")
	}
	if _, e = tx.Exec(ctx, `UPDATE remedy_evidence SET revoked=true WHERE evidence_id=$1`, id); e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "remedy.evidence_revoked", id.String(), map[string]string{"evidence_ref": ref}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) checkEvidence(ctx context.Context, tx pgx.Tx, p RemedyPolicy, business string, xs []ids.ID) error {
	if len(xs) == 0 {
		return core.Fail(403, "REMEDY_EVIDENCE_REQUIRED")
	}
	for _, id := range xs {
		var b []byte
		var service string
		e := tx.QueryRow(ctx, `SELECT snapshot,source_service FROM remedy_evidence WHERE evidence_id=$1 AND NOT revoked AND expires_at>now() FOR SHARE`, id).Scan(&b, &service)
		if e != nil {
			return core.Fail(403, "STALE_REMEDY_EVIDENCE")
		}
		var ev RemedyEvidence
		if e = decode(b, &ev); e != nil {
			return e
		}
		if ev.BusinessRef != business || !has(p.EvidenceServices, service) {
			return core.Fail(403, "REMEDY_EVIDENCE_CONTEXT_MISMATCH")
		}
	}
	return nil
}
func accountBalance(ctx context.Context, q query, id ids.ID) (int64, error) {
	var n int64
	e := q.QueryRow(ctx, `SELECT balance_minor::bigint FROM account_balances WHERE account_id=$1`, id).Scan(&n)
	return n, e
}
func (s *Service) remedyPost(ctx context.Context, tx pgx.Tx, a Allocation, op, ref string, entries []core.Entry) (ids.ID, error) {
	return s.PostTx(trusted(ctx), tx, core.Posting{OperationRef: op, PoolID: a.PoolID, LegalEntity: a.LegalEntity, Currency: a.Currency, PolicyVersion: "remedy:" + a.PolicyID.String(), Authorization: map[string]string{"evidence_ref": ref, "approved_by": "authorized-remedy-workflow"}, Entries: entries})
}

type Deposit struct {
	ID                 ids.ID    `json:"deposit_id"`
	LineID             ids.ID    `json:"line_id"`
	Type               string    `json:"deposit_type"`
	CustodyRef         string    `json:"custody_ref"`
	IngressRef         string    `json:"ingress_ref"`
	InspectionIDs      []ids.ID  `json:"inspection_evidence_ids"`
	IntentID           ids.ID    `json:"intent_id"`
	QuoteID            ids.ID    `json:"quote_id"`
	PayerWalletID      ids.ID    `json:"payer_wallet_id"`
	BusinessRef        string    `json:"business_ref"`
	PolicyID           ids.ID    `json:"policy_id"`
	Jurisdiction       string    `json:"jurisdiction"`
	Currency           string    `json:"currency"`
	AmountMinor        int64     `json:"amount_minor"`
	LiabilityAccountID ids.ID    `json:"liability_account_id"`
	State              string    `json:"state"`
	ClaimDeadline      time.Time `json:"claim_deadline"`
}

func (s *Service) RegisterDeposit(ctx context.Context, in Deposit) (Deposit, error) {
	a, e := s.lineAllocation(ctx, in.LineID)
	if e != nil {
		return in, e
	}
	if e = s.sourceFor(ctx, a); e != nil {
		return in, e
	}
	var q Quote
	if e = load(ctx, s.DB, `SELECT snapshot FROM locked_quotes WHERE quote_id=$1`, a.QuoteID, &q); e != nil {
		return in, e
	}
	if in.ID.ValidateV7() != nil || in.IngressRef == "" || in.CustodyRef == "" || !has([]string{"RESIDENTIAL_SECURITY", "HOSPITALITY_DAMAGE", "EVENT_CLEANING_DAMAGE", "LAND_RESTORATION", "POLICY_REFUNDABLE_RESERVE"}, in.Type) {
		return in, fail("REFUNDABLE_DEPOSIT_CONTEXT_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return in, e
	}
	defer tx.Rollback(ctx)
	var kind, state, component string
	e = tx.QueryRow(ctx, `SELECT l.protected_account_id,l.amount_minor,b.kind,l.state,c->>'kind' FROM allocation_lines l JOIN ledger_accounts b ON b.account_id=l.protected_account_id JOIN allocation_snapshots s USING(allocation_id) JOIN locked_quotes q ON q.quote_id=(s.snapshot->>'quote_id')::uuid CROSS JOIN LATERAL jsonb_array_elements(q.snapshot->'components') c WHERE l.line_id=$1 AND c->>'component_ref'=l.snapshot->>'component_ref' FOR UPDATE OF l`, in.LineID).Scan(&in.LiabilityAccountID, &in.AmountMinor, &kind, &state, &component)
	if e != nil {
		return in, e
	}
	if !has([]string{"DEPOSIT", "RESERVE"}, kind) || state != "PROTECTED" || !has([]string{"DEPOSIT", "PURCHASE_DEPOSIT", "RESERVATION", "RESERVE", "CLEANING_RESERVE"}, component) {
		return in, core.Fail(403, "DEPOSIT_LIABILITY_REQUIRED")
	}
	rp, e := s.remedyPolicy(ctx, tx, a.PolicyID)
	if e != nil {
		return in, e
	}
	if !has(rp.DepositTypes, in.Type) {
		return in, core.Fail(403, "DEPOSIT_TYPE_POLICY_REQUIRED")
	}
	if e = s.checkEvidence(ctx, tx, rp, q.BusinessRef, in.InspectionIDs); e != nil {
		return in, e
	}
	in.IntentID = a.IntentID
	in.QuoteID = a.QuoteID
	in.PayerWalletID = q.PayerWalletID
	in.BusinessRef = q.BusinessRef
	in.PolicyID = a.PolicyID
	in.Jurisdiction = q.Jurisdiction
	in.Currency = a.Currency
	in.State = "PROTECTED"
	in.ClaimDeadline = time.Now().Add(time.Duration(rp.ClaimSeconds) * time.Second)
	var prior Deposit
	e = load(ctx, tx, `SELECT snapshot FROM refundable_deposits WHERE deposit_id=$1`, in.ID, &prior)
	if e == nil {
		in.ClaimDeadline = prior.ClaimDeadline
		if core.Fingerprint(prior) != core.Fingerprint(in) {
			return in, core.Fail(409, "DEPOSIT_IMMUTABLE")
		}
		return prior, nil
	}
	_, e = tx.Exec(ctx, `INSERT INTO refundable_deposits(deposit_id,line_id,snapshot,fingerprint) VALUES($1,$2,$3,$4)`, in.ID, in.LineID, raw(in), core.Fingerprint(in))
	if e != nil {
		return in, e
	}
	if e = s.Audit(ctx, tx, "deposit.registered", in.ID.String(), in); e != nil {
		return in, e
	}
	if e = s.Emit(ctx, tx, "finance.deposit_registered.v1", "deposit", in.ID, in); e != nil {
		return in, e
	}
	return in, tx.Commit(ctx)
}
