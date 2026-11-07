package businesshttp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type RMSAdvancedCommand struct {
	PolicyID    ids.ID                     `json:"policy_id"`
	ForecastID  ids.ID                     `json:"forecast_id"`
	AnalyticsID *ids.ID                    `json:"analytics_id,omitempty"`
	Kind        string                     `json:"kind"`
	Parameters  revenue.AdvancedParameters `json:"parameters"`
	Reason      string                     `json:"reason"`
}
type RMSAdvancedRecommendation struct {
	ID                ids.ID              `json:"id"`
	PropertyID        ids.ID              `json:"property_id"`
	PoolID            ids.ID              `json:"pool_id"`
	RatePlanID        ids.ID              `json:"rate_plan_id"`
	PolicyID          ids.ID              `json:"policy_id"`
	ForecastID        ids.ID              `json:"forecast_id"`
	AnalyticsID       *ids.ID             `json:"analytics_id,omitempty"`
	Kind              string              `json:"kind"`
	Application       string              `json:"application"`
	AdvisoryReason    string              `json:"advisory_reason"`
	DateFrom          string              `json:"date_from"`
	DateTo            string              `json:"date_to"`
	TargetPlanVersion int64               `json:"target_plan_version"`
	ChangeDigest      string              `json:"change_digest"`
	Proposal          RMSAdvancedProposal `json:"proposal"`
	ExpiresAt         time.Time           `json:"expires_at"`
	State             string              `json:"state"`
	ApprovalID        *ids.ID             `json:"approval_id,omitempty"`
	AppliedReference  map[string]any      `json:"applied_reference"`
	Version           int64               `json:"version"`
}
type RMSAdvancedProposal struct {
	Recommendation    revenue.AdvancedProposal `json:"recommendation"`
	PreviousVersionID ids.ID                   `json:"previous_version_id"`
	Reason            string                   `json:"reason"`
}
type RMSAdvancedDecisionCommand struct {
	Version    int64  `json:"version"`
	Action     string `json:"action"`
	ApprovalID ids.ID `json:"approval_id,omitempty"`
	Reason     string `json:"reason"`
}

var rmsAdvancedKinds = []string{"DERIVED_RATE", "RATE_RANGE", "MIN_LOS", "MAX_LOS", "BOOKING_WINDOW", "EARLY_BOOKER", "LAST_MINUTE", "LONG_STAY", "CHANNEL_ALLOCATION", "CHANNEL_PRICING", "OVERBOOKING"}

func (h *Handler) registerRMSAdvanced(r chi.Router) {
	r.Get("/revenue/advanced-recommendations", h.rmsAdvancedList)
	r.Post("/revenue/advanced-recommendations", h.rmsAdvancedCreate)
	r.Get("/revenue/advanced-recommendations/{id}", h.rmsAdvancedGet)
	r.Post("/revenue/advanced-recommendations/{id}/decision", h.rmsAdvancedDecide)
}

