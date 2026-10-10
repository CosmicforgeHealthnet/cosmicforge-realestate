package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"time"
)

func (s *Service) payoutPartyControl(ctx context.Context, q query, ap WithdrawalApproval) error {
	var policyID ids.ID
	var auth Authority
	var business string
	var b []byte
	e := q.QueryRow(ctx, `SELECT (s.snapshot->>'policy_id')::uuid,a.snapshot,lq.snapshot->>'business_ref' FROM allocation_lines l JOIN allocation_snapshots s USING(allocation_id) JOIN beneficiary_authorities a USING(authority_id) JOIN locked_quotes lq ON lq.quote_id=(s.snapshot->>'quote_id')::uuid WHERE line_id=$1`, ap.LineID).Scan(&policyID, &b, &business)
	if e != nil {
		return e
	}
	if e = decode(b, &auth); e != nil {
		return e
	}
	p, e := s.Policy(ctx, policyID)
	if e != nil {
		return e
	}
	if !p.Active || ap.AmountMinor > p.WithdrawalLimitMinor || p.WithdrawalLimitMinor <= 0 {
		return core.Fail(403, "WITHDRAWAL_POLICY_DISABLED")
	}
	var configured bool
	if e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM remedy_policies WHERE policy_id=$1)`, policyID).Scan(&configured); e != nil {
		return e
	}
	var rp RemedyPolicy
	if configured {
		rp, e = s.remedyPolicy(ctx, q, policyID)
		if e != nil {
			return e
		}
	}
	corporate := has([]string{"CORPORATE_OWNER", "HOSPITALITY", "DEVELOPER", "MANAGEMENT_COMPANY", "SERVICE_COMPANY", "PROFESSIONAL_FIRM"}, auth.Role)
	if (corporate || rp.CorporateApprovalRequired) && (ap.ApprovalChainRef == "" || ap.BranchProjectRef != business || !dual(ap.Approvers)) {
		return core.Fail(403, "CORPORATE_PAYOUT_CHAIN_REQUIRED")
	}
	if rp.ConsentRequired && ap.ConsentRef == "" {
		return core.Fail(403, "CURRENT_PAYOUT_CONSENT_REQUIRED")
	}
	if rp.WorkMandateRequired && has([]string{"ARTISAN", "PROFESSIONAL", "PROFESSIONAL_FIRM"}, auth.Role) && (ap.WorkMandateRef == "" || auth.MandateRef == "" || ap.WorkMandateRef != auth.MandateRef) {
		return core.Fail(403, "CURRENT_WORK_MANDATE_REQUIRED")
	}
	return nil
}

type CaseOverride struct {
	ID           ids.ID    `json:"override_id"`
	CaseID       ids.ID    `json:"case_id"`
	ActorRef     string    `json:"actor_ref"`
	AuthorityRef string    `json:"authority_ref"`
	ReasonRef    string    `json:"reason_ref"`
	EvidenceIDs  []ids.ID  `json:"evidence_ids"`
	Approvers    []string  `json:"approvers"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// A privileged exception extends review authority only. It does not replace a
// decision, edit a journal, release funds, or rewrite a closed case.
func (s *Service) OverrideCase(ctx context.Context, in CaseOverride) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || in.ActorRef == "" || in.AuthorityRef == "" || in.ReasonRef == "" || !dual(in.Approvers) || in.ActorRef == in.Approvers[0] || in.ActorRef == in.Approvers[1] || !in.ExpiresAt.After(time.Now()) {
		return fail("PRIVILEGED_OVERRIDE_AUTHORITY_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "case-override:"+in.CaseID.String()); e != nil {
		return e
	}
	var locked ids.ID
	if e = tx.QueryRow(ctx, `SELECT case_id FROM financial_cases WHERE case_id=$1 FOR UPDATE`, in.CaseID).Scan(&locked); e != nil {
		return e
	}
	c, e := s.caseRow(ctx, tx, in.CaseID)
	if e != nil {
		return e
	}
	if c.State == "CLOSED" {
		return core.Fail(409, "CASE_CLOSED")
	}
	rp, e := s.remedyPolicy(ctx, tx, c.PolicyID)
	if e != nil {
		return e
	}
	if in.ExpiresAt.After(time.Now().Add(time.Duration(rp.ReserveExtensionSeconds) * time.Second)) {
		return fail("OVERRIDE_DURATION_POLICY_REQUIRED")
	}
	if e = s.checkEvidence(ctx, tx, rp, c.BusinessRef, in.EvidenceIDs); e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM financial_case_facts WHERE fact_id=$1`, in.ID).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	_, e = tx.Exec(ctx, `INSERT INTO financial_case_facts(fact_id,case_id,operation_key,fingerprint,snapshot,kind) VALUES($1,$2,$3,$4,$5,'OVERRIDE')`, in.ID, in.CaseID, "case-override:"+in.ID.String(), core.Fingerprint(in), raw(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "case.privileged_override", c.ID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type ReconciliationResolution struct {
	ID            ids.ID   `json:"resolution_id"`
	FindingID     ids.ID   `json:"finding_id"`
	FollowupRunID ids.ID   `json:"followup_run_id"`
	AuthorityRef  string   `json:"authority_ref"`
	EvidenceRef   string   `json:"evidence_ref"`
	Approvers     []string `json:"approvers"`
}

func (s *Service) ResolveReconciliation(ctx context.Context, in ReconciliationResolution) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || in.AuthorityRef == "" || in.EvidenceRef == "" || !dual(in.Approvers) {
		return fail("RECONCILIATION_RESOLUTION_AUTHORITY_REQUIRED")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "finding:"+in.FindingID.String()); e != nil {
		return e
	}
	var old ReconciliationResolution
	if e = load(ctx, tx, `SELECT snapshot FROM reconciliation_resolutions WHERE finding_id=$1`, in.FindingID, &old); e == nil {
		if core.Fingerprint(old) != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	var matched bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reconciliation_findings f JOIN reconciliation_runs r USING(run_id) JOIN reconciliation_runs n ON n.run_id=$2 AND n.provider_account_id=r.provider_account_id AND n.created_at>r.created_at WHERE f.finding_id=$1 AND NOT EXISTS(SELECT 1 FROM reconciliation_findings nf WHERE nf.run_id=n.run_id AND nf.kind=f.kind AND nf.aggregate_ref=f.aggregate_ref))`, in.FindingID, in.FollowupRunID).Scan(&matched)
	if e != nil {
		return e
	}
	if !matched {
		return core.Fail(409, "FOLLOWUP_RECONCILIATION_REQUIRED")
	}
	_, e = tx.Exec(ctx, `INSERT INTO reconciliation_resolutions(resolution_id,finding_id,snapshot) VALUES($1,$2,$3)`, in.ID, in.FindingID, raw(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "reconciliation.finding_resolved", in.FindingID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
