package finance

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/jackc/pgx/v5"
	"time"
)

var capabilities = map[string]bool{"WALLET_PROVISIONING": true, "WALLET_FUNDING": true, "WALLET_SPENDING": true, "INTERNAL_TRANSFER": true, "WITHDRAWAL": true, "PROVIDER_PAYOUT": true, "REFUND": true, "CUSTODY_RELEASE": true, "DVA_ASSIGNMENT": true, "RECURRING_RENT": true, "SAVED_AUTHORIZATION": true, "CUSTOMER_CASHOUT": true}
var stages = map[string]bool{"IMPLEMENTATION": true, "LOCAL_TEST": true, "PROVIDER_TEST": true, "ACCOUNT": true, "COMMERCIAL": true, "REGULATORY": true, "SAFEGUARDING": true, "JURISDICTION": true, "STAGING": true, "PRODUCTION": true}

// Environment controls are a hard ceiling, independent of recorded approvals.
func (s *Service) CheckEnvironmentCapability(capability string) error {
	if !capabilities[capability] {
		return core.Fail(422, "UNKNOWN_FINANCIAL_CAPABILITY")
	}
	if capability == "RECURRING_RENT" || capability == "SAVED_AUTHORIZATION" || capability == "CUSTOMER_CASHOUT" {
		return core.Fail(403, "CAPABILITY_INTENTIONALLY_DEFERRED")
	}
	if s.Environment == "production" && capability != "WALLET_PROVISIONING" {
		return core.Fail(403, "PRODUCTION_CAPABILITY_DISABLED")
	}
	return nil
}

