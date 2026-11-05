package workforce

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
)

// LoadDirectGrants shares current role and scope loading between authenticated
// requests and narrowly recorded worker authority. It adds no temporary grants.
func LoadDirectGrants(ctx context.Context, q database.Querier, org, member, role ids.ID, m *Membership) error {
	rows, err := q.Query(ctx, `SELECT permission FROM role_permissions WHERE role_id=$1`, role)
	if err != nil {
		return err
	}
	for rows.Next() {
		var capability permissions.Permission
		if err := rows.Scan(&capability); err != nil {
			rows.Close()
			return err
		}
		m.RoleCapabilities[capability] = struct{}{}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT capability,scope_kind,scope_id,inherit,deny,effective_at,expires_at,revoked_at,amount_limit_minor,currency,require_mfa FROM staff_scope_grants WHERE organisation_id=$1 AND membership_id=$2`, org, member)
	if err != nil {
		return err
	}
	for rows.Next() {
		g := Grant{Scope: Scope{Organisation: org}}
		var currency *string
		if err := rows.Scan(&g.Capability, &g.Scope.Kind, &g.Scope.ID, &g.Inherit, &g.Deny, &g.EffectiveAt, &g.ExpiresAt, &g.RevokedAt, &g.AmountLimitMinor, &currency, &g.RequireMFA); err != nil {
			rows.Close()
			return err
		}
		if currency != nil {
			g.Currency = *currency
		}
		m.Grants = append(m.Grants, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return nil
}
