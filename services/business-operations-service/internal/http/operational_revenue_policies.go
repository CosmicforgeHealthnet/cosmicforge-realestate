package businesshttp

import (
	"context"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type RMSPolicy struct {
	ID         ids.ID                `json:"id"`
	PropertyID ids.ID                `json:"property_id"`
	BranchID   ids.ID                `json:"branch_id"`
	PoolID     ids.ID                `json:"pool_id"`
	RatePlanID ids.ID                `json:"rate_plan_id"`
	Version    int64                 `json:"version"`
	State      string                `json:"state"`
	Definition revenue.PricingPolicy `json:"definition"`
}
type RMSPolicyCommand struct {
	PropertyID      ids.ID                `json:"property_id"`
	PoolID          ids.ID                `json:"pool_id"`
	RatePlanID      ids.ID                `json:"rate_plan_id"`
	ExpectedVersion int64                 `json:"expected_version"`
	State           string                `json:"state"`
	Definition      revenue.PricingPolicy `json:"definition"`
	ApprovalID      ids.ID                `json:"approval_id,omitempty"`
	Reason          string                `json:"reason"`
}

func (h *Handler) registerRMSPolicies(r chi.Router) {
	r.Get("/revenue/policies", h.rmsPolicyList)
	r.Post("/revenue/policies", h.rmsPolicySave)
	r.Get("/revenue/policies/{id}", h.rmsPolicyGet)
	h.registerRMSRecommendations(r)
}
func loadRMSPolicy(ctx context.Context, q database.Querier, org, id ids.ID) (RMSPolicy, error) {
	raw, e := db.New().GetRMSPolicy(ctx, q, db.GetRMSPolicyParams{OrganisationID: org, ID: id})
	return decodeRMSRecord[RMSPolicy](raw, e)
}
func (h *Handler) rmsPolicyList(w http.ResponseWriter, r *http.Request) {
	h.rmsScopedList(w, r, "rms_policies")
}
func (h *Handler) rmsScopedList(w http.ResponseWriter, r *http.Request, table string) {
	if table != "rms_policies" && table != "pricing_recommendations" && table != "pricing_simulations" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Unsupported RMS collection."))
		return
	}
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
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "rms:read", "", "")
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM `+table+` p WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) rmsPolicyGet(w http.ResponseWriter, r *http.Request) {
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
	v, e := loadRMSPolicy(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "rms:read", "PROPERTY", v.PropertyID, false)
	}
	reply(w, r, 200, v, e)
}
func (h *Handler) rmsPolicySave(w http.ResponseWriter, r *http.Request) {
	var in RMSPolicyCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.PoolID.ValidateV7() != nil || in.RatePlanID.ValidateV7() != nil || in.ExpectedVersion < 0 || in.State != "ACTIVE" && in.State != "PAUSED" || in.Definition.Validate() != nil || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Explicit valid bounded RMS policy, scope, version, state and reason required."))
		return
	}
	if in.State == "ACTIVE" && (in.Definition.Mode == "APPROVAL_AUTOMATION" || in.Definition.Mode == "AUTOPILOT_WITH_LIMITS") {
		reply(w, r, 0, nil, apperrors.Unavailable("Automatic mode activation is unavailable. Save a paused configuration or use MANUAL/RECOMMEND_ONLY."))
		return
	}
	h.execute(w, r, "revenue:policy:"+in.RatePlanID.String(), in, "rms:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		p, e := portfolio.Get(ctx, tx, t.Organisation, in.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		pool, e := inventory.GetPool(ctx, tx, t.Organisation, in.PoolID)
		if e != nil {
			return 0, nil, e
		}
		plan, e := revenue.GetPlan(ctx, tx, t.Organisation, in.RatePlanID)
		if e != nil {
			return 0, nil, e
		}
		if pool.PropertyID != p.ID || plan.PropertyID != p.ID || in.Definition.Currency != p.Currency {
			return 0, nil, apperrors.PermissionDenied("RMS scope and currency must match canonical records.")
		}
		var current int64
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM rms_policies WHERE organisation_id=$1 AND rate_plan_id=$2`, t.Organisation, plan.ID).Scan(&current); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion {
			return 0, nil, apperrors.Conflict("RMS_POLICY_VERSION", "RMS policy changed.")
		}
		if in.State == "ACTIVE" {
			if e = h.commercialGate(ctx, tx, t, p.ID); e != nil {
				return 0, nil, e
			}
			now := h.Auth.Service.Now().UTC()
			if !now.Before(in.Definition.ExpiresAt) || in.Definition.EffectiveAt.After(now.Add(24*time.Hour)) {
				return 0, nil, apperrors.InvalidRequest("Policy authorization must be current or begin within 24 hours.")
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RMS_POLICY", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", nil, ""); e != nil {
				return 0, nil, e
			}
			if in.Definition.Mode == "AUTOPILOT_WITH_LIMITS" {
				if _, _, e = h.authorize(ctx, tx, "rms:autopilot", "PROPERTY", p.ID, true); e != nil {
					return 0, nil, e
				}
			}
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Pausing does not consume an activation approval.")
		}
		id := ids.New()
		var approval any
		if !in.ApprovalID.IsZero() {
			approval = in.ApprovalID
		}
		if _, e = tx.Exec(ctx, `INSERT INTO rms_policies(id,organisation_id,property_id,branch_id,pool_id,rate_plan_id,version,state,definition,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, t.Organisation, p.ID, p.BranchID, pool.ID, plan.ID, current+1, in.State, marshalCommercial(in.Definition), approval, in.Reason, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, p.ID, id, "rms_policy", "RMS_POLICY_VERSION_CREATED", "revenue.policy.changed.v1", current+1, map[string]any{"policy_id": id, "rate_plan_id": plan.ID, "state": in.State, "mode": in.Definition.Mode, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		out, e := loadRMSPolicy(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}
