package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/hospitality"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type PMSArrivalPolicyCommand struct {
	PropertyID            ids.ID    `json:"property_id"`
	ExpectedVersion       int64     `json:"expected_version"`
	Mode                  string    `json:"mode"`
	AllowedSources        []string  `json:"allowed_sources"`
	MaxReservationsPerRun int       `json:"max_reservations_per_run"`
	MaxReservationMinor   int64     `json:"max_reservation_minor"`
	MaxTotalMinor         int64     `json:"max_total_minor"`
	Currency              string    `json:"currency"`
	EffectiveAt           time.Time `json:"effective_at"`
	ExpiresAt             time.Time `json:"expires_at"`
	ApprovalID            ids.ID    `json:"approval_id,omitempty"`
	Reason                string    `json:"reason"`
}
type PMSArrivalPolicy struct {
	ID                      ids.ID    `json:"id"`
	OrganisationID          ids.ID    `json:"organisation_id"`
	PropertyID              ids.ID    `json:"property_id"`
	Version                 int64     `json:"version"`
	Mode                    string    `json:"mode"`
	AllowedSources          []string  `json:"allowed_sources"`
	MaxReservationsPerRun   int       `json:"max_reservations_per_run"`
	MaxReservationMinor     int64     `json:"max_reservation_minor"`
	MaxTotalMinor           int64     `json:"max_total_minor"`
	Currency                string    `json:"currency"`
	EffectiveAt             time.Time `json:"effective_at"`
	ExpiresAt               time.Time `json:"expires_at"`
	AuthorizingMembershipID ids.ID    `json:"authorizing_membership_id"`
	AuthorizingRoleID       ids.ID    `json:"authorizing_role_id"`
	ApprovalID              *ids.ID   `json:"approval_id,omitempty"`
	Reason                  string    `json:"reason"`
	CreatedBy               ids.ID    `json:"created_by"`
	CreatedAt               time.Time `json:"created_at"`
}

