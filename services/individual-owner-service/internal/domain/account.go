// Package domain holds individual-owner-service domain rules that are
// independent of persistence and transport.
//
// This file implements the Section 7 account lifecycle (lifecycle family 1)
// and the account-closure lifecycle (family 21) as explicit data: valid
// states, valid transitions and the actor class authorized for each one.
// Anything not listed is rejected with INVALID_STATE_TRANSITION.
package domain

import (
	"fmt"
	"net/http"
	"regexp"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
)

// AccountStatus is the owner account lifecycle state.
type AccountStatus string

const (
	StatusPendingVerification AccountStatus = "PENDING_VERIFICATION"
	StatusActive              AccountStatus = "ACTIVE"
	StatusRestricted          AccountStatus = "RESTRICTED"
	StatusSuspended           AccountStatus = "SUSPENDED"
	StatusClosurePending      AccountStatus = "CLOSURE_PENDING"
	StatusClosed              AccountStatus = "CLOSED"
)

// AccountStatuses lists every state.
func AccountStatuses() []AccountStatus {
	return []AccountStatus{StatusPendingVerification, StatusActive, StatusRestricted, StatusSuspended, StatusClosurePending, StatusClosed}
}

// Valid reports whether s is a known state.
func (s AccountStatus) Valid() bool {
	for _, x := range AccountStatuses() {
		if x == s {
			return true
		}
	}
	return false
}

// CanAuthenticate reports whether credentials of an account in s may start
// or continue a session. Suspended and closed accounts cannot.
func (s AccountStatus) CanAuthenticate() bool {
	switch s {
	case StatusPendingVerification, StatusActive, StatusRestricted, StatusClosurePending:
		return true
	}
	return false
}

// TransitionActor is the class of principal allowed to cause a transition.
type TransitionActor string

const (
	// ActorOwner: the account holder, authenticated in their own session.
	ActorOwner TransitionActor = "OWNER"
	// ActorSystem: the service itself (verification completion, workers).
	ActorSystem TransitionActor = "SYSTEM"
	// ActorAdmin: an attributable admin-domain actor delegated by admin-service.
	ActorAdmin TransitionActor = "ADMIN"
)

// TransitionRule describes one allowed edge.
type TransitionRule struct {
	Actors []TransitionActor
	// RequiresResolution: the transition is a remediation and must carry an
	// authorized resolution reference (Section 5 D).
	RequiresResolution bool
	// RequiresStepUp: owner-initiated transitions that need fresh step-up.
	RequiresStepUp bool
}

type edge struct{ from, to AccountStatus }

// accountTransitions is the canonical account lifecycle.
//
//	PENDING_VERIFICATION -> ACTIVE            (SYSTEM: required contact verified)
//	PENDING_VERIFICATION -> SUSPENDED         (ADMIN)
//	PENDING_VERIFICATION -> CLOSURE_PENDING   (OWNER, step-up)
//	ACTIVE               -> RESTRICTED        (ADMIN)
//	ACTIVE               -> SUSPENDED         (ADMIN)
//	ACTIVE               -> CLOSURE_PENDING   (OWNER, step-up)
//	RESTRICTED           -> ACTIVE            (ADMIN, resolution required)
//	RESTRICTED           -> SUSPENDED         (ADMIN)
//	SUSPENDED            -> ACTIVE            (ADMIN, resolution required)
//	SUSPENDED            -> RESTRICTED        (ADMIN, resolution required)
//	CLOSURE_PENDING      -> ACTIVE            (OWNER cancels; contact verified)
//	CLOSURE_PENDING      -> PENDING_VERIFICATION (OWNER cancels; not yet verified)
//	CLOSURE_PENDING      -> SUSPENDED         (ADMIN)
//	CLOSURE_PENDING      -> CLOSED            (SYSTEM, after cooling-off, no holds)
//	CLOSED               -> (terminal)
//
// RESTRICTED and SUSPENDED accounts cannot request closure: the restriction
// must be resolved first so closure cannot be used to evade a review.
var accountTransitions = map[edge]TransitionRule{
	{StatusPendingVerification, StatusActive}:         {Actors: []TransitionActor{ActorSystem}},
	{StatusPendingVerification, StatusSuspended}:      {Actors: []TransitionActor{ActorAdmin}},
	{StatusPendingVerification, StatusClosurePending}: {Actors: []TransitionActor{ActorOwner}, RequiresStepUp: true},
	{StatusActive, StatusRestricted}:                  {Actors: []TransitionActor{ActorAdmin}},
	{StatusActive, StatusSuspended}:                   {Actors: []TransitionActor{ActorAdmin}},
	{StatusActive, StatusClosurePending}:              {Actors: []TransitionActor{ActorOwner}, RequiresStepUp: true},
	{StatusRestricted, StatusActive}:                  {Actors: []TransitionActor{ActorAdmin}, RequiresResolution: true},
	{StatusRestricted, StatusSuspended}:               {Actors: []TransitionActor{ActorAdmin}},
	{StatusSuspended, StatusActive}:                   {Actors: []TransitionActor{ActorAdmin}, RequiresResolution: true},
	{StatusSuspended, StatusRestricted}:               {Actors: []TransitionActor{ActorAdmin}, RequiresResolution: true},
	{StatusClosurePending, StatusActive}:              {Actors: []TransitionActor{ActorOwner}},
	{StatusClosurePending, StatusPendingVerification}: {Actors: []TransitionActor{ActorOwner}},
	{StatusClosurePending, StatusSuspended}:           {Actors: []TransitionActor{ActorAdmin}},
	{StatusClosurePending, StatusClosed}:              {Actors: []TransitionActor{ActorSystem}},
}

