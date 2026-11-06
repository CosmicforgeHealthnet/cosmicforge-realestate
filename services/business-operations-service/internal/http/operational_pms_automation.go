package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type PMSDayAutomationCommand struct {
	PropertyID               ids.ID    `json:"property_id"`
	ExpectedVersion          int64     `json:"expected_version"`
	Mode                     string    `json:"mode"`
	HospitalityPolicyVersion int64     `json:"hospitality_policy_version"`
	EffectiveAt              time.Time `json:"effective_at"`
	ExpiresAt                time.Time `json:"expires_at"`
	MaxDaysPerRun            int       `json:"max_days_per_run"`
	ApprovalID               ids.ID    `json:"approval_id,omitempty"`
	Reason                   string    `json:"reason"`
}
type PMSDayAutomationPolicy struct {
	ID                       ids.ID    `json:"id"`
	OrganisationID           ids.ID    `json:"organisation_id"`
	PropertyID               ids.ID    `json:"property_id"`
	Version                  int64     `json:"version"`
	Mode                     string    `json:"mode"`
	HospitalityPolicyVersion int64     `json:"hospitality_policy_version"`
	Timezone                 string    `json:"timezone"`
	EffectiveAt              time.Time `json:"effective_at"`
	ExpiresAt                time.Time `json:"expires_at"`
	MaxDaysPerRun            int       `json:"max_days_per_run"`
	AuthorizingMembershipID  ids.ID    `json:"authorizing_membership_id"`
	AuthorizingRoleID        ids.ID    `json:"authorizing_role_id"`
	ApprovalID               *ids.ID   `json:"approval_id,omitempty"`
	Reason                   string    `json:"reason"`
	CreatedBy                ids.ID    `json:"created_by"`
	CreatedAt                time.Time `json:"created_at"`
}
type PMSDayAutomationRevoked struct {
	ID       ids.ID `json:"id"`
	PolicyID ids.ID `json:"policy_id"`
	State    string `json:"state"`
}

