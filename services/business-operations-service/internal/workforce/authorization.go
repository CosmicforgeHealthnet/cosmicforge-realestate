// Package workforce owns business membership and scoped authorization rules.
package workforce

import (
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
)

var ErrDenied = errors.New("business authorization denied")

type Scope struct {
	Organisation ids.ID `json:"organisation_id"`
	Kind         string `json:"kind"`
	ID           ids.ID `json:"id"`
}

func (s Scope) Valid() bool {
	if s.Organisation.ValidateV7() != nil || s.ID.ValidateV7() != nil {
		return false
	}
	switch s.Kind {
	case "COMPANY":
		return s.ID == s.Organisation
	case "REGION", "BRANCH", "DEPARTMENT", "TEAM", "PORTFOLIO", "PROPERTY", "DEVELOPMENT", "UNIT", "FACILITY", "DEVELOPMENT_PHASE", "PARCEL", "BUILDING", "BLOCK", "FLOOR", "ROOM_TYPE", "ROOM", "COMMON_AREA", "ASSET":
		return true
	}
	return false
}

// Ancestors must be resolved from trusted organisation-owned hierarchy data.
// A future inventory adapter supplies portfolio/property ancestors; untrusted
// request bodies must never supply this path to the evaluator.
type Target struct {
	Scope     Scope
	Ancestors []Scope
}

type Grant struct {
	Capability       permissions.Permission
	Scope            Scope
	Inherit          bool
	Deny             bool
	EffectiveAt      time.Time
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
	AmountLimitMinor *int64
	Currency         string
	RequireMFA       bool
}

func (g Grant) active(at time.Time) bool {
	return g.RevokedAt == nil && !at.Before(g.EffectiveAt) && (g.ExpiresAt == nil || at.Before(*g.ExpiresAt))
}
func (g Grant) matches(target Target) bool {
	if !g.Scope.Valid() || !target.Scope.Valid() || g.Scope.Organisation != target.Scope.Organisation {
		return false
	}
	if g.Scope == target.Scope {
		return true
	}
	if !g.Inherit {
		return false
	}
	for _, s := range target.Ancestors {
		if s.Valid() && s == g.Scope {
			return true
		}
	}
	return false
}

// ScopeContains evaluates only a trusted, resolved hierarchy path.
func ScopeContains(parent Scope, target Target, inherit bool) bool {
	return (Grant{Scope: parent, Inherit: inherit}).matches(target)
}

type Membership struct {
	AccountID, OrganisationID ids.ID
	Status                    string
	EffectiveAt               time.Time
	ExpiresAt                 *time.Time
	// RoleCapabilities is resolved from the current immutable role version,
	// never from a client-supplied role name or access-token role claim.
	RoleCapabilities permissions.Set
	Grants           []Grant
}

type DecisionInput struct {
	AccountID    ids.ID
	Target       Target
	Capability   permissions.Permission
	Now          time.Time
	AmountMinor  *int64
	Currency     string
	MFAAt        *time.Time
	StepUpWindow time.Duration
	Restricted   bool
}

func RecentMFA(at *time.Time, now time.Time, window time.Duration) bool {
	return at != nil && window > 0 && !at.After(now) && now.Sub(*at) < window
}

// Authorize fails closed. It combines a live membership, explicit role
// capability and a matching grant; any applicable deny wins. Loading these
// records afresh is required for immediate revocation enforcement.
func Authorize(m Membership, in DecisionInput) error {
	if in.Now.IsZero() || in.AccountID.ValidateV7() != nil || in.AccountID != m.AccountID || !in.Target.Scope.Valid() || m.OrganisationID != in.Target.Scope.Organisation || m.Status != "ACTIVE" || in.Now.Before(m.EffectiveAt) || (m.ExpiresAt != nil && !in.Now.Before(*m.ExpiresAt)) || in.Restricted || !in.Capability.Valid() || !m.RoleCapabilities.Has(in.Capability) {
		return ErrDenied
	}
	if in.AmountMinor != nil && (*in.AmountMinor < 0 || !validCurrency(in.Currency)) {
		return ErrDenied
	}
	allowed := false
	for _, g := range m.Grants {
		if g.Capability != in.Capability || !g.active(in.Now) || !g.matches(in.Target) {
			continue
		}
		if g.Deny {
			return ErrDenied
		}
		if g.RequireMFA && !RecentMFA(in.MFAAt, in.Now, in.StepUpWindow) {
			continue
		}
		if g.AmountLimitMinor != nil && (*g.AmountLimitMinor < 0 || in.AmountMinor == nil || !validCurrency(g.Currency) || in.Currency != g.Currency || *in.AmountMinor > *g.AmountLimitMinor) {
			continue
		}
		allowed = true
	}
	if !allowed {
		return ErrDenied
	}
	return nil
}

func validCurrency(s string) bool {
	_, err := money.ParseCurrency(s)
	return err == nil
}

// ValidateDelegation proves a temporary grant is a subset of direct authority.
// Callers must supply only direct grants; delegation cannot be re-delegated.
func ValidateDelegation(delegator Membership, delegate ids.ID, grant Grant, target Target, now time.Time, maxDuration time.Duration) error {
	if delegator.ExpiresAt != nil && (grant.ExpiresAt == nil || grant.ExpiresAt.After(*delegator.ExpiresAt)) {
		return ErrDenied
	}
	if delegate.ValidateV7() != nil || delegate == delegator.AccountID || grant.Deny || grant.RevokedAt != nil || grant.ExpiresAt == nil || grant.EffectiveAt.Before(now) || !grant.ExpiresAt.After(grant.EffectiveAt) || maxDuration <= 0 || grant.ExpiresAt.Sub(grant.EffectiveAt) > maxDuration || grant.Scope != target.Scope {
		return ErrDenied
	}
	input := DecisionInput{AccountID: delegator.AccountID, Target: target, Capability: grant.Capability, Now: now, AmountMinor: grant.AmountLimitMinor, Currency: grant.Currency}
	// MFA can never be removed by delegating. The command itself must separately
	// enforce the delegator's recent step-up authentication.
	for _, g := range delegator.Grants {
		if g.Capability != grant.Capability || !g.active(now) || !g.matches(target) || g.Deny {
			continue
		}
		if grant.Inherit && !g.Inherit {
			continue
		}
		if g.ExpiresAt != nil && grant.ExpiresAt.After(*g.ExpiresAt) {
			continue
		}
		if grant.EffectiveAt.Before(g.EffectiveAt) {
			continue
		}
		if g.RequireMFA && !grant.RequireMFA {
			continue
		}
		if g.AmountLimitMinor != nil && (grant.AmountLimitMinor == nil || *grant.AmountLimitMinor > *g.AmountLimitMinor || grant.Currency != g.Currency) {
			continue
		}
		// Evaluate denies and membership independently of a command's MFA context.
		copyMembership := delegator
		copyMembership.Grants = append([]Grant(nil), delegator.Grants...)
		for i := range copyMembership.Grants {
			copyMembership.Grants[i].RequireMFA = false
		}
		if Authorize(copyMembership, input) == nil {
			return nil
		}
	}
	return ErrDenied
}
