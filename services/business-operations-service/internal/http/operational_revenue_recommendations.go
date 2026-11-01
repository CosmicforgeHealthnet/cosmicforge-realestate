package businesshttp

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type RMSRecommendationCommand struct {
	PolicyID            ids.ID `json:"policy_id"`
	ForecastID          ids.ID `json:"forecast_id"`
	ProposedAmountMinor *int64 `json:"proposed_amount_minor,omitempty"`
	Reason              string `json:"reason"`
}
type RMSPricingSnapshot struct {
	Algorithm     string                   `json:"algorithm"`
	PolicyVersion int64                    `json:"policy_version"`
	SourceDigest  string                   `json:"source_digest"`
	SourceAsOf    time.Time                `json:"source_as_of"`
	Segments      []revenue.PricingSegment `json:"segments"`
	Explanation   string                   `json:"explanation"`
	Reason        string                   `json:"reason"`
}
type RMSRecommendation struct {
	ID                ids.ID             `json:"id"`
	PropertyID        ids.ID             `json:"property_id"`
	PoolID            ids.ID             `json:"pool_id"`
	RatePlanID        ids.ID             `json:"rate_plan_id"`
	PolicyID          ids.ID             `json:"policy_id"`
	ForecastID        ids.ID             `json:"forecast_id"`
	DateFrom          string             `json:"date_from"`
	DateTo            string             `json:"date_to"`
	TargetPlanVersion int64              `json:"target_plan_version"`
	ChangeDigest      string             `json:"change_digest"`
	Snapshot          RMSPricingSnapshot `json:"snapshot"`
	ExpiresAt         time.Time          `json:"expires_at"`
	State             string             `json:"state"`
	Version           int64              `json:"version"`
	ApprovalID        *ids.ID            `json:"approval_id,omitempty"`
	AppliedVersions   []ids.ID           `json:"applied_versions"`
	CreatedBy         ids.ID             `json:"created_by"`
}

