-- name: CreateAccount :exec
INSERT INTO business_accounts(id,email,display_name) VALUES($1,$2,$3);
-- name: CreateCredential :exec
INSERT INTO business_credentials(account_id,password_hash) VALUES($1,$2);
-- name: GetAccountCredential :one
SELECT a.id,a.email,a.display_name,a.status,c.password_hash FROM business_accounts a JOIN business_credentials c ON c.account_id=a.id WHERE a.email=$1;
-- name: GetAccount :one
SELECT id,email,display_name,status,version,created_at,updated_at FROM business_accounts WHERE id=$1;
-- name: GetLiveSession :one
SELECT s.* FROM business_sessions s JOIN business_accounts a ON a.id=s.account_id WHERE s.id=$1 AND s.account_id=$2 AND s.revoked_at IS NULL AND s.expires_at>now() AND a.status='ACTIVE';
-- name: ListSessions :many
SELECT id,device_id,organisation_id,created_at,expires_at,last_refreshed_at,revoked_at,revoke_reason,mfa_at FROM business_sessions WHERE account_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3;
-- name: GetRefreshToken :one
SELECT t.id,t.session_id,t.token_hash,t.issued_at,t.expires_at AS token_expires_at,t.consumed_at,t.replaced_by,
s.account_id,s.created_at,s.expires_at AS session_expires_at,s.last_refreshed_at,s.revoked_at,s.revoke_reason
FROM business_refresh_tokens t JOIN business_sessions s ON s.id=t.session_id WHERE t.token_hash=$1 FOR UPDATE OF t,s;
-- name: CreateSession :exec
INSERT INTO business_sessions(id,account_id,device_id,organisation_id,created_at,expires_at,last_refreshed_at,mfa_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8);
-- name: CreateRefreshToken :exec
INSERT INTO business_refresh_tokens(id,session_id,token_hash,issued_at,expires_at) VALUES($1,$2,$3,$4,$5);
-- name: ConsumeRefreshToken :execrows
UPDATE business_refresh_tokens SET consumed_at=$2,replaced_by=$3 WHERE id=$1 AND consumed_at IS NULL;
-- name: TouchSession :exec
UPDATE business_sessions SET last_refreshed_at=$2 WHERE id=$1;
-- name: RevokeSession :execrows
UPDATE business_sessions SET revoked_at=$2,revoke_reason=$3 WHERE id=$1 AND revoked_at IS NULL;
-- name: RevokeAccountSessions :exec
UPDATE business_sessions SET revoked_at=$2,revoke_reason=$3 WHERE account_id=$1 AND revoked_at IS NULL;
