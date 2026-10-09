package usecases

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/schema"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Jurisdiction legal policy (§12 policy configuration). Timelines and
// automatic decisions with legal effect come from a policy version approved
// for the property's jurisdiction. Without one, the operational defaults
// below apply to timers only, and every consequential automatic decision is
// disabled in favour of explicit manual review.

// LegalRules are the jurisdiction rules a legal approval covers.
type LegalRules struct {
	DamageClaimResponseHours   int32 `json:"damage_claim_response_hours" v:"required,min=24,max=720" doc:"Guest response window for a damage claim; silence never accepts a claim"`
	DepositResponseDays        int32 `json:"deposit_response_days" v:"required,min=1,max=90" doc:"Tenant response window for proposed deposit deductions"`
	DepositReleaseDaysMax      int32 `json:"deposit_release_days_max" v:"required,min=1,max=180" doc:"Latest deposit settlement deadline after move-out an agreement may set"`
	DepositSilenceAcceptance   bool  `json:"deposit_silence_acceptance" doc:"Tenant silence by the deadline accepts the proposed deductions (otherwise they go to manual review)"`
	AutomaticLateFees          bool  `json:"automatic_late_fees" doc:"Agreed late fees are charged automatically when rent becomes overdue (otherwise the owner assesses them)"`
	MaxLateFeeBps              int64 `json:"max_late_fee_bps" v:"min=0,max=5000" doc:"Late fee cap as basis points of the periodic rent; 0 forbids late fees"`
	ShortletApprovalHours      int32 `json:"shortlet_approval_hours" v:"required,min=1,max=72" doc:"How long a booking request waits for the owner's approval"`
	ShortletPaymentHoldMinutes int32 `json:"shortlet_payment_hold_minutes" v:"required,min=5,max=1440" doc:"How long dates are held for the guest's payment"`
}

func (r LegalRules) paymentHold() time.Duration {
	return time.Duration(r.ShortletPaymentHoldMinutes) * time.Minute
}

// LegalPolicy is the policy in force for a property.
type LegalPolicy struct {
	ID       *ids.ID    `json:"id"`
	Country  string     `json:"country"`
	Region   *string    `json:"region"`
	Version  int32      `json:"version_no"`
	Approved bool       `json:"approved" doc:"false: no legally approved policy for the jurisdiction; consequential automatic decisions are disabled"`
	Rules    LegalRules `json:"rules"`
}

// auditRef identifies the policy a decision applied.
func (p LegalPolicy) auditRef() map[string]any {
	if !p.Approved {
		return map[string]any{"legal_policy": "NONE_APPROVED", "jurisdiction": p.Country}
	}
	return map[string]any{"legal_policy": "APPROVED", "legal_policy_id": p.ID.String(), "legal_policy_version": p.Version, "jurisdiction": p.Country}
}

// unapprovedRules are the operational timers used without an approved
// policy. Every consequential automatic decision is off.
func (s *Service) unapprovedRules() LegalRules {
	d := s.Policy.Domain
	return LegalRules{DamageClaimResponseHours: int32(d.DamageResponseWindow / time.Hour), DepositResponseDays: int32(d.DepositResponseWindow / (24 * time.Hour)),
		DepositReleaseDaysMax: 180, MaxLateFeeBps: d.MaxLateFeeBps,
		ShortletApprovalHours: int32(d.ShortletApprovalTTL / time.Hour), ShortletPaymentHoldMinutes: int32(d.ShortletPaymentHold / time.Minute)}
}

// legalPolicy resolves the approved policy for a property's jurisdiction:
// the region-specific version wins over the country-wide one.
func (s *Service) legalPolicy(ctx context.Context, propertyID ids.ID) (LegalPolicy, error) {
	addr, err := s.q.GetPropertyAddress(ctx, s.dbq(ctx), propertyID)
	if err != nil {
		return LegalPolicy{}, err
	}
	jp, err := s.q.EffectiveJurisdictionPolicy(ctx, s.dbq(ctx), db.EffectiveJurisdictionPolicyParams{Country: addr.CountryCode, Region: strings.TrimSpace(addr.StateRegion)})
	if isNoRows(err) {
		return LegalPolicy{Country: addr.CountryCode, Rules: s.unapprovedRules()}, nil
	}
	if err != nil {
		return LegalPolicy{}, err
	}
	var r LegalRules
	if err := json.Unmarshal(jp.Rules, &r); err != nil {
		return LegalPolicy{}, err
	}
	return LegalPolicy{ID: &jp.ID, Country: jp.Country, Region: jp.Region, Version: jp.VersionNo, Approved: true, Rules: r}, nil
}

