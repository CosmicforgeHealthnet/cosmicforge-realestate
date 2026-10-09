-- name: CreateChallenge :exec
INSERT INTO auth_challenges (challenge_id, account_id, session_id, parent_id, purpose, channel, destination,
    secret_hash, max_attempts, delivery_status, context, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: GetChallengeForUpdate :one
SELECT * FROM auth_challenges WHERE challenge_id = $1 FOR UPDATE;

-- name: RecordChallengeAttempt :exec
UPDATE auth_challenges SET attempts = attempts + 1,
       invalidated_at = CASE WHEN attempts + 1 >= max_attempts THEN sqlc.arg(now)::timestamptz ELSE invalidated_at END
 WHERE challenge_id = sqlc.arg(challenge_id);

-- name: ConsumeChallenge :execrows
UPDATE auth_challenges SET consumed_at = $2
 WHERE challenge_id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL AND expires_at > $2;

-- name: InvalidateOpenChallenges :exec
UPDATE auth_challenges SET invalidated_at = sqlc.arg(now)::timestamptz
 WHERE account_id = sqlc.arg(account_id) AND purpose = sqlc.arg(purpose)
   AND consumed_at IS NULL AND invalidated_at IS NULL
   AND (sqlc.narg(parent_id)::uuid IS NULL OR parent_id = sqlc.narg(parent_id)::uuid);

-- name: CountRecentChallenges :one
SELECT count(*) FROM auth_challenges WHERE account_id = $1 AND purpose = $2 AND created_at > $3;

-- name: SetChallengeDelivery :exec
UPDATE auth_challenges SET delivery_status = $2, notification_id = $3 WHERE challenge_id = $1;

-- name: GetOpenChildChallenge :one
SELECT * FROM auth_challenges
 WHERE parent_id = $1 AND purpose = $2 AND consumed_at IS NULL AND invalidated_at IS NULL
 ORDER BY created_at DESC LIMIT 1
 FOR UPDATE;

-- name: PurgeExpiredChallenges :execrows
DELETE FROM auth_challenges WHERE expires_at < $1 AND parent_id IS NULL;
