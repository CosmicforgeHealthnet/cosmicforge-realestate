package businesshttp

import (
	"context"
	"net/http"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

type RMSVersionCommand struct {
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}
type RMSDecisionCommand struct {
	Version       int64   `json:"version"`
	Action        string  `json:"action"`
	ApprovalID    ids.ID  `json:"approval_id,omitempty"`
	ReplacementID *ids.ID `json:"replacement_id,omitempty"`
	Reason        string  `json:"reason"`
}

func rmsApprovalOperation(v RMSRecommendation, rollback bool) map[string]any {
	return map[string]any{"recommendation_id": v.ID, "change_digest": v.ChangeDigest, "policy_id": v.PolicyID, "forecast_id": v.ForecastID, "target_plan_version": v.TargetPlanVersion, "rollback": rollback}
}
func (h *Handler) rmsRecommendationDecide(w http.ResponseWriter, r *http.Request) {
	var in RMSDecisionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || in.ReplacementID != nil && in.ReplacementID.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current recommendation version and decision reason required."))
		return
	}
	_, pre, e := h.rmsRecommendationTarget(r, "rms:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:decision:"+pre.ID.String(), in, "rms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := loadRMSRecommendation(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.ChangeDigest != hashCommercialPayload(marshalCommercial(v.Snapshot)) {
			return 0, nil, apperrors.Conflict("RMS_RECOMMENDATION_VERSION", "Recommendation version or retained proposal changed.")
		}
		state := ""
		approval := v.ApprovalID
		applied := v.AppliedVersions
		now := h.Auth.Service.Now().UTC()
		if in.Action != "APPROVE" && in.Action != "ROLLBACK" && !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Approval references may only authorize exact approval/rollback commands.")
		}
		if in.Action != "OVERRIDE" && in.ReplacementID != nil {
			return 0, nil, apperrors.InvalidRequest("A replacement reference belongs only to an override decision.")
		}
		switch in.Action {
		case "SUBMIT":
			if v.State == "DRAFT" && now.Before(v.ExpiresAt) {
				state = "PENDING_APPROVAL"
			}
		case "REJECT":
			if v.State == "DRAFT" || v.State == "PENDING_APPROVAL" || v.State == "APPROVED" {
				state = "REJECTED"
			}
		case "EXPIRE":
			if !now.Before(v.ExpiresAt) && (v.State == "DRAFT" || v.State == "PENDING_APPROVAL" || v.State == "APPROVED") {
				state = "EXPIRED"
			}
		case "OVERRIDE":
			if (v.State == "DRAFT" || v.State == "PENDING_APPROVAL") && in.ReplacementID != nil && *in.ReplacementID != v.ID {
				next, e := loadRMSRecommendation(ctx, tx, t.Organisation, *in.ReplacementID)
				if e != nil {
					return 0, nil, e
				}
				if next.State == "DRAFT" && next.PropertyID == v.PropertyID && next.PoolID == v.PoolID && next.RatePlanID == v.RatePlanID && next.DateFrom == v.DateFrom && next.DateTo == v.DateTo && next.CreatedBy == t.Account && now.Before(next.ExpiresAt) {
					state = "OVERRIDDEN"
				}
			}
		case "APPROVE", "APPLY":
			if in.Action == "APPROVE" && v.State != "PENDING_APPROVAL" || in.Action == "APPLY" && v.State != "APPROVED" {
				break
			}
			if !now.Before(v.ExpiresAt) {
				return 0, nil, apperrors.Conflict("RMS_RECOMMENDATION_EXPIRED", "Recommendation expired.")
			}
			if _, _, e = h.authorize(ctx, tx, "rates:write", "PROPERTY", v.PropertyID, true); e != nil {
				return 0, nil, e
			}
			if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
				return 0, nil, e
			}
			p, e := loadRMSPolicy(ctx, tx, t.Organisation, v.PolicyID)
			if e != nil {
				return 0, nil, e
			}
			if e = h.rmsCurrentPolicy(ctx, tx, t, p); e != nil {
				return 0, nil, e
			}
			f, e := loadRMSForecast(ctx, tx, t.Organisation, v.ForecastID)
			if e != nil {
				return 0, nil, e
			}
			if e = h.rmsForecastUsable(ctx, tx, t, p, f); e != nil {
				return 0, nil, e
			}
			if e = h.rmsValidateCurrent(ctx, tx, t, p, v); e != nil {
				return 0, nil, e
			}
			exposure := rmsExposure(v, false)
			if in.Action == "APPROVE" {
				if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RMS_RATE_CHANGE", target.Scope, in.ApprovalID, rmsApprovalOperation(v, false), "", &exposure, p.Definition.Currency); e != nil {
					return 0, nil, e
				}
				approval = &in.ApprovalID
				state = "APPROVED"
			} else {
				if approval == nil {
					return 0, nil, apperrors.PermissionDenied("Completed independent approval required.")
				}
				var usable bool
				if e = tx.QueryRow(ctx, `SELECT state='APPROVED' AND consumed_by_account_id=$3 AND requester_id=$3 AND deadline>$4 AND action='COMMERCIAL_RMS_RATE_CHANGE' FROM approval_requests WHERE organisation_id=$1 AND id=$2`, t.Organisation, *approval, t.Account, now).Scan(&usable); e != nil {
					return 0, nil, e
				}
				if !usable {
					return 0, nil, apperrors.PermissionDenied("Original approval/session actor is no longer authorized for application.")
				}
				applied, e = h.rmsWriteSegments(ctx, tx, t, v, *approval, false)
				if e != nil {
					return 0, nil, e
				}
				state = "APPLIED"
			}
		case "ROLLBACK":
			if v.State != "APPLIED" {
				break
			}
			if _, _, e = h.authorize(ctx, tx, "rates:write", "PROPERTY", v.PropertyID, true); e != nil {
				return 0, nil, e
			}
			if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
				return 0, nil, e
			}
			if e = h.rmsRollbackCurrent(ctx, tx, t, v); e != nil {
				return 0, nil, e
			}
			exposure := rmsExposure(v, true)
			p, e := loadRMSPolicy(ctx, tx, t.Organisation, v.PolicyID)
			if e != nil {
				return 0, nil, e
			}
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RMS_RATE_ROLLBACK", target.Scope, in.ApprovalID, rmsApprovalOperation(v, true), "", &exposure, p.Definition.Currency); e != nil {
				return 0, nil, e
			}
			if _, e = h.rmsWriteSegments(ctx, tx, t, v, in.ApprovalID, true); e != nil {
				return 0, nil, e
			}
			approval = &in.ApprovalID
			state = "ROLLED_BACK"
		default:
			return 0, nil, apperrors.InvalidRequest("Unsupported RMS decision action.")
		}
		if state == "" {
			return 0, nil, apperrors.Conflict("RMS_DECISION_STATE", "Decision is invalid for the current state, version or expiry.")
		}
		if e = h.rmsRecordDecision(ctx, tx, t, v, state, in.Action, approval, applied, in.Reason, map[string]any{"replacement_id": in.ReplacementID}); e != nil {
			return 0, nil, e
		}
		out, e := loadRMSRecommendation(ctx, tx, t.Organisation, v.ID)
		return 200, out, e
	})
}

