package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) registerStaff(r chi.Router) {
	r.Get("/staff", h.listStaff)
	r.Post("/staff", h.inviteStaff)
	r.Get("/staff/me", h.staffMe)
	r.Get("/staff/{staff_id}", h.getStaff)
	r.Patch("/staff/{staff_id}", h.updateStaff)
	r.Get("/staff/invitations", h.listInvitations)
	r.Post("/staff/invitations/{id}/revoke", h.revokeInvitation)
}

const staffQuery = `SELECT to_jsonb(x) FROM (SELECT m.id AS staff_id,m.account_id,a.display_name,a.email,m.role_id,r.name AS role_name,r.version AS role_version,m.department_id,m.team_id,m.kind,m.status,m.scope_kind,m.scope_id,m.effective_at,m.expires_at,m.version,m.created_at,m.updated_at,coalesce((SELECT jsonb_agg(jsonb_build_object('grant_id',g.id,'capability',g.capability,'scope_kind',g.scope_kind,'scope_id',g.scope_id,'inherit',g.inherit,'deny',g.deny,'effective_at',g.effective_at,'expires_at',g.expires_at,'amount_limit_minor',g.amount_limit_minor,'currency',g.currency,'require_mfa',g.require_mfa) ORDER BY g.created_at,g.id) FROM staff_scope_grants g WHERE g.organisation_id=m.organisation_id AND g.membership_id=m.id AND g.revoked_at IS NULL),'[]'::jsonb) AS grants FROM staff_memberships m JOIN business_accounts a ON a.id=m.account_id JOIN role_templates r ON r.id=m.role_id AND r.organisation_id=m.organisation_id WHERE m.organisation_id=$1 AND m.id=$2) x`

