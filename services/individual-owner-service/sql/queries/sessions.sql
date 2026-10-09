-- name: CreateSession :exec
INSERT INTO auth_sessions (id, account_id, created_at, expires_at, last_refreshed_at, device_id, device_name,
    device_platform, user_agent, ip_address, auth_method, mfa_method, authenticated_at, step_up_at, step_up_method)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: CreateRefreshToken :exec
INSERT INTO auth_refresh_tokens (id, session_id, token_hash, issued_at, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetRefreshTokenByHash :one
SELECT t.id, t.session_id, t.token_hash, t.issued_at, t.expires_at AS token_expires_at, t.consumed_at, t.replaced_by,
       s.account_id, s.created_at, s.expires_at AS session_expires_at, s.last_refreshed_at, s.revoked_at,
       s.revoke_reason, s.device_id, s.device_name, s.device_platform, s.user_agent, s.ip_address
  FROM auth_refresh_tokens t JOIN auth_sessions s ON s.id = t.session_id
 WHERE t.token_hash = $1;

-- name: ConsumeRefreshToken :execrows
UPDATE auth_refresh_tokens SET consumed_at = $2, replaced_by = $3
 WHERE id = $1 AND consumed_at IS NULL;

-- name: TouchSession :exec
UPDATE auth_sessions SET last_refreshed_at = $2 WHERE id = $1;

-- name: RevokeSession :execrows
UPDATE auth_sessions SET revoked_at = $2, revoke_reason = $3 WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeAccountSessions :execrows
UPDATE auth_sessions SET revoked_at = sqlc.arg(now)::timestamptz, revoke_reason = sqlc.arg(reason)::text
 WHERE account_id = sqlc.arg(account_id) AND revoked_at IS NULL
   AND (sqlc.narg(keep_session_id)::uuid IS NULL OR id <> sqlc.narg(keep_session_id)::uuid);

-- name: RevokeDeviceSessions :execrows
UPDATE auth_sessions SET revoked_at = $3, revoke_reason = $4
 WHERE account_id = $1 AND device_id = $2 AND revoked_at IS NULL;

-- name: GetSessionAuth :one
SELECT s.id AS session_id, s.account_id, s.expires_at, s.revoked_at, s.auth_method, s.mfa_method,
       s.authenticated_at, s.step_up_at, s.step_up_method, s.device_id,
       a.status AS account_status, a.mfa_enabled, a.version AS account_version
  FROM auth_sessions s JOIN owner_accounts a ON a.account_id = s.account_id
 WHERE s.id = $1;

-- name: GetSession :one
SELECT * FROM auth_sessions WHERE id = $1 AND account_id = $2;

-- name: ListActiveSessions :many
SELECT * FROM auth_sessions
 WHERE account_id = $1 AND revoked_at IS NULL AND expires_at > $2
 ORDER BY last_refreshed_at DESC, id DESC
 LIMIT $3;

-- name: SetSessionStepUp :exec
UPDATE auth_sessions SET step_up_at = $2, step_up_method = $3 WHERE id = $1 AND revoked_at IS NULL;

-- name: PurgeEndedSessions :execrows
DELETE FROM auth_sessions
 WHERE (revoked_at IS NOT NULL AND revoked_at < $1) OR expires_at < $1;