func (h *Handler) registerPMSArrivals(r chi.Router) {
	r.Get("/hospitality-arrival-automation", h.pmsArrivalPolicies)
	r.Post("/hospitality-arrival-automation", h.pmsArrivalPolicy)
	r.Post("/hospitality-arrival-automation/{id}/revoke", h.pmsArrivalRevoke)
	r.Get("/hospitality-arrival-runs", h.pmsArrivalRuns)
}
func (h *Handler) pmsArrivalPolicies(w http.ResponseWriter, r *http.Request) {
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
	properties, e := h.operationalProperties(r.Context(), h.Auth.Service.Pool, t, "reservations:read", r.URL.Query().Get("property_id"), r.URL.Query().Get("branch_id"))
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items, e := commercialJSONRows(r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p)||jsonb_build_object('recorded_state',CASE WHEN EXISTS(SELECT 1 FROM hospitality_arrival_policies later WHERE later.organisation_id=p.organisation_id AND later.property_id=p.property_id AND later.version>p.version) THEN 'SUPERSEDED' WHEN EXISTS(SELECT 1 FROM hospitality_arrival_revocations rev WHERE rev.organisation_id=p.organisation_id AND rev.policy_id=p.id) THEN 'REVOKED' WHEN p.mode='MANUAL' THEN 'MANUAL' WHEN p.expires_at<=$5 THEN 'EXPIRED' WHEN p.effective_at>$5 THEN 'NOT_YET_EFFECTIVE' ELSE 'RECORDED' END,'used_exposure_minor',(SELECT coalesce(sum(run.original_amount_minor),0) FROM hospitality_arrival_runs run WHERE run.organisation_id=p.organisation_id AND run.policy_id=p.id AND run.state='MARKED_NO_SHOW'),'source_review_count',CASE WHEN NOT EXISTS(SELECT 1 FROM hospitality_arrival_policies later WHERE later.organisation_id=p.organisation_id AND later.property_id=p.property_id AND later.version>p.version) THEN (SELECT count(*) FROM hospitality_arrival_cursors c WHERE c.organisation_id=p.organisation_id AND c.property_id=p.property_id AND (c.consecutive_failures>0 OR c.state='QUARANTINED')) ELSE 0 END) FROM hospitality_arrival_policies p WHERE organisation_id=$1 AND property_id=ANY($2::uuid[]) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, properties, limit, offset, h.Auth.Service.Now().UTC())
	if items == nil {
		items = []json.RawMessage{}
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, e)
}
func (h *Handler) pmsArrivalRuns(w http.ResponseWriter, r *http.Request) {
	h.pmsList(w, r, "hospitality_arrival_runs", "reservations:read")
}
func (h *Handler) pmsArrivalPolicy(w http.ResponseWriter, r *http.Request) {
	var in PMSArrivalPolicyCommand
	if !decode(w, r, &in) {
		return
	}
	now := h.Auth.Service.Now().UTC()
	_, currencyErr := money.New(0, in.Currency)
	if in.PropertyID.ValidateV7() != nil || in.ExpectedVersion < 0 || !validStaffReason(in.Reason) || (in.Mode != "MANUAL" && in.Mode != "AUTOMATIC_NO_SHOW") || in.MaxReservationsPerRun < 1 || in.MaxReservationsPerRun > 50 || in.MaxReservationMinor < 0 || in.MaxTotalMinor < in.MaxReservationMinor || currencyErr != nil || len(in.AllowedSources) < 1 || len(in.AllowedSources) > 4 || in.EffectiveAt.Before(now.Add(-5*time.Minute)) || in.EffectiveAt.After(now.Add(24*time.Hour)) || !in.ExpiresAt.After(in.EffectiveAt) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) || in.ExpiresAt.After(in.EffectiveAt.Add(7*24*time.Hour)) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current property policy, supported sources, integer-minor exposure/batch limits, currency, bounded expiry and reason required."))
		return
	}
	seen := map[string]bool{}
	for _, source := range in.AllowedSources {
		if !slices.Contains([]string{"DIRECT", "WALK_IN", "CORPORATE", "GROUP"}, source) || seen[source] {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Unique supported business-owned sources required; remote-source automation is not certified."))
			return
		}
		seen[source] = true
	}
	h.execute(w, r, "pms:arrival-policy:"+in.PropertyID.String(), in, "reservations:cancel", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "reservations:cancel", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		now := h.Auth.Service.Now().UTC()
		if !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(7*24*time.Hour)) {
			return 0, nil, apperrors.Conflict("ARRIVAL_POLICY_EXPIRED", "Current bounded policy required.")
		}
		var current int64
		var currency string
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM hospitality_arrival_policies WHERE organisation_id=$1 AND property_id=$2`, t.Organisation, in.PropertyID).Scan(&current); e != nil {
			return 0, nil, e
		}
		if e = tx.QueryRow(ctx, `SELECT currency FROM inventory_nodes WHERE organisation_id=$1 AND id=$2 AND kind='PROPERTY' AND state='ACTIVE'`, t.Organisation, in.PropertyID).Scan(&currency); e != nil {
			return 0, nil, e
		}
		if current != in.ExpectedVersion || in.Currency != currency {
			return 0, nil, apperrors.Conflict("ARRIVAL_POLICY_VERSION", "Property currency or current policy version changed.")
		}
		var approval any
		if in.Mode == "AUTOMATIC_NO_SHOW" {
			if t.Direct.ExpiresAt != nil && in.ExpiresAt.After(*t.Direct.ExpiresAt) {
				return 0, nil, apperrors.PermissionDenied("A worker grant cannot outlive its current direct membership.")
			}
			for _, capability := range []permissions.Permission{"reservations:cancel", "pms:write"} {
				if e = workforce.Authorize(t.Direct, workforce.DecisionInput{AccountID: t.Account, Target: target, Capability: capability, Now: now}); e != nil {
					return 0, nil, apperrors.PermissionDenied("Direct current unattended cancellation and front-desk scopes required; per-action MFA and temporary grants cannot authorize workers.")
				}
			}
			op := in
			op.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PMS_NO_SHOW_AUTOMATION", target.Scope, in.ApprovalID, map[string]any{"command": op}, "", &in.MaxTotalMinor, in.Currency); e != nil {
				return 0, nil, e
			}
			approval = in.ApprovalID
		} else if !in.ApprovalID.IsZero() {
			return 0, nil, apperrors.InvalidRequest("Manual mode does not consume an automation approval.")
		}
		id := ids.New()
		_, e = tx.Exec(ctx, `INSERT INTO hospitality_arrival_policies(id,organisation_id,property_id,version,mode,allowed_sources,max_reservations_per_run,max_reservation_minor,max_total_minor,currency,effective_at,expires_at,authorizing_membership_id,authorizing_role_id,approval_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, id, t.Organisation, in.PropertyID, current+1, in.Mode, in.AllowedSources, in.MaxReservationsPerRun, in.MaxReservationMinor, in.MaxTotalMinor, in.Currency, in.EffectiveAt, in.ExpiresAt, t.Member, t.Role, approval, in.Reason, t.Account)
		if e != nil {
			return 0, nil, e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM hospitality_arrival_cursors WHERE organisation_id=$1 AND property_id=$2`, t.Organisation, in.PropertyID); e != nil {
			return 0, nil, e
		}
		if e = h.pmsCommit(ctx, tx, t, in.PropertyID, id, "hospitality_arrival_policy", "ARRIVAL_AUTOMATION_POLICY_RECORDED", "hospitality.arrival.automation.updated.v1", 1, map[string]any{"version": current + 1, "command": in, "approval_id": approval}); e != nil {
			return 0, nil, e
		}
		value, e := loadPMSArrivalPolicy(ctx, tx, t.Organisation, in.PropertyID)
		return 201, value, e
	})
}
func (h *Handler) pmsArrivalRevoke(w http.ResponseWriter, r *http.Request) {
	var in PMSVersionCommand
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Exact immutable policy version and revocation reason required."))
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
	p, e := hospitality.Load[PMSArrivalPolicy](r.Context(), h.Auth.Service.Pool, `SELECT to_jsonb(p) FROM hospitality_arrival_policies p WHERE organisation_id=$1 AND id=$2`, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	h.execute(w, r, "pms:arrival-policy:revoke:"+id.String(), in, "reservations:cancel", "PROPERTY", p.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "reservations:cancel", "PROPERTY", p.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		if p.Version != in.Version {
			return 0, nil, apperrors.Conflict("ARRIVAL_POLICY_VERSION", "Exact immutable policy version required.")
		}
		rid := ids.New()
		_, e = tx.Exec(ctx, `INSERT INTO hospitality_arrival_revocations(id,organisation_id,property_id,policy_id,reason,created_by) VALUES($1,$2,$3,$4,$5,$6)`, rid, t.Organisation, p.PropertyID, id, in.Reason, t.Account)
		if e != nil {
			return 0, nil, e
		}
		e = h.pmsCommit(ctx, tx, t, p.PropertyID, rid, "hospitality_arrival_revocation", "ARRIVAL_AUTOMATION_REVOKED", "hospitality.arrival.automation.updated.v1", 1, map[string]any{"policy_id": id, "reason": in.Reason})
		return 201, PMSDayAutomationRevoked{ID: rid, PolicyID: id, State: "REVOKED"}, e
	})
}
func loadPMSArrivalPolicy(ctx context.Context, q database.Querier, org, property ids.ID) (PMSArrivalPolicy, error) {
	var out PMSArrivalPolicy
	raw, e := db.New().GetLatestHospitalityArrivalPolicy(ctx, q, db.GetLatestHospitalityArrivalPolicyParams{OrganisationID: org, PropertyID: property})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, apperrors.NotFound("ARRIVAL_POLICY_NOT_FOUND", "Policy not found.")
	}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(raw, &out)
	return out, e
}
