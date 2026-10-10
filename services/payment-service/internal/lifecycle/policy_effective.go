package lifecycle

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/payment-service/internal/core"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"time"
)

type PolicySelector struct {
	Key          string    `json:"policy_key"`
	Jurisdiction string    `json:"jurisdiction"`
	Currency     string    `json:"currency"`
	ContractType string    `json:"contract_type"`
	Product      string    `json:"product"`
	At           time.Time `json:"effective_at"`
}

func (s *Service) EffectivePolicy(ctx context.Context, in PolicySelector) (Policy, error) {
	if _, e := core.Caller(ctx); e != nil {
		return Policy{}, e
	}
	if in.At.IsZero() || in.Key == "" || in.Jurisdiction == "" || in.ContractType == "" || in.Product == "" || !core.ValidCurrency(in.Currency) {
		return Policy{}, fail("POLICY_SELECTOR_REQUIRED")
	}
	var id ids.ID
	e := s.DB.QueryRow(ctx, `SELECT p.policy_id FROM financial_policies p WHERE p.active AND p.policy_key=$1 AND p.jurisdiction=$2 AND p.currency=$3 AND p.contract_type=$4 AND p.product=$5 AND p.effective_from<=$6 AND (p.effective_until IS NULL OR p.effective_until>$6) AND NOT EXISTS(SELECT 1 FROM policy_deactivations d WHERE d.policy_id=p.policy_id AND d.effective_at<=$6) ORDER BY effective_from DESC LIMIT 1`, in.Key, in.Jurisdiction, in.Currency, in.ContractType, in.Product, in.At).Scan(&id)
	if e != nil {
		return Policy{}, core.Fail(403, "APPROVED_POLICY_PACK_REQUIRED")
	}
	return s.Policy(ctx, id)
}

type PolicyDeactivation struct {
	ID          ids.ID    `json:"decision_id"`
	PolicyID    ids.ID    `json:"policy_id"`
	EffectiveAt time.Time `json:"effective_at"`
	EvidenceRef string    `json:"evidence_ref"`
}

func (s *Service) DeactivatePolicy(ctx context.Context, in PolicyDeactivation) error {
	if e := core.Require(ctx, platform.AdminService); e != nil {
		return e
	}
	if in.ID.ValidateV7() != nil || in.EffectiveAt.IsZero() || in.EvidenceRef == "" {
		return fail("POLICY_DEACTIVATION_EVIDENCE_REQUIRED")
	}
	p, e := s.Policy(ctx, in.PolicyID)
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, "policy:"+p.Key+":"+p.Jurisdiction+":"+p.ContractType+":"+p.Product); e != nil {
		return e
	}
	var prior PolicyDeactivation
	e = load(ctx, tx, `SELECT snapshot FROM policy_deactivations WHERE decision_id=$1`, in.ID, &prior)
	if e == nil {
		if core.Fingerprint(prior) != core.Fingerprint(in) {
			return core.Fail(409, "IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	_, e = tx.Exec(ctx, `INSERT INTO policy_deactivations(decision_id,policy_id,effective_at,evidence_ref,snapshot) VALUES($1,$2,$3,$4,$5)`, in.ID, in.PolicyID, in.EffectiveAt, in.EvidenceRef, raw(in))
	if e != nil {
		return e
	}
	if e = s.Audit(ctx, tx, "policy.deactivated", in.PolicyID.String(), in); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