func (h *Handler) staffTarget(r *http.Request, id ids.ID, capability string, stepUp bool) (tenant, workforce.Target, error) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		return t, workforce.Target{}, err
	}
	var kind string
	var scope ids.ID
	err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT scope_kind,scope_id FROM staff_memberships WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&kind, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, workforce.Target{}, apperrors.NotFound("STAFF_NOT_FOUND", "Staff membership not found.")
	}
	if err != nil {
		return t, workforce.Target{}, err
	}
	return h.authorize(r.Context(), h.Auth.Service.Pool, capability, kind, scope, stepUp)
}
func (h *Handler) getStaff(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "staff_id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid staff identifier."))
		return
	}
	t, _, err := h.staffTarget(r, id, "staff:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := oneJSON(r.Context(), h.Auth.Service.Pool, staffQuery, t.Organisation, id)
	reply(w, r, 200, out, err)
}
func (h *Handler) staffMe(w http.ResponseWriter, r *http.Request) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := oneJSON(r.Context(), h.Auth.Service.Pool, staffQuery, t.Organisation, t.Member)
	reply(w, r, 200, out, err)
}
func (h *Handler) listStaff(w http.ResponseWriter, r *http.Request) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if !t.Membership.RoleCapabilities.Has("staff:read") {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Capability denied."))
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "INVITED", "ACTIVE", "SUSPENDED", "HANDOVER_PENDING", "TERMINATED":
	default:
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid membership status filter."))
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT id,scope_kind,scope_id FROM staff_memberships WHERE organisation_id=$1 AND ($2='' OR status=$2) ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, status, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	type member struct {
		id, scope ids.ID
		kind      string
	}
	members := []member{}
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.id, &m.kind, &m.scope); err != nil {
			rows.Close()
			reply(w, r, 0, nil, err)
			return
		}
		members = append(members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	items := []json.RawMessage{}
	for _, m := range members {
		if _, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "staff:read", m.kind, m.scope, false); err != nil {
			if _, ok := err.(*apperrors.Error); ok {
				continue
			}
			reply(w, r, 0, nil, err)
			return
		}
		raw, err := oneJSON(r.Context(), h.Auth.Service.Pool, staffQuery, t.Organisation, m.id)
		if err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, raw)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "next_offset": offset + len(members)}, nil)
}
func (h *Handler) validateStaffTeam(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target, department, team *ids.ID) error {
	if team != nil && department == nil {
		return apperrors.InvalidRequest("A team assignment requires its department.")
	}
	if department != nil {
		if department.ValidateV7() != nil {
			return apperrors.InvalidRequest("Invalid department.")
		}
		resolved, err := resolve(ctx, tx, t.Organisation, "DEPARTMENT", *department)
		if err != nil {
			return err
		}
		if !workforce.ScopeContains(target.Scope, resolved, true) {
			return apperrors.PermissionDenied("Department is outside the staff management scope.")
		}
	}
	if team != nil {
		if team.ValidateV7() != nil {
			return apperrors.InvalidRequest("Invalid team.")
		}
		var belongs bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM teams WHERE organisation_id=$1 AND id=$2 AND department_id=$3 AND status='ACTIVE')`, t.Organisation, *team, *department).Scan(&belongs); err != nil {
			return err
		}
		if !belongs {
			return apperrors.PermissionDenied("Team does not belong to the assigned department.")
		}
	}
	return nil
}

type staffPatch struct {
	Version         int64                `json:"version"`
	RoleID          *ids.ID              `json:"role_id,omitempty"`
	Status          *string              `json:"status,omitempty"`
	ScopeKind       *string              `json:"scope_kind,omitempty"`
	ScopeID         *ids.ID              `json:"scope_id,omitempty"`
	DepartmentID    *ids.ID              `json:"department_id,omitempty"`
	TeamID          *ids.ID              `json:"team_id,omitempty"`
	ClearAssignment bool                 `json:"clear_assignment,omitempty"`
	Kind            *string              `json:"kind,omitempty"`
	EffectiveAt     *time.Time           `json:"effective_at,omitempty"`
	ExpiresAt       *time.Time           `json:"expires_at,omitempty"`
	ClearExpiry     bool                 `json:"clear_expiry,omitempty"`
	Grants          *[]scopeGrantCommand `json:"grants,omitempty"`
	Reason          string               `json:"reason"`
	ApprovalID      ids.ID               `json:"approval_id,omitempty"`
}

func (h *Handler) updateStaff(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "staff_id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid staff identifier."))
		return
	}
	var in staffPatch
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A current version and written reason are required."))
		return
	}
	_, primary, err := h.staffTarget(r, id, "staff:write", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	operation := map[string]any{"resource": "staff_membership", "staff_id": id, "changes": in}
	clean := in
	clean.ApprovalID = ids.Nil
	operation["changes"] = clean
	h.execute(w, r, "staff:update:"+id.String(), in, "staff:write", primary.Scope.Kind, primary.Scope.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "staff:write", primary.Scope.Kind, primary.Scope.ID, true)
		if err != nil {
			return 0, nil, err
		}
		var account, role, scope ids.ID
		var kind, status, scopeKind string
		var department, team *ids.ID
		var effective time.Time
		var expiry *time.Time
		var version int64
		err = tx.QueryRow(ctx, `SELECT account_id,role_id,department_id,team_id,kind,status,scope_kind,scope_id,effective_at,expires_at,version FROM staff_memberships WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id).Scan(&account, &role, &department, &team, &kind, &status, &scopeKind, &scope, &effective, &expiry, &version)
		if err != nil {
			return 0, nil, apperrors.NotFound("STAFF_NOT_FOUND", "Staff membership not found.")
		}
		if version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Membership version has changed.")
		}
		privilegeChange := in.RoleID != nil || in.Grants != nil || in.ScopeKind != nil || in.ScopeID != nil || in.Kind != nil || in.EffectiveAt != nil || in.ExpiresAt != nil || in.ClearExpiry || in.DepartmentID != nil || in.TeamID != nil || in.ClearAssignment || in.Status != nil && *in.Status == "ACTIVE"
		if account == t.Representative {
			return 0, nil, apperrors.Conflict("REPRESENTATIVE_HANDOVER_REQUIRED", "Representative membership changes require the verified handover workflow.")
		}
		if privilegeChange && account == t.Account {
			return 0, nil, apperrors.PermissionDenied("Self-service role or scope changes are prohibited.")
		}
		if status == "TERMINATED" {
			return 0, nil, apperrors.Conflict("MEMBERSHIP_TERMINATED", "Terminated membership is immutable.")
		}
		nextStatus := status
		if in.Status != nil {
			nextStatus = *in.Status
			if status == "INVITED" && nextStatus == "ACTIVE" {
				return 0, nil, apperrors.PermissionDenied("Invitation acceptance is required for activation.")
			}
			if !workforce.ValidMembershipTransition(status, nextStatus) {
				return 0, nil, apperrors.Conflict("MEMBERSHIP_STATE_CONFLICT", "Invalid membership lifecycle transition.")
			}
		}
		if nextStatus == "HANDOVER_PENDING" {
			return 0, nil, apperrors.InvalidRequest("Start an evidenced handover through the handover command.")
		}
		if nextStatus == "TERMINATED" && status != "INVITED" {
			var completed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_handovers WHERE organisation_id=$1 AND membership_id=$2 AND status='COMPLETED')`, t.Organisation, id).Scan(&completed); err != nil {
				return 0, nil, err
			}
			if !completed {
				return 0, nil, apperrors.Conflict("HANDOVER_REQUIRED", "Complete the acknowledged handover before termination. Suspension remains available for immediate containment.")
			}
		}
		if status == "HANDOVER_PENDING" && nextStatus == "SUSPENDED" {
			if _, err := tx.Exec(ctx, `UPDATE staff_handovers SET status='CANCELLED',cancelled_by=$3,cancelled_at=now(),completion_reason=$4,version=version+1 WHERE organisation_id=$1 AND membership_id=$2 AND status IN ('PENDING','ACKNOWLEDGED')`, t.Organisation, id, t.Account, in.Reason); err != nil {
				return 0, nil, err
			}
		}
		if privilegeChange {
			action := approvals.ActionStaffRoleChange
			if in.Grants != nil || in.ScopeKind != nil || in.ScopeID != nil {
				action = approvals.ActionStaffScopeChange
			}
			if err := h.requirePrivilegeChange(ctx, tx, t, action, primary.Scope, in.ApprovalID, operation); err != nil {
				return 0, nil, err
			}
		}
		if in.RoleID != nil {
			role = *in.RoleID
		}
		if in.ScopeKind != nil {
			scopeKind = *in.ScopeKind
		}
		if in.ScopeID != nil {
			scope = *in.ScopeID
		}
		if in.Kind != nil {
			kind = *in.Kind
		}
		if in.EffectiveAt != nil {
			effective = *in.EffectiveAt
		}
		if in.ExpiresAt != nil {
			expiry = in.ExpiresAt
		}
		if in.ClearExpiry {
			expiry = nil
		}
		if in.ClearAssignment {
			department, team = nil, nil
		}
		if in.DepartmentID != nil {
			department = in.DepartmentID
		}
		if in.TeamID != nil {
			team = in.TeamID
		}
		if !validMembershipKind(kind, expiry) || expiry != nil && !expiry.After(effective) {
			return 0, nil, apperrors.InvalidRequest("Invalid membership dates or kind.")
		}
		target, err := resolve(ctx, tx, t.Organisation, scopeKind, scope)
		if err != nil {
			return 0, nil, err
		}
		if _, _, err := h.authorize(ctx, tx, "staff:write", scopeKind, scope, true); err != nil {
			return 0, nil, err
		}
		if err := h.validateStaffTeam(ctx, tx, t, target, department, team); err != nil {
			return 0, nil, err
		}
		if (in.RoleID != nil || in.ScopeKind != nil || in.ScopeID != nil || in.ExpiresAt != nil || in.ClearExpiry || in.EffectiveAt != nil) && in.Grants == nil {
			return 0, nil, apperrors.InvalidRequest("Provide the complete new grants when changing role, scope or dates.")
		}
		if in.Grants != nil {
			grants, err := h.validateStaffGrants(ctx, tx, t, role, target, *in.Grants, effective, expiry)
			if err != nil {
				return 0, nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE staff_scope_grants SET revoked_at=$3 WHERE organisation_id=$1 AND membership_id=$2 AND revoked_at IS NULL`, t.Organisation, id, h.Auth.Service.Now().UTC()); err != nil {
				return 0, nil, err
			}
			if err := insertStaffGrants(ctx, tx, t.Organisation, id, grants); err != nil {
				return 0, nil, err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE staff_memberships SET role_id=$3,department_id=$4,team_id=$5,kind=$6,status=$7,scope_kind=$8,scope_id=$9,effective_at=$10,expires_at=$11,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2 AND version=$12`, t.Organisation, id, role, department, team, kind, nextStatus, scopeKind, scope, effective, expiry, version)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Membership version has changed.")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO staff_membership_history(id,organisation_id,membership_id,actor_id,from_status,to_status,reason) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids.New(), t.Organisation, id, t.Account, status, nextStatus, in.Reason); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE organisations SET authorization_version=authorization_version+1 WHERE id=$1`, t.Organisation); err != nil {
			return 0, nil, err
		}
		if privilegeChange || nextStatus != "ACTIVE" {
			if _, err := tx.Exec(ctx, `UPDATE business_sessions SET revoked_at=now(),revoke_reason='MEMBERSHIP_CHANGED' WHERE account_id=$1 AND organisation_id=$2 AND revoked_at IS NULL`, account, t.Organisation); err != nil {
				return 0, nil, err
			}
		}
		if nextStatus != "ACTIVE" {
			if _, err := tx.Exec(ctx, `UPDATE staff_invitations SET status='REVOKED',encrypted_delivery=''::bytea WHERE organisation_id=$1 AND (membership_id=$2 OR invited_by=$3) AND status='PENDING'`, t.Organisation, id, account); err != nil {
				return 0, nil, err
			}
		}
		if err := audit(ctx, tx, t, "STAFF_MEMBERSHIP_UPDATED", "staff_membership", id, operation); err != nil {
			return 0, nil, err
		}
		if err := h.staffEvent(ctx, tx, t.Organisation, id, account, nextStatus); err != nil {
			return 0, nil, err
		}
		if err := h.queueNotice(ctx, tx, account, "Business membership update", "Your organisation membership permissions or status changed.", true); err != nil {
			return 0, nil, err
		}
		out, err := oneJSON(ctx, tx, staffQuery, t.Organisation, id)
		return 200, out, err
	})
}
func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "staff:invite", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT to_jsonb(x) FROM (SELECT id AS invitation_id,membership_id AS staff_id,account_id,email,role_id,status,expires_at,accepted_at,created_at FROM staff_invitations WHERE organisation_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3) x`, t.Organisation, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, raw)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, rows.Err())
}
func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid invitation identifier."))
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A written reason is required."))
		return
	}
	h.execute(w, r, "staff:invitation:revoke:"+id.String(), in, "staff:invite", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "staff:invite", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE staff_invitations SET status='REVOKED',encrypted_delivery=''::bytea WHERE organisation_id=$1 AND id=$2 AND status='PENDING'`, t.Organisation, id)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("INVITATION_UNAVAILABLE", "Invitation is no longer pending.")
		}
		return 200, map[string]bool{"revoked": true}, audit(ctx, tx, t, "STAFF_INVITATION_REVOKED", "staff_invitation", id, in)
	})
}
