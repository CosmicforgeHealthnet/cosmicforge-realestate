package businesshttp

import (
	"context"
	"encoding/json"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

// Permission administration is never delegated. Approval delegation retains
// its original authority actor and is checked against every decision's SoD.
func temporaryCapability(code string, emergency bool) bool {
	switch code {
	case "hierarchy:read", "staff:read", "portfolio:read", "development:read", "parcel:read", "property:read", "structure:read", "assets:read", "imports:read":
		return true
	case "hierarchy:write", "portfolio:write", "development:write", "parcel:write", "property:write", "structure:write", "assets:write", "inventory:approve":
		return !emergency
	case "approvals:read", "approvals:decide", "staff:approve", "roles:approve":
		return !emergency
	}
	return false
}

type delegatedAuthority struct {
	Grant  workforce.Grant
	Source ids.ID
}

func directMember(ctx context.Context, q database.Querier, org, member ids.ID) (workforce.Membership, error) {
	m := workforce.Membership{OrganisationID: org, RoleCapabilities: permissions.NewSet()}
	var role ids.ID
	err := q.QueryRow(ctx, `SELECT m.account_id,m.role_id,m.status,m.effective_at,m.expires_at FROM staff_memberships m JOIN role_templates r ON r.organisation_id=m.organisation_id AND r.id=m.role_id JOIN business_accounts a ON a.id=m.account_id WHERE m.organisation_id=$1 AND m.id=$2 AND r.active AND a.status='ACTIVE'`, org, member).Scan(&m.AccountID, &role, &m.Status, &m.EffectiveAt, &m.ExpiresAt)
	if err != nil {
		return m, apperrors.PermissionDenied("Active membership required.")
	}
	rows, err := q.Query(ctx, `SELECT permission FROM role_permissions WHERE role_id=$1`, role)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var code permissions.Permission
		if err := rows.Scan(&code); err != nil {
			rows.Close()
			return m, err
		}
		m.RoleCapabilities[code] = struct{}{}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return m, err
	}
	rows, err = q.Query(ctx, `SELECT capability,scope_kind,scope_id,inherit,deny,effective_at,expires_at,revoked_at,amount_limit_minor,coalesce(currency,''),require_mfa FROM staff_scope_grants WHERE organisation_id=$1 AND membership_id=$2`, org, member)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		g := workforce.Grant{Scope: workforce.Scope{Organisation: org}}
		if err := rows.Scan(&g.Capability, &g.Scope.Kind, &g.Scope.ID, &g.Inherit, &g.Deny, &g.EffectiveAt, &g.ExpiresAt, &g.RevokedAt, &g.AmountLimitMinor, &g.Currency, &g.RequireMFA); err != nil {
			rows.Close()
			return m, err
		}
		m.Grants = append(m.Grants, g)
	}
	err = rows.Err()
	rows.Close()
	return m, err
}