func (h *Handler) registerRMSRecommendations(r chi.Router) {
	r.Get("/revenue/recommendations", h.rmsRecommendationList)
	r.Post("/revenue/recommendations", h.rmsRecommendationCreate)
	r.Get("/revenue/recommendations/{id}", h.rmsRecommendationGet)
	r.Post("/revenue/recommendations/{id}/decision", h.rmsRecommendationDecide)
	r.Get("/revenue/recommendations/{id}/decisions", h.rmsRecommendationDecisions)
	r.Post("/revenue/recommendations/{id}/simulate", h.rmsRecommendationSimulate)
	r.Get("/revenue/simulations", h.rmsSimulationList)
}
func loadRMSRecommendation(ctx context.Context, q database.Querier, org, id ids.ID) (RMSRecommendation, error) {
	raw, e := db.New().GetRMSRecommendation(ctx, q, db.GetRMSRecommendationParams{OrganisationID: org, ID: id})
	return decodeRMSRecord[RMSRecommendation](raw, e)
}
func (h *Handler) rmsRecommendationTarget(r *http.Request, capability string, stepUp bool) (tenant, RMSRecommendation, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, RMSRecommendation{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, RMSRecommendation{}, e
	}
	v, e := loadRMSRecommendation(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		return t, v, e
	}
	t, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", v.PropertyID, stepUp)
	return t, v, e
}
func (h *Handler) rmsRecommendationList(w http.ResponseWriter, r *http.Request) {
	h.rmsScopedList(w, r, "pricing_recommendations")
}
func (h *Handler) rmsSimulationList(w http.ResponseWriter, r *http.Request) {
	h.rmsScopedList(w, r, "pricing_simulations")
}
func (h *Handler) rmsRecommendationGet(w http.ResponseWriter, r *http.Request) {
	_, v, e := h.rmsRecommendationTarget(r, "rms:read", false)
	reply(w, r, 200, v, e)
}
func (h *Handler) rmsRecommendationDecisions(w http.ResponseWriter, r *http.Request) {
	t, v, e := h.rmsRecommendationTarget(r, "rms:read", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(d) FROM pricing_decisions d WHERE organisation_id=$1 AND recommendation_id=$2 ORDER BY previous_version,id LIMIT $3 OFFSET $4`, t.Organisation, v.ID, limit, offset)
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) rmsCurrentPolicy(ctx context.Context, q database.Querier, t tenant, p RMSPolicy) error {
	var latest ids.ID
	if e := q.QueryRow(ctx, `SELECT id FROM rms_policies WHERE organisation_id=$1 AND rate_plan_id=$2 ORDER BY version DESC LIMIT 1`, t.Organisation, p.RatePlanID).Scan(&latest); e != nil {
		return e
	}
	now := h.Auth.Service.Now().UTC()
	if p.ID != latest || p.State != "ACTIVE" || now.Before(p.Definition.EffectiveAt) || !now.Before(p.Definition.ExpiresAt) {
		return apperrors.Conflict("RMS_POLICY_INACTIVE", "The exact current RMS policy must be active and unexpired.")
	}
	property, e := portfolio.Get(ctx, q, t.Organisation, p.PropertyID)
	if e != nil {
		return e
	}
	if property.BranchID != p.BranchID || property.Currency != p.Definition.Currency {
		return apperrors.Conflict("RMS_POLICY_SCOPE_CHANGED", "Property branch/currency changed after policy authorization.")
	}
	var unsafe bool
	if e = q.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM sellable_products WHERE organisation_id=$1 AND property_id=$2 AND pool_id=$3 AND rate_plan_id=$4 AND state='ACTIVE') OR EXISTS(SELECT 1 FROM sellable_products WHERE organisation_id=$1 AND rate_plan_id=$4 AND pool_id<>$3 AND state<>'ARCHIVED')`, t.Organisation, p.PropertyID, p.PoolID, p.RatePlanID).Scan(&unsafe); e != nil {
		return e
	}
	if unsafe {
		return apperrors.Conflict("RMS_RATE_SCOPE_AMBIGUOUS", "The rate must be active on the forecast pool and cannot affect another pool without its own aggregate mandate.")
	}
	return nil
}
func (h *Handler) rmsForecastUsable(ctx context.Context, q database.Querier, t tenant, p RMSPolicy, f RMSForecastRun) error {
	now := h.Auth.Service.Now().UTC()
	if f.PropertyID != p.PropertyID || f.PoolID != p.PoolID || f.Status != "BASELINE_READY" || f.Result.HistoryCoverageBPS < p.Definition.MinimumHistoryCoverageBPS || f.SourceAsOf.After(now) || now.Sub(f.SourceAsOf) > time.Duration(p.Definition.MaxSourceAgeSeconds)*time.Second {
		return apperrors.Conflict("RMS_SOURCE_NOT_USABLE", "Current sufficiently observed exact-scope forecast required.")
	}
	for _, missing := range f.Result.MissingInputs {
		if slices.Contains(p.Definition.MandatoryInputs, missing) {
			return apperrors.Conflict("RMS_MANDATORY_INPUT_UNAVAILABLE", "A policy-mandated forecast input is unavailable.")
		}
	}
	// Unknown input names must not masquerade as supplied features. This baseline
	// supports only these verifiable inputs; optional missingness is explicit.
	for _, input := range p.Definition.MandatoryInputs {
		if !slices.Contains([]string{"canonical_confirmed_nights", "physical_calendar", "accepted_accommodation"}, input) {
			return apperrors.Conflict("RMS_MANDATORY_INPUT_UNAVAILABLE", "This registered baseline has no authoritative adapter for a mandatory input.")
		}
	}
	if slices.Contains(p.Definition.MandatoryInputs, "accepted_accommodation") {
		source, e := loadRMSDemandSnapshot(ctx, q, t.Organisation, f.SourceSnapshotID)
		if e != nil {
			return e
		}
		for _, day := range source.Dataset.Days {
			if day.AccommodationMinor == nil {
				return apperrors.Conflict("RMS_MANDATORY_INPUT_UNAVAILABLE", "Accepted accommodation attribution is unknown for an observed source day.")
			}
		}
	}
	return nil
}
func (h *Handler) rmsRecommendationCreate(w http.ResponseWriter, r *http.Request) {
	var in RMSRecommendationCommand
	if !decode(w, r, &in) {
		return
	}
	if in.PolicyID.ValidateV7() != nil || in.ForecastID.ValidateV7() != nil || in.ProposedAmountMinor != nil && *in.ProposedAmountMinor < 0 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Retained policy/forecast and a valid recommendation reason required."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	p, e := loadRMSPolicy(r.Context(), h.Auth.Service.Pool, t.Organisation, in.PolicyID)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "revenue:recommendation:create", in, "rms:write", "PROPERTY", p.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", p.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		if e = h.commercialGate(ctx, tx, t, p.PropertyID); e != nil {
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
			return 0, nil, apperrors.Conflict("RATE_INACTIVE", "Recommendations require a current active canonical rate.")
		}
		source, e := loadRMSDemandSnapshot(ctx, tx, t.Organisation, f.SourceSnapshotID)
		if e != nil {
			return 0, nil, e
		}
		observed := map[string]revenue.DemandDay{}
		for _, day := range source.Dataset.Days {
			observed[day.Day] = day
		}
		segments := []revenue.PricingSegment{}
		for _, day := range f.Result.Days {
			prev, e := revenue.VersionForDay(ctx, tx, t.Organisation, plan.ID, day.Day)
			if e != nil {
				return 0, nil, e
			}
			if prev.Definition.ParentVersionID != nil || len(prev.Definition.Adjustments) > 0 || len(prev.Definition.Rules) > 0 {
				return 0, nil, apperrors.Conflict("RMS_COMPLEX_RATE_REQUIRES_SCENARIO", "Derived/conditional rate recommendations require the canonical scenario adapter; no base-only mutation is permitted.")
			}
			amount, e := revenue.RecommendAmount(p.Definition, prev.Definition, day)
			if e != nil {
				return 0, nil, apperrors.Conflict("RMS_POLICY_BOUNDS", "Forecast or policy bounds do not permit this rate scenario.")
			}
			if in.ProposedAmountMinor != nil {
				amount = *in.ProposedAmountMinor
			}
			next := prev.Definition
			next.AmountMinor = amount
			if next.Validate() != nil || amount < p.Definition.FloorMinor || amount > p.Definition.CeilingMinor || slices.Contains(p.Definition.BlackoutDates, day.Day) {
				return 0, nil, apperrors.Conflict("RMS_POLICY_BOUNDS", "Proposed amount violates a canonical or RMS hard bound.")
			}
			end, _ := time.Parse(time.DateOnly, day.Day)
			segments = append(segments, revenue.PricingSegment{DateFrom: day.Day, DateTo: end.AddDate(0, 0, 1).Format(time.DateOnly), Previous: prev, Proposed: next, CalendarVersion: observed[day.Day].CalendarVersion})
		}
		snapshot := RMSPricingSnapshot{Algorithm: "operator-bounded-occupancy-feedback-v1", PolicyVersion: p.Version, SourceDigest: f.SourceDigest, SourceAsOf: f.SourceAsOf, Segments: segments, Explanation: "Operator-configured occupancy feedback, bounded by canonical and RMS price limits; no calibrated price elasticity is asserted. Staff overrides are retained as explicit proposals.", Reason: in.Reason}
		if in.ProposedAmountMinor != nil {
			snapshot.Algorithm = "explicit-operator-scenario-v1"
		}
		digest := hashCommercialPayload(marshalCommercial(snapshot))
		id := ids.New()
		expires := h.Auth.Service.Now().UTC().Add(time.Duration(p.Definition.RecommendationTTLSeconds) * time.Second)
		if expires.After(p.Definition.ExpiresAt) {
			expires = p.Definition.ExpiresAt
		}
		if _, e = tx.Exec(ctx, `INSERT INTO pricing_recommendations(id,organisation_id,property_id,pool_id,rate_plan_id,policy_id,forecast_id,date_from,date_to,target_plan_version,change_digest,snapshot,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8::date,$9::date,$10,$11,$12,$13,$14)`, id, t.Organisation, p.PropertyID, p.PoolID, plan.ID, p.ID, f.ID, f.DateFrom, f.DateTo, plan.Version, digest, marshalCommercial(snapshot), expires, t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, p.PropertyID, id, "pricing_recommendation", "RMS_RECOMMENDATION_CREATED", "revenue.recommendation.changed.v1", 1, map[string]any{"state": "DRAFT", "change_digest": digest, "policy_id": p.ID, "forecast_id": f.ID, "rates_changed": false}); e != nil {
			return 0, nil, e
		}
		out, e := loadRMSRecommendation(ctx, tx, t.Organisation, id)
		return 201, out, e
	})
}