type CapabilityEvidence struct {
	ID           ids.ID    `json:"evidence_id"`
	Capability   string    `json:"capability"`
	Stage        string    `json:"stage"`
	LegalEntity  string    `json:"legal_entity"`
	ProviderID   *ids.ID   `json:"provider_account_id,omitempty"`
	Jurisdiction string    `json:"jurisdiction"`
	Version      string    `json:"version"`
	Status       string    `json:"status"`
	EvidenceRef  string    `json:"evidence_ref"`
	IssuerRef    string    `json:"issuer_ref"`
	Approvers    []string  `json:"approvers"`
	EffectiveAt  time.Time `json:"effective_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (s *Service) RecordCapabilityEvidence(ctx context.Context, in CapabilityEvidence) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || !capabilities[in.Capability] || !stages[in.Stage] || in.LegalEntity == "" || in.Jurisdiction == "" || in.Version == "" || in.EvidenceRef == "" || in.IssuerRef == "" || !in.ExpiresAt.After(in.EffectiveAt) || len(in.Approvers) != 2 || in.Approvers[0] == "" || in.Approvers[1] == "" || in.Approvers[0] == in.Approvers[1] {
		return core.Fail(422, "CAPABILITY_EVIDENCE_REQUIRED")
	}
	switch in.Status {
	case "NOT_RUN", "CERTIFIED", "APPROVED", "BLOCKED", "REVOKED":
	default:
		return core.Fail(422, "INVALID_EVIDENCE_STATUS")
	}
	if in.ProviderID != nil {
		var entity string
		if e := s.DB.QueryRow(ctx, `SELECT legal_entity FROM provider_accounts WHERE provider_account_id=$1`, *in.ProviderID).Scan(&entity); e != nil {
			return e
		}
		if entity != in.LegalEntity {
			return core.Fail(403, "PROVIDER_ENTITY_MISMATCH")
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "capability-evidence:"+in.ID.String())
	if e != nil {
		return e
	}
	var fp string
	e = tx.QueryRow(ctx, `SELECT fingerprint FROM financial_capability_evidence WHERE evidence_id=$1`, in.ID).Scan(&fp)
	if e == nil {
		if fp != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	raw, _ := json.Marshal(in)
	_, e = tx.Exec(ctx, `INSERT INTO financial_capability_evidence(evidence_id,capability,stage,legal_entity,provider_account_id,jurisdiction,version,status,snapshot,fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, in.ID, in.Capability, in.Stage, in.LegalEntity, in.ProviderID, in.Jurisdiction, in.Version, in.Status, string(raw), core.Fingerprint(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "capability.evidence_recorded", in.ID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type CapabilityRequest struct {
	Capability   string `json:"capability"`
	WalletID     ids.ID `json:"wallet_id"`
	ProviderID   ids.ID `json:"provider_account_id"`
	Jurisdiction string `json:"jurisdiction"`
	PolicyRef    string `json:"policy_ref"`
}
type CapabilityDecision struct {
	Capability  string            `json:"capability"`
	Environment string            `json:"environment"`
	Allowed     bool              `json:"allowed"`
	Reason      string            `json:"reason"`
	Stages      map[string]string `json:"stages"`
}

// This is an eligibility readback, not a substitute for a workflow's quote,
// entitlement, risk, dual approval, balance/coverage and transactional checks.
func (s *Service) EvaluateCapability(ctx context.Context, in CapabilityRequest) (CapabilityDecision, error) {
	out := CapabilityDecision{Capability: in.Capability, Environment: s.Environment, Reason: "WORKFLOW_AUTHORIZATION_REQUIRED", Stages: map[string]string{}}
	if !capabilities[in.Capability] || in.PolicyRef == "" || in.Jurisdiction == "" {
		return out, core.Fail(422, "CAPABILITY_CONTEXT_REQUIRED")
	}
	w, e := s.OwnWallet(ctx, in.WalletID)
	if e != nil {
		return out, e
	}
	var c ProviderConfig
	var raw []byte
	if e = s.DB.QueryRow(ctx, `SELECT config FROM provider_accounts WHERE provider_account_id=$1`, in.ProviderID).Scan(&raw); e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &c); e != nil {
		return out, e
	}
	if c.LegalEntity != w.LegalEntity || !contains(c.Currencies, w.Currency) || !contains(c.Countries, in.Jurisdiction) {
		return out, core.Fail(403, "CAPABILITY_SCOPE_MISMATCH")
	}
	for stage := range stages {
		out.Stages[stage] = "NOT_RUN"
	}
	rows, e := s.DB.Query(ctx, `SELECT DISTINCT ON(stage) stage,status,snapshot FROM financial_capability_evidence WHERE capability=$1 AND legal_entity=$2 AND provider_account_id IS NOT DISTINCT FROM $3 AND jurisdiction=$4 ORDER BY stage,created_at DESC,evidence_id DESC`, in.Capability, w.LegalEntity, in.ProviderID, in.Jurisdiction)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var stage, status string
		var b []byte
		if e = rows.Scan(&stage, &status, &b); e != nil {
			return out, e
		}
		var fact CapabilityEvidence
		if e = json.Unmarshal(b, &fact); e != nil {
			return out, e
		}
		if fact.EffectiveAt.After(time.Now()) {
			status = "NOT_EFFECTIVE"
		} else if !fact.ExpiresAt.After(time.Now()) {
			status = "EXPIRED"
		}
		out.Stages[stage] = status
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if e = s.CheckEnvironmentCapability(in.Capability); e != nil {
		if s.Environment == "production" {
			out.Reason = "PRODUCTION_CAPABILITY_DISABLED"
		} else {
			out.Reason = "CAPABILITY_INTENTIONALLY_DEFERRED"
		}
		return out, nil
	}
	if w.State != "ACTIVE" {
		out.Reason = "WALLET_RESTRICTED"
		return out, nil
	}
	if c.Mode != "test" || !c.Approved || !c.Operational {
		out.Reason = "APPROVED_TEST_PROVIDER_REQUIRED"
		return out, nil
	}
	// Evaluate actual immutable policy scope. Production remains a hard ceiling;
	// local eligibility is never a journal/withdrawal authorization.
	policyID, err := ids.ParseV7(in.PolicyRef)
	if err != nil {
		out.Reason = "POLICY_CONTEXT_REQUIRED"
		return out, nil
	}
	var policyRaw []byte
	err = s.DB.QueryRow(ctx, `SELECT snapshot FROM financial_policies WHERE policy_id=$1 UNION ALL SELECT snapshot FROM wallet_transfer_policies WHERE policy_id=$1 LIMIT 1`, policyID).Scan(&policyRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		out.Reason = "POLICY_CONTEXT_REQUIRED"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var policy struct {
		Currency       string     `json:"currency"`
		Jurisdiction   string     `json:"jurisdiction"`
		LegalEntity    string     `json:"legal_entity"`
		PoolID         ids.ID     `json:"pool_id"`
		LocalTestOnly  bool       `json:"local_test_only"`
		SourceDomains  []string   `json:"source_domains"`
		EffectiveAt    time.Time  `json:"effective_at"`
		ExpiresAt      time.Time  `json:"expires_at"`
		Active         bool       `json:"active"`
		ApprovalRef    string     `json:"approval_ref"`
		EffectiveFrom  time.Time  `json:"effective_from"`
		EffectiveUntil *time.Time `json:"effective_until"`
	}
	if err = json.Unmarshal(policyRaw, &policy); err != nil {
		return out, err
	}
	valid := policy.Currency == w.Currency && policy.Jurisdiction == in.Jurisdiction
	if in.Capability == "INTERNAL_TRANSFER" {
		valid = valid && policy.LocalTestOnly && policy.PoolID == c.PoolID && policy.LegalEntity == w.LegalEntity && contains(policy.SourceDomains, w.Domain) && !policy.EffectiveAt.After(time.Now()) && policy.ExpiresAt.After(time.Now())
	} else {
		var disabled bool
		if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM policy_deactivations WHERE policy_id=$1 AND effective_at<=now())`, policyID).Scan(&disabled); err != nil {
			return out, err
		}
		valid = valid && policy.Active && policy.ApprovalRef != "" && !disabled && !policy.EffectiveFrom.After(time.Now()) && (policy.EffectiveUntil == nil || policy.EffectiveUntil.After(time.Now()))
	}
	if !valid {
		out.Reason = "POLICY_SCOPE_OR_EFFECTIVITY_DENIED"
		return out, nil
	}
	var verified bool
	if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_refs WHERE identity_id=$1 AND state='VERIFIED' AND expires_at>now())`, w.IdentityID).Scan(&verified); err != nil {
		return out, err
	}
	if !verified {
		out.Reason = "CURRENT_VERIFICATION_REQUIRED"
		return out, nil
	}
	for _, status := range out.Stages {
		if status == "BLOCKED" || status == "REVOKED" || status == "EXPIRED" || status == "NOT_EFFECTIVE" {
			out.Reason = "CAPABILITY_EVIDENCE_RESTRICTED"
			return out, nil
		}
	}
	out.Allowed = true
	out.Reason = "LOCAL_ELIGIBLE_WORKFLOW_AUTHORIZATION_REQUIRED"
	return out, nil
}
