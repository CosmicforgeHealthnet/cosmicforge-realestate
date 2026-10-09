-- 07 Listings -------------------------------------------------------------

-- name: InsertListing :one
INSERT INTO listings (id, property_id, unit_id, transaction_type, status, title, description, details, copy_of_listing_id,
    created_by_actor_domain, created_by_actor_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'DRAFT', $5, $6, $7, $8, $9, $10, 1, $11, $11)
RETURNING *;

-- name: GetListing :one
SELECT * FROM listings WHERE id = $1;

-- name: GetListingForUpdate :one
SELECT * FROM listings WHERE id = $1 FOR UPDATE;

-- name: ListListingsForAccount :many
SELECT l.* FROM listings l
 WHERE l.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR l.property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR l.status = sqlc.narg(status)::text)
   AND (sqlc.narg(transaction_type)::text IS NULL OR l.transaction_type = sqlc.narg(transaction_type)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (l.created_at, l.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY l.created_at DESC, l.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: ListListingsForProperty :many
SELECT * FROM listings WHERE property_id = $1 ORDER BY created_at DESC, id DESC LIMIT 200;

-- name: LiveListingsForProperty :many
SELECT * FROM listings WHERE property_id = $1 AND status IN ('PUBLISHED','PAUSED','READY_FOR_REVIEW');

-- name: UpdateListingContent :one
UPDATE listings SET title = $3, description = $4, details = $5, version = version + 1, updated_at = $6
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: SetListingPublication :one
UPDATE listings SET current_version_no = $2, published_version_no = $2, published_at = $3,
    first_published_at = coalesce(first_published_at, $3), pause_source = NULL, pause_reason = NULL, quality_score = $4, updated_at = $3
 WHERE id = $1
RETURNING *;

-- name: SetListingPause :exec
UPDATE listings SET pause_source = $2, pause_reason = $3, updated_at = $4 WHERE id = $1;

-- name: InsertListingVersion :one
INSERT INTO listing_versions (id, listing_id, version_no, snapshot, pricing_version_id, reason, actor_domain, actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetListingVersion :one
SELECT * FROM listing_versions WHERE listing_id = $1 AND version_no = $2;

-- name: ListListingVersions :many
SELECT * FROM listing_versions WHERE listing_id = $1 ORDER BY version_no DESC LIMIT 100;

-- name: InsertListingPublication :exec
INSERT INTO listing_publication (id, listing_id, version_no, action, channel, source, reason, occurred_at)
VALUES ($1, $2, $3, $4, 'MARKETPLACE', $5, $6, $7);

-- name: DeleteListingMedia :exec
DELETE FROM listing_media_links WHERE listing_id = $1;

-- name: InsertListingMedia :exec
INSERT INTO listing_media_links (listing_id, media_id, position, is_cover, created_at) VALUES ($1, $2, $3, $4, $5);

-- name: ListListingMedia :many
SELECT lm.listing_id, lm.media_id, lm.position, lm.is_cover,
       coalesce((SELECT pm.media_status FROM property_media_links pm JOIN listings l ON l.property_id = pm.property_id
                  WHERE l.id = lm.listing_id AND pm.media_id = lm.media_id ORDER BY pm.created_at LIMIT 1), 'DELETED')::text AS media_status
  FROM listing_media_links lm WHERE lm.listing_id = $1 ORDER BY lm.position, lm.media_id;

-- name: ListingsUsingMedia :many
SELECT DISTINCT l.id, l.status FROM listing_media_links lm JOIN listings l ON l.id = lm.listing_id WHERE lm.media_id = $1;

-- 08 Pricing ---------------------------------------------------------------

-- name: GetPriceSchedule :one
SELECT * FROM price_schedules WHERE listing_id = $1;

-- name: GetPriceScheduleForUpdate :one
SELECT * FROM price_schedules WHERE listing_id = $1 FOR UPDATE;

-- name: InsertPriceSchedule :one
INSERT INTO price_schedules (listing_id, active_pricing_version_id, currency, version, updated_at) VALUES ($1, $2, $3, 1, $4) RETURNING *;

-- name: UpdatePriceSchedule :one
UPDATE price_schedules SET active_pricing_version_id = $3, currency = $4, version = version + 1, updated_at = $5
 WHERE listing_id = $1 AND version = $2
RETURNING *;

-- name: NextPricingVersionNo :one
SELECT coalesce(max(version_no), 0)::int + 1 FROM pricing_versions WHERE listing_id = $1;

-- name: InsertPricingVersion :one
INSERT INTO pricing_versions (id, listing_id, version_no, currency, base_amount_minor, base_period, payment_cycle, deposit_amount_minor,
    negotiable, policy_version, created_by_actor_domain, created_by_actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetPricingVersion :one
SELECT * FROM pricing_versions WHERE id = $1;

-- name: ListPricingVersions :many
SELECT * FROM pricing_versions WHERE listing_id = $1 ORDER BY version_no DESC LIMIT 100;

-- name: AcceptPricingVersion :execrows
UPDATE pricing_versions SET accepted_at = $2 WHERE id = $1 AND accepted_at IS NULL;

-- name: InsertFeeDefinition :exec
INSERT INTO fee_definitions (id, pricing_version_id, code, category, label, calculation, amount_minor, rate_bps, frequency, mandatory,
    refundable, payable_to)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ListFeeDefinitions :many
SELECT * FROM fee_definitions WHERE pricing_version_id = $1 ORDER BY code;

-- name: InsertPricingRule :exec
INSERT INTO pricing_rules (id, pricing_version_id, rule_type, position, params) VALUES ($1, $2, $3, $4, $5);

-- name: ListPricingRules :many
SELECT * FROM pricing_rules WHERE pricing_version_id = $1 ORDER BY position;

-- 09 Availability ------------------------------------------------------------

-- name: InsertAvailability :one
INSERT INTO availability_state (id, property_id, status, available_from, available_until, note, version, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 1, $7)
RETURNING *;

-- name: GetAvailability :one
SELECT * FROM availability_state WHERE id = $1;

-- name: SetAvailabilityWindow :one
UPDATE availability_state SET available_from = $2, available_until = $3, note = $4, updated_at = $5 WHERE id = $1 RETURNING *;

-- name: SetAvailabilityConfirmation :one
UPDATE availability_state SET last_confirmed_at = $2, next_confirmation_due = $3, updated_at = $2 WHERE id = $1 RETURNING *;

-- name: InsertFreshnessCheck :one
INSERT INTO freshness_checks (id, listing_id, trigger, status, due_at, report_type, created_at)
VALUES ($1, $2, $3, 'PENDING', $4, $5, $6)
ON CONFLICT (listing_id) WHERE status IN ('PENDING','REMINDED') DO UPDATE
   SET due_at = LEAST(freshness_checks.due_at, EXCLUDED.due_at),
       report_type = coalesce(EXCLUDED.report_type, freshness_checks.report_type),
       trigger = CASE WHEN EXCLUDED.trigger = 'REPORT' THEN 'REPORT' ELSE freshness_checks.trigger END
RETURNING *;

-- name: ResolveFreshnessChecks :execrows
UPDATE freshness_checks SET status = $2, resolved_at = $3 WHERE listing_id = $1 AND status IN ('PENDING','REMINDED');

-- name: GetOpenFreshnessCheck :one
SELECT * FROM freshness_checks WHERE listing_id = $1 AND status IN ('PENDING','REMINDED');

-- name: DueFreshnessReminders :many
UPDATE freshness_checks SET status = 'REMINDED', reminded_at = sqlc.arg(now)::timestamptz
 WHERE id IN (SELECT f.id FROM freshness_checks f WHERE f.status = 'PENDING' AND f.due_at <= sqlc.arg(remind_before)::timestamptz
              ORDER BY f.due_at LIMIT sqlc.arg(max_rows) FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: DueFreshnessMisses :many
SELECT f.* FROM freshness_checks f WHERE f.status IN ('PENDING','REMINDED') AND f.due_at <= sqlc.arg(overdue)::timestamptz
 ORDER BY f.due_at LIMIT sqlc.arg(max_rows) FOR UPDATE SKIP LOCKED;

-- name: UpsertCalendarDay :exec
INSERT INTO availability_calendar (listing_id, day, status, source, ref_id, price_override_minor, min_nights, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (listing_id, day) DO UPDATE SET status = EXCLUDED.status, source = EXCLUDED.source, ref_id = EXCLUDED.ref_id,
    price_override_minor = EXCLUDED.price_override_minor, min_nights = EXCLUDED.min_nights, updated_at = EXCLUDED.updated_at;

-- name: ListCalendar :many
SELECT * FROM availability_calendar WHERE listing_id = $1 AND day BETWEEN $2 AND $3 ORDER BY day;

-- name: CalendarConflicts :many
SELECT * FROM availability_calendar WHERE listing_id = $1 AND day >= $2 AND day < $3 AND status IN ('BOOKED','HOLD','BLOCKED') FOR UPDATE;

-- name: ReleaseCalendarRef :exec
UPDATE availability_calendar SET status = 'AVAILABLE', source = 'SYSTEM', ref_id = NULL, updated_at = $2 WHERE ref_id = $1;

-- Marketplace access to live listing media ---------------------------------

-- name: MediaNeedingMarketplaceGrant :many
SELECT DISTINCT lm.media_id FROM listing_media_links lm JOIN listings l ON l.id = lm.listing_id
 WHERE l.status IN ('PUBLISHED','PAUSED')
   AND EXISTS (SELECT 1 FROM property_media_links pm WHERE pm.media_id = lm.media_id AND pm.media_status = 'READY')
   AND NOT EXISTS (SELECT 1 FROM media_marketplace_access a WHERE a.media_id = lm.media_id AND a.granted)
 LIMIT $1;

-- name: MediaNeedingMarketplaceRevoke :many
SELECT a.media_id FROM media_marketplace_access a
 WHERE a.granted
   AND NOT EXISTS (SELECT 1 FROM listing_media_links lm JOIN listings l ON l.id = lm.listing_id
                    WHERE lm.media_id = a.media_id AND l.status IN ('PUBLISHED','PAUSED'))
 LIMIT $1;

-- name: SetMarketplaceAccess :exec
INSERT INTO media_marketplace_access (media_id, granted, synced_at) VALUES ($1, $2, $3)
ON CONFLICT (media_id) DO UPDATE SET granted = EXCLUDED.granted, synced_at = EXCLUDED.synced_at;
