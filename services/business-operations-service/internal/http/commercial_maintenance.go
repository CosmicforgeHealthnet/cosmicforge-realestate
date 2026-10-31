package businesshttp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	businessauth "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/auth"
	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

// ProcessCommercialMaintenance is part of the established worker scheduler.
// Eligibility queries also test deadlines immediately; cleanup never grants
// expired stock or permissions while the scheduler is stopped.
func (h *Handler) ProcessCommercialMaintenance(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	now := h.Auth.Service.Now().UTC()
	for _, table := range []string{"inventory_holds", "inventory_allotments"} {
		deadline := "expires_at"
		if table == "inventory_allotments" {
			deadline = "release_at"
		}
		rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM `+table+` WHERE state='ACTIVE' AND `+deadline+`<=$1 ORDER BY organisation_id,id LIMIT $2`, now, limit)
		if e != nil {
			return e
		}
		type ref struct{ org, id ids.ID }
		refs := []ref{}
		for rows.Next() {
			var v ref
			if e = rows.Scan(&v.org, &v.id); e != nil {
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
		for _, v := range refs {
			e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, v.org); e != nil {
					return e
				}
				var property, pool ids.ID
				var from, to, state string
				var version int64
				var at time.Time
				if e := tx.QueryRow(ctx, `SELECT property_id,pool_id,date_from::text,date_to::text,state,version,`+deadline+` FROM `+table+` WHERE organisation_id=$1 AND id=$2 FOR UPDATE`, v.org, v.id).Scan(&property, &pool, &from, &to, &state, &version, &at); e != nil {
					return e
				}
				if state != "ACTIVE" || now.Before(at) {
					return nil
				}
				if _, e := tx.Exec(ctx, `UPDATE `+table+` SET state='EXPIRED',version=version+1 WHERE organisation_id=$1 AND id=$2`, v.org, v.id); e != nil {
					return e
				}
				if table == "inventory_allotments" {
					if _, e := tx.Exec(ctx, `UPDATE inventory_allotment_days SET released_at=$3 WHERE organisation_id=$1 AND allotment_id=$2 AND released_at IS NULL`, v.org, v.id, now); e != nil {
						return e
					}
				}
				if _, e := tx.Exec(ctx, `UPDATE inventory_calendar SET version=version+1,updated_at=now() WHERE organisation_id=$1 AND pool_id=$2 AND day>=$3::date AND day<$4::date`, v.org, pool, from, to); e != nil {
					return e
				}
				return businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, v.org, property, v.id, ids.Nil, table, "COMMERCIAL_STOCK_RESERVATION_EXPIRED", businessEvents.InventoryChanged, version+1, map[string]any{"pool_id": pool, "date_from": from, "date_to": to, "state": "EXPIRED"})
			})
			if e != nil {
				return e
			}
		}
	}
	if _, e := h.Auth.Service.Pool.Exec(ctx, `UPDATE bulk_rate_commands SET state='EXPIRED',version=version+1,updated_at=now() WHERE state='PREVIEW' AND expires_at<=$1`, now); e != nil {
		return e
	}
	rows, e := h.Auth.Service.Pool.Query(ctx, `SELECT organisation_id,id FROM channel_connections WHERE state IN ('CONNECTED','DEGRADED','SUSPENDED') ORDER BY coalesce((SELECT last_maintenance_at FROM channel_sync_cursors s WHERE s.connection_id=channel_connections.id),'epoch'),id LIMIT $1`, limit)
	if e != nil {
		return e
	}
	type connectionRef struct{ org, id ids.ID }
	connections := []connectionRef{}
	for rows.Next() {
		var v connectionRef
		if e = rows.Scan(&v.org, &v.id); e != nil {
			break
		}
		connections = append(connections, v)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if rowErr != nil {
		return rowErr
	}
	for _, v := range connections {
		// Rotate bounded maintenance batches independently of provider success.
		if _, e = h.Auth.Service.Pool.Exec(ctx, `UPDATE channel_sync_cursors SET last_maintenance_at=$3,version=version+1 WHERE organisation_id=$1 AND connection_id=$2`, v.org, v.id, now); e != nil {
			return e
		}
		if e = h.reconcileCommercialSupply(ctx, v.org, v.id); e != nil {
			return e
		}
		c, e := h.loadChannelConnection(ctx, h.Auth.Service.Pool, v.org, v.id)
		if e != nil {
			return e
		}
		measurements, e := h.channelMeasurements(ctx, h.Auth.Service.Pool, v.org, v.id)
		if e != nil {
			return e
		}
		raw, e := json.Marshal(measurements)
		if e != nil {
			return e
		}
		if _, e = h.Auth.Service.Pool.Exec(ctx, `INSERT INTO channel_metrics_snapshots(id,organisation_id,connection_id,snapshot) VALUES($1,$2,$3,$4)`, ids.New(), v.org, v.id, raw); e != nil {
			return e
		}
		if h.requireChannelCapability(ctx, h.Auth.Service.Pool, c, "POLLING") == nil {
			call, cancel := context.WithTimeout(ctx, 10*time.Second)
			pollErr := h.PollChannel(call, v.org, v.id)
			cancel()
			if pollErr != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if pollErr != nil {
				if e = database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
					return h.recordChannelConflict(ctx, tx, v.org, c.PropertyID, c.ID, ids.Nil, "PROVIDER_POLL_FAILED", "HIGH", "POLL", map[string]any{"classification": commercialErrorCode(pollErr)}, "RESTORE_AUTHENTICATED_PROVIDER_POLLING")
				}); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func (h *Handler) reconcileCommercialSupply(ctx context.Context, org, id ids.ID) error {
	return database.WithTx(ctx, h.Auth.Service.Pool, database.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT id FROM organisations WHERE id=$1 FOR UPDATE`, org); e != nil {
			return e
		}
		c, e := h.loadChannelConnection(ctx, tx, org, id)
		if e != nil {
			return e
		}
		var cursor, latest int64
		if e = tx.QueryRow(ctx, `SELECT last_history_sequence FROM channel_sync_cursors WHERE organisation_id=$1 AND connection_id=$2 FOR UPDATE`, org, id).Scan(&cursor); e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT coalesce(max(sequence),0) FROM commercial_history WHERE organisation_id=$1 AND property_id=$2 AND entity_type IN ('inventory_pool','sellable_product','inventory_hold','inventory_allotment','inventory_holds','inventory_allotments','inventory_calendar','rate_plan','rate_restriction','promotion','packages','add_ons','tax_profile','compliance_hold','channel_field_authority','channel_content_override')`, org, c.PropertyID).Scan(&latest); e != nil {
			return e
		}
		pubs, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(p) FROM listing_publication p WHERE organisation_id=$1 AND connection_id=$2 AND desired_state='PUBLISHED' ORDER BY id LIMIT 100`, org, id)
		if e != nil {
			return e
		}
		deferHistory := false
		for _, raw := range pubs {
			var pub CommercialPublication
			if e = json.Unmarshal(raw, &pub); e != nil {
				return e
			}
			var actor ids.ID
			var session *ids.ID
			var payload []byte
			if e = tx.QueryRow(ctx, `SELECT actor_id,session_id,payload FROM channel_sync_jobs WHERE organisation_id=$1 AND publication_id=$2 AND kind='CONTENT' ORDER BY created_at DESC,id DESC LIMIT 1`, org, pub.ID).Scan(&actor, &session, &payload); e != nil {
				return e
			}
			job := CommercialSyncJob{OrganisationID: org, PropertyID: c.PropertyID, ConnectionID: id, PublicationID: &pub.ID, ActorID: actor, SessionID: session}
			jobCtx := ctx
			if session != nil {
				jobCtx = businessauth.StoredJobContext(ctx, actor, org, *session, pub.ID)
			}
			t, _, authorityErr := h.authorize(jobCtx, tx, "publication:write", "PROPERTY", c.PropertyID, false)
			if authorityErr == nil {
				authorityErr = h.commercialGate(jobCtx, tx, t, c.PropertyID)
			}
			if authorityErr == nil {
				authorityErr = h.requireChannelCapability(jobCtx, tx, c, "ARI")
			}
			var source struct {
				Product inventory.Product     `json:"product"`
				Pool    inventory.Pool        `json:"pool"`
				Price   revenue.QuoteSnapshot `json:"price_example"`
				Content CommercialContent     `json:"content"`
			}
			if e = json.Unmarshal(payload, &source); e != nil {
				return e
			}
			listing, e := loadListing(ctx, tx, org, pub.ListingID)
			if e != nil {
				return e
			}
			content, e := latestListingVersion(ctx, tx, org, pub.ListingID)
			if e != nil {
				return e
			}
			property, e := portfolio.Get(ctx, tx, org, c.PropertyID)
			if e != nil {
				return e
			}
			product, e := inventory.GetProduct(ctx, tx, org, listing.ProductID)
			if e != nil {
				return e
			}
			pool, e := inventory.GetPool(ctx, tx, org, product.PoolID)
			if e != nil {
				return e
			}
			if authorityErr == nil && content.PropertyVersion != property.Version {
				authorityErr = apperrors.Conflict("PUBLICATION_PROPERTY_REVIEW_STALE", "Canonical property changed after content review.")
			}
			if authorityErr == nil {
				authorityErr = h.commercialProductEligibility(jobCtx, tx, t, product, product.MinGuests, 0, c.ProviderCode)
			}
			if authorityErr == nil {
				authorityErr = inventory.CheckStockAuthority(ctx, tx, pool)
			}
			if authorityErr == nil {
				authorityErr = h.requireConfirmedMappings(ctx, tx, c, product)
			}
			if authorityErr != nil {
				if e = h.recordChannelConflict(ctx, tx, org, c.PropertyID, c.ID, ids.Nil, "CANONICAL_SUPPLY_AUTHORITY_REVOKED", "CRITICAL", "PUBLICATION:"+pub.ID.String(), map[string]any{"classification": commercialErrorCode(authorityErr)}, "CONFIRM_PROVIDER_PAUSE_BEFORE_REACTIVATION"); e != nil {
					return e
				}
				if e = h.queueSafetyPause(ctx, tx, job, commercialErrorCode(authorityErr)); e != nil {
					return e
				}
				continue
			}
			// Do not replace an in-flight first publication with ARI. Its immutable
			// content and approval must be acknowledged before incremental supply updates.
			if latest <= cursor {
				if pub.DeliveryState == "CONFIRMED" {
					if e = h.queuePeriodicChannelReadback(jobCtx, tx, t, c, pub); e != nil {
						return e
					}
				}
				continue
			}
			if pub.DeliveryState != "CONFIRMED" {
				deferHistory = true
				continue
			}
			rangeDates := inventory.DateRange{DateFrom: source.Price.DateFrom, DateTo: source.Price.DateTo}
			today := h.Auth.Service.Now().In(mustCommercialLocation(property.Timezone)).Format(time.DateOnly)
			if rangeDates.DateFrom < today {
				rangeDates.DateFrom = today
			}
			if rangeDates.DateTo <= rangeDates.DateFrom {
				arrival, _ := time.Parse(time.DateOnly, today)
				rangeDates.DateTo = arrival.AddDate(0, 0, 90).Format(time.DateOnly)
			}
			ari, e := h.currentChannelARI(jobCtx, tx, t, c, product, pool, rangeDates, property.Timezone)
			if e != nil {
				if e = h.queueSafetyPause(ctx, tx, job, commercialErrorCode(e)); e != nil {
					return e
				}
				continue
			}
			version := pub.CanonicalVersion + 1
			restrictionContext, e := revenue.Restrictions(ctx, tx, product, *product.RatePlanID, rangeDates, c.ProviderCode)
			if e != nil {
				return e
			}
			pricingContext, e := h.channelPricingContext(ctx, tx, org, ari)
			if e != nil {
				return e
			}
			body := map[string]any{"organisation_id": org, "property_id": c.PropertyID, "listing_id": listing.ID, "listing_version_id": content.ID, "canonical_version": version, "desired_state": "PUBLISHED", "product": product, "pool": pool, "ari": ari, "pricing_context": pricingContext, "restrictions": restrictionContext, "content": content.Content, "source_history_sequence": latest}
			if e = h.applyChannelContentAuthority(ctx, tx, c, listing.ID, body); e != nil {
				if e = h.queueSafetyPause(ctx, tx, job, commercialErrorCode(e)); e != nil {
					return e
				}
				continue
			}
			raw, e := json.Marshal(body)
			if e != nil {
				return e
			}
			newJob := ids.New()
			if _, e = tx.Exec(ctx, `UPDATE listing_publication SET canonical_version=$3,delivery_state='PENDING',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, org, pub.ID, version); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO channel_sync_jobs(id,organisation_id,property_id,connection_id,publication_id,kind,canonical_version,payload,payload_hash,idempotency_key,actor_id,session_id,correlation_context) VALUES($1,$2,$3,$4,$5,'ARI',$6,$7,$8,$9,$10,$11,$12)`, newJob, org, c.PropertyID, c.ID, pub.ID, version, raw, hashCommercialPayload(raw), "ari:"+newJob.String(), actor, session, marshalCommercial(requestcontext.From(jobCtx))); e != nil {
				return e
			}
		}
		if latest > cursor && !deferHistory {
			if _, e = tx.Exec(ctx, `UPDATE channel_sync_cursors SET last_history_sequence=$3,version=version+1 WHERE organisation_id=$1 AND connection_id=$2`, org, id, latest); e != nil {
				return e
			}
		}

		return nil
	})
}

