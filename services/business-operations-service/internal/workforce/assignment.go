package workforce

import "time"

// ValidateAssignment ensures that a directly authorized administrator cannot
// assign capabilities, scopes, duration or limits beyond their own authority.
// Commands separately require scoped staff/permission management and MFA.
func ValidateAssignment(actor Membership, grant Grant, target Target, now time.Time) error {
	if !grant.Capability.Valid() || grant.Scope != target.Scope || !grant.Scope.Valid() || grant.EffectiveAt.Before(now.Add(-5*time.Second)) || grant.ExpiresAt != nil && !grant.ExpiresAt.After(grant.EffectiveAt) || grant.AmountLimitMinor != nil && (*grant.AmountLimitMinor < 0 || !validCurrency(grant.Currency)) {
		return ErrDenied
	}
	if actor.ExpiresAt != nil && (grant.ExpiresAt == nil || grant.ExpiresAt.After(*actor.ExpiresAt)) {
		return ErrDenied
	}
	input := DecisionInput{AccountID: actor.AccountID, Target: target, Capability: grant.Capability, Now: now, AmountMinor: grant.AmountLimitMinor, Currency: grant.Currency}
	copyActor := actor
	copyActor.Grants = append([]Grant(nil), actor.Grants...)
	for i := range copyActor.Grants {
		copyActor.Grants[i].RequireMFA = false
	}
	if Authorize(copyActor, input) != nil {
		return ErrDenied
	}
	for _, direct := range actor.Grants {
		if direct.Capability != grant.Capability || !direct.active(now) || !direct.matches(target) || direct.Deny || grant.Inherit && !direct.Inherit || direct.RequireMFA && !grant.RequireMFA {
			continue
		}
		if direct.ExpiresAt != nil && (grant.ExpiresAt == nil || grant.ExpiresAt.After(*direct.ExpiresAt)) {
			continue
		}
		if grant.EffectiveAt.Before(direct.EffectiveAt) {
			continue
		}
		if direct.AmountLimitMinor != nil && (grant.AmountLimitMinor == nil || *grant.AmountLimitMinor > *direct.AmountLimitMinor || grant.Currency != direct.Currency) {
			continue
		}
		return nil
	}
	return ErrDenied
}

func ValidMembershipTransition(from, to string) bool {
	switch from {
	case "INVITED":
		return to == "ACTIVE" || to == "TERMINATED"
	case "ACTIVE":
		return to == "SUSPENDED" || to == "HANDOVER_PENDING" || to == "TERMINATED"
	case "SUSPENDED":
		return to == "ACTIVE" || to == "HANDOVER_PENDING" || to == "TERMINATED"
	case "HANDOVER_PENDING":
		return to == "SUSPENDED" || to == "TERMINATED"
	}
	return false
}
