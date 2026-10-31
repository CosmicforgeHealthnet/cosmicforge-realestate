-- name: GetSellableProduct :one
SELECT * FROM sellable_products WHERE organisation_id=$1 AND id=$2;
-- name: GetCommercialPool :one
SELECT * FROM inventory_pools WHERE organisation_id=$1 AND id=$2;
-- name: GetCommercialRateVersion :one
SELECT * FROM rate_plan_versions WHERE organisation_id=$1 AND id=$2;
-- name: GetCommercialQuote :one
SELECT * FROM price_quotes WHERE organisation_id=$1 AND id=$2;
-- name: GetCommercialHold :one
SELECT * FROM inventory_holds WHERE organisation_id=$1 AND id=$2;
-- name: LockCommercialCalendar :many
SELECT * FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2 AND day>=sqlc.arg(date_from)::date AND day<sqlc.arg(date_to)::date ORDER BY day FOR UPDATE;
-- name: CommercialHoldWork :many
SELECT id,organisation_id,property_id,pool_id,version FROM inventory_holds WHERE state='ACTIVE' AND expires_at<=now() ORDER BY organisation_id,id LIMIT $1;
-- name: CommercialSyncClaim :one
UPDATE channel_sync_jobs SET state='RUNNING',lease_owner=sqlc.arg(owner)::text,lease_expires_at=now()+interval '30 seconds',lease_generation=lease_generation+1,attempts=attempts+1,updated_at=now()
WHERE id=(SELECT id FROM channel_sync_jobs WHERE (state IN ('QUEUED','RETRY') OR state='RUNNING' AND lease_expires_at<now()) AND available_at<=now() ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
RETURNING *;
