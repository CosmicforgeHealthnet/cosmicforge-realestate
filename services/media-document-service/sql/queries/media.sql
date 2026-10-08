-- name: CreateMedia :one
INSERT INTO media_objects (media_id, owner_domain, owner_type, owner_id, created_by_service, created_by_actor_domain, created_by_actor_id,
    media_class, filename, mime_type, size_bytes, checksum_sha256, storage_key, access_classification, scan_status, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'NOT_SCANNED', 'INITIATED', $15, $15)
RETURNING *;

-- name: GetMedia :one
SELECT * FROM media_objects WHERE media_id = $1;

-- name: GetMediaForUpdate :one
SELECT * FROM media_objects WHERE media_id = $1 FOR UPDATE;

-- name: UpdateMediaState :one
UPDATE media_objects
   SET status = sqlc.arg(status),
       scan_status = coalesce(sqlc.narg(scan_status), scan_status),
       detected_mime_type = coalesce(sqlc.narg(detected_mime_type), detected_mime_type),
       rejection_reason = coalesce(sqlc.narg(rejection_reason), rejection_reason),
       retention_until = coalesce(sqlc.narg(retention_until), retention_until),
       uploaded_at = coalesce(sqlc.narg(uploaded_at), uploaded_at),
       ready_at = coalesce(sqlc.narg(ready_at), ready_at),
       rejected_at = coalesce(sqlc.narg(rejected_at), rejected_at),
       deleted_at = coalesce(sqlc.narg(deleted_at), deleted_at),
       next_work_at = sqlc.narg(next_work_at),
       lease_owner = NULL, lease_expires_at = NULL,
       updated_at = sqlc.arg(updated_at)
 WHERE media_id = sqlc.arg(media_id) AND status = sqlc.arg(expected_status)
RETURNING *;

-- name: SetAccessClassification :one
UPDATE media_objects SET access_classification = $2, updated_at = $3 WHERE media_id = $1 RETURNING *;

-- name: ClaimForScan :many
WITH due AS (
    SELECT media_id FROM media_objects
     WHERE ((status = 'UPLOADED' AND next_work_at <= sqlc.arg(now)::timestamptz)
         OR (status = 'SCANNING' AND next_work_at <= sqlc.arg(now)::timestamptz
             AND (lease_expires_at IS NULL OR lease_expires_at < sqlc.arg(now)::timestamptz)))
     ORDER BY next_work_at
     LIMIT sqlc.arg(row_limit)
     FOR UPDATE SKIP LOCKED)
UPDATE media_objects m
   SET lease_owner = sqlc.arg(lease_owner), lease_expires_at = sqlc.arg(lease_expires_at)::timestamptz,
       scan_attempts = m.scan_attempts + 1, updated_at = sqlc.arg(now)::timestamptz
  FROM due WHERE m.media_id = due.media_id
RETURNING m.*;

-- name: ClaimForDeletion :many
WITH due AS (
    SELECT media_id FROM media_objects
     WHERE status = 'DELETE_PENDING' AND next_work_at <= sqlc.arg(now)::timestamptz
       AND (lease_expires_at IS NULL OR lease_expires_at < sqlc.arg(now)::timestamptz)
     ORDER BY next_work_at
     LIMIT sqlc.arg(row_limit)
     FOR UPDATE SKIP LOCKED)
UPDATE media_objects m
   SET lease_owner = sqlc.arg(lease_owner), lease_expires_at = sqlc.arg(lease_expires_at)::timestamptz, updated_at = sqlc.arg(now)::timestamptz
  FROM due WHERE m.media_id = due.media_id
RETURNING m.*;

-- name: RescheduleWork :exec
UPDATE media_objects SET next_work_at = $2, lease_owner = NULL, lease_expires_at = NULL, updated_at = $3
 WHERE media_id = $1 AND status = $4;

-- name: ClaimRejectedForPurge :many
SELECT m.* FROM media_objects m JOIN retention_rules r ON r.media_class = m.media_class
 WHERE m.status = 'REJECTED' AND m.rejected_at < sqlc.arg(now)::timestamptz - make_interval(days => r.auto_delete_rejected_after_days)
 ORDER BY m.rejected_at
 LIMIT sqlc.arg(row_limit)
 FOR UPDATE OF m SKIP LOCKED;

-- name: ListMediaByOwner :many
SELECT * FROM media_objects
 WHERE owner_domain = sqlc.arg(owner_domain) AND owner_type = sqlc.arg(owner_type) AND owner_id = sqlc.arg(owner_id)
   AND status <> 'DELETED'
   AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
        OR (created_at, media_id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY created_at DESC, media_id DESC
 LIMIT sqlc.arg(row_limit);

-- name: GetRetentionRule :one
SELECT * FROM retention_rules WHERE media_class = $1;