// GetLegalPolicy shows the owner which policy governs a property.
func (s *Service) GetLegalPolicy(ctx context.Context, subj authz.Subject, propertyID ids.ID) (LegalPolicy, error) {
	if _, err := s.authorize(ctx, subj, authz.Read(authz.CapPropertyView), propertyID); err != nil {
		return LegalPolicy{}, err
	}
	return s.legalPolicy(ctx, propertyID)
}

// JurisdictionPolicyView is a policy version.
type JurisdictionPolicyView struct {
	ID               ids.ID     `json:"id"`
	Country          string     `json:"country"`
	Region           *string    `json:"region"`
	VersionNo        int32      `json:"version_no"`
	Status           string     `json:"status"`
	Rules            LegalRules `json:"rules"`
	Summary          string     `json:"summary"`
	LegalApprovalRef *string    `json:"legal_approval_ref"`
	ApprovedAt       *time.Time `json:"approved_at"`
	RetiredAt        *time.Time `json:"retired_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

func jurisdictionPolicyView(p db.JurisdictionPolicy) JurisdictionPolicyView {
	v := JurisdictionPolicyView{ID: p.ID, Country: p.Country, Region: p.Region, VersionNo: p.VersionNo, Status: p.Status, Summary: p.Summary,
		LegalApprovalRef: p.LegalApprovalRef, ApprovedAt: p.ApprovedAt, RetiredAt: p.RetiredAt, CreatedAt: p.CreatedAt}
	_ = json.Unmarshal(p.Rules, &v.Rules)
	return v
}

// JurisdictionPolicyInput drafts the next policy version for a jurisdiction.
type JurisdictionPolicyInput struct {
	Country string     `json:"country" v:"required,format=country"`
	Region  *string    `json:"region,omitempty" v:"min=1,max=100" doc:"State or region as recorded on property addresses; omit for the whole country"`
	Summary string     `json:"summary" v:"required,min=3,max=500"`
	Rules   LegalRules `json:"rules"`
}

// AdminDraftJurisdictionPolicy records a DRAFT version. Drafts have no effect.
func (s *Service) AdminDraftJurisdictionPolicy(ctx context.Context, subj authz.Subject, in JurisdictionPolicyInput) (JurisdictionPolicyView, error) {
	if err := schema.Validate(in); err != nil {
		return JurisdictionPolicyView{}, err
	}
	var out JurisdictionPolicyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminTemplates, authz.Resource{}); err != nil {
			return err
		}
		_, actor, err := s.adminActorOf(ctx)
		if err != nil {
			return err
		}
		var region *string
		if in.Region != nil {
			region = ptr(upper(strings.TrimSpace(*in.Region)))
		}
		p, err := s.q.InsertJurisdictionPolicy(ctx, mustTx(ctx), db.InsertJurisdictionPolicyParams{ID: ids.New(), Country: in.Country, Region: region,
			Rules: jsonb(in.Rules), Summary: in.Summary, CreatedByActorID: actor, CreatedAt: s.now()})
		if err != nil {
			return err
		}
		out = jurisdictionPolicyView(p)
		return s.record(ctx, "legal_policy.draft", "jurisdiction_policy", p.ID, map[string]any{"country": p.Country, "region": p.Region, "version_no": p.VersionNo}, nil)
	})
	return out, err
}

// PolicyApprovalInput records the legal approval of a draft.
type PolicyApprovalInput struct {
	LegalApprovalRef string `json:"legal_approval_ref" v:"required,min=3,max=200" doc:"Reference of the legal sign-off (opinion, memo or ticket) this approval rests on"`
}

// AdminApproveJurisdictionPolicy approves a draft against a recorded legal
// approval (a second operator: the drafter cannot approve), retiring the
// version it supersedes. Approved rules are immutable.
func (s *Service) AdminApproveJurisdictionPolicy(ctx context.Context, subj authz.Subject, id ids.ID, in PolicyApprovalInput) (JurisdictionPolicyView, error) {
	if err := schema.Validate(in); err != nil {
		return JurisdictionPolicyView{}, err
	}
	var out JurisdictionPolicyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminTemplates, authz.Resource{}); err != nil {
			return err
		}
		_, actor, err := s.adminActorOf(ctx)
		if err != nil {
			return err
		}
		tx, now := mustTx(ctx), s.now()
		p, err := s.q.GetJurisdictionPolicyForUpdate(ctx, tx, id)
		if isNoRows(err) {
			return notFound("jurisdiction_policy")
		}
		if err != nil {
			return err
		}
		if p.Status != "DRAFT" {
			return conflict("POLICY_NOT_DRAFT", "Only drafts can be approved.")
		}
		if p.CreatedByActorID == actor {
			return conflict("SECOND_APPROVER_REQUIRED", "A policy must be approved by an operator other than its drafter.")
		}
		var r LegalRules
		if err := json.Unmarshal(p.Rules, &r); err != nil {
			return err
		}
		if err := schema.Validate(r); err != nil {
			return err
		}
		prev, err := s.q.ApprovedPolicyForUpdate(ctx, tx, db.ApprovedPolicyForUpdateParams{Country: p.Country, Region: p.Region})
		if err == nil {
			if _, err := s.q.RetireJurisdictionPolicy(ctx, tx, db.RetireJurisdictionPolicyParams{ID: prev.ID, RetiredAt: &now}); err != nil {
				return err
			}
			if err := s.record(ctx, "legal_policy.retire", "jurisdiction_policy", prev.ID, map[string]any{"superseded_by": p.ID.String()}, nil); err != nil {
				return err
			}
		} else if !isNoRows(err) {
			return err
		}
		if p, err = s.q.ApproveJurisdictionPolicy(ctx, tx, db.ApproveJurisdictionPolicyParams{ID: id, LegalApprovalRef: &in.LegalApprovalRef,
			ApprovedByActorID: &actor, ApprovedAt: &now}); err != nil {
			return err
		}
		out = jurisdictionPolicyView(p)
		return s.record(ctx, "legal_policy.approve", "jurisdiction_policy", p.ID, map[string]any{"version_no": p.VersionNo, "legal_approval_ref": in.LegalApprovalRef}, nil)
	})
	return out, err
}

// AdminRetireJurisdictionPolicy withdraws an approved version; the
// jurisdiction falls back to manual review until another is approved.
func (s *Service) AdminRetireJurisdictionPolicy(ctx context.Context, subj authz.Subject, id ids.ID) (JurisdictionPolicyView, error) {
	var out JurisdictionPolicyView
	err := s.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminTemplates, authz.Resource{}); err != nil {
			return err
		}
		p, err := s.q.RetireJurisdictionPolicy(ctx, mustTx(ctx), db.RetireJurisdictionPolicyParams{ID: id, RetiredAt: ptr(s.now())})
		if isNoRows(err) {
			return conflict("POLICY_NOT_APPROVED", "Only an approved policy version can be retired.")
		}
		if err != nil {
			return err
		}
		out = jurisdictionPolicyView(p)
		return s.record(ctx, "legal_policy.retire", "jurisdiction_policy", id, nil, nil)
	})
	return out, err
}

// AdminListJurisdictionPolicies lists policy versions.
func (s *Service) AdminListJurisdictionPolicies(ctx context.Context, subj authz.Subject, country string) ([]JurisdictionPolicyView, error) {
	if _, err := s.Authz.Require(ctx, subj, authz.ActInternalAdminTemplates, authz.Resource{}); err != nil {
		return nil, err
	}
	rows, err := s.q.ListJurisdictionPolicies(ctx, s.dbq(ctx), strp(country))
	out := make([]JurisdictionPolicyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, jurisdictionPolicyView(r))
	}
	return out, err
}