func rmsExposure(v RMSRecommendation, rollback bool) int64 {
	var amount int64
	for _, s := range v.Snapshot.Segments {
		d := s.Proposed
		if rollback {
			d = s.Previous.Definition
		}
		amount = max(amount, d.CeilingMinor)
	}
	return amount
}
func (h *Handler) rmsValidateCurrent(ctx context.Context, q database.Querier, t tenant, p RMSPolicy, v RMSRecommendation) error {
	plan, e := revenue.GetPlan(ctx, q, t.Organisation, v.RatePlanID)
	if e != nil {
		return e
	}
	if plan.State != "ACTIVE" || plan.Version != v.TargetPlanVersion {
		return apperrors.Conflict("RMS_RATE_VERSION_CHANGED", "A target canonical rate changed after recommendation creation.")
	}
	pool, e := inventory.GetPool(ctx, q, t.Organisation, v.PoolID)
	if e != nil {
		return e
	}
	if e = inventory.CheckStockAuthority(ctx, q, pool); e != nil {
		return e
	}
	property, e := portfolio.Get(ctx, q, t.Organisation, v.PropertyID)
	if e != nil {
		return e
	}
	days, e := inventory.Calendar(ctx, q, t.Organisation, pool.ID, inventory.DateRange{DateFrom: v.DateFrom, DateTo: v.DateTo}, property.Timezone, "", h.Auth.Service.Now().UTC(), false)
	if e != nil {
		return e
	}
	if len(days) != len(v.Snapshot.Segments) {
		return apperrors.Conflict("RMS_CALENDAR_CHANGED", "Exact observed calendar required.")
	}
	for i, s := range v.Snapshot.Segments {
		if days[i].Day != s.DateFrom || days[i].Version != s.CalendarVersion || days[i].Overbooked > 0 {
			return apperrors.Conflict("RMS_CALENDAR_CHANGED", "Observed calendar changed or has unresolved excess demand.")
		}
		current, e := revenue.VersionForDay(ctx, q, t.Organisation, v.RatePlanID, s.DateFrom)
		if e != nil {
			return e
		}
		if current.ID != s.Previous.ID {
			return apperrors.Conflict("RMS_RATE_VERSION_CHANGED", "An effective canonical rate changed.")
		}
		if e = h.rmsMovementBudget(ctx, q, t, p, s); e != nil {
			return e
		}
	}
	return nil
}
func (h *Handler) rmsRollbackCurrent(ctx context.Context, q database.Querier, t tenant, v RMSRecommendation) error {
	plan, e := revenue.GetPlan(ctx, q, t.Organisation, v.RatePlanID)
	if e != nil {
		return e
	}
	if plan.Version != v.TargetPlanVersion+1 || plan.State != "ACTIVE" || len(v.AppliedVersions) != len(v.Snapshot.Segments) {
		return apperrors.Conflict("RMS_ROLLBACK_INTERVENING_CHANGE", "Rollback cannot overwrite an intervening canonical change.")
	}
	for i, s := range v.Snapshot.Segments {
		current, e := revenue.VersionForDay(ctx, q, t.Organisation, v.RatePlanID, s.DateFrom)
		if e != nil {
			return e
		}
		if current.ID != v.AppliedVersions[i] {
			return apperrors.Conflict("RMS_ROLLBACK_INTERVENING_CHANGE", "Applied version is no longer current.")
		}
	}
	return nil
}
func (h *Handler) rmsWriteSegments(ctx context.Context, tx pgx.Tx, t tenant, v RMSRecommendation, approval ids.ID, rollback bool) ([]ids.ID, error) {
	idsOut := []ids.ID{}
	for _, s := range v.Snapshot.Segments {
		d := s.Proposed
		if rollback {
			d = s.Previous.Definition
		}
		written, e := h.writeRateVersion(ctx, tx, t, v.PropertyID, v.RatePlanID, d, s.DateFrom, s.DateTo, "RMS exact recommendation "+v.ID.String(), approval)
		if e != nil {
			return nil, e
		}
		idsOut = append(idsOut, written.ID)
	}
	if _, e := tx.Exec(ctx, `UPDATE rate_plans SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.RatePlanID); e != nil {
		return nil, e
	}
	plan, e := revenue.GetPlan(ctx, tx, t.Organisation, v.RatePlanID)
	if e != nil {
		return nil, e
	}
	if e = h.commercialCommit(ctx, tx, t, v.PropertyID, v.RatePlanID, "rate_plan", "RMS_CANONICAL_RATE_APPLIED", businessEvents.RatePlanChanged, plan.Version, map[string]any{"recommendation_id": v.ID, "change_digest": v.ChangeDigest, "approval_id": approval, "rollback": rollback, "rate_version_ids": idsOut, "external_delivery_confirmed": false}); e != nil {
		return nil, e
	}
	return idsOut, nil
}
func (h *Handler) rmsRecordDecision(ctx context.Context, tx pgx.Tx, t tenant, v RMSRecommendation, state, action string, approval *ids.ID, applied []ids.ID, reason string, evidence any) error {
	var actor any
	if !t.Account.IsZero() {
		actor = t.Account
	}
	if _, e := tx.Exec(ctx, `INSERT INTO pricing_decisions(id,organisation_id,property_id,recommendation_id,previous_version,prior_state,state,action,actor_id,approval_id,reason,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, ids.New(), t.Organisation, v.PropertyID, v.ID, v.Version, v.State, state, action, actor, approval, reason, marshalCommercial(evidence)); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `UPDATE pricing_recommendations SET state=$3,approval_id=$4,applied_versions=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, state, approval, marshalCommercial(applied)); e != nil {
		return e
	}
	snapshot := map[string]any{"state": state, "prior_state": v.State, "action": action, "reason": reason, "change_digest": v.ChangeDigest, "approval_id": approval, "evidence": evidence}
	if e := h.commercialCommit(ctx, tx, t, v.PropertyID, v.ID, "pricing_recommendation", "RMS_RECOMMENDATION_DECIDED", "revenue.recommendation.changed.v1", v.Version+1, snapshot); e != nil {
		return e
	}
	if state == "APPLIED" {
		return businessEvents.PublishCommercial(ctx, tx, h.Auth.Service.Outbox, t.Organisation, v.PropertyID, v.ID, "pricing_recommendation", "RMS_RECOMMENDATION_DECIDED", "pricing.applied.v1", v.Version+1, snapshot)
	}
	return nil
}

// Count absolute canonical movements, including manual changes and reversals,
// against both budgets. Repeated up/down commands cannot reset a net-change cap.
func (h *Handler) rmsMovementBudget(ctx context.Context, q database.Querier, t tenant, p RMSPolicy, s revenue.PricingSegment) error {
	rows, e := q.Query(ctx, `SELECT amount_minor,created_at,definition->'parent_version_id',currency FROM rate_plan_versions WHERE organisation_id=$1 AND rate_plan_id=$2 AND effective_from<=$3::date AND effective_to>$3::date ORDER BY version`, t.Organisation, p.RatePlanID, s.DateFrom)
	if e != nil {
		return e
	}
	defer rows.Close()
	type observation struct {
		amount int64
		at     time.Time
	}
	observations := []observation{}
	for rows.Next() {
		var o observation
		var parent []byte
		var currency string
		if e = rows.Scan(&o.amount, &o.at, &parent, &currency); e != nil {
			return e
		}
		if currency != p.Definition.Currency {
			return apperrors.Conflict("RMS_MOVEMENT_CURRENCY", "Mixed-currency rate history cannot authorize a movement budget.")
		}
		if string(parent) != "" && string(parent) != "null" {
			return apperrors.Conflict("RMS_MOVEMENT_UNKNOWN", "Derived rate history requires an authoritative movement adapter.")
		}
		observations = append(observations, o)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if len(observations) == 0 {
		return apperrors.Conflict("RMS_RATE_HISTORY_MISSING", "Canonical rate history required.")
	}
	for _, window := range []struct {
		duration time.Duration
		limit    int64
	}{{24 * time.Hour, p.Definition.MaxDailyChangeBPS}, {time.Duration(p.Definition.CumulativeDays) * 24 * time.Hour, p.Definition.MaxCumulativeChangeBPS}} {
		cutoff := h.Auth.Service.Now().UTC().Add(-window.duration)
		anchor := observations[0].amount
		var movement int64
		for i, o := range observations {
			if o.at.Before(cutoff) {
				anchor = o.amount
				continue
			}
			if i > 0 {
				delta := o.amount - observations[i-1].amount
				if delta < 0 {
					delta = -delta
				}
				if delta > 1000000000000000 || movement > 1000000000000000-delta {
					return apperrors.Conflict("RMS_MOVEMENT_LIMIT", "Rate movement exceeds bounded policy arithmetic.")
				}
				movement += delta
			}
		}
		delta := s.Proposed.AmountMinor - s.Previous.AmountMinor
		if delta < 0 {
			delta = -delta
		}
		budget, e := revenue.Ratio(anchor, window.limit, 10000)
		if e != nil {
			return e
		}
		if delta > budget || movement > budget-delta {
			return apperrors.Conflict("RMS_MOVEMENT_LIMIT", "Daily or cumulative absolute canonical rate movement exceeds policy.")
		}
	}
	return nil
}
