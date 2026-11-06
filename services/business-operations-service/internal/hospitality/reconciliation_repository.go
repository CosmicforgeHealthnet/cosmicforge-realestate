package hospitality

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type RoomReconciliationReport struct {
	ID               ids.ID          `json:"id"`
	OrganisationID   ids.ID          `json:"organisation_id"`
	PropertyID       ids.ID          `json:"property_id"`
	RoomID           ids.ID          `json:"room_id"`
	SourceDigest     string          `json:"source_digest"`
	State            string          `json:"state"`
	Proof            json.RawMessage `json:"proof"`
	Changes          json.RawMessage `json:"changes"`
	ApprovalID       *ids.ID         `json:"approval_id,omitempty"`
	Reason           string          `json:"reason"`
	CreatedBy        *ids.ID         `json:"created_by,omitempty"`
	CreatedByService string          `json:"created_by_service"`
	CreatedAt        time.Time       `json:"created_at"`
}

func RoomProof(ctx context.Context, q database.Querier, org, room ids.ID) (RoomSafetyProof, error) {
	raw, e := db.New().GetRoomSafetyProof(ctx, q, db.GetRoomSafetyProofParams{OrganisationID: org, RoomID: room})
	return decodeRecord[RoomSafetyProof](raw, e)
}
func GetRoomReconciliationReport(ctx context.Context, q database.Querier, org, id ids.ID) (RoomReconciliationReport, error) {
	raw, e := db.New().GetRoomReconciliationReport(ctx, q, db.GetRoomReconciliationReportParams{OrganisationID: org, ID: id})
	return decodeRecord[RoomReconciliationReport](raw, e)
}
