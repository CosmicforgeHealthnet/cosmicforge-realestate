-- name: InsertDocument :exec
INSERT INTO documents (media_id, document_type, title, created_at) VALUES ($1, $2, $3, $4);

-- name: GetDocument :one
SELECT * FROM documents WHERE media_id = $1;

-- name: CreateUploadSession :one
INSERT INTO upload_sessions (upload_id, media_id, status, expires_at, created_at) VALUES ($1, $2, 'OPEN', $3, $4) RETURNING *;

-- name: GetUploadSessionForUpdate :one
SELECT * FROM upload_sessions WHERE media_id = $1 ORDER BY created_at DESC LIMIT 1 FOR UPDATE;

-- name: CompleteUploadSession :exec
UPDATE upload_sessions SET status = 'COMPLETED', completed_at = $2 WHERE upload_id = $1 AND status = 'OPEN';

-- name: ExpireUploadSessions :execrows
UPDATE upload_sessions SET status = 'EXPIRED' WHERE status = 'OPEN' AND expires_at < $1;

-- name: InsertStorageObject :exec
INSERT INTO storage_objects (storage_key, media_id, bucket, size_bytes, etag, created_at) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (storage_key) DO NOTHING;

-- name: MarkStorageObjectDeleted :exec
UPDATE storage_objects SET deleted_at = $2 WHERE storage_key = $1 AND deleted_at IS NULL;

-- name: InsertGrant :one
INSERT INTO access_policies (policy_id, media_id, grantee_type, grantee_service, grantee_domain, grantee_actor_id, permission,
    granted_by_service, granted_by_actor_domain, granted_by_actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, 'READ', $7, $8, $9, $10)
RETURNING *;

-- name: RevokeAllGrants :execrows
UPDATE access_policies SET revoked_at = $2 WHERE media_id = $1 AND revoked_at IS NULL;

-- name: ListActiveGrants :many
SELECT * FROM access_policies WHERE media_id = $1 AND revoked_at IS NULL ORDER BY created_at, policy_id;

-- name: InsertScanResult :exec
INSERT INTO scan_results (scan_id, media_id, scanner, verdict, reason, signature, detected_mime_type, computed_sha256, computed_size, duration_ms, scanned_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: InsertDownloadGrant :exec
INSERT INTO download_grants (grant_id, media_id, grantee_service, grantee_domain, grantee_actor_id, purpose, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: CreateDeletionRequest :one
INSERT INTO deletion_requests (request_id, media_id, status, reason, requested_by_service, requested_by_actor_domain, requested_by_actor_id, created_at)
VALUES ($1, $2, 'PENDING', $3, $4, $5, $6, $7)
RETURNING *;

-- name: CompleteDeletionRequest :exec
UPDATE deletion_requests SET status = 'COMPLETED', completed_at = $2 WHERE media_id = $1 AND status = 'PENDING';
