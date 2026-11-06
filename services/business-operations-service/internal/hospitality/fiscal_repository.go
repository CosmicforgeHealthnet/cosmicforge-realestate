package hospitality

import (
	"context"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

type FiscalReview struct {
	ID                   ids.ID          `json:"id"`
	OrganisationID       ids.ID          `json:"organisation_id"`
	PropertyID           ids.ID          `json:"property_id"`
	FolioID              ids.ID          `json:"folio_id"`
	FolioItemID          ids.ID          `json:"folio_item_id"`
	ReservationRoomID    ids.ID          `json:"reservation_room_id"`
	SourceQuoteID        ids.ID          `json:"source_quote_id"`
	ReviewVersion        int64           `json:"review_version"`
	ExpectedFolioVersion int64           `json:"expected_folio_version"`
	State                string          `json:"state"`
	SourceKind           string          `json:"source_kind"`
	Breakdown            FiscalBreakdown `json:"breakdown"`
	Digest               string          `json:"digest"`
	ApprovalID           *ids.ID         `json:"approval_id,omitempty"`
	EvidenceReference    string          `json:"evidence_reference,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
}

func FolioItems(ctx context.Context, q database.Querier, org, folio ids.ID) ([]Item, error) {
	raw, e := db.New().GetFolioItemsWithFiscalDetails(ctx, q, db.GetFolioItemsWithFiscalDetailsParams{OrganisationID: org, FolioID: folio})
	return decodeRecord[[]Item](raw, e)
}

func FiscalReady(ctx context.Context, q database.Querier, org, folio ids.ID) (bool, error) {
	var ready bool
	e := q.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM folio_items i LEFT JOIN LATERAL(SELECT state FROM folio_fiscal_reviews v WHERE v.organisation_id=i.organisation_id AND v.folio_item_id=i.id ORDER BY review_version DESC LIMIT 1)v ON true WHERE i.organisation_id=$1 AND i.folio_id=$2 AND i.kind='ACCOMMODATION' AND i.policy_snapshot->'accepted_quote' IS NOT NULL AND (i.amount_minor=0 OR i.amount_minor+coalesce((SELECT sum(a.amount_minor) FROM folio_items a WHERE a.organisation_id=i.organisation_id AND a.adjusts_item_id=i.id),0)>0) AND (v.state IS NULL OR v.state NOT IN ('ITEMIZED','OPERATOR_REVIEWED')))`, org, folio).Scan(&ready)
	return ready, e
}

func GetFiscalReview(ctx context.Context, q database.Querier, org, id ids.ID) (FiscalReview, error) {
	raw, e := db.New().GetFolioFiscalReview(ctx, q, db.GetFolioFiscalReviewParams{OrganisationID: org, ID: id})
	return decodeRecord[FiscalReview](raw, e)
}
func ListFiscalReviews(ctx context.Context, q database.Querier, org, folio ids.ID, limit, offset int32) ([]FiscalReview, error) {
	rows, e := db.New().ListFolioFiscalReviews(ctx, q, db.ListFolioFiscalReviewsParams{OrganisationID: org, FolioID: folio, PageLimit: limit, PageOffset: offset})
	if e != nil {
		return nil, e
	}
	out := make([]FiscalReview, 0, len(rows))
	for _, raw := range rows {
		v, e := decodeRecord[FiscalReview](raw, nil)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
