// Package organisation owns business lifecycle and operating capacity rules.
package organisation

import (
	"errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"time"
)

var ErrTransition = errors.New("organisation transition denied")

type State string

const (
	Draft          State = "DRAFT"
	KYBPending     State = "KYB_PENDING"
	Active         State = "ACTIVE"
	Restricted     State = "RESTRICTED"
	Suspended      State = "SUSPENDED"
	ClosurePending State = "CLOSURE_PENDING"
	Closed         State = "CLOSED"
)

type Verification struct {
	CaseID    ids.ID
	Status    string
	ExpiresAt *time.Time
}

func (v Verification) Current(now time.Time) bool {
	return !now.IsZero() && v.CaseID.ValidateV7() == nil && v.Status == "VERIFIED" && v.ExpiresAt != nil && now.Before(*v.ExpiresAt)
}

// Transition validates lifecycle invariants. Verification must come from the
// authenticated Verification adapter, never from a frontend status field.
func Transition(from, to State, verification Verification, now time.Time, closureReady bool) error {
	if now.IsZero() || from == to {
		return ErrTransition
	}
	ok := false
	switch from {
	case Draft:
		ok = to == KYBPending && verification.CaseID.ValidateV7() == nil || to == ClosurePending
	case KYBPending:
		ok = to == Active && verification.Current(now) || to == Restricted || to == Suspended || to == ClosurePending
	case Active:
		ok = to == Restricted || to == Suspended || to == ClosurePending
	case Restricted, Suspended:
		ok = to == Active && verification.Current(now) || to == Restricted && from == Suspended || to == Suspended && from == Restricted || to == ClosurePending
	case ClosurePending:
		ok = to == Closed && closureReady
	}
	if !ok {
		return ErrTransition
	}
	return nil
}

type Capacity struct {
	Code, Status string
	Verification Verification
	EffectiveAt  time.Time
	ExpiresAt    *time.Time
	RestrictedAt *time.Time
}

func (c Capacity) Authorizes(code string, organisationState State, now time.Time) bool {
	return code != "" && c.Code == code && organisationState == Active && c.Status == "ACTIVE" && c.RestrictedAt == nil && !now.Before(c.EffectiveAt) && c.ExpiresAt != nil && now.Before(*c.ExpiresAt) && c.Verification.Current(now)
}

// EvaluatePack requires every declared capacity; unsupported or expired
// declarations fail closed without inventing future property records.
func EvaluatePack(required []string, capacities []Capacity, state State, now time.Time) bool {
	if len(required) == 0 {
		return false
	}
	for _, code := range required {
		found := false
		for _, c := range capacities {
			if c.Authorizes(code, state, now) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
