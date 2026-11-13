// Package cfmeet is the Business Operations port to CF Meet, the authoritative
// human conversation system (ADR-0003). The authenticated client in client.go
// speaks CF Meet's published public Messaging contract (messaging v1.16.0,
// repository CosmicforgeHealthnet/cfmeet,
// packages/contracts/bundled/public/messaging/v1.openapi.json). Without a
// configured CF Meet API credential the default implementation fails closed and
// Business Operations keeps contexts PENDING_AUTHORITY. Business Operations
// never reads CF Meet storage and never stores native message bodies.
package cfmeet

import (
	"context"
	"errors"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

// ErrContractUnavailable means no authenticated CF Meet contract is configured.
var ErrContractUnavailable = errors.New("cfmeet: owning service contract unavailable")

// ErrRejected is a definite CF Meet refusal (authorization or validation).
var ErrRejected = errors.New("cfmeet: request rejected by owning service")

// ErrPrincipalUnmapped means a Real Estate account has no CF Meet principal in
// the configured Platform Access environment yet.
var ErrPrincipalUnmapped = errors.New("cfmeet: account has no CF Meet principal")

// CFMeetContextRequest asks CF Meet to create (idempotently) a business
// conversation context. ContextID is the Business Operations reference and the
// idempotency key; participants are business account identifiers authorised
// by Business Operations RBAC at request time.
type CFMeetContextRequest struct {
	ContextID      ids.ID   `json:"context_id"`
	OrganisationID ids.ID   `json:"organisation_id"`
	PropertyID     ids.ID   `json:"property_id"`
	ReservationID  *ids.ID  `json:"reservation_id,omitempty"`
	Subject        string   `json:"subject"`
	Participants   []ids.ID `json:"participants"`
}

// CFMeetContext is the authoritative CF Meet readback.
type CFMeetContext struct {
	ConversationRef string `json:"conversation_ref"`
	DeepLinkRef     string `json:"deep_link_ref"`
	State           string `json:"state"`
	// PendingParticipants lists Business participants that CF Meet could not
	// admit yet because they have no CF Meet principal.
	PendingParticipants []ids.ID `json:"pending_participants,omitempty"`
}

// API is implemented only by an authenticated client for the deployed CF Meet
// contract (or a signed contract double in tests).
type API interface {
	EnsureContext(ctx context.Context, in CFMeetContextRequest) (CFMeetContext, error)
	ReadContext(ctx context.Context, conversationRef string) (CFMeetContext, error)
	RevokeParticipant(ctx context.Context, conversationRef string, account ids.ID) error
}

// Unavailable is the default until a CF Meet API credential is configured.
type Unavailable struct{}

func (Unavailable) EnsureContext(context.Context, CFMeetContextRequest) (CFMeetContext, error) {
	return CFMeetContext{}, ErrContractUnavailable
}
func (Unavailable) ReadContext(context.Context, string) (CFMeetContext, error) {
	return CFMeetContext{}, ErrContractUnavailable
}
func (Unavailable) RevokeParticipant(context.Context, string, ids.ID) error {
	return ErrContractUnavailable
}
