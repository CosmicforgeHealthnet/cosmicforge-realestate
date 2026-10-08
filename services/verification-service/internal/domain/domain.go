// Package domain holds verification-service's domain model: verification
// types, subject references, the approved case state machine
// (FOUNDATION_STATE_MACHINES.md), caller policy and error codes.
package domain

import (
	"net/http"
	"time"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/fsm"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

// Type is a verification type.
type Type string

const (
	TypeIdentity               Type = "IDENTITY"
	TypeBusiness               Type = "BUSINESS"
	TypePropertyAuthority      Type = "PROPERTY_AUTHORITY"
	TypeProfessionalCredential Type = "PROFESSIONAL_CREDENTIAL"
)

// Types lists the supported verification types.
func Types() []Type {
	return []Type{TypeIdentity, TypeBusiness, TypePropertyAuthority, TypeProfessionalCredential}
}

// SubjectType classifies what is verified.
type SubjectType string

const (
	SubjectUser         SubjectType = "USER"
	SubjectBusiness     SubjectType = "BUSINESS"
	SubjectProperty     SubjectType = "PROPERTY"
	SubjectProfessional SubjectType = "PROFESSIONAL"
)

// Subject is a domain-aware reference to the verified entity. The ID is
// owned by SubjectDomain; verification-service never reads that domain's
// database.
type Subject struct {
	Type   SubjectType     `json:"subject_type"`
	Domain platform.Domain `json:"subject_domain"`
	ID     ids.ID          `json:"subject_id"`
}

// Status is a verification case state.
type Status string

const (
	StatusDraft        Status = "DRAFT"
	StatusSubmitted    Status = "SUBMITTED"
	StatusProcessing   Status = "PROCESSING"
	StatusManualReview Status = "MANUAL_REVIEW"
	StatusVerified     Status = "VERIFIED"
	StatusRejected     Status = "REJECTED"
	StatusExpired      Status = "EXPIRED"
)

// Substatus is internal PROCESSING detail. Ambiguous provider outcomes stay
// PROCESSING/UNKNOWN_PROVIDER_RESULT until read-back resolves them.
type Substatus string

const (
	SubstatusAwaitingProvider      Substatus = "AWAITING_PROVIDER"
	SubstatusProviderSubmitted     Substatus = "PROVIDER_SUBMITTED"
	SubstatusUnknownProviderResult Substatus = "UNKNOWN_PROVIDER_RESULT"
)

// Machine is exactly the approved verification state machine:
//
//	DRAFT -> SUBMITTED -> PROCESSING -> {VERIFIED, REJECTED, MANUAL_REVIEW}
//	MANUAL_REVIEW -> {VERIFIED, REJECTED}
//	VERIFIED -> EXPIRED
var Machine = fsm.New(map[Status][]Status{
	StatusDraft:        {StatusSubmitted},
	StatusSubmitted:    {StatusProcessing},
	StatusProcessing:   {StatusVerified, StatusRejected, StatusManualReview},
	StatusManualReview: {StatusVerified, StatusRejected},
	StatusVerified:     {StatusExpired},
})

// Open reports whether a case in s is still in progress.
func Open(s Status) bool {
	switch s {
	case StatusDraft, StatusSubmitted, StatusProcessing, StatusManualReview:
		return true
	}
	return false
}

// DecisionSource records who decided an outcome.
type DecisionSource string

const (
	SourceProvider     DecisionSource = "PROVIDER"
	SourceManualReview DecisionSource = "MANUAL_REVIEW"
	SourceSystemExpiry DecisionSource = "SYSTEM_EXPIRY"
	SourceAdmin        DecisionSource = "ADMIN"
)

// ReviewReason explains why a case needs manual review.
type ReviewReason string

const (
	ReviewProviderInconclusive ReviewReason = "PROVIDER_INCONCLUSIVE"
	ReviewProviderFailure      ReviewReason = "PROVIDER_FAILURE"
	ReviewProviderRequested    ReviewReason = "PROVIDER_REQUESTED"
)

// typeSubjects defines which subject types each verification type supports.
var typeSubjects = map[Type][]SubjectType{
	TypeIdentity:               {SubjectUser},
	TypeBusiness:               {SubjectBusiness},
	TypePropertyAuthority:      {SubjectProperty},
	TypeProfessionalCredential: {SubjectProfessional, SubjectUser},
}

// typeDomains defines which user domains may initiate each type. A domain
// service may only initiate verification of its own subjects.
var typeDomains = map[Type][]platform.Domain{
	TypeIdentity: {
		platform.DomainMarketplace, platform.DomainIndividualOwner, platform.DomainBusinessOperations,
		platform.DomainPropertyManager, platform.DomainArtisan, platform.DomainProfessionalServices, platform.DomainAdmin,
	},
	TypeBusiness: {
		platform.DomainBusinessOperations, platform.DomainPropertyManager, platform.DomainArtisan, platform.DomainProfessionalServices,
	},
	TypePropertyAuthority: {
		platform.DomainIndividualOwner, platform.DomainBusinessOperations, platform.DomainPropertyManager,
	},
	TypeProfessionalCredential: {
		platform.DomainProfessionalServices, platform.DomainArtisan,
	},
}

// ValidType reports whether t is supported.
func ValidType(t Type) bool { _, ok := typeSubjects[t]; return ok }

// SupportsSubject reports whether t can verify subject type st.
func SupportsSubject(t Type, st SubjectType) bool {
	for _, x := range typeSubjects[t] {
		if x == st {
			return true
		}
	}
	return false
}

// CanInitiate enforces the caller allow-list: caller must be the account
// authority of subject.Domain, and that domain must be approved for t.
func CanInitiate(caller platform.Service, t Type, subject Subject) error {
	callerDomain, ok := platform.DomainForService(caller)
	if !ok {
		return apperrors.PermissionDenied("Only user-domain services may initiate verifications.")
	}
	if callerDomain != subject.Domain {
		return apperrors.PermissionDenied("A service may only initiate verification of subjects in its own domain.")
	}
	for _, d := range typeDomains[t] {
		if d == callerDomain {
			return nil
		}
	}
	return apperrors.PermissionDenied("This service is not approved to initiate this verification type.")
}

// CanRead reports whether caller may read a case: the subject's own domain
// service, the service that requested it, or admin-service.
func CanRead(caller platform.Service, subjectDomain platform.Domain, requestedBy string) bool {
	if caller == platform.AdminService || string(caller) == requestedBy {
		return true
	}
	d, ok := platform.DomainForService(caller)
	return ok && d == subjectDomain
}

// Validity is how long a VERIFIED decision remains valid per type.
type Validity map[Type]time.Duration

// DefaultValidity is the platform default.
var DefaultValidity = Validity{
	TypeIdentity:               365 * 24 * time.Hour,
	TypeBusiness:               365 * 24 * time.Hour,
	TypePropertyAuthority:      730 * 24 * time.Hour,
	TypeProfessionalCredential: 365 * 24 * time.Hour,
}

// Error codes owned by verification-service.
const (
	CodeNotFound                = "VERIFICATION_NOT_FOUND"
	CodeAlreadyOpen             = "VERIFICATION_ALREADY_OPEN"
	CodeUnsupportedSubject      = "UNSUPPORTED_SUBJECT_TYPE"
	CodeInvalidType             = "INVALID_VERIFICATION_TYPE"
	CodeEvidenceRequired        = "EVIDENCE_REQUIRED"
	CodeEvidenceNotReady        = "EVIDENCE_NOT_READY"
	CodeEvidenceNotFound        = "EVIDENCE_MEDIA_NOT_FOUND"
	CodeEvidenceOwnerMismatch   = "EVIDENCE_OWNER_MISMATCH"
	CodeEvidenceLocked          = "EVIDENCE_LOCKED"
	CodeProviderUnavailable     = "PROVIDER_NOT_AVAILABLE"
	CodeReviewNotOpen           = "MANUAL_REVIEW_NOT_OPEN"
	CodeReviewAssignedToOther   = "MANUAL_REVIEW_ASSIGNED_TO_OTHER"
	CodeAdminActorRequired      = "ADMIN_ACTOR_REQUIRED"
	CodeReverifyNotAllowed      = "REVERIFY_NOT_ALLOWED"
	CodeInvalidProviderCallback = "INVALID_PROVIDER_CALLBACK"
)

// ErrNotFound is returned for missing or invisible cases (no existence leak).
func ErrNotFound() *apperrors.Error {
	return apperrors.NotFound(CodeNotFound, "Verification not found.")
}

// ErrAlreadyOpen reports a duplicate open case.
func ErrAlreadyOpen(existing ids.ID) *apperrors.Error {
	return apperrors.Conflict(CodeAlreadyOpen, "An open verification already exists for this subject and type.").
		WithDetails(apperrors.Detail{Field: "verification_id", Code: "EXISTING", Message: existing.String()})
}

// ErrProviderUnavailable reports an unknown/disabled provider.
func ErrProviderUnavailable() *apperrors.Error {
	return apperrors.New(http.StatusUnprocessableEntity, CodeProviderUnavailable, "No verification provider is available for this type.")
}
