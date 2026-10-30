// Package federation defines adapter boundaries for configured enterprise
// providers. It never accepts IdP roles as business organisation permissions.
package federation

import (
	"context"
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"time"
)

type Protocol string

const (
	OIDC Protocol = "OIDC"
	SAML Protocol = "SAML"
)

type Challenge struct {
	OrganisationID                      ids.ID
	ProviderID                          string
	StateHash, NonceHash, PKCEChallenge string
	ExpiresAt                           time.Time
	ConsumedAt                          *time.Time
}
type Assertion struct {
	StateHash                             string
	ProviderID, Issuer, Subject, Audience string
	NonceHash                             string
	AuthenticatedAt, ExpiresAt            time.Time
	MFA                                   bool
}
type Provider interface {
	Protocol() Protocol
	Begin(context.Context, Challenge) (string, error)
	Verify(context.Context, Challenge, []byte) (Assertion, error)
}

func Validate(c Challenge, a Assertion, issuer, audience string, now time.Time) error {
	if c.OrganisationID.ValidateV7() != nil || c.ProviderID == "" || c.ProviderID != a.ProviderID || c.ConsumedAt != nil || c.StateHash == "" || c.StateHash != a.StateHash || c.NonceHash == "" || c.NonceHash != a.NonceHash || !now.Before(c.ExpiresAt) || issuer == "" || audience == "" || a.Issuer != issuer || a.Audience != audience || a.Subject == "" || len(a.Subject) > 512 || a.AuthenticatedAt.IsZero() || a.AuthenticatedAt.After(now) || !now.Before(a.ExpiresAt) {
		return errors.New("federation assertion denied")
	}
	return nil
}

// Provisioning requests map external immutable subjects to local accounts.
// Implementations must execute the same scoped workforce commands, approvals,
// audit and session revocation as interactive changes; group names never grant
// authority. Credentials and unvalidated provider claims are not accepted.
type ProvisioningCommand struct {
	OrganisationID, ActorID                                ids.ID
	ProviderID, ExternalSubject, IdempotencyKey, Operation string
	ExpectedVersion                                        int64
	MembershipID, RoleID, ApprovalID                       ids.ID
}
type Provisioner interface {
	Apply(context.Context, ProvisioningCommand) error
}