func loadRMSAdvanced(ctx context.Context, q database.Querier, org, id ids.ID) (RMSAdvancedRecommendation, error) {
	var raw []byte
	e := q.QueryRow(ctx, `SELECT to_jsonb(a) FROM rms_advanced_recommendations a WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&raw)
	return decodeRMSRecord[RMSAdvancedRecommendation](raw, e)
}

func (h *Handler) rmsAdvancedList(w http.ResponseWriter, r *http.Request) {
	h.rmsAutomationScopedList(w, r, "rms_advanced_recommendations", "created_at DESC,id DESC")
}
func (h *Handler) rmsAdvancedGet(w http.ResponseWriter, r *http.Request) {
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
	v, e := loadRMSAdvanced(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		_, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, "rms:read", "PROPERTY", v.PropertyID, false)
	}
	reply(w, r, 200, v, e)
}

// rmsAdvancedCreate derives one advanced proposal from retained facts. It never
// changes a rate, restriction or promotion; application is a separate decision.
func (h *Handler) rmsAdvancedCreate(w http.ResponseWriter, r *http.Request) {
	var in RMSAdvancedCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PolicyID.ValidateV7() != nil || in.ForecastID.ValidateV7() != nil || in.AnalyticsID != nil && in.AnalyticsID.ValidateV7() != nil || !slices.Contains(rmsAdvancedKinds, in.Kind) || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Retained policy/forecast, supported recommendation kind and reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pre, e := loadRMSPolicy(r.Context(), h.Auth.Service.Pool, t.Organisation, in.PolicyID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:advanced:create", in, "rms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, pre.PropertyID); e != nil {
			return 0, nil, e
		}
		p, e := loadRMSPolicy(ctx, tx, t.Organisation, in.PolicyID)
		if e != nil {
			return 0, nil, e
		}
		if e = h.rmsCurrentPolicy(ctx, tx, t, p); e != nil {
			return 0, nil, e
		}
		f, e := loadRMSForecast(ctx, tx, t.Organisation, in.ForecastID)
		if e != nil {
			return 0, nil, e
		}
		if e = h.rmsForecastUsable(ctx, tx, t, p, f); e != nil {
			return 0, nil, e
		}
		plan, e := revenue.GetPlan(ctx, tx, t.Organisation, p.RatePlanID)
		if e != nil {
			return 0, nil, e
		}
		if plan.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("RATE_INACTIVE", "An active canonical rate is required.")
		}
		var current revenue.RateVersion
		for i, d := range f.Result.Days {
			v, e := revenue.VersionForDay(ctx, tx, t.Organisation, plan.ID, d.Day)
			if e != nil {
				return 0, nil, e
			}
			if i > 0 && v.ID != current.ID {
				return 0, nil, apperrors.Conflict("RMS_MIXED_RATE_VERSIONS", "Advanced recommendations require one current canonical rate version across the horizon.")
			}
			current = v
		}
		var analytics *revenue.AnalyticsResult
		if in.AnalyticsID != nil {
			a, e := h.loadRMSAnalytics(ctx, tx, t.Organisation, *in.AnalyticsID)
			if e != nil {
				return 0, nil, e
			}
			if e = h.rmsAnalyticsAuthorized(ctx, tx, a); e != nil {
				return 0, nil, e
			}
			if !slices.Contains(a.PropertyIDs, p.PropertyID) {
				return 0, nil, apperrors.PermissionDenied("Analytics snapshot must cover the policy property.")
			}
			analytics = &a.Result
		}
		property, e := portfolio.Get(ctx, tx, t.Organisation, p.PropertyID)
		if e != nil {
			return 0, nil, e
		}
		loc, e := time.LoadLocation(property.Timezone)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		proposal, e := revenue.RecommendAdvanced(revenue.AdvancedContext{Kind: in.Kind, Policy: p.Definition, Forecast: f.Result, Analytics: analytics, Current: current.Definition, Today: now.In(loc).Format(time.DateOnly), Now: now, Parameters: in.Parameters})
		if errors.Is(e, revenue.ErrNoRecommendation) {
			return 0, nil, apperrors.Conflict("RMS_NO_SUPPORTED_RECOMMENDATION", "The retained facts do not support this recommendation kind now.")
		}
		if e != nil {
			return 0, nil, apperrors.Conflict("RMS_ADVANCED_SOURCE_INVALID", "The retained forecast/policy cannot support this recommendation.")
		}
		if proposal.Promotion != nil {
			proposal.Promotion.RatePlanIDs = []ids.ID{plan.ID}
			if proposal.Promotion.Validate() != nil {
				return 0, nil, apperrors.Conflict("RMS_ADVANCED_SOURCE_INVALID", "The derived promotion is not a valid canonical promotion.")
			}
		}
		id := ids.New()
		if proposal.PromotionCode != "" {
			proposal.PromotionCode = strings.ToUpper(strings.ReplaceAll(proposal.PromotionCode, ":", "") + "-" + id.String()[:8])
		}
		stored := RMSAdvancedProposal{Recommendation: proposal, PreviousVersionID: current.ID, Reason: in.Reason}
		digest := hashCommercialPayload(marshalCommercial(stored))
		expires := now.Add(time.Duration(p.Definition.RecommendationTTLSeconds) * time.Second)
		if expires.After(p.Definition.ExpiresAt) {
			expires = p.Definition.ExpiresAt
		}
		if _, e = tx.Exec(ctx, `INSERT INTO rms_advanced_recommendations(id,organisation_id,property_id,pool_id,rate_plan_id,policy_id,forecast_id,analytics_id,kind,application,advisory_reason,date_from,date_to,target_plan_version,change_digest,proposal,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::date,$13::date,$14,$15,$16,$17,$18)`, id, t.Organisation, p.PropertyID, p.PoolID, plan.ID, p.ID, f.ID, in.AnalyticsID, in.Kind, proposal.Application, proposal.AdvisoryReason, proposal.DateFrom, proposal.DateTo, plan.Version, digest, marshalCommercial(stored), expires, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, p.PropertyID, id, "rms_advanced_recommendation", "RMS_ADVANCED_RECOMMENDATION_CREATED", "pricing.advanced_recommendation.changed.v1", 1, map[string]any{"kind": in.Kind, "application": proposal.Application, "state": "DRAFT", "change_digest": digest, "rates_changed": false}); e != nil {
			return 0, nil, e
		}
		out, e := loadRMSAdvanced(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}

func rmsAdvancedOperation(v RMSAdvancedRecommendation) map[string]any {
	return map[string]any{"advanced_recommendation_id": v.ID, "change_digest": v.ChangeDigest, "kind": v.Kind, "application": v.Application, "target_plan_version": v.TargetPlanVersion}
}
func rmsAdvancedExposure(v RMSAdvancedRecommendation) (*int64, string) {
	p := v.Proposal.Recommendation
	switch {
	case p.Rate != nil:
		value := p.Rate.CeilingMinor
		return &value, p.Rate.Currency
	case p.Promotion != nil:
		value := p.Promotion.MaxDiscountMinor
		return &value, p.Promotion.Currency
	}
	return nil, ""
}

func (h *Handler) rmsAdvancedDecide(w http.ResponseWriter, r *http.Request) {
	var in RMSAdvancedDecisionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || !slices.Contains([]string{"SUBMIT", "APPROVE", "APPLY", "REJECT", "EXPIRE", "ACKNOWLEDGE"}, in.Action) || in.Action != "APPROVE" && !in.ApprovalID.IsZero() {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, supported action and reason required; only approval consumes an approval reference."))
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
	pre, e := loadRMSAdvanced(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:advanced:decision:"+id.String(), in, "rms:write", "PROPERTY", pre.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", pre.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		v, e := loadRMSAdvanced(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.ChangeDigest != hashCommercialPayload(marshalCommercial(v.Proposal)) {
			return 0, nil, apperrors.Conflict("RMS_RECOMMENDATION_VERSION", "Recommendation version or retained proposal changed.")
		}
		now := h.Auth.Service.Now().UTC()
		advisory := v.Application == "ADVISORY"
		state, approval, applied := "", v.ApprovalID, map[string]any(nil)
		switch in.Action {
		case "ACKNOWLEDGE":
			if advisory && v.State == "DRAFT" {
				state = "ACKNOWLEDGED"
			}
		case "SUBMIT":
			if !advisory && v.State == "DRAFT" && now.Before(v.ExpiresAt) {
				state = "PENDING_APPROVAL"
			}
		case "REJECT":
			if slices.Contains([]string{"DRAFT", "PENDING_APPROVAL", "APPROVED"}, v.State) {
				state = "REJECTED"
			}
		case "EXPIRE":
			if !now.Before(v.ExpiresAt) && slices.Contains([]string{"DRAFT", "PENDING_APPROVAL", "APPROVED"}, v.State) {
				state = "EXPIRED"
			}
		case "APPROVE", "APPLY":
			if advisory || in.Action == "APPROVE" && v.State != "PENDING_APPROVAL" || in.Action == "APPLY" && v.State != "APPROVED" {
				break
			}
			if !now.Before(v.ExpiresAt) {
				return 0, nil, apperrors.Conflict("RMS_RECOMMENDATION_EXPIRED", "Recommendation expired.")
			}
			if e = h.commercialGate(ctx, tx, t, v.PropertyID); e != nil {
				return 0, nil, e
			}
			if e = h.rmsAdvancedCurrent(ctx, tx, t, v); e != nil {
				return 0, nil, e
			}
			capability := map[string]string{"CANONICAL_RATE_VERSION": "rates:write", "CANONICAL_RESTRICTION": "restrictions:write", "CANONICAL_PROMOTION": "promotions:write"}[v.Application]
			if _, _, e = h.authorize(ctx, tx, capability, "PROPERTY", v.PropertyID, true); e != nil {
				return 0, nil, e
			}
			if in.Action == "APPROVE" {
				amount, currency := rmsAdvancedExposure(v)
				if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_RMS_ADVANCED_CHANGE", target.Scope, in.ApprovalID, rmsAdvancedOperation(v), "", amount, currency); e != nil {
					return 0, nil, e
				}
				approval, state = &in.ApprovalID, "APPROVED"
				break
			}
			var usable bool
			if e = tx.QueryRow(ctx, `SELECT state='APPROVED' AND consumed_by_account_id=$3 AND requester_id=$3 AND deadline>$4 AND action='COMMERCIAL_RMS_ADVANCED_CHANGE' FROM approval_requests WHERE organisation_id=$1 AND id=$2`, t.Organisation, *approval, t.Account, now).Scan(&usable); e != nil {
				return 0, nil, e
			}
			if !usable {
				return 0, nil, apperrors.PermissionDenied("Original approval/session actor is no longer authorized for application.")
			}
			if applied, e = h.rmsAdvancedApply(ctx, tx, t, v, *approval); e != nil {
				return 0, nil, e
			}
			state = "APPLIED"
		}
		if state == "" {
			return 0, nil, apperrors.Conflict("RMS_DECISION_STATE", "Decision is invalid for the current state, application kind, version or expiry.")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO rms_advanced_decisions(id,organisation_id,property_id,recommendation_id,previous_version,prior_state,state,action,actor_id,approval_id,reason,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, ids.New(), t.Organisation, v.PropertyID, v.ID, v.Version, v.State, state, in.Action, t.Account, approval, in.Reason, marshalCommercial(map[string]any{"applied": applied})); e != nil {
			return 0, nil, e
		}
		reference := v.AppliedReference
		if applied != nil {
			reference = applied
		}
		if _, e = tx.Exec(ctx, `UPDATE rms_advanced_recommendations SET state=$3,approval_id=$4,applied_reference=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.ID, state, approval, marshalCommercial(reference)); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, v.PropertyID, v.ID, "rms_advanced_recommendation", "RMS_ADVANCED_RECOMMENDATION_DECIDED", "pricing.advanced_recommendation.changed.v1", v.Version+1, map[string]any{"kind": v.Kind, "application": v.Application, "prior_state": v.State, "state": state, "approval_id": approval, "applied": applied}); e != nil {
			return 0, nil, e
		}
		if state == "APPLIED" {
			if e = businessEvents.PublishCommercial(ctx, tx, h.Auth.Service.Outbox, t.Organisation, v.PropertyID, v.ID, "rms_advanced_recommendation", "RMS_ADVANCED_RECOMMENDATION_APPLIED", "pricing.applied.v1", v.Version+1, map[string]any{"kind": v.Kind, "application": v.Application, "applied": applied, "external_delivery_confirmed": false}); e != nil {
				return 0, nil, e
			}
		}
		out, e := loadRMSAdvanced(ctx, tx, t.Organisation, v.ID)
		return 200, out, e
	})
}

