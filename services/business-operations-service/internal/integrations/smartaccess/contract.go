// Package smartaccess is a port for an authenticated device adapter. No device
// provider is registered by default. Providers deliver credential secrets to the
// guest themselves; this service retains opaque references and readback only.
package smartaccess

import (
	"context"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type Device struct {
	OrganisationID  ids.ID `json:"organisation_id"`
	PropertyID      ids.ID `json:"property_id"`
	RoomID          ids.ID `json:"room_id"`
	Provider        string `json:"provider"`
	DeviceReference string `json:"device_reference"`
}
type DeviceProof struct {
	Device            Device    `json:"device"`
	AccountReference  string    `json:"account_reference"`
	EvidenceReference string    `json:"evidence_reference"`
	ObservedAt        time.Time `json:"observed_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}
type Request struct {
	ID                  ids.ID    `json:"id"`
	ConfigurationID     ids.ID    `json:"configuration_id"`
	Device              Device    `json:"device"`
	AccountReference    string    `json:"account_reference"`
	StayID              ids.ID    `json:"stay_id"`
	AssignmentID        ids.ID    `json:"assignment_id"`
	IssueID             ids.ID    `json:"issue_id,omitempty"`
	Operation           string    `json:"operation"`
	StartsAt            time.Time `json:"starts_at"`
	EndsAt              time.Time `json:"ends_at"`
	CredentialReference string    `json:"credential_reference,omitempty"`
}
type Observation struct {
	RequestDigest       string    `json:"request_digest"`
	State               string    `json:"state"`
	CredentialReference string    `json:"credential_reference,omitempty"`
	EvidenceReference   string    `json:"evidence_reference"`
	ObservedAt          time.Time `json:"observed_at"`
}

// API implementations must authenticate the provider/account and bind every
// observation to the exact original request digest/key. ABSENT means definitive
// absence, never a timeout or a pending provider request. Lookup is read-only.
// Execute must support stable idempotency keys for both ISSUE and REVOKE.
// REVOKE names the original ISSUE by IssueID, including when the caller lost
// its response. A definitive REVOKED result must prevent later activation of
// that same issuance key; an in-flight or ambiguous issue is not ABSENT.
// A provider that cannot establish these semantics must not be registered.
type API interface {
	Inspect(context.Context, Device) (DeviceProof, error)
	Execute(context.Context, Request, string) (Observation, error)
	Lookup(context.Context, Request, string) (Observation, error)
}
