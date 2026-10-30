package businesshttp

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type scopeGrantCommand struct {
	Capability       string     `json:"capability"`
	ScopeKind        string     `json:"scope_kind"`
	ScopeID          ids.ID     `json:"scope_id,omitempty"`
	Inherit          bool       `json:"inherit"`
	Deny             bool       `json:"deny"`
	EffectiveAt      time.Time  `json:"effective_at,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	AmountLimitMinor *int64     `json:"amount_limit_minor,omitempty"`
	Currency         string     `json:"currency,omitempty"`
	RequireMFA       bool       `json:"require_mfa"`
}

func (h *Handler) validateStaffGrants(ctx context.Context, tx pgx.Tx, t tenant, role ids.ID, primary workforce.Target, commands []scopeGrantCommand, effective time.Time, expiry *time.Time) ([]workforce.Grant, error) {
	if len(commands) < 1 || len(commands) > 100 {
		return nil, apperrors.InvalidRequest("Between one and one hundred explicit scope grants are required.")
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT active FROM role_templates WHERE organisation_id=$1 AND id=$2`, t.Organisation, role).Scan(&active); err != nil {
		return nil, apperrors.NotFound("ROLE_NOT_FOUND", "Role not found.")
	}
	if !active {
		return nil, apperrors.PermissionDenied("The selected role is inactive.")
	}
	roles := permissions.NewSet()
	rows, err := tx.Query(ctx, `SELECT permission FROM role_permissions WHERE role_id=$1`, role)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p permissions.Permission
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		roles[p] = struct{}{}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	now := h.Auth.Service.Now().UTC()
	result := make([]workforce.Grant, 0, len(commands))
	for _, command := range commands {
		if command.ScopeKind == "" {
			command.ScopeKind = primary.Scope.Kind
			command.ScopeID = primary.Scope.ID
		}
		if command.ScopeKind == "COMPANY" && command.ScopeID.IsZero() {
			command.ScopeID = t.Organisation
		}
		target, err := resolve(ctx, tx, t.Organisation, command.ScopeKind, command.ScopeID)
		if err != nil {
			return nil, err
		}
		if !workforce.ScopeContains(primary.Scope, target, true) {
			return nil, apperrors.PermissionDenied("A staff grant cannot escape its management scope.")
		}
		if command.EffectiveAt.IsZero() {
			command.EffectiveAt = effective
			if command.EffectiveAt.Before(now) {
				command.EffectiveAt = now
			}
		}
		if command.ExpiresAt == nil {
			command.ExpiresAt = expiry
		}
		if command.EffectiveAt.Before(effective) || expiry != nil && (command.ExpiresAt == nil || command.ExpiresAt.After(*expiry)) {
			return nil, apperrors.InvalidRequest("Grant dates must fit within membership dates.")
		}
		grant := workforce.Grant{Capability: permissions.Permission(command.Capability), Scope: target.Scope, Inherit: command.Inherit, Deny: command.Deny, EffectiveAt: command.EffectiveAt, ExpiresAt: command.ExpiresAt, AmountLimitMinor: command.AmountLimitMinor, Currency: command.Currency, RequireMFA: command.RequireMFA}
		if !roles.Has(grant.Capability) || workforce.ValidateAssignment(t.Direct, grant, target, now) != nil {
			return nil, apperrors.PermissionDenied("Assigned capabilities, scope, dates, MFA or limits exceed direct authority.")
		}
		result = append(result, grant)
	}
	return result, nil
}
func insertStaffGrants(ctx context.Context, tx pgx.Tx, org, member ids.ID, grants []workforce.Grant) error {
	for _, g := range grants {
		if _, err := tx.Exec(ctx, `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,deny,effective_at,expires_at,amount_limit_minor,currency,require_mfa) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,nullif($12,''),$13)`, ids.New(), org, member, string(g.Capability), g.Scope.Kind, g.Scope.ID, g.Inherit, g.Deny, g.EffectiveAt, g.ExpiresAt, g.AmountLimitMinor, g.Currency, g.RequireMFA); err != nil {
			return err
		}
	}
	return nil
}
func validMembershipKind(kind string, expiry *time.Time) bool {
	switch kind {
	case "EMPLOYEE":
		return true
	case "CONTRACTOR", "TEMPORARY":
		return expiry != nil
	}
	return false
}
func validStaffReason(reason string) bool {
	return len(strings.TrimSpace(reason)) >= 3 && len(reason) <= 2000
}