type CommercialARIDay struct {
	Restriction       revenue.Restriction `json:"restriction"`
	Day               string              `json:"day"`
	Available         int                 `json:"available"`
	CalendarVersion   int64               `json:"calendar_version"`
	AmountMinor       int64               `json:"amount_minor"`
	Currency          string              `json:"currency"`
	RateVersionID     ids.ID              `json:"rate_version_id"`
	TaxProfileID      *ids.ID             `json:"tax_profile_id,omitempty"`
	MinNights         int                 `json:"minimum_nights"`
	MaxNights         int                 `json:"maximum_nights"`
	StopSell          bool                `json:"stop_sell"`
	ClosedToArrival   bool                `json:"closed_to_arrival"`
	ClosedToDeparture bool                `json:"closed_to_departure"`
}

func (h *Handler) currentChannelARI(ctx context.Context, tx pgx.Tx, t tenant, c CommercialChannelConnection, p inventory.Product, pool inventory.Pool, dates inventory.DateRange, timezone string) ([]CommercialARIDay, error) {
	if p.RatePlanID == nil {
		return nil, apperrors.Conflict("RATE_REQUIRED", "Canonical ARI requires an active rate plan.")
	}
	stock, e := inventory.Calendar(ctx, tx, t.Organisation, pool.ID, dates, timezone, c.ProviderCode, h.Auth.Service.Now().UTC(), false)
	if e != nil {
		return nil, e
	}
	records, e := revenue.Restrictions(ctx, tx, p, *p.RatePlanID, dates, c.ProviderCode)
	if e != nil {
		return nil, e
	}
	result := []CommercialARIDay{}
	for _, day := range stock {
		v, e := revenue.VersionForDay(ctx, tx, t.Organisation, *p.RatePlanID, day.Day)
		if e != nil {
			return nil, e
		}
		base, _, e := revenue.ResolveBase(ctx, tx, t.Organisation, v, day.Day)
		if e != nil {
			return nil, e
		}
		amount, e := revenue.Nightly(v.Definition, base, day.Day, c.ProviderCode, v.Definition.MinNights, p.MinGuests, 0)
		if e != nil {
			return nil, e
		}
		if v.Definition.TaxProfileID == nil {
			return nil, apperrors.Conflict("FISCAL_POLICY_REQUIRED", "An approved fiscal policy is required for ARI.")
		}
		var fiscal bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tax_profiles WHERE organisation_id=$1 AND property_id=$2 AND id=$3 AND approval_id IS NOT NULL AND effective_at<=now() AND (expires_at IS NULL OR expires_at>now()))`, t.Organisation, p.PropertyID, *v.Definition.TaxProfileID).Scan(&fiscal); e != nil {
			return nil, e
		}
		if !fiscal {
			return nil, apperrors.Conflict("FISCAL_POLICY_EXPIRED", "ARI cannot publish expired fiscal authority.")
		}
		d := CommercialARIDay{Day: day.Day, Available: day.Available, CalendarVersion: day.Version, AmountMinor: amount.AmountMinor, Currency: v.Currency, RateVersionID: v.ID, TaxProfileID: v.Definition.TaxProfileID, MinNights: v.Definition.MinNights, MaxNights: v.Definition.MaxNights}
		rules := []revenue.Restriction{}
		at, _ := time.Parse(time.DateOnly, day.Day)
		for _, r := range records {
			if day.Day >= r.DateFrom && day.Day < r.DateTo && (len(r.Definition.Weekdays) == 0 || slicesCommercialDay(r.Definition.Weekdays, int(at.Weekday()))) {
				rules = append(rules, r.Definition)
			}
		}
		restriction, e := revenue.Intersect(rules)
		if e != nil {
			return nil, e
		}
		d.MinNights = max(d.MinNights, restriction.MinNights)
		if restriction.MaxNights > 0 {
			d.MaxNights = min(d.MaxNights, restriction.MaxNights)
		}
		d.StopSell = restriction.StopSell
		d.Restriction = restriction
		d.ClosedToArrival = restriction.ClosedToArrival
		d.ClosedToDeparture = restriction.ClosedToDeparture
		if d.StopSell {
			d.Available = 0
		}
		result = append(result, d)
	}
	if e = h.channelARIControls(ctx, tx, c, result); e != nil {
		return nil, e
	}
	return result, nil
}
func (h *Handler) applyChannelContentAuthority(ctx context.Context, q database.Querier, c CommercialChannelConnection, listing ids.ID, body map[string]any) error {
	var override []byte
	var overrideVersion int64
	var expiry *time.Time
	e := q.QueryRow(ctx, `SELECT content,version,expires_at FROM channel_content_overrides WHERE organisation_id=$1 AND connection_id=$2 AND listing_id=$3`, c.OrganisationID, c.ID, listing).Scan(&override, &overrideVersion, &expiry)
	if e == nil {
		if expiry != nil && !h.Auth.Service.Now().Before(*expiry) {
			return apperrors.Conflict("CHANNEL_OVERRIDE_EXPIRED", "An expired channel override requires reviewed reconciliation.")
		}
		var content CommercialContent
		if e = json.Unmarshal(override, &content); e != nil {
			return e
		}
		body["content"] = content
		body["content_override_version"] = overrideVersion
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	raw, e := json.Marshal(body["content"])
	if e != nil {
		return e
	}
	content := map[string]any{}
	if e = json.Unmarshal(raw, &content); e != nil {
		return e
	}
	versions := map[string]int64{}
	rows, e := q.Query(ctx, `SELECT field,mode,value,version,expires_at FROM channel_field_authority WHERE organisation_id=$1 AND connection_id=$2 ORDER BY field`, c.OrganisationID, c.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var field, mode string
		var value []byte
		var version int64
		var expires *time.Time
		if e = rows.Scan(&field, &mode, &value, &version, &expires); e != nil {
			return e
		}
		versions[field] = version
		if expires != nil && !h.Auth.Service.Now().Before(*expires) {
			return apperrors.Conflict("CHANNEL_OVERRIDE_EXPIRED", "Expired field authority requires reviewed reconciliation.")
		}
		if field == "stop_sell" || field == "minimum_nights" {
			continue
		}
		if mode == "CHANNEL_OVERRIDE" {
			delete(content, field)
		} else if mode == "MANUAL_OVERRIDE" {
			var replacement any
			if e = json.Unmarshal(value, &replacement); e != nil {
				return e
			}
			content[field] = replacement
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	body["content"] = content
	body["field_authority_versions"] = versions
	return nil
}
