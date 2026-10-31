package businesshttp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/jackc/pgx/v5"
)

// Connection overrides can add stricter sale controls; they cannot widen any
// canonical stock, compliance, price or stay-length constraint.
func (h *Handler) channelSaleControls(ctx context.Context, q database.Querier, p inventory.Product, channel string, nights int) error {
	if channel == "" {
		return nil
	}
	rows, e := q.Query(ctx, `SELECT f.field,f.mode,f.value,f.expires_at FROM channel_field_authority f JOIN channel_connections c ON c.organisation_id=f.organisation_id AND c.id=f.connection_id JOIN channel_providers p ON p.id=c.provider_id WHERE c.organisation_id=$1 AND c.property_id=$2 AND p.code=$3 AND c.state='CONNECTED' AND f.field IN ('stop_sell','minimum_nights') ORDER BY c.id,f.field`, p.OrganisationID, p.PropertyID, channel)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var field, mode string
		var raw []byte
		var expires *time.Time
		if e = rows.Scan(&field, &mode, &raw, &expires); e != nil {
			return e
		}
		if mode != "MANUAL_OVERRIDE" {
			continue
		}
		if expires == nil || !h.Auth.Service.Now().Before(*expires) {
			return apperrors.Conflict("CHANNEL_OVERRIDE_EXPIRED", "Channel sale-control authority expired and requires reviewed reconciliation.")
		}
		if field == "stop_sell" {
			var closed bool
			if e = json.Unmarshal(raw, &closed); e != nil {
				return e
			}
			if closed {
				return apperrors.Conflict("CHANNEL_STOP_SELL", "An approved channel stop-sell prohibits this sale.")
			}
		} else {
			var minimum int
			if e = json.Unmarshal(raw, &minimum); e != nil {
				return e
			}
			if nights < minimum {
				return apperrors.Conflict("CHANNEL_MINIMUM_STAY", "The approved channel minimum stay denies this stay.")
			}
		}
	}
	return rows.Err()
}
func (h *Handler) channelARIControls(ctx context.Context, q database.Querier, c CommercialChannelConnection, days []CommercialARIDay) error {
	rows, e := q.Query(ctx, `SELECT field,mode,value,expires_at FROM channel_field_authority WHERE organisation_id=$1 AND connection_id=$2 AND field IN ('stop_sell','minimum_nights') ORDER BY field`, c.OrganisationID, c.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var field, mode string
		var raw []byte
		var expires *time.Time
		if e = rows.Scan(&field, &mode, &raw, &expires); e != nil {
			return e
		}
		if mode != "MANUAL_OVERRIDE" {
			continue
		}
		if expires == nil || !h.Auth.Service.Now().Before(*expires) {
			return apperrors.Conflict("CHANNEL_OVERRIDE_EXPIRED", "Channel control expired; provider state must be reconciled.")
		}
		for i := range days {
			if field == "stop_sell" {
				days[i].StopSell = true
				days[i].Restriction.StopSell = true
				days[i].Available = 0
			} else {
				var nights int
				if e = json.Unmarshal(raw, &nights); e != nil {
					return e
				}
				days[i].MinNights = max(days[i].MinNights, nights)
				days[i].Restriction.MinNights = days[i].MinNights
				if days[i].MaxNights > 0 && days[i].MinNights > days[i].MaxNights {
					return apperrors.Conflict("CHANNEL_RESTRICTION_CONFLICT", "Channel and canonical stay windows cannot both be satisfied.")
				}
			}
		}
	}
	return rows.Err()
}
func (h *Handler) validateQueuedChannelAuthority(ctx context.Context, tx pgx.Tx, c CommercialChannelConnection, job CommercialSyncJob) error {
	var source struct {
		FieldVersions   map[string]int64 `json:"field_authority_versions"`
		OverrideVersion *int64           `json:"content_override_version,omitempty"`
		ListingID       string           `json:"listing_id"`
	}
	if e := json.Unmarshal(job.Payload, &source); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT field,version,expires_at FROM channel_field_authority WHERE organisation_id=$1 AND connection_id=$2 ORDER BY field`, c.OrganisationID, c.ID)
	if e != nil {
		return e
	}
	count := 0
	for rows.Next() {
		var field string
		var version int64
		var expires *time.Time
		if e = rows.Scan(&field, &version, &expires); e != nil {
			break
		}
		if source.FieldVersions[field] != version || expires != nil && !h.Auth.Service.Now().Before(*expires) {
			e = apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Channel field authority changed or expired after queuing.")
			break
		}
		count++
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	if count != len(source.FieldVersions) {
		return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Queued field authority no longer matches its owning configuration.")
	}
	var version int64
	var expires *time.Time
	e = tx.QueryRow(ctx, `SELECT version,expires_at FROM channel_content_overrides WHERE organisation_id=$1 AND connection_id=$2 AND listing_id::text=$3`, c.OrganisationID, c.ID, source.ListingID).Scan(&version, &expires)
	if e == pgx.ErrNoRows {
		if source.OverrideVersion != nil {
			return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Queued content override disappeared.")
		}
		return nil
	}
	if e != nil {
		return e
	}
	if source.OverrideVersion == nil || *source.OverrideVersion != version || expires != nil && !h.Auth.Service.Now().Before(*expires) {
		return apperrors.Conflict("SYNC_CANONICAL_VERSION_CHANGED", "Queued content override changed or expired.")
	}
	return nil
}