// AccountTransitionRule returns the rule for from -> to.
func AccountTransitionRule(from, to AccountStatus) (TransitionRule, bool) {
	r, ok := accountTransitions[edge{from, to}]
	return r, ok
}

// CheckAccountTransition validates from -> to for actor. resolution is the
// authorized remediation reference (required for RequiresResolution edges).
func CheckAccountTransition(from, to AccountStatus, actor TransitionActor, resolution string) (TransitionRule, error) {
	r, ok := AccountTransitionRule(from, to)
	if !ok {
		return TransitionRule{}, apperrors.InvalidStateTransition(string(from), string(to))
	}
	allowed := false
	for _, a := range r.Actors {
		if a == actor {
			allowed = true
		}
	}
	if !allowed {
		return TransitionRule{}, apperrors.PermissionDenied(fmt.Sprintf("Only %v may move an account from %s to %s.", r.Actors, from, to))
	}
	if r.RequiresResolution && !ValidReference(resolution) {
		return TransitionRule{}, apperrors.Unprocessable("RESOLUTION_REFERENCE_REQUIRED",
			"Reinstating an account requires an authorized resolution reference.")
	}
	return r, nil
}

// AccountTargets returns the states reachable from s (for documentation/tests).
func AccountTargets(s AccountStatus) []AccountStatus {
	var out []AccountStatus
	for _, to := range AccountStatuses() {
		if _, ok := accountTransitions[edge{s, to}]; ok {
			out = append(out, to)
		}
	}
	return out
}

var (
	reasonPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	referencePattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]{3,128}$`)
)

// ValidReasonCode reports whether code is a SCREAMING_SNAKE_CASE reason.
func ValidReasonCode(code string) bool { return reasonPattern.MatchString(code) }

// ValidReference reports whether ref is an acceptable external reference
// (case ID, ticket number) for an authorized resolution.
func ValidReference(ref string) bool { return referencePattern.MatchString(ref) }

// Domain error codes (Section 5).
func ErrAccountNotFound() *apperrors.Error {
	return apperrors.NotFound("ACCOUNT_NOT_FOUND", "Account not found.")
}

func ErrInvalidCredentials() *apperrors.Error {
	return apperrors.New(http.StatusUnauthorized, "INVALID_CREDENTIALS", "The credentials are incorrect.")
}

func ErrAccountLocked(retryAfter int) *apperrors.Error {
	return apperrors.New(http.StatusTooManyRequests, "ACCOUNT_TEMPORARILY_LOCKED",
		"Too many failed sign-in attempts. Try again later.").WithRetryAfter(retryAfter)
}

func ErrAccountUnavailable() *apperrors.Error {
	return apperrors.New(http.StatusForbidden, "ACCOUNT_UNAVAILABLE", "This account cannot sign in. Contact support.")
}

func ErrEmailTaken() *apperrors.Error {
	return apperrors.Conflict("EMAIL_ALREADY_REGISTERED", "An account with this email already exists.")
}

func ErrPhoneTaken() *apperrors.Error {
	return apperrors.Conflict("PHONE_ALREADY_REGISTERED", "This phone number is already verified on another account.")
}

func ErrInvalidCode() *apperrors.Error {
	return apperrors.Unprocessable("INVALID_VERIFICATION_CODE", "The code is invalid, expired or already used.")
}

func ErrTooManyCodes(retryAfter int) *apperrors.Error {
	return apperrors.New(http.StatusTooManyRequests, "VERIFICATION_CODE_LIMIT",
		"Too many codes requested. Try again later.").WithRetryAfter(retryAfter)
}

func ErrStepUpRequired() *apperrors.Error {
	return apperrors.New(http.StatusForbidden, "STEP_UP_REQUIRED",
		"Recent re-authentication is required for this operation.")
}

func ErrMFARequired() *apperrors.Error {
	return apperrors.New(http.StatusForbidden, "MFA_REQUIRED", "This operation requires multi-factor authentication to be enabled.")
}

func ErrSessionInvalid() *apperrors.Error {
	return apperrors.New(http.StatusUnauthorized, "SESSION_REVOKED", "The session has ended. Sign in again.")
}

func ErrVersionConflict() *apperrors.Error {
	return apperrors.Conflict("VERSION_CONFLICT", "The resource was modified concurrently. Reload and retry.")
}

func ErrPreconditionRequired() *apperrors.Error {
	return apperrors.New(http.StatusPreconditionRequired, "PRECONDITION_REQUIRED", "An If-Match header with the current version is required.")
}

func ErrNotificationUnavailable(outcome apperrors.Outcome, cause error) *apperrors.Error {
	return apperrors.New(http.StatusServiceUnavailable, "NOTIFICATION_UNAVAILABLE",
		"The code could not be delivered right now. Try again shortly.").WithOutcome(outcome).WithCause(cause)
}

func ErrFeatureUnavailable(code, message string) *apperrors.Error {
	return apperrors.New(http.StatusServiceUnavailable, code, message)
}