// rmsAdvancedCurrent refuses to apply a proposal derived from a superseded
// rate version, policy or forecast.
func (h *Handler) rmsAdvancedCurrent(ctx context.Context, tx pgx.Tx, t tenant, v RMSAdvancedRecommendation) error {
	p, e := loadRMSPolicy(ctx, tx, t.Organisation, v.PolicyID)
	if e != nil {
		return e
	}
	if e = h.rmsCurrentPolicy(ctx, tx, t, p); e != nil {
		return e
	}
	plan, e := revenue.GetPlan(ctx, tx, t.Organisation, v.RatePlanID)
	if e != nil {
		return e
	}
	if plan.State != "ACTIVE" || plan.Version != v.TargetPlanVersion {
		return apperrors.Conflict("RMS_RATE_VERSION_CHANGED", "The canonical rate changed after this recommendation.")
	}
	start, _ := time.Parse(time.DateOnly, v.DateFrom)
	end, _ := time.Parse(time.DateOnly, v.DateTo)
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		current, e := revenue.VersionForDay(ctx, tx, t.Organisation, v.RatePlanID, d.Format(time.DateOnly))
		if e != nil {
			return e
		}
		if current.ID != v.Proposal.PreviousVersionID {
			return apperrors.Conflict("RMS_RATE_VERSION_CHANGED", "An effective canonical rate changed.")
		}
	}
	return nil
}