func (h *Handler) loadTemporary(ctx context.Context, q database.Querier, t *tenant, now time.Time) error {
	type delegation struct {
		g      workforce.Grant
		issuer ids.ID
	}
	rows, err := q.Query(ctx, `SELECT delegator_membership_id,capability,scope_kind,scope_id,inherit,effective_at,expires_at,require_mfa,amount_limit_minor,coalesce(currency,'') FROM delegation_grants WHERE organisation_id=$1 AND delegate_membership_id=$2 AND revoked_at IS NULL AND effective_at<=$3 AND expires_at>$3`, t.Organisation, t.Member, now)
	if err != nil {
		return err
	}
	grants := []delegation{}
	for rows.Next() {
		d := delegation{g: workforce.Grant{Scope: workforce.Scope{Organisation: t.Organisation}}}
		if err := rows.Scan(&d.issuer, &d.g.Capability, &d.g.Scope.Kind, &d.g.Scope.ID, &d.g.Inherit, &d.g.EffectiveAt, &d.g.ExpiresAt, &d.g.RequireMFA, &d.g.AmountLimitMinor, &d.g.Currency); err != nil {
			rows.Close()
			return err
		}
		grants = append(grants, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range grants {
		if !temporaryCapability(string(d.g.Capability), false) {
			continue
		}
		issuer, err := directMember(ctx, q, t.Organisation, d.issuer)
		if err != nil {
			continue
		}
		target, err := resolve(ctx, q, t.Organisation, d.g.Scope.Kind, d.g.Scope.ID)
		if err != nil {
			continue
		}
		current := d.g
		current.EffectiveAt = now
		if workforce.ValidateDelegation(issuer, t.Account, current, target, now, 30*24*time.Hour) == nil {
			t.Membership.Grants = append(t.Membership.Grants, d.g)
			t.Delegated = append(t.Delegated, delegatedAuthority{Grant: d.g, Source: issuer.AccountID})
		}
	}
	// Emergency authority is explicitly recorded, short lived, read only and
	// cannot remove existing denies or the role's explicit capability ceiling.
	var eligible bool
	if err := q.QueryRow(ctx, `SELECT senior_emergency_eligible FROM role_templates WHERE organisation_id=$1 AND id=$2 AND active`, t.Organisation, t.Role).Scan(&eligible); err != nil {
		return err
	}
	if !eligible || !workforce.RecentMFA(t.MFAAt, now, 5*time.Minute) {
		return nil
	}
	rows, err = q.Query(ctx, `SELECT id,scope_kind,scope_id,capabilities,created_at,expires_at FROM break_glass_grants WHERE organisation_id=$1 AND membership_id=$2 AND revoked_at IS NULL AND expires_at>$3`, t.Organisation, t.Member, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, scope ids.ID
		var kind string
		var caps []string
		var start, end time.Time
		if err := rows.Scan(&id, &kind, &scope, &caps, &start, &end); err != nil {
			return err
		}
		t.Emergency = append(t.Emergency, id)
		for _, capability := range caps {
			if temporaryCapability(capability, true) {
				t.Membership.Grants = append(t.Membership.Grants, workforce.Grant{Capability: permissions.Permission(capability), Scope: workforce.Scope{Organisation: t.Organisation, Kind: kind, ID: scope}, Inherit: true, EffectiveAt: start, ExpiresAt: &end, RequireMFA: true})
			}
		}
	}
	return rows.Err()
}

func (h *Handler) registerTemporary(r chi.Router) {
	r.Get("/delegations", h.listDelegations)
	r.Post("/delegations", h.createDelegation)
	r.Delete("/delegations/{id}", h.revokeDelegation)
	r.Get("/break-glass", h.listEmergency)
	r.Post("/break-glass", h.createEmergency)
	r.Delete("/break-glass/{id}", h.revokeEmergency)
	r.Post("/break-glass/{id}/review", h.reviewEmergency)
}

type delegationCommand struct {
	Delegate ids.ID            `json:"delegate_membership_id"`
	Grant    scopeGrantCommand `json:"grant"`
	Reason   string            `json:"reason"`
}

func (h *Handler) createDelegation(w http.ResponseWriter, r *http.Request) {
	var in delegationCommand
	if !decode(w, r, &in) {
		return
	}
	if !validStaffReason(in.Reason) || in.Delegate.ValidateV7() != nil || !temporaryCapability(in.Grant.Capability, false) || in.Grant.Deny || in.Grant.ExpiresAt == nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid temporary delegation."))
		return
	}
	h.execute(w, r, "delegation:create", in, "delegations:write", in.Grant.ScopeKind, in.Grant.ScopeID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "delegations:write", in.Grant.ScopeKind, in.Grant.ScopeID, true)
		if err != nil {
			return 0, nil, err
		}
		delegate, err := directMember(ctx, tx, t.Organisation, in.Delegate)
		if err != nil {
			return 0, nil, err
		}
		g := workforce.Grant{Capability: permissions.Permission(in.Grant.Capability), Scope: target.Scope, Inherit: in.Grant.Inherit, EffectiveAt: in.Grant.EffectiveAt, ExpiresAt: in.Grant.ExpiresAt, AmountLimitMinor: in.Grant.AmountLimitMinor, Currency: in.Grant.Currency, RequireMFA: in.Grant.RequireMFA}
		now := h.Auth.Service.Now().UTC()
		if g.EffectiveAt.IsZero() {
			g.EffectiveAt = now
			in.Grant.EffectiveAt = now
		}
		if delegate.Status != "ACTIVE" || now.Before(delegate.EffectiveAt) || delegate.ExpiresAt != nil && !now.Before(*delegate.ExpiresAt) || !delegate.RoleCapabilities.Has(g.Capability) || workforce.ValidateDelegation(t.Direct, delegate.AccountID, g, target, now, 30*24*time.Hour) != nil {
			return 0, nil, apperrors.PermissionDenied("Delegation exceeds direct authority or recipient capability.")
		}
		id := ids.New()
		_, err = tx.Exec(ctx, `INSERT INTO delegation_grants(id,organisation_id,delegator_membership_id,delegate_membership_id,capability,scope_kind,scope_id,inherit,effective_at,expires_at,require_mfa,amount_limit_minor,currency,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,nullif($13,''),$14)`, id, t.Organisation, t.Member, in.Delegate, string(g.Capability), g.Scope.Kind, g.Scope.ID, g.Inherit, g.EffectiveAt, g.ExpiresAt, g.RequireMFA, g.AmountLimitMinor, g.Currency, in.Reason)
		if err != nil {
			return 0, nil, err
		}
		if err = h.accessEvent(ctx, tx, t, "DELEGATION_GRANTED", "business.delegation.changed.v1", "delegation", id, in); err != nil {
			return 0, nil, err
		}
		if err = h.queueNotice(ctx, tx, delegate.AccountID, "Temporary access granted", "Your organisation granted temporary scoped access.", true); err != nil {
			return 0, nil, err
		}
		return 201, map[string]any{"delegation_id": id, "expires_at": g.ExpiresAt}, nil
	})
}
func (h *Handler) listDelegations(w http.ResponseWriter, r *http.Request) {
	h.listTemporary(w, r, false)
}
func (h *Handler) listEmergency(w http.ResponseWriter, r *http.Request) { h.listTemporary(w, r, true) }
func (h *Handler) listTemporary(w http.ResponseWriter, r *http.Request, emergency bool) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	query := `SELECT to_jsonb(x) FROM (SELECT id AS delegation_id,delegator_membership_id,delegate_membership_id,capability,scope_kind,scope_id,inherit,effective_at,expires_at,revoked_at,require_mfa,amount_limit_minor,currency,reason FROM delegation_grants WHERE organisation_id=$1 AND (delegator_membership_id=$2 OR delegate_membership_id=$2) ORDER BY created_at DESC,id LIMIT $3 OFFSET $4) x`
	if emergency {
		query = `SELECT to_jsonb(x) FROM (SELECT id AS break_glass_id,membership_id,scope_kind,scope_id,capabilities,reason,created_at,expires_at,revoked_at,reviewed_by,review_reason,reviewed_at FROM break_glass_grants WHERE organisation_id=$1 AND membership_id=$2 ORDER BY created_at DESC,id LIMIT $3 OFFSET $4) x`
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), query, t.Organisation, t.Member, limit, offset)
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
func (h *Handler) revokeDelegation(w http.ResponseWriter, r *http.Request) {
	h.revokeTemporary(w, r, false)
}
func (h *Handler) revokeEmergency(w http.ResponseWriter, r *http.Request) {
	h.revokeTemporary(w, r, true)
}
func (h *Handler) revokeTemporary(w http.ResponseWriter, r *http.Request, emergency bool) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid grant ID."))
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A revocation reason is required."))
		return
	}
	capability, table, event := "delegations:write", "delegation_grants", "business.delegation.changed.v1"
	if emergency {
		capability, table, event = "break_glass:request", "break_glass_grants", "business.security.break_glass_revoked.v1"
	}
	current, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	var grantKind string
	var grantScope ids.ID
	if err = h.Auth.Service.Pool.QueryRow(r.Context(), `SELECT scope_kind,scope_id FROM `+table+` WHERE organisation_id=$1 AND id=$2`, current.Organisation, id).Scan(&grantKind, &grantScope); err != nil {
		reply(w, r, 0, nil, apperrors.NotFound("GRANT_NOT_FOUND", "Grant not found."))
		return
	}
	// table is selected exclusively from this closed server catalogue.
	h.execute(w, r, table+":revoke:"+id.String(), in, capability, grantKind, grantScope, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, capability, grantKind, grantScope, true)
		if err != nil {
			return 0, nil, err
		}
		var kind string
		var scope, member ids.ID
		column := "delegator_membership_id"
		if emergency {
			column = "membership_id"
		}
		err = tx.QueryRow(ctx, `SELECT scope_kind,scope_id,`+column+` FROM `+table+` WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, t.Organisation, id).Scan(&kind, &scope, &member)
		if err != nil {
			return 0, nil, apperrors.NotFound("GRANT_NOT_FOUND", "Grant not found.")
		}
		if member != t.Member {
			reviewCapability, reviewKind, reviewScope := "delegations:write", "COMPANY", t.Organisation
			if emergency {
				reviewCapability, reviewKind, reviewScope = "break_glass:review", kind, scope
			}
			if _, _, err = h.authorize(ctx, tx, reviewCapability, reviewKind, reviewScope, true); err != nil {
				return 0, nil, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE `+table+` SET revoked_at=coalesce(revoked_at,now()) WHERE organisation_id=$1 AND id=$2`, t.Organisation, id); err != nil {
			return 0, nil, err
		}
		err = h.accessEvent(ctx, tx, t, "TEMPORARY_ACCESS_REVOKED", event, table, id, in)
		return 200, map[string]any{"revoked": true}, err
	})
}

