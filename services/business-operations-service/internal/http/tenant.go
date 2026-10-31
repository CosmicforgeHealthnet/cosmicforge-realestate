// Package businesshttp composes business-domain transport across the bounded
// contexts. Domain rules remain in organisation, workforce and approvals.
package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/notification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workforce"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	httpx "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

type Handler struct {
	Media                   media.API
	Auth                    *businessauth.Handler
	Verification            verification.API
	Notification            notification.Sender
	InvitationAcceptURL     string
	CapacityAuthority       verification.CapacityAuthority
	RepresentativeAuthority verification.RepresentativeAuthority
	ClosureReadiness        verification.ClosureReadiness
	InventoryDependencies   portfolio.DependencyGuard
}
type tenant struct {
	Account, Organisation, Member, Role ids.ID
	State                               string
	MFAAt                               *time.Time
	Representative                      ids.ID
	Membership                          workforce.Membership
	Direct                              workforce.Membership
	Emergency                           []ids.ID
	Delegated                           []delegatedAuthority
}

func (h *Handler) load(ctx context.Context, q database.Querier) (tenant, error) {
	p, ok := businessauth.PrincipalFrom(ctx)
	if !ok || p.OrganizationID.IsZero() {
		return tenant{}, apperrors.PermissionDenied("Select an active organisation membership.")
	}
	var t tenant
	var status string
	var effective time.Time
	var expiry *time.Time
	query := `SELECT m.id,m.account_id,m.organisation_id,m.role_id,m.status,m.effective_at,m.expires_at,o.state,o.representative_account_id,s.mfa_at FROM staff_memberships m JOIN role_templates r ON r.id=m.role_id AND r.organisation_id=m.organisation_id JOIN organisations o ON o.id=m.organisation_id JOIN business_sessions s ON s.account_id=m.account_id AND s.id=$3 AND s.organisation_id=m.organisation_id WHERE m.account_id=$1 AND m.organisation_id=$2 AND r.active AND s.revoked_at IS NULL AND s.expires_at>now()`
	if _, transaction := q.(pgx.Tx); transaction {
		query += " FOR UPDATE OF o FOR SHARE OF m,r,s"
	}
	err := q.QueryRow(ctx, query, p.ActorID, p.OrganizationID, p.SessionID).Scan(&t.Member, &t.Account, &t.Organisation, &t.Role, &status, &effective, &expiry, &t.State, &t.Representative, &t.MFAAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, apperrors.PermissionDenied("Organisation access denied.")
	}
	if err != nil {
		return t, err
	}
	now := h.Auth.Service.Now().UTC()
	if status != "ACTIVE" || now.Before(effective) || expiry != nil && !now.Before(*expiry) || t.State == "CLOSED" {
		return t, apperrors.PermissionDenied("Organisation access denied.")
	}
	t.Membership = workforce.Membership{AccountID: t.Account, OrganisationID: t.Organisation, Status: status, EffectiveAt: effective, ExpiresAt: expiry, RoleCapabilities: permissions.NewSet()}
	rows, err := q.Query(ctx, `SELECT permission FROM role_permissions WHERE role_id=$1`, t.Role)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var capability permissions.Permission
		if err := rows.Scan(&capability); err != nil {
			rows.Close()
			return t, err
		}
		t.Membership.RoleCapabilities[capability] = struct{}{}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return t, err
	}
	rows, err = q.Query(ctx, `SELECT capability,scope_kind,scope_id,inherit,deny,effective_at,expires_at,revoked_at,amount_limit_minor,currency,require_mfa FROM staff_scope_grants WHERE organisation_id=$1 AND membership_id=$2`, t.Organisation, t.Member)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		g := workforce.Grant{Scope: workforce.Scope{Organisation: t.Organisation}}
		var currency *string
		if err := rows.Scan(&g.Capability, &g.Scope.Kind, &g.Scope.ID, &g.Inherit, &g.Deny, &g.EffectiveAt, &g.ExpiresAt, &g.RevokedAt, &g.AmountLimitMinor, &currency, &g.RequireMFA); err != nil {
			rows.Close()
			return t, err
		}
		if currency != nil {
			g.Currency = *currency
		}
		t.Membership.Grants = append(t.Membership.Grants, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return t, err
	}
	t.Direct = t.Membership
	t.Direct.Grants = append([]workforce.Grant(nil), t.Membership.Grants...)
	// A delayed maintenance worker must never extend verified authority.
	if t.State == "ACTIVE" {
		var current bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_links WHERE organisation_id=$1 AND purpose='KYB' AND provider_status='VERIFIED' AND expires_at>$2)`, t.Organisation, now).Scan(&current); err != nil {
			return t, err
		}
		if !current {
			t.State = "RESTRICTED"
		}
	}
	err = h.loadTemporary(ctx, q, &t, now)
	return t, err
}

// resolve never trusts ancestor paths supplied by clients. Future inventory
// references fail closed until that owning context supplies a graph adapter.
func resolve(ctx context.Context, q database.Querier, org ids.ID, kind string, id ids.ID) (workforce.Target, error) {
	target := workforce.Target{Scope: workforce.Scope{Organisation: org, Kind: kind, ID: id}}
	if !target.Scope.Valid() {
		return target, apperrors.InvalidRequest("Invalid resource scope.")
	}
	if kind == "COMPANY" {
		return target, nil
	}
	target.Ancestors = append(target.Ancestors, workforce.Scope{Organisation: org, Kind: "COMPANY", ID: org})
	var branch, department ids.ID
	var region *ids.ID
	var err error
	switch kind {
	case "REGION":
		var found ids.ID
		err = q.QueryRow(ctx, `SELECT id FROM regions WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&found)
	case "BRANCH":
		err = q.QueryRow(ctx, `SELECT region_id FROM branches WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&region)
	case "DEPARTMENT":
		err = q.QueryRow(ctx, `SELECT d.branch_id,b.region_id FROM departments d JOIN branches b ON b.id=d.branch_id AND b.organisation_id=d.organisation_id WHERE d.organisation_id=$1 AND d.id=$2`, org, id).Scan(&branch, &region)
	case "TEAM":
		err = q.QueryRow(ctx, `SELECT t.department_id,d.branch_id,b.region_id FROM teams t JOIN departments d ON d.id=t.department_id AND d.organisation_id=t.organisation_id JOIN branches b ON b.id=d.branch_id AND b.organisation_id=d.organisation_id WHERE t.organisation_id=$1 AND t.id=$2`, org, id).Scan(&department, &branch, &region)
	default:
		return portfolio.Resolve(ctx, q, org, kind, id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return target, apperrors.NotFound("RESOURCE_NOT_FOUND", "Resource not found.")
	}
	if err != nil {
		return target, err
	}
	if region != nil {
		target.Ancestors = append(target.Ancestors, workforce.Scope{Organisation: org, Kind: "REGION", ID: *region})
	}
	if !branch.IsZero() {
		target.Ancestors = append(target.Ancestors, workforce.Scope{Organisation: org, Kind: "BRANCH", ID: branch})
	}
	if !department.IsZero() {
		target.Ancestors = append(target.Ancestors, workforce.Scope{Organisation: org, Kind: "DEPARTMENT", ID: department})
	}
	return target, nil
}
func (h *Handler) authorize(ctx context.Context, q database.Querier, capability, kind string, id ids.ID, stepUp bool) (tenant, workforce.Target, error) {
	t, err := h.load(ctx, q)
	if err != nil {
		return t, workforce.Target{}, err
	}
	if kind == "COMPANY" {
		if !id.IsZero() && id != t.Organisation {
			return t, workforce.Target{}, apperrors.PermissionDenied("Organisation scope mismatch.")
		}
		id = t.Organisation
	}
	target, err := resolve(ctx, q, t.Organisation, kind, id)
	if err != nil {
		return t, target, err
	}
	restricted := (t.State == "RESTRICTED" || t.State == "SUSPENDED" || t.State == "CLOSURE_PENDING") && !strings.HasSuffix(capability, ":read") && capability != "organisation:verify" && capability != "approvals:create" && capability != "approvals:decide" && capability != "break_glass:review"
	now := h.Auth.Service.Now().UTC()
	if stepUp && !workforce.RecentMFA(t.MFAAt, now, 5*time.Minute) {
		return t, target, apperrors.PermissionDenied("Recent MFA is required.")
	}
	err = workforce.Authorize(t.Membership, workforce.DecisionInput{AccountID: t.Account, Target: target, Capability: permissions.Permission(capability), Now: now, MFAAt: t.MFAAt, StepUpWindow: 5 * time.Minute, Restricted: restricted})
	if err != nil {
		return t, target, apperrors.PermissionDenied("Capability or scope denied.")
	}
	if len(t.Emergency) > 0 && workforce.Authorize(t.Direct, workforce.DecisionInput{AccountID: t.Account, Target: target, Capability: permissions.Permission(capability), Now: now, MFAAt: t.MFAAt, StepUpWindow: 5 * time.Minute, Restricted: restricted}) != nil {
		if err := audit(ctx, q, t, "BREAK_GLASS_USED", "resource", target.Scope.ID, map[string]any{"scope": target.Scope, "capability": capability, "grants": t.Emergency}); err != nil {
			return t, target, err
		}
	}
	return t, target, nil
}
func audit(ctx context.Context, q database.Querier, t tenant, action, kind string, id ids.ID, change any) error {
	raw, err := json.Marshal(change)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_events(id,actor_id,organisation_id,action,target_type,target_id,correlation_id,change_context) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), t.Account, t.Organisation, action, kind, id, requestcontext.From(ctx).RequestID, raw)
	return err
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := httpx.DecodeJSON(w, r, v, 64<<10); err != nil {
		httpx.WriteError(w, r, err)
		return false
	}
	return true
}
func reply(w http.ResponseWriter, r *http.Request, status int, data any, err error) {
	if err != nil {
		switch database.SQLState(err) {
		case database.CodeForeignKeyViolation:
			err = apperrors.Conflict("RESOURCE_REFERENCE_CONFLICT", "The resource is still referenced or its hierarchy changed.")
		case database.CodeUniqueViolation:
			err = apperrors.Conflict("DUPLICATE_RESOURCE", "An equivalent resource already exists.")
		case database.CodeCheckViolation:
			err = apperrors.InvalidRequest("The requested values violate a resource constraint.")
		case database.CodeSerializationFailure, database.CodeDeadlockDetected:
			err = apperrors.Conflict("CONCURRENT_MODIFICATION", "The resource changed concurrently. Retry with its current version.")
		}
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteData(w, status, data)
}

func oneJSON(ctx context.Context, q database.Querier, query string, args ...any) (json.RawMessage, error) {
	var raw json.RawMessage
	err := q.QueryRow(ctx, query, args...).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperrors.NotFound("RESOURCE_NOT_FOUND", "Resource not found.")
	}
	return raw, err
}
