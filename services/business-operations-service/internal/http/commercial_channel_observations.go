package businesshttp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/jackc/pgx/v5"
)

// Provider and Media network reads happen outside the owning DB transaction.
func (h *Handler) validateDeliveryMedia(ctx context.Context, job CommercialSyncJob) error {
	if h.Media == nil {
		return apperrors.Unavailable("Live Media metadata is unavailable for publication delivery.")
	}
	var source struct {
		Version ids.ID `json:"listing_version_id"`
	}
	if e := json.Unmarshal(job.Payload, &source); e != nil {
		return e
	}
	if source.Version.ValidateV7() != nil {
		return apperrors.Conflict("SYNC_PAYLOAD_CORRUPT", "Immutable listing content version is required.")
	}
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT m.media_id,m.node_id FROM listing_media_links l JOIN inventory_media_links m ON m.organisation_id=l.organisation_id AND m.id=l.media_link_id WHERE l.organisation_id=$1 AND l.listing_version_id=$2 ORDER BY m.id`, job.OrganisationID, source.Version)
	if e != nil {
		return e
	}
	type ref struct{ media, node ids.ID }
	refs := []ref{}
	for rows.Next() {
		var v ref
		if e = rows.Scan(&v.media, &v.node); e != nil {
			break
		}
		refs = append(refs, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	if len(refs) == 0 {
		return apperrors.PermissionDenied("Publication requires approved public photos.")
	}
	for _, v := range refs {
		metadata, e := h.Media.Get(ctx, v.media)
		if e != nil {
			return apperrors.Unavailable("Live publication media metadata cannot be confirmed.")
		}
		node, e := portfolio.Get(ctx, h.Auth.Service.Pool, job.OrganisationID, v.node)
		if e != nil {
			return e
		}
		if metadata.ID != v.media || metadata.Status != "READY" || metadata.Classification != "PUBLIC" || !mediaOwned(metadata, tenant{Organisation: job.OrganisationID, Account: job.ActorID}, node) {
			return apperrors.PermissionDenied("Publication media was removed, restricted or no longer belongs to its canonical owner.")
		}
	}
	return nil
}
func (h *Handler) recordSyncObservations(ctx context.Context, tx pgx.Tx, job CommercialSyncJob, now time.Time) error {
	if commercialExpectedProviderState(job) != "PUBLISHED" {
		return nil
	}
	var body struct {
		Product         inventory.Product `json:"product"`
		Fields          map[string]int64  `json:"field_authority_versions"`
		OverrideVersion *int64            `json:"content_override_version,omitempty"`
		ListingID       ids.ID            `json:"listing_id"`
	}
	if e := json.Unmarshal(job.Payload, &body); e != nil {
		return e
	}
	for field, version := range body.Fields {
		if _, e := tx.Exec(ctx, `UPDATE channel_field_authority SET last_synced_at=$5 WHERE organisation_id=$1 AND connection_id=$2 AND field=$3 AND version=$4`, job.OrganisationID, job.ConnectionID, field, version, now); e != nil {
			return e
		}
	}
	if body.OverrideVersion != nil {
		if _, e := tx.Exec(ctx, `UPDATE channel_content_overrides SET last_synced_at=$5 WHERE organisation_id=$1 AND connection_id=$2 AND listing_id=$3 AND version=$4`, job.OrganisationID, job.ConnectionID, body.ListingID, *body.OverrideVersion, now); e != nil {
			return e
		}
	}
	if _, e := tx.Exec(ctx, `UPDATE channel_property_mappings SET last_synced_at=$3 WHERE organisation_id=$1 AND connection_id=$2 AND state='CONFIRMED'`, job.OrganisationID, job.ConnectionID, now); e != nil {
		return e
	}
	if !body.Product.ID.IsZero() {
		if _, e := tx.Exec(ctx, `UPDATE channel_product_mappings SET last_synced_at=$4 WHERE organisation_id=$1 AND connection_id=$2 AND product_id=$3 AND state='CONFIRMED'`, job.OrganisationID, job.ConnectionID, body.Product.ID, now); e != nil {
			return e
		}
		if body.Product.RatePlanID != nil {
			if _, e := tx.Exec(ctx, `UPDATE channel_rate_mappings SET last_synced_at=$4 WHERE organisation_id=$1 AND connection_id=$2 AND rate_plan_id=$3 AND state='CONFIRMED'`, job.OrganisationID, job.ConnectionID, *body.Product.RatePlanID, now); e != nil {
				return e
			}
		}
	}
	return nil
}
