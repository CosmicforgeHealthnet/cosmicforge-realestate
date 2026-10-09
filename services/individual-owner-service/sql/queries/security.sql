-- name: InsertSecurityEvent :exec
INSERT INTO account_security_events (event_id, account_id, event_type, session_id, device_id, ip_address,
    user_agent, metadata, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListSecurityEvents :many
SELECT * FROM account_security_events
 WHERE account_id = sqlc.arg(account_id)
   AND (sqlc.narg(cursor_occurred_at)::timestamptz IS NULL
        OR (occurred_at, event_id) < (sqlc.narg(cursor_occurred_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY occurred_at DESC, event_id DESC
 LIMIT sqlc.arg(row_limit);

-- name: InsertDispatch :exec
INSERT INTO notification_dispatches (dispatch_id, account_id, template_key, channels, variables,
    destination_email, destination_phone, status, next_attempt_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'PENDING', $8, $8);

-- name: ClaimDispatches :many
WITH due AS (
    SELECT dispatch_id FROM notification_dispatches
     WHERE status = 'PENDING' AND next_attempt_at <= sqlc.arg(now)::timestamptz
       AND (lease_expires_at IS NULL OR lease_expires_at < sqlc.arg(now)::timestamptz)
     ORDER BY next_attempt_at
     LIMIT sqlc.arg(row_limit)
     FOR UPDATE SKIP LOCKED)
UPDATE notification_dispatches d
   SET lease_owner = sqlc.arg(owner)::text, lease_expires_at = sqlc.arg(lease_until)::timestamptz, attempts = d.attempts + 1
  FROM due WHERE d.dispatch_id = due.dispatch_id
RETURNING d.*;

-- name: CompleteDispatch :execrows
UPDATE notification_dispatches
   SET status = 'ACCEPTED', notification_id = sqlc.arg(notification_id)::uuid, completed_at = sqlc.arg(now)::timestamptz,
       lease_owner = NULL, lease_expires_at = NULL, destination_email = NULL, destination_phone = NULL, last_error = NULL
 WHERE dispatch_id = sqlc.arg(dispatch_id) AND lease_owner = sqlc.arg(owner)::text AND status = 'PENDING';

-- name: FailDispatch :execrows
UPDATE notification_dispatches
   SET status = CASE WHEN sqlc.arg(dead)::boolean THEN 'DEAD' ELSE 'PENDING' END,
       next_attempt_at = sqlc.arg(retry_at)::timestamptz, last_error = sqlc.arg(last_error)::text,
       lease_owner = NULL, lease_expires_at = NULL,
       destination_email = CASE WHEN sqlc.arg(dead)::boolean THEN NULL ELSE destination_email END,
       destination_phone = CASE WHEN sqlc.arg(dead)::boolean THEN NULL ELSE destination_phone END,
       completed_at = CASE WHEN sqlc.arg(dead)::boolean THEN sqlc.arg(retry_at)::timestamptz ELSE NULL END
 WHERE dispatch_id = sqlc.arg(dispatch_id) AND lease_owner = sqlc.arg(owner)::text AND status = 'PENDING';

-- name: DispatchStats :one
SELECT count(*) FILTER (WHERE status = 'PENDING') AS pending,
       count(*) FILTER (WHERE status = 'DEAD') AS dead
  FROM notification_dispatches;
