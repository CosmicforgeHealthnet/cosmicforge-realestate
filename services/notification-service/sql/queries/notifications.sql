-- name: InsertRequest :one
INSERT INTO notification_requests (notification_id, recipient_domain, recipient_id, template_id, template_key, template_version, locale,
    category, critical, requested_by_service, requested_by_actor_domain, requested_by_actor_id, caller_reference, suppressed, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING *;

-- name: GetRequest :one
SELECT * FROM notification_requests WHERE notification_id = $1;

-- name: InsertDelivery :one
INSERT INTO notification_deliveries (delivery_id, notification_id, channel, provider, push_endpoint_id, destination, status, max_attempts,
    next_attempt_at, subject, body, html_body, data, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'QUEUED', $7, $8, $9, $10, $11, $12, $13, $14, $14)
RETURNING *;

-- name: ListDeliveries :many
SELECT * FROM notification_deliveries WHERE notification_id = $1 ORDER BY created_at, delivery_id;

-- name: ClaimDueDeliveries :many
WITH due AS (
    SELECT delivery_id FROM notification_deliveries
     WHERE ((status IN ('QUEUED','RETRY_SCHEDULED') AND next_attempt_at <= sqlc.arg(now)::timestamptz)
         OR (status = 'DISPATCHING' AND lease_expires_at < sqlc.arg(now)::timestamptz))
     ORDER BY next_attempt_at
     LIMIT sqlc.arg(row_limit)
     FOR UPDATE SKIP LOCKED)
UPDATE notification_deliveries d
   SET lease_owner = sqlc.arg(lease_owner), lease_expires_at = sqlc.arg(lease_expires_at)::timestamptz, updated_at = sqlc.arg(now)::timestamptz
  FROM due WHERE d.delivery_id = due.delivery_id
RETURNING d.*;

-- name: GetDeliveryForUpdate :one
SELECT * FROM notification_deliveries WHERE delivery_id = $1 FOR UPDATE;

-- name: UpdateDelivery :one
UPDATE notification_deliveries
   SET status = sqlc.arg(status),
       attempts = sqlc.arg(attempts),
       next_attempt_at = sqlc.arg(next_attempt_at),
       lease_owner = sqlc.narg(lease_owner),
       lease_expires_at = sqlc.narg(lease_expires_at),
       provider_message_id = coalesce(sqlc.narg(provider_message_id), provider_message_id),
       last_error_code = sqlc.narg(last_error_code),
       sent_at = coalesce(sqlc.narg(sent_at), sent_at),
       failed_at = coalesce(sqlc.narg(failed_at), failed_at),
       updated_at = sqlc.arg(updated_at)
 WHERE delivery_id = sqlc.arg(delivery_id) AND status = sqlc.arg(expected_status)
RETURNING *;

-- name: InsertAttempt :one
INSERT INTO delivery_attempts (attempt_id, delivery_id, attempt_no, provider, outcome, error_code, provider_message_id, duration_ms, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (delivery_id, attempt_no) DO NOTHING
RETURNING *;

-- name: InsertProviderResponse :exec
INSERT INTO provider_responses (response_id, attempt_id, provider, http_status, provider_code, retry_after_seconds, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: PurgeDeliveredContent :execrows
UPDATE notification_deliveries SET body = '', subject = NULL, html_body = NULL, data = NULL
 WHERE status IN ('SENT','FAILED_FINAL') AND updated_at < $1 AND body <> '';

-- name: ClearDeliveryContent :exec
UPDATE notification_deliveries SET body = '', subject = NULL, html_body = NULL, data = NULL WHERE delivery_id = $1;

-- name: ListAttempts :many
SELECT * FROM delivery_attempts WHERE delivery_id = $1 ORDER BY attempt_no;

-- name: DeliveryStats :many
SELECT channel, status, count(*)::bigint AS n FROM notification_deliveries
 WHERE updated_at > $1 GROUP BY channel, status ORDER BY channel, status;