type emergencyCommand struct {
	ScopeKind       string   `json:"scope_kind"`
	ScopeID         ids.ID   `json:"scope_id"`
	Capabilities    []string `json:"capabilities"`
	Reason          string   `json:"reason"`
	DurationSeconds int      `json:"duration_seconds"`
	ApprovalID      ids.ID   `json:"approval_id,omitempty"`
}

func (h *Handler) createEmergency(w http.ResponseWriter, r *http.Request) {
	var in emergencyCommand
	if !decode(w, r, &in) {
		return
	}
	if in.DurationSeconds == 0 {
		in.DurationSeconds = 300
	}
	if len(strings.TrimSpace(in.Reason)) < 20 || len(in.Reason) > 2000 || in.DurationSeconds < 30 || in.DurationSeconds > 900 || len(in.Capabilities) < 1 || len(in.Capabilities) > 2 || (in.ScopeKind != "BRANCH" && in.ScopeKind != "DEPARTMENT" && in.ScopeKind != "TEAM") {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Emergency access requires a narrow scope, written reason and 30–900 second lifetime."))
		return
	}
	for _, c := range in.Capabilities {
		if !temporaryCapability(c, true) {
			reply(w, r, 0, nil, apperrors.PermissionDenied("Emergency capability is not permitted."))
			return
		}
	}
	h.execute(w, r, "break-glass:create", in, "break_glass:request", in.ScopeKind, in.ScopeID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "break_glass:request", in.ScopeKind, in.ScopeID, true)
		if err != nil {
			return 0, nil, err
		}
		var eligible bool
		if err = tx.QueryRow(ctx, `SELECT senior_emergency_eligible FROM role_templates WHERE organisation_id=$1 AND id=$2 AND active`, t.Organisation, t.Role).Scan(&eligible); err != nil {
			return 0, nil, err
		}
		if !eligible {
			return 0, nil, apperrors.PermissionDenied("A designated senior role is required.")
		}
		for _, c := range in.Capabilities {
			if !t.Direct.RoleCapabilities.Has(permissions.Permission(c)) {
				return 0, nil, apperrors.PermissionDenied("Emergency access cannot add role capabilities.")
			}
		}
		operation := map[string]any{"scope_kind": in.ScopeKind, "scope_id": in.ScopeID, "capabilities": in.Capabilities, "reason": in.Reason, "duration_seconds": in.DurationSeconds}
		if err = h.requirePrivilegeChange(ctx, tx, t, approvals.ActionBreakGlassAccess, target.Scope, in.ApprovalID, operation); err != nil {
			return 0, nil, err
		}
		now := h.Auth.Service.Now().UTC()
		end := now.Add(time.Duration(in.DurationSeconds) * time.Second)
		id := ids.New()
		if _, err = tx.Exec(ctx, `INSERT INTO break_glass_grants(id,organisation_id,membership_id,scope_kind,scope_id,capabilities,reason,expires_at,mfa_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, t.Organisation, t.Member, in.ScopeKind, in.ScopeID, in.Capabilities, in.Reason, end, t.MFAAt, now); err != nil {
			return 0, nil, err
		}
		if err = h.accessEvent(ctx, tx, t, "BREAK_GLASS_GRANTED", "business.security.break_glass_granted.v1", "break_glass", id, operation); err != nil {
			return 0, nil, err
		}
		if err = h.notifyEmergencyReviewers(ctx, tx, t, target, id); err != nil {
			return 0, nil, err
		}
		if err = h.queueNotice(ctx, tx, t.Representative, "Emergency access", "Emergency access "+id.String()+" was activated. Independent review is required.", true); err != nil {
			return 0, nil, err
		}
		if t.Account != t.Representative {
			if err = h.queueNotice(ctx, tx, t.Account, "Emergency access", "Your short-lived emergency access is active and audited.", true); err != nil {
				return 0, nil, err
			}
		}
		return 201, map[string]any{"break_glass_id": id, "expires_at": end}, nil
	})
}
func (h *Handler) reviewEmergency(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid grant ID."))
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(strings.TrimSpace(in.Reason)) < 20 || len(in.Reason) > 2000 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A detailed review is required."))
		return
	}
	h.execute(w, r, "break-glass:review:"+id.String(), in, "break_glass:review", "COMPANY", ids.Nil, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, "break_glass:review", "COMPANY", ids.Nil, true)
		if err != nil {
			return 0, nil, err
		}
		var actor ids.ID
		var reviewed *time.Time
		if err = tx.QueryRow(ctx, `SELECT m.account_id,g.reviewed_at FROM break_glass_grants g JOIN staff_memberships m ON m.id=g.membership_id AND m.organisation_id=g.organisation_id WHERE g.organisation_id=$1 AND g.id=$2 FOR UPDATE OF g`, t.Organisation, id).Scan(&actor, &reviewed); err != nil {
			return 0, nil, apperrors.NotFound("GRANT_NOT_FOUND", "Grant not found.")
		}
		if actor == t.Account || reviewed != nil {
			return 0, nil, apperrors.Conflict("INDEPENDENT_REVIEW_REQUIRED", "An independent, single review is required.")
		}
		if _, err = tx.Exec(ctx, `UPDATE break_glass_grants SET reviewed_by=$3,review_reason=$4,reviewed_at=now(),revoked_at=coalesce(revoked_at,now()) WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, t.Account, in.Reason); err != nil {
			return 0, nil, err
		}
		err = h.accessEvent(ctx, tx, t, "BREAK_GLASS_REVIEWED", "business.security.break_glass_reviewed.v1", "break_glass", id, in)
		return 200, map[string]any{"reviewed": true, "revoked": true}, err
	})
}
func (h *Handler) accessEvent(ctx context.Context, tx pgx.Tx, t tenant, action, eventType, kind string, id ids.ID, change any) error {
	if err := audit(ctx, tx, t, action, kind, id, change); err != nil {
		return err
	}
	if err := h.Auth.Service.RecordSecurity(ctx, tx, t.Account, action, id); err != nil {
		return err
	}
	payload := map[string]any{"organisation_id": t.Organisation, "target_id": id, "action": action}
	if kind == "capacity" {
		var code, status string
		var expiry *time.Time
		if err := tx.QueryRow(ctx, `SELECT code,status,expires_at FROM organisation_capacities WHERE organisation_id=$1 AND id=$2`, t.Organisation, id).Scan(&code, &status, &expiry); err != nil {
			return err
		}
		payload["capacity_id"] = id
		payload["code"] = code
		payload["status"] = status
		payload["expires_at"] = expiry
	}
	event, err := events.New(ctx, platform.BusinessOperationsService, eventType, kind, id, payload)
	if err != nil {
		return err
	}
	return h.Auth.Service.Outbox.Add(ctx, tx, event)
}

