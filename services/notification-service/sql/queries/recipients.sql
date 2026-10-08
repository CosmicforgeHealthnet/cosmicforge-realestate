-- name: UpsertPushEndpoint :one
INSERT INTO push_endpoints (endpoint_id, recipient_domain, recipient_id, provider, platform, token, device_id, app_version, status, last_seen_at, created_at, updated_at)
VALUES ($1, $2, $3, 'FCM', $4, $5, $6, $7, 'ACTIVE', $8, $8, $8)
ON CONFLICT (token) DO UPDATE
   SET recipient_domain = EXCLUDED.recipient_domain, recipient_id = EXCLUDED.recipient_id, platform = EXCLUDED.platform,
       device_id = EXCLUDED.device_id, app_version = EXCLUDED.app_version, status = 'ACTIVE', invalid_reason = NULL,
       last_seen_at = EXCLUDED.last_seen_at, updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: GetPushEndpoint :one
SELECT * FROM push_endpoints WHERE endpoint_id = $1;

-- name: ListActivePushEndpoints :many
SELECT * FROM push_endpoints WHERE recipient_domain = $1 AND recipient_id = $2 AND status = 'ACTIVE' ORDER BY last_seen_at DESC LIMIT 20;

-- name: RevokePushEndpoint :execrows
UPDATE push_endpoints SET status = 'REVOKED', updated_at = $4
 WHERE endpoint_id = $1 AND recipient_domain = $2 AND recipient_id = $3 AND status <> 'REVOKED';

-- name: InvalidatePushEndpoint :exec
UPDATE push_endpoints SET status = 'INVALID', invalid_reason = $2, updated_at = $3 WHERE endpoint_id = $1 AND status = 'ACTIVE';

-- name: ListPreferences :many
SELECT * FROM notification_preferences WHERE recipient_domain = $1 AND recipient_id = $2 ORDER BY category, channel;

-- name: UpsertPreference :exec
INSERT INTO notification_preferences (recipient_domain, recipient_id, category, channel, enabled, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (recipient_domain, recipient_id, category, channel) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = EXCLUDED.updated_at;

-- name: GetSettings :one
SELECT * FROM recipient_settings WHERE recipient_domain = $1 AND recipient_id = $2;

-- name: UpsertSettings :exec
INSERT INTO recipient_settings (recipient_domain, recipient_id, quiet_start, quiet_end, timezone, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (recipient_domain, recipient_id) DO UPDATE
   SET quiet_start = EXCLUDED.quiet_start, quiet_end = EXCLUDED.quiet_end, timezone = EXCLUDED.timezone, updated_at = EXCLUDED.updated_at;

-- name: GetPushEndpointByToken :one
SELECT * FROM push_endpoints WHERE token = $1;
