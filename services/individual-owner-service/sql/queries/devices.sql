-- name: GetDeviceByKey :one
SELECT * FROM auth_devices WHERE account_id = $1 AND device_key_hash = $2 FOR UPDATE;

-- name: CountKnownDevices :one
SELECT count(*) FROM auth_devices WHERE account_id = $1;

-- name: InsertDevice :one
INSERT INTO auth_devices (device_id, account_id, device_key_hash, name, platform, user_agent, last_ip, first_seen_at, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
RETURNING *;

-- name: TouchDevice :one
UPDATE auth_devices
   SET last_seen_at = $2, last_ip = $3, user_agent = $4, name = coalesce($5, name), platform = $6, revoked_at = NULL
 WHERE device_id = $1
RETURNING *;

-- name: ListDevices :many
SELECT * FROM auth_devices WHERE account_id = $1 AND revoked_at IS NULL
 ORDER BY last_seen_at DESC, device_id DESC LIMIT $2;

-- name: GetDevice :one
SELECT * FROM auth_devices WHERE device_id = $1 AND account_id = $2;

-- name: TrustDevice :one
UPDATE auth_devices SET trusted_at = coalesce(trusted_at, $3)
 WHERE device_id = $1 AND account_id = $2 AND revoked_at IS NULL
RETURNING *;

-- name: RevokeDevice :one
UPDATE auth_devices SET revoked_at = $3, trusted_at = NULL
 WHERE device_id = $1 AND account_id = $2 AND revoked_at IS NULL
RETURNING *;
