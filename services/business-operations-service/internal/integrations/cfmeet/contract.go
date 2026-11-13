// Package cfmeet is the Business Operations port to CF Meet, the authoritative
// human conversation system (ADR-0003). No deployed CF Meet internal contract
// exists in this repository, so the default implementation fails closed and
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
}

// API is implemented only by an authenticated client for the deployed CF Meet
// contract (or a signed contract double in tests).
type API interface {
	EnsureContext(ctx context.Context, in CFMeetContextRequest) (CFMeetContext, error)
	ReadContext(ctx context.Context, contextID ids.ID) (CFMeetContext, error)
	RevokeParticipant(ctx context.Context, contextID, account ids.ID) error
}

// Unavailable is the production default until CF Meet publishes its contract.
type Unavailable struct{}

func (Unavailable) EnsureContext(context.Context, CFMeetContextRequest) (CFMeetContext, error) {
	return CFMeetContext{}, ErrContractUnavailable
}
func (Unavailable) ReadContext(context.Context, ids.ID) (CFMeetContext, error) {
	return CFMeetContext{}, ErrContractUnavailable
}
func (Unavailable) RevokeParticipant(context.Context, ids.ID, ids.ID) error {
	return ErrContractUnavailable
}