func (h *Handler) rmsRecommendationSimulate(w http.ResponseWriter, r *http.Request) {
	_, pre, e := h.rmsRecommendationTarget(r, "rms:write", false)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	var in RMSVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current recommendation version and reason required."))
		return
	}
	h.execute(w, r, "revenue:simulate:"+pre.ID.String(), in, "rms:write", "PROPERTY", pre.PropertyID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "rms:write", "PROPERTY", pre.PropertyID, false)
		if e != nil {
			return 0, nil, e
		}
		v, e := loadRMSRecommendation(ctx, tx, t.Organisation, pre.ID)
		if e != nil {
			return 0, nil, e
		}
		if v.Version != in.Version || v.ChangeDigest != hashCommercialPayload(marshalCommercial(v.Snapshot)) {
			return 0, nil, apperrors.Conflict("RMS_RECOMMENDATION_VERSION", "Recommendation version/digest changed.")
		}
		f, e := loadRMSForecast(ctx, tx, t.Organisation, v.ForecastID)
		if e != nil {
			return 0, nil, e
		}
		out, e := revenue.SimulatePricing(f.Result, v.Snapshot.Segments)
		if e != nil {
			return 0, nil, apperrors.Conflict("RMS_SIMULATION_UNSUPPORTED", "This retained scenario cannot be evaluated by the registered base-rate simulator.")
		}
		id := ids.New()
		if _, e = tx.Exec(ctx, `INSERT INTO pricing_simulations(id,organisation_id,property_id,recommendation_id,change_digest,forecast_id,result,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, t.Organisation, v.PropertyID, v.ID, v.ChangeDigest, f.ID, marshalCommercial(out), t.Account); e != nil {
			return 0, nil, e
		}
		if e = h.commercialCommit(ctx, tx, t, v.PropertyID, id, "pricing_simulation", "RMS_SCENARIO_SIMULATED", "", 1, map[string]any{"recommendation_id": v.ID, "change_digest": v.ChangeDigest, "rates_changed": false, "reason": in.Reason}); e != nil {
			return 0, nil, e
		}
		return 201, map[string]any{"id": id, "recommendation_id": v.ID, "change_digest": v.ChangeDigest, "result": out}, nil
	})
}