// rmsAdvancedApply writes through the existing canonical engines only.
func (h *Handler) rmsAdvancedApply(ctx context.Context, tx pgx.Tx, t tenant, v RMSAdvancedRecommendation, approval ids.ID) (map[string]any, error) {
	p := v.Proposal.Recommendation
	reason := "RMS advanced recommendation " + v.ID.String() + " (" + v.Kind + ")"
	switch v.Application {
	case "CANONICAL_RATE_VERSION":
		if p.Rate == nil {
			return nil, apperrors.Conflict("RMS_ADVANCED_PROPOSAL_INVALID", "Rate proposal missing.")
		}
		written, e := h.writeRateVersion(ctx, tx, t, v.PropertyID, v.RatePlanID, *p.Rate, v.DateFrom, v.DateTo, reason, approval)
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE rate_plans SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, v.RatePlanID); e != nil {
			return nil, e
		}
		plan, e := revenue.GetPlan(ctx, tx, t.Organisation, v.RatePlanID)
		if e != nil {
			return nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, v.PropertyID, v.RatePlanID, "rate_plan", "RMS_ADVANCED_RATE_APPLIED", businessEvents.RatePlanChanged, plan.Version, map[string]any{"advanced_recommendation_id": v.ID, "kind": v.Kind, "rate_version_id": written.ID, "previous_version_id": v.Proposal.PreviousVersionID, "approval_id": approval, "external_delivery_confirmed": false}); e != nil {
			return nil, e
		}
		return map[string]any{"rate_version_id": written.ID, "previous_version_id": v.Proposal.PreviousVersionID}, nil
	case "CANONICAL_RESTRICTION":
		if p.Restriction == nil {
			return nil, apperrors.Conflict("RMS_ADVANCED_PROPOSAL_INVALID", "Restriction proposal missing.")
		}
		plan := v.RatePlanID
		record, e := h.createRestriction(ctx, tx, t, CommercialRestrictionCreate{PropertyID: v.PropertyID, RatePlanID: &plan, DateFrom: v.DateFrom, DateTo: v.DateTo, Definition: *p.Restriction, Reason: reason})
		if e != nil {
			return nil, e
		}
		return map[string]any{"restriction_id": record.ID}, nil
	case "CANONICAL_PROMOTION":
		if p.Promotion == nil || p.PromotionCode == "" || !commercialCode.MatchString(p.PromotionCode) {
			return nil, apperrors.Conflict("RMS_ADVANCED_PROPOSAL_INVALID", "Promotion proposal missing.")
		}
		for _, plan := range p.Promotion.RatePlanIDs {
			if e := h.validateProductRate(ctx, tx, t.Organisation, v.PropertyID, plan); e != nil {
				return nil, e
			}
		}
		id := ids.New()
		if _, e := tx.Exec(ctx, `INSERT INTO promotions(id,organisation_id,property_id,code,definition,state,approval_id,created_by) VALUES($1,$2,$3,$4,$5,'ACTIVE',$6,$7)`, id, t.Organisation, v.PropertyID, p.PromotionCode, marshalCommercial(p.Promotion), approval, t.Account); e != nil {
			return nil, e
		}
		value, e := typedJSON[revenue.Promotion](ctx, tx, `SELECT to_jsonb(p) FROM promotions p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
		if e != nil {
			return nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, v.PropertyID, id, "promotion", "PROMOTION_ACTIVATED_BY_RMS", businessEvents.RatePlanChanged, 1, value); e != nil {
			return nil, e
		}
		return map[string]any{"promotion_id": id, "code": p.PromotionCode}, nil
	}
	return nil, apperrors.Conflict("RMS_ADVISORY_ONLY", "Advisory recommendations are never applied.")
}
