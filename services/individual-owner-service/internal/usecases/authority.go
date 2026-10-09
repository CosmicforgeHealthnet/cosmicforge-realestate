package usecases

import (
	"context"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/authz"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/dbx"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/individual-owner-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// PropertyAuthority resolves authorization facts from individual_owner_db:
//
//   - the property's owner account → OWNER;
//   - active ownership parties (co-owner, authorized representative,
//     estate executor) with their explicit capabilities and validity window;
//   - live manager assignments (ACTIVE/EXPIRING) of the delegated
//     property-manager / business-operations actor with the capabilities of
//     the assignment's current permission version, valid until ends_at;
//   - primary ownership record VERIFIED and unexpired → AuthorityVerified;
//   - property status RESTRICTED → AdminRestricted.
//
// It reads through the transaction in ctx when present so that the facts and
// the command observe the same snapshot.
type PropertyAuthority struct {
	Pool db.DBTX
	Now  func() time.Time
}

// PropertyFacts implements authz.AuthorityResolver.
func (a PropertyAuthority) PropertyFacts(ctx context.Context, s authz.Subject, propertyID ids.ID) (authz.PropertyFacts, error) {
	q, conn := db.New(), dbx.Q(ctx, a.Pool)
	now := a.Now().UTC()
	st, err := q.PropertyAuthorityState(ctx, conn, db.PropertyAuthorityStateParams{Now: now, PropertyID: propertyID})
	if isNoRows(err) {
		return authz.PropertyFacts{}, nil
	}
	if err != nil {
		return authz.PropertyFacts{}, err
	}
	f := authz.PropertyFacts{Exists: true, AuthorityVerified: st.AuthorityVerified, AdminRestricted: st.Status == "RESTRICTED"}
	switch {
	case s.Account != nil:
		rows, err := q.PropertyAccessForAccount(ctx, conn, db.PropertyAccessForAccountParams{PropertyID: propertyID, AccountID: s.Account.AccountID})
		if err != nil {
			return authz.PropertyFacts{}, err
		}
		for _, r := range rows {
			role, ok := partyRole(r.Role)
			if !ok {
				continue
			}
			f.Relationships = append(f.Relationships, authz.Relationship{Role: role, Capabilities: caps(r.Capabilities),
				ValidFrom: r.ValidFrom, ValidUntil: r.ValidUntil})
		}
		principals, err := q.PropertyPrincipalAccounts(ctx, conn, db.PropertyPrincipalAccountsParams{PropertyID: propertyID, Now: now})
		if err != nil {
			return authz.PropertyFacts{}, err
		}
		for _, p := range principals {
			if p != s.Account.AccountID {
				f.CoOwnerCount++
			}
		}
	case s.Kind == authz.KindService && (s.ActorDomain == platform.DomainPropertyManager || s.ActorDomain == platform.DomainBusinessOperations):
		rows, err := q.ManagerAccess(ctx, conn, db.ManagerAccessParams{PropertyID: propertyID, ManagerDomain: string(s.ActorDomain), ManagerRefID: s.ActorID})
		if err != nil {
			return authz.PropertyFacts{}, err
		}
		for _, r := range rows {
			from, until := r.StartsAt, r.EndsAt
			f.Relationships = append(f.Relationships, authz.Relationship{Role: authz.RoleManager, ReferenceID: r.ID,
				Capabilities: caps(r.Capabilities), ValidFrom: &from, ValidUntil: &until})
		}
	}
	return f, nil
}

func partyRole(r string) (authz.Role, bool) {
	switch r {
	case "OWNER":
		return authz.RoleOwner, true
	case "CO_OWNER":
		return authz.RoleCoOwner, true
	case "AUTHORIZED_REPRESENTATIVE", "ESTATE_EXECUTOR":
		return authz.RoleRepresentative, true
	}
	return "", false // beneficial owners hold no operational authority
}

func caps(in []string) []authz.Capability {
	out := make([]authz.Capability, 0, len(in))
	for _, c := range in {
		out = append(out, authz.Capability(c))
	}
	return out
}
