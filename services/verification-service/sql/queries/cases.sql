-- name: CreateCase :one
INSERT INTO verification_cases (
    verification_id, verification_type, subject_type, subject_domain, subject_id, status, provider,
    requested_by_service, requested_by_actor_domain, requested_by_actor_id, organization_id,
    supersedes_verification_id, caller_reference, provider_metadata, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'DRAFT', $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
RETURNING *;

-- name: GetCase :one
SELECT * FROM verification_cases WHERE verification_id = $1;

-- name: GetCaseForUpdate :one
SELECT * FROM verification_cases WHERE verification_id = $1 FOR UPDATE;

-- name: FindOpenCase :one
SELECT * FROM verification_cases
 WHERE verification_type = $1 AND subject_type = $2 AND subject_domain = $3 AND subject_id = $4
   AND status IN ('DRAFT','SUBMITTED','PROCESSING','MANUAL_REVIEW');

-- name: UpdateCaseState :one
UPDATE verification_cases
   SET status = sqlc.arg(status),
       processing_substatus = sqlc.narg(processing_substatus),
       submitted_at = coalesce(sqlc.narg(submitted_at), submitted_at),
       decided_at = coalesce(sqlc.narg(decided_at), decided_at),
       expires_at = coalesce(sqlc.narg(expires_at), expires_at),
       expired_at = coalesce(sqlc.narg(expired_at), expired_at),
       reason_code = coalesce(sqlc.narg(reason_code), reason_code),
       updated_at = sqlc.arg(updated_at)
 WHERE verification_id = sqlc.arg(verification_id) AND status = sqlc.arg(expected_status)
RETURNING *;

-- name: ListCasesBySubject :many
SELECT * FROM verification_cases
 WHERE subject_domain = sqlc.arg(subject_domain) AND subject_type = sqlc.arg(subject_type) AND subject_id = sqlc.arg(subject_id)
   AND (sqlc.narg(verification_type)::text IS NULL OR verification_type = sqlc.narg(verification_type))
   AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
        OR (created_at, verification_id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY created_at DESC, verification_id DESC
 LIMIT sqlc.arg(row_limit);

-- name: ClaimExpiredCases :many
SELECT * FROM verification_cases
 WHERE status = 'VERIFIED' AND expires_at <= sqlc.arg(now)::timestamptz
 ORDER BY expires_at
 LIMIT sqlc.arg(row_limit)
 FOR UPDATE SKIP LOCKED;