func (h *Handler) pmsDayAutomationPolicies(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "night_audit:read", r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p)||jsonb_build_object('recorded_state',CASE WHEN EXISTS(SELECT 1 FROM hospitality_day_automation_policies later WHERE later.organisation_id=p.organisation_id AND later.property_id=p.property_id AND later.version>p.version) THEN 'SUPERSEDED' WHEN EXISTS(SELECT 1 FROM hospitality_day_automation_revocations rev WHERE rev.organisation_id=p.organisation_id AND rev.policy_id=p.id) THEN 'REVOKED' WHEN p.mode='MANUAL' THEN 'MANUAL' WHEN p.expires_at<=$5 THEN 'EXPIRED' WHEN p.effective_at>$5 THEN 'NOT_YET_EFFECTIVE' ELSE 'RECORDED' END,'source_review',CASE WHEN NOT EXISTS(SELECT 1 FROM hospitality_day_automation_policies later WHERE later.organisation_id=p.organisation_id AND later.property_id=p.property_id AND later.version>p.version) THEN (SELECT jsonb_build_object('state',c.state,'consecutive_failures',c.consecutive_failures,'last_error_code',c.last_error_code,'next_check_at',c.next_check_at) FROM hospitality_day_automation_cursors c WHERE c.organisation_id=p.organisation_id AND c.property_id=p.property_id) ELSE NULL END) FROM hospitality_day_automation_policies p WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset, h.Auth.Service.Now().UTC())
	if items == nil {
		items = []json.RawMessage{}
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) pmsDayAutomationRuns(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "hospitality_day_automation_runs", "night_audit:read")
}
func (h *Handler) pmsDayAutomationPolicy(w http.ResponseWriter, r *http.Request) {
	var in PMSDayAutomationCommand
	if !decode(w, r, &in) {
		return
	}
	now := h.Auth.Service.Now().UTC()
	if in.PropertyID.ValidateV7() != nil || in.ExpectedVersion < 0 || in.HospitalityPolicyVersion < 0 || !validStaffReason(in.Reason) || (in.Mode != "MANUAL" && in.Mode != "AUDIT_ONLY" && in.Mode != "CLOSE_IF_CLEAR") || in.MaxDaysPerRun < 1 || in.MaxDaysPerRun > 7 || in.EffectiveAt.Before(now.Add(-5*time.Minute)) || in.EffectiveAt.After(now.Add(24*time.Hour)) || !in.ExpiresAt.After(in.EffectiveAt) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) || in.ExpiresAt.After(in.EffectiveAt.Add(7*24*time.Hour)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Property, current versions, supported mode, bounded one-to-seven-day policy, batch bound and reason required."))
		return
	}
	h.execute(w, r, "pms:day-automation:policy:"+in.PropertyID.String(), in, "night_audit:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "night_audit:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) {
			return 0, nil, apperrors.Conflict("AUTOMATION_POLICY_EXPIRED", "A current bounded automation policy is required.")
		}
		var current, cutoffVersion int64
		var zone string
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM hospitality_day_automation_policies WHERE organisation_id=$1 AND property_id=$2`, t.Organisation, in.PropertyID).Scan(&current); e != nil {
			return 0, nil, e
		}
		if e = tx.QueryRow(ctx, `SELECT n.timezone,coalesce((SELECT max(version) FROM hospitality_policies p WHERE p.organisation_id=n.organisation_id AND p.property_id=n.id),0) FROM inventory_nodes n WHERE n.organisation_id=$1 AND n.id=$2 AND n.kind='PROPERTY' AND n.state='ACTIVE'`, t.Organisation, in.PropertyID).Scan(&zone, &cutoffVersion); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion || cutoffVersion != in.HospitalityPolicyVersion {
			return 0, nil, apperrors.Conflict("AUTOMATION_POLICY_VERSION", "Automation or business-day cutoff policy changed.")
		}
		var approval any
		if in.Mode != "MANUAL" {
			if t.Direct.ExpiresAt != nil && in.ExpiresAt.After(*t.Direct.ExpiresAt) {
				return 0, nil, apperrors.PermissionDenied("Automation cannot outlive its direct membership.")
			}
			if e = workforce.Authorize(t.Direct, workforce.DecisionInput{AccountID: t.Account, Target: target, Capability: "night_audit:write", Now: now, MFAAt: t.MFAAt, StepUpWindow: 5 * time.Minute}); e != nil {
				return 0, nil, apperrors.PermissionDenied("Direct current authority is required to authorize a worker.")
			}
			// A sessionless worker cannot satisfy a per-action step-up grant. Check the
			// same direct authority without borrowing the enrolling session's MFA.
			if e = workforce.Authorize(t.Direct, workforce.DecisionInput{AccountID: t.Account, Target: target, Capability: "night_audit:write", Now: now}); e != nil {
				return 0, nil, apperrors.PermissionDenied("Per-action MFA grants cannot authorize unattended execution.")
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_DAY_AUTOMATION", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Manual mode does not consume an automation approval.")
		}
		id := ids.New()
		_, e = tx.Exec(ctx, `INSERT INTO hospitality_day_automation_policies(id,organisation_id,property_id,version,mode,hospitality_policy_version,timezone,effective_at,expires_at,max_days_per_run,authorizing_membership_id,authorizing_role_id,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, t.Organisation, in.PropertyID, current+1, in.Mode, cutoffVersion, zone, in.EffectiveAt, in.ExpiresAt, in.MaxDaysPerRun, t.Member, t.Role, approval, in.Reason, t.Account)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM hospitality_day_automation_cursors WHERE organisation_id=$1 AND property_id=$2`, t.Organisation, in.PropertyID); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "hospitality_day_automation_policy", "DAY_AUTOMATION_POLICY_RECORDED", "hospitality.day.automation.updated.v1", 1, map[string]any{"version": current + 1, "mode": in.Mode, "approval_id": approval, "command": in}); e != nil {
			return 0, nil, e
		}
		value, e := hospitality.Load[PMSDayAutomationPolicy](ctx, tx, `SELECT to_jsonb(p) FROM hospitality_day_automation_policies p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		return 201, value, e
	})
}
func (h *Handler) pmsDayAutomationRevoke(w http.ResponseWriter, r *http.Request) {
	var in PMSVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current immutable policy version and reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	p, e := hospitality.Load[PMSDayAutomationPolicy](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM hospitality_day_automation_policies p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:day-automation:revoke:"+id.String(), in, "night_audit:write", "PROPERTY", p.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "night_audit:write", "PROPERTY", p.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if p.Version != in.Version {
			return 0, nil, apperrors.Conflict("AUTOMATION_POLICY_VERSION", "Exact immutable policy version required.")
		}
		rid := ids.New()
		_, e = tx.Exec(ctx, `INSERT INTO hospitality_day_automation_revocations(id,organisation_id,property_id,policy_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6)`, rid, t.Organisation, p.PropertyID, id, in.Reason, t.Account)
		if e != nil {
			return 0, nil, e
		}
		e = h.pmsCommit(ctx, tx, t, p.PropertyID, rid, "hospitality_day_automation_revocation", "DAY_AUTOMATION_REVOKED", "hospitality.day.automation.updated.v1", 1, map[string]any{"policy_id": id, "reason": in.Reason})
		return 201, PMSDayAutomationRevoked{ID: rid, PolicyID: id, State: "REVOKED"}, e
	})
}

// Keeping the typed policy loader in this composition reuses the existing
// owning transaction and authorization boundaries; no second scheduler exists.
func loadDayAutomation(ctx context.Context, q database.Querier, org, property ids.ID) (PMSDayAutomationPolicy, error) {
	var out PMSDayAutomationPolicy
	raw, e := db.New().GetLatestHospitalityDayAutomation(ctx, q, db.GetLatestHospitalityDayAutomationParams{OrganisationID: org, PropertyID: property})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, apperrors.NotFound("DAY_AUTOMATION_POLICY_NOT_FOUND", "Policy not found.")
	}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(raw, &out)
	return out, e
}
