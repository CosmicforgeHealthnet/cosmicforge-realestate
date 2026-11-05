package hospitality

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type Group struct {
	ID               ids.ID `json:"id"`
	OrganisationID   ids.ID `json:"organisation_id"`
	PropertyID       ids.ID `json:"property_id"`
	Name             string `json:"name"`
	State            string `json:"state"`
	SourceReference  string `json:"source_reference"`
	BillingReference string `json:"billing_reference"`
	Version          int64  `json:"version"`
}

func GetGroup(ctx context.Context, q database.Querier, org, id ids.ID) (Group, error) {
	raw, e := (&db.Queries{}).GetHospitalityGroup(ctx, q, db.GetHospitalityGroupParams{OrganisationID: org, ID: id})
	return decodeRecord[Group](raw, e)
}