func (h *Handler) notifyEmergencyReviewers(ctx context.Context, tx pgx.Tx, t tenant, target workforce.Target, id ids.ID) error {
	rows, err := tx.Query(ctx, `SELECT m.id FROM staff_memberships m JOIN role_permissions p ON p.role_id=m.role_id WHERE m.organisation_id=$1 AND m.status='ACTIVE' AND m.account_id<>$2 AND p.permission='break_glass:review' ORDER BY m.id`, t.Organisation, t.Account)
	if err != nil {
		return err
	}
	members := []ids.ID{}
	for rows.Next() {
		var member ids.ID
		if err := rows.Scan(&member); err != nil {
			rows.Close()
			return err
		}
		members = append(members, member)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	count := 0
	for _, memberID := range members {
		m, err := directMember(ctx, tx, t.Organisation, memberID)
		if err != nil {
			continue
		}
		for i := range m.Grants {
			m.Grants[i].RequireMFA = false
		}
		if workforce.Authorize(m, workforce.DecisionInput{AccountID: m.AccountID, Target: target, Capability: "break_glass:review", Now: h.Auth.Service.Now().UTC()}) != nil {
			continue
		}
		if err = h.queueNotice(ctx, tx, m.AccountID, "Emergency security review required", "Emergency grant "+id.String()+" requires your independent review.", true); err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return apperrors.PermissionDenied("An active independent emergency reviewer must be configured.")
	}
	return nil
}
