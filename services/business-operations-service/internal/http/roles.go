package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type roleCommand struct {
	Name           string   `json:"name"`
	Capabilities   []string `json:"capabilities"`
	Version        int64    `json:"version,omitempty"`
	SeniorEligible bool     `json:"senior_emergency_eligible"`
	ApprovalID     ids.ID   `json:"approval_id,omitempty"`
}
type roleOperation struct {
	Resource       string   `json:"resource"`
	PreviousRoleID ids.ID   `json:"previous_role_id,omitempty"`
	Name           string   `json:"name"`
	Capabilities   []string `json:"capabilities"`
	Version        int64    `json:"version,omitempty"`
	SeniorEligible bool     `json:"senior_emergency_eligible"`
}

func (h *Handler) registerRoles(r chi.Router) {
	r.Get("/permissions", h.permissionCatalogue)
	r.Get("/roles", h.listRoles)
	r.Post("/roles", func(w http.ResponseWriter, r *http.Request) { h.writeRole(w, r, false) })
	r.Get("/roles/{id}", h.getRole)
	r.Patch("/roles/{id}", func(w http.ResponseWriter, r *http.Request) { h.writeRole(w, r, true) })
	r.Post("/roles/{id}/activation", h.activateRole)
}
func (h *Handler) permissionCatalogue(w http.ResponseWriter, r *http.Request) {
	if _, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "permissions:read", "COMPANY", ids.Nil, false); err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT to_jsonb(x) FROM (SELECT code,description FROM permissions ORDER BY code LIMIT $1 OFFSET $2) x`, limit, offset)
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

const roleQuery = `SELECT to_jsonb(x) FROM (SELECT r.id AS role_id,r.role_key,r.name,r.version,r.active,r.senior_emergency_eligible,r.created_at,coalesce((SELECT jsonb_agg(permission ORDER BY permission) FROM role_permissions p WHERE p.role_id=r.id),'[]'::jsonb) AS capabilities FROM role_templates r WHERE r.organisation_id=$1 AND r.id=$2) x`

func (h *Handler) getRole(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid role identifier."))
		return
	}
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "roles:read", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	out, err := oneJSON(r.Context(), h.Auth.Service.Pool, roleQuery, t.Organisation, id)
	reply(w, r, 200, out, err)
}
func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	t, _, err := h.authorize(r.Context(), h.Auth.Service.Pool, "roles:read", "COMPANY", ids.Nil, false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT to_jsonb(x) FROM (SELECT r.id AS role_id,r.role_key,r.name,r.version,r.active,r.senior_emergency_eligible,coalesce((SELECT jsonb_agg(permission ORDER BY permission) FROM role_permissions p WHERE p.role_id=r.id),'[]'::jsonb) AS capabilities FROM role_templates r WHERE r.organisation_id=$1 ORDER BY r.name,r.version DESC,r.id LIMIT $2 OFFSET $3) x`, t.Organisation, limit, offset)
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
func (h *Handler) writeRole(w http.ResponseWriter, r *http.Request, update bool) {
	var in roleCommand
	if !decode(w, r, &in) {
		return
	}
	if len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 120 || len(in.Capabilities) < 1 || len(in.Capabilities) > 100 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A role name and explicit capabilities are required."))
		return
	}
	sort.Strings(in.Capabilities)
	for i, capability := range in.Capabilities {
		if !permissions.Permission(capability).Valid() || i > 0 && capability == in.Capabilities[i-1] {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Capabilities must be valid and distinct."))
			return
		}
	}
	previous := ids.Nil
	if update {
		var err error
		previous, err = ids.ParseV7(chi.URLParam(r, "id"))
		if err != nil || in.Version < 1 {
			reply(w, r, 0, nil, apperrors.InvalidRequest("A valid role and version are required."))
			return
		}
	}
	operation := roleOperation{Resource: "role_template", PreviousRoleID: previous, Name: in.Name, Capabilities: in.Capabilities, Version: in.Version, SeniorEligible: in.SeniorEligible}
	h.execute(w, r, "role:write:"+previous.String(), in, "roles:write", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "roles:write", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		if err := h.requirePrivilegeChange(ctx, tx, t, approvals.ActionStaffRoleChange, target.Scope, in.ApprovalID, operation); err != nil {
			return 0, nil, err
		}
		for _, capability := range in.Capabilities {
			if err := workforce.Authorize(t.Direct, workforce.DecisionInput{AccountID: t.Account, Target: target, Capability: permissions.Permission(capability), Now: h.Auth.Service.Now().UTC(), MFAAt: t.MFAAt, StepUpWindow: 5 * time.Minute}); err != nil {
				return 0, nil, apperrors.PermissionDenied("A role cannot exceed the creator's direct authority.")
			}
		}
		id, key, version := ids.New(), ids.New(), int64(1)
		if update {
			var oldVersion int64
			if err := tx.QueryRow(ctx, `SELECT role_key,version FROM role_templates WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, previous).Scan(&key, &oldVersion); errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, apperrors.NotFound("ROLE_NOT_FOUND", "Role not found.")
			} else if err != nil {
				return 0, nil, err
			}
			var latest int64
			if err := tx.QueryRow(ctx, `SELECT max(version) FROM role_templates WHERE organisation_id=$1 AND role_key=$2`, t.Organisation, key).Scan(&latest); err != nil {
				return 0, nil, err
			}
			if oldVersion != in.Version || latest != in.Version {
				return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Role version has changed.")
			}
			version = latest + 1
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_templates(id,organisation_id,role_key,name,version,senior_emergency_eligible) VALUES($1,$2,$3,$4,$5,$6)`, id, t.Organisation, key, in.Name, version, in.SeniorEligible); err != nil {
			return 0, nil, err
		}
		for _, capability := range in.Capabilities {
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(role_id,permission) VALUES($1,$2)`, id, capability); err != nil {
				return 0, nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE organisations SET authorization_version=authorization_version+1 WHERE id=$1`, t.Organisation); err != nil {
			return 0, nil, err
		}
		if err := h.accessEvent(ctx, tx, t, "ROLE_VERSION_CREATED", "business.role.changed.v1", "role_template", id, operation); err != nil {
			return 0, nil, err
		}
		out, err := oneJSON(ctx, tx, roleQuery, t.Organisation, id)
		return 201, out, err
	})
}
func (h *Handler) activateRole(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid role identifier."))
		return
	}
	var in struct {
		Version    int64  `json:"version"`
		Active     bool   `json:"active"`
		ApprovalID ids.ID `json:"approval_id,omitempty"`
	}
	if !decode(w, r, &in) {
		return
	}
	operation := map[string]any{"resource": "role_activation", "role_id": id, "version": in.Version, "active": in.Active}
	h.execute(w, r, "role:activation:"+id.String(), in, "roles:write", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "roles:write", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		if err := h.requirePrivilegeChange(ctx, tx, t, approvals.ActionStaffRoleChange, target.Scope, in.ApprovalID, operation); err != nil {
			return 0, nil, err
		}
		if !in.Active {
			var owner bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staff_memberships WHERE organisation_id=$1 AND account_id=$2 AND role_id=$3 AND status='ACTIVE')`, t.Organisation, t.Representative, id).Scan(&owner); err != nil {
				return 0, nil, err
			}
			if owner {
				return 0, nil, apperrors.Conflict("OWNER_HANDOVER_REQUIRED", "The representative's role cannot be deactivated before a controlled handover.")
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE role_templates SET active=$3 WHERE organisation_id=$1 AND id=$2 AND version=$4`, t.Organisation, id, in.Active, in.Version)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Role version has changed.")
		}
		if !in.Active {
			if _, err := tx.Exec(ctx, `UPDATE staff_invitations SET status='REVOKED',encrypted_delivery=''::bytea WHERE organisation_id=$1 AND role_id=$2 AND status='PENDING'`, t.Organisation, id); err != nil {
				return 0, nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE organisations SET authorization_version=authorization_version+1 WHERE id=$1`, t.Organisation); err != nil {
			return 0, nil, err
		}
		if err := h.accessEvent(ctx, tx, t, "ROLE_ACTIVATION_CHANGED", "business.role.changed.v1", "role_template", id, operation); err != nil {
			return 0, nil, err
		}
		out, err := oneJSON(ctx, tx, roleQuery, t.Organisation, id)
		return 200, out, err
	})
}
