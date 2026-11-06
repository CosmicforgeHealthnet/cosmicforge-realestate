package hospitality

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/smartaccess"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type AccessConfiguration struct {
	ID                      ids.ID          `json:"id"`
	OrganisationID          ids.ID          `json:"organisation_id"`
	PropertyID              ids.ID          `json:"property_id"`
	RoomID                  ids.ID          `json:"room_id"`
	Version                 int64           `json:"version"`
	Method                  string          `json:"method"`
	Provider                string          `json:"provider"`
	DeviceReference         string          `json:"device_reference"`
	AccountReference        string          `json:"account_reference"`
	DeviceProof             json.RawMessage `json:"device_proof"`
	ExpiresAt               time.Time       `json:"expires_at"`
	Reason                  string          `json:"reason"`
	CreatedBy               ids.ID          `json:"created_by"`
	CreatedAt               time.Time       `json:"created_at"`
	ApprovalID              *ids.ID         `json:"approval_id,omitempty"`
	AuthorizingMembershipID ids.ID          `json:"authorizing_membership_id"`
	AuthorizingRoleID       ids.ID          `json:"authorizing_role_id"`
}
type AccessReference struct {
	ID                ids.ID    `json:"id"`
	OrganisationID    ids.ID    `json:"organisation_id"`
	PropertyID        ids.ID    `json:"property_id"`
	StayID            ids.ID    `json:"stay_id"`
	AssignmentID      ids.ID    `json:"assignment_id"`
	Operation         string    `json:"operation"`
	State             string    `json:"state"`
	ProviderReference string    `json:"provider_reference"`
	Version           int64     `json:"version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
type AccessJob struct {
	ID              ids.ID              `json:"id"`
	OrganisationID  ids.ID              `json:"organisation_id"`
	PropertyID      ids.ID              `json:"property_id"`
	ConfigurationID ids.ID              `json:"configuration_id"`
	Request         smartaccess.Request `json:"request"`
	RequestDigest   string              `json:"request_digest"`
	IdempotencyKey  string              `json:"idempotency_key"`
	DeliveryState   string              `json:"delivery_state"`
	Attempts        int                 `json:"attempts"`
	AvailableAt     time.Time           `json:"available_at"`
	DispatchedAt    *time.Time          `json:"dispatched_at,omitempty"`
	LeaseOwner      string              `json:"lease_owner"`
	LeaseGeneration int64               `json:"lease_generation"`
	LastErrorCode   string              `json:"last_error_code"`
}

func LatestAccessConfiguration(ctx context.Context, q database.Querier, org, room ids.ID) (AccessConfiguration, error) {
	raw, e := db.New().GetLatestHospitalityAccessConfiguration(ctx, q, db.GetLatestHospitalityAccessConfigurationParams{OrganisationID: org, RoomID: room})
	return decodeRecord[AccessConfiguration](raw, e)
}
func GetAccessReference(ctx context.Context, q database.Querier, org, id ids.ID) (AccessReference, error) {
	raw, e := db.New().GetHospitalityAccessReference(ctx, q, db.GetHospitalityAccessReferenceParams{OrganisationID: org, ID: id})
	return decodeRecord[AccessReference](raw, e)
}

// A provider observation cannot enlarge the locally accepted window. No raw
// credential/code is part of the request or accepted result.
func ValidAccessObservation(o smartaccess.Observation, digest, operation string, now time.Time) bool {
	if o.RequestDigest != digest || o.EvidenceReference == "" || len(o.EvidenceReference) > 300 || o.ObservedAt.IsZero() || o.ObservedAt.After(now.Add(time.Minute)) || o.ObservedAt.Before(now.Add(-5*time.Minute)) {
		return false
	}
	if operation == "ISSUE" {
		return o.State == "ABSENT" || o.State == "ACTIVE" && len(o.CredentialReference) >= 5 && len(o.CredentialReference) <= 300
	}
	return operation == "REVOKE" && (o.State == "ABSENT" || o.State == "REVOKED" || o.State == "ACTIVE" && len(o.CredentialReference) >= 5 && len(o.CredentialReference) <= 300)
}
