-- name: NextAttemptNo :one
SELECT coalesce(max(attempt_no), 0)::int + 1 AS next FROM verification_attempts WHERE verification_id = $1;

-- name: CreateAttempt :one
INSERT INTO verification_attempts (attempt_id, verification_id, attempt_no, provider, client_reference, state, next_attempt_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'QUEUED', $6, $6, $6)
RETURNING *;

-- name: ClaimDueAttempts :many
WITH due AS (
    SELECT attempt_id FROM verification_attempts
     WHERE state IN ('QUEUED','SUBMITTING','SUBMITTED','UNKNOWN')
       AND next_attempt_at <= sqlc.arg(now)
       AND (lease_expires_at IS NULL OR lease_expires_at < sqlc.arg(now))
     ORDER BY next_attempt_at
     LIMIT sqlc.arg(row_limit)
     FOR UPDATE SKIP LOCKED)
UPDATE verification_attempts a
   SET lease_owner = sqlc.arg(lease_owner), lease_expires_at = sqlc.arg(lease_expires_at), tries = a.tries + 1, updated_at = sqlc.arg(now)
  FROM due
 WHERE a.attempt_id = due.attempt_id
RETURNING a.*;

-- name: GetAttemptForUpdate :one
SELECT * FROM verification_attempts WHERE attempt_id = $1 FOR UPDATE;

-- name: GetAttemptByClientReference :one
SELECT * FROM verification_attempts WHERE client_reference = $1;

-- name: GetAttemptByProviderReference :one
SELECT * FROM verification_attempts WHERE provider = $1 AND provider_reference = $2;

-- name: GetLatestAttempt :one
SELECT * FROM verification_attempts WHERE verification_id = $1 ORDER BY attempt_no DESC LIMIT 1;

-- name: UpdateAttempt :one
UPDATE verification_attempts
   SET state = sqlc.arg(state),
       provider_reference = coalesce(sqlc.narg(provider_reference), provider_reference),
       next_attempt_at = sqlc.arg(next_attempt_at),
       last_error_code = sqlc.narg(last_error_code),
       completed_at = sqlc.narg(completed_at),
       lease_owner = NULL, lease_expires_at = NULL,
       updated_at = sqlc.arg(updated_at)
 WHERE attempt_id = sqlc.arg(attempt_id)
RETURNING *;

-- name: MarkAttemptSubmitting :execrows
UPDATE verification_attempts SET state = 'SUBMITTING', updated_at = sqlc.arg(now)
 WHERE attempt_id = sqlc.arg(attempt_id) AND lease_owner = sqlc.arg(lease_owner) AND state IN ('QUEUED','SUBMITTING','UNKNOWN');

-- name: ScheduleAttemptNow :exec
UPDATE verification_attempts SET next_attempt_at = sqlc.arg(now), updated_at = sqlc.arg(now)
 WHERE attempt_id = sqlc.arg(attempt_id) AND state IN ('SUBMITTED','UNKNOWN');
