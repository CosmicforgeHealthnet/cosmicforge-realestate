// Package esign is the provider-neutral electronic-signature adapter
// contract (spec §12 "Electronic signature"). The owner service creates
// envelopes for an immutable agreement version, reads envelope state back
// (never repeating an ambiguous side effect blindly) and stores the signed
// artifact / certificate only as media-document-service IDs.
//
// No provider is selected for this platform yet, so the service is built
// with a nil API: electronic signing answers 503 FEATURE_UNAVAILABLE and
// owners use the OFFLINE signing method (signed scan + counterparty
// confirmation) instead.
package esign

import (
	"context"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// Signer is one person asked to sign.
type Signer struct {
	ParticipantID ids.ID
	Name          string
	Email         string
	Order         int
}

// EnvelopeRequest asks the provider to collect signatures on a document.
type EnvelopeRequest struct {
	AgreementID     ids.ID
	VersionNo       int32
	DocumentMediaID ids.ID
	Signers         []Signer
}

// Signer statuses reported by the provider.
const (
	SignerSent     = "SENT"
	SignerViewed   = "VIEWED"
	SignerSigned   = "SIGNED"
	SignerDeclined = "DECLINED"
	SignerExpired  = "EXPIRED"
)

// SignerStatus is the provider's view of one signer.
type SignerStatus struct {
	ParticipantID ids.ID
	SignerRef     string
	Status        string
	SignedAt      *time.Time
	Reason        string
}

// Envelope is the provider's view of a signature envelope.
type Envelope struct {
	Ref     string
	Status  string // SENT | COMPLETED | DECLINED | EXPIRED | VOIDED
	Signers []SignerStatus
	// CertificateMediaID is the completion certificate stored in
	// media-document-service by the adapter once the envelope completes.
	CertificateMediaID *ids.ID
	// SignedDocumentMediaID is the final signed document (media service).
	SignedDocumentMediaID *ids.ID
}

// API is the adapter contract.
type API interface {
	// CreateEnvelope is idempotent per idemKey: a retry after an ambiguous
	// failure returns the envelope created by the first attempt.
	CreateEnvelope(ctx context.Context, req EnvelopeRequest, idemKey string) (Envelope, error)
	GetEnvelope(ctx context.Context, ref string) (Envelope, error)
	VoidEnvelope(ctx context.Context, ref, reason string) error
}
