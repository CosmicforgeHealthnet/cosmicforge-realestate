package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func highRiskAction(action string) bool {
	switch action {
	case approvals.ActionInventoryRestructure, approvals.ActionInventoryMapping, "ORGANISATION_STATE_CHANGE", "REPRESENTATIVE_CHANGE", approvals.ActionPayoutDestinationChange, approvals.ActionHighValueRefund, approvals.ActionVerifiedPropertyPublication, approvals.ActionExceptionalRateChange, approvals.ActionInventoryConflictOverride, approvals.ActionDeliberateOverbooking, approvals.ActionExceptionalCheckIn, approvals.ActionNightAuditExceptionClosure, approvals.ActionExternalManagerAccess, approvals.ActionProfessionalDataRoomGrant, approvals.ActionStaffRoleChange, approvals.ActionStaffScopeChange, approvals.ActionBreakGlassAccess, approvals.ActionBulkDisposalOrSale:
		return true
	}
	return false
}

type policyCommand struct {
	Channel        string            `json:"channel,omitempty"`
	Version        int64             `json:"version,omitempty"`
	Action         string            `json:"action"`
	Risk           string            `json:"risk"`
	ScopeKind      string            `json:"scope_kind"`
	ScopeID        ids.ID            `json:"scope_id,omitempty"`
	ActorRoleKey   *ids.ID           `json:"actor_role_key,omitempty"`
	ThresholdMinor *int64            `json:"threshold_minor,omitempty"`
	Currency       string            `json:"currency,omitempty"`
	Levels         []approvals.Level `json:"levels"`
	EffectiveAt    time.Time         `json:"effective_at"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
}

func (h *Handler) registerApprovals(r chi.Router) {
	r.Get("/approval-policies", h.listPolicies)
	r.Post("/approval-policies", func(w http.ResponseWriter, r *http.Request) { h.writePolicy(w, r, false) })
	r.Get("/approval-policies/{id}", h.getPolicy)
	r.Patch("/approval-policies/{id}", func(w http.ResponseWriter, r *http.Request) { h.writePolicy(w, r, true) })
	r.Post("/approval-policies/{id}/activation", h.activatePolicy)
	r.Post("/approval-policies/evaluate", h.evaluateApproval)
	r.Get("/approvals", h.listApprovals)
	r.Post("/approvals", h.createApproval)
	r.Get("/approvals/{id}", h.getApproval)
	r.Post("/approvals/{id}/decision", h.decideApproval)
	r.Post("/approvals/{id}/cancel", h.cancelApproval)
	r.Post("/approvals/{id}/submit", h.submitApproval)
}
func (h *Handler) writePolicy(w http.ResponseWriter, r *http.Request, update bool) {
	var in policyCommand
	if !decode(w, r, &in) {
		return
	}
	if !highRiskAction(in.Action) || in.Risk != "HIGH" && in.Risk != "CRITICAL" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A supported high-risk action and risk are required."))
		return
	}
	if in.ScopeKind == "" {
		in.ScopeKind = "COMPANY"
	}
	for _, level := range in.Levels {
		if !level.RequireMFA {
			reply(w, r, 0, nil, apperrors.InvalidRequest("High-risk approvers require step-up MFA."))
			return
		}
	}
	previous := ids.Nil
	if update {
		var err error
		previous, err = ids.ParseV7(chi.URLParam(r, "id"))
		if err != nil || in.Version < 1 {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid policy identifier or version."))
			return
		}
	}
	h.execute(w, r, "policy:write:"+previous.String(), in, "policies:write", in.ScopeKind, in.ScopeID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "policies:write", in.ScopeKind, in.ScopeID, true)
		if err != nil {
			return 0, nil, err
		}
		p := approvals.Policy{ID: ids.New(), OrganisationID: t.Organisation, Version: 1, Action: in.Action, Risk: in.Risk, Active: true, EffectiveAt: in.EffectiveAt, ExpiresAt: in.ExpiresAt, ThresholdMinor: in.ThresholdMinor, Currency: in.Currency, SeparationOfDuties: true, DistinctAcrossLevels: true, Levels: in.Levels, Scope: &target.Scope, ActorRoleKey: in.ActorRoleKey, Channel: in.Channel}
		if p.EffectiveAt.IsZero() {
			p.EffectiveAt = h.Auth.Service.Now().UTC()
		}
		key := p.ID
		if update {
			var oldVersion int64
			if err := tx.QueryRow(ctx, `SELECT policy_key,version FROM approval_policies WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, previous).Scan(&key, &oldVersion); errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, apperrors.NotFound("POLICY_NOT_FOUND", "Policy not found.")
			} else if err != nil {
				return 0, nil, err
			}
			var latest int64
			if err := tx.QueryRow(ctx, `SELECT max(version) FROM approval_policies WHERE organisation_id=$1 AND policy_key=$2`, t.Organisation, key).Scan(&latest); err != nil {
				return 0, nil, err
			}
			if oldVersion != in.Version || latest != in.Version {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Policy version has changed.")
			}
			p.Version = latest + 1
			if _, err := tx.Exec(ctx, `UPDATE approval_policies SET active=false WHERE organisation_id=$1 AND policy_key=$2`, t.Organisation, key); err != nil {
				return 0, nil, err
			}
		}
		if p.Validate() != nil {
			return 0, nil, apperrors.InvalidRequest("Invalid approval policy.")
		}
		for _, level := range p.Levels {
			var valid bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM permissions WHERE code=$1)`, string(level.Capability)).Scan(&valid); err != nil {
				return 0, nil, err
			}
			if !valid {
				return 0, nil, apperrors.InvalidRequest("Unknown approver capability.")
			}
		}
		if p.ActorRoleKey != nil {
			var valid bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM role_templates WHERE organisation_id=$1 AND role_key=$2 AND active)`, t.Organisation, *p.ActorRoleKey).Scan(&valid); err != nil {
				return 0, nil, err
			}
			if !valid {
				return 0, nil, apperrors.InvalidRequest("Unknown actor role template.")
			}
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return 0, nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO approval_policies(id,organisation_id,policy_key,version,action,risk,active,definition,effective_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,true,$7,$8,$9)`, p.ID, t.Organisation, key, p.Version, p.Action, p.Risk, raw, p.EffectiveAt, p.ExpiresAt)
		if database.IsUniqueViolation(err, "") {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Policy version has changed.")
		}
		if err != nil {
			return 0, nil, err
		}
		if err := audit(ctx, tx, t, "APPROVAL_POLICY_VERSION_CREATED", "approval_policy", p.ID, map[string]any{"policy_key": key, "version": p.Version, "previous_id": previous}); err != nil {
			return 0, nil, err
		}
		return 201, map[string]any{"policy": p, "policy_key": key}, nil
	})
}
func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid policy identifier."))
		return
	}
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	var raw json.RawMessage
	var active bool
	var key ids.ID
	err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT definition,active,policy_key FROM approval_policies WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&raw, &active, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		err = apperrors.NotFound("POLICY_NOT_FOUND", "Policy not found.")
	}
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	var p approvals.Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if p.Scope == nil {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Policy scope is unavailable."))
		return
	}
	if _, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "policies:read", p.Scope.Kind, p.Scope.ID, false); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	p.Active = active
	reply(w, r, 200, map[string]any{"policy": p, "policy_key": key}, nil)
}
func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if !t.Membership.RoleCapabilities.Has("policies:read") {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Capability denied."))
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT definition,active,policy_key FROM approval_policies WHERE organisation_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, t.Organisation, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	type entry struct {
		p      approvals.Policy
		key    ids.ID
		active bool
	}
	entries := []entry{}
	for rows.Next() {
		var raw []byte
		var e entry
		if err := rows.Scan(&raw, &e.active, &e.key); err != nil {
			rows.Close()
			reply(w, r, 0, nil, err)
			return
		}
		if err := json.Unmarshal(raw, &e.p); err != nil {
			rows.Close()
			reply(w, r, 0, nil, err)
			return
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	items := []map[string]any{}
	for _, e := range entries {
		if e.p.Scope == nil {
			continue
		}
		if _, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "policies:read", e.p.Scope.Kind, e.p.Scope.ID, false); err == nil {
			e.p.Active = e.active
			items = append(items, map[string]any{"policy": e.p, "policy_key": e.key})
		} else if _, ok := err.(*apperrors.Error); !ok {
			reply(w, r, 0, nil, err)
			return
		}
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, nil)
}
func (h *Handler) activatePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid policy identifier."))
		return
	}
	var in struct {
		Active  bool  `json:"active"`
		Version int64 `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	h.execute(w, r, "policy:activation:"+id.String(), in, "policies:write", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "policies:write", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		var key ids.ID
		var version int64
		if err := tx.QueryRow(ctx, `SELECT policy_key,version FROM approval_policies WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id).Scan(&key, &version); err != nil {
			return 0, nil, apperrors.NotFound("POLICY_NOT_FOUND", "Policy not found.")
		}
		if version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Policy version has changed.")
		}
		if in.Active {
			var latest int64
			if err := tx.QueryRow(ctx, `SELECT max(version) FROM approval_policies WHERE organisation_id=$1 AND policy_key=$2`, t.Organisation, key).Scan(&latest); err != nil {
				return 0, nil, err
			}
			if latest != version {
				return 0, nil, apperrors.Conflict("POLICY_SUPERSEDED", "Only the latest policy version can be activated.")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE approval_policies SET active=$3 WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.Active); err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"policy_id": id, "active": in.Active}, audit(ctx, tx, t, "APPROVAL_POLICY_ACTIVATION_CHANGED", "approval_policy", id, in)
	})
}

func (h *Handler) findPolicy(ctx context.Context, q database.Querier, t tenant, target workforce.Target, action, risk string, amount *int64, currency, channel string) (approvals.Policy, error) {
	var roleKey ids.ID
	if err := q.QueryRow(ctx, `SELECT role_key FROM role_templates WHERE organisation_id=$1 AND id=$2`, t.Organisation, t.Role).Scan(&roleKey); err != nil {
		return approvals.Policy{}, err
	}
	rows, err := q.Query(ctx, `SELECT definition FROM approval_policies WHERE organisation_id=$1 AND action=$2 AND risk=$3 AND active AND effective_at<=$4 AND (expires_at IS NULL OR expires_at>$4) ORDER BY effective_at DESC,version DESC,id`, t.Organisation, action, risk, h.Auth.Service.Now().UTC())
	if err != nil {
		return approvals.Policy{}, err
	}
	defer rows.Close()
	candidates := []approvals.Policy{}
	for rows.Next() {
		var raw []byte
		var p approvals.Policy
		if err := rows.Scan(&raw); err != nil {
			return p, err
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, err
		}
		if p.Applies(t.Organisation, action, risk, amount, currency, h.Auth.Service.Now().UTC()) && p.MatchesTarget(target, roleKey) && (p.Channel == "" || p.Channel == channel) {
			candidates = append(candidates, p)
		}
	}
	if err := rows.Err(); err != nil {
		return approvals.Policy{}, err
	}
	if len(candidates) == 0 {
		return approvals.Policy{}, apperrors.PermissionDenied("No effective approval policy matches this action and scope.")
	}
	// Multiple independent active policies cannot silently reduce the required
	// controls. Require the administrator to make the governing policy explicit.
	if len(candidates) > 1 {
		return approvals.Policy{}, apperrors.Conflict("AMBIGUOUS_APPROVAL_POLICY", "Multiple active approval policies match this action and scope.")
	}
	return candidates[0], nil
}
