-- name: CreateAccount :one
INSERT INTO owner_accounts (account_id, status, email, phone_e164, display_name, locale, password_hash,
    password_changed_at, status_changed_at, created_at, updated_at)
VALUES ($1, 'PENDING_VERIFICATION', $2, $3, $4, $5, $6, $7, $7, $7, $7)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM owner_accounts WHERE account_id = $1;

-- name: GetAccountForUpdate :one
SELECT * FROM owner_accounts WHERE account_id = $1 FOR UPDATE;

-- name: GetOpenAccountByEmail :one
SELECT * FROM owner_accounts WHERE email = $1 AND status <> 'CLOSED';

-- name: GetOpenAccountByVerifiedPhone :one
SELECT * FROM owner_accounts
 WHERE phone_e164 = sqlc.arg(phone)::text AND phone_verified_at IS NOT NULL AND status <> 'CLOSED';

-- name: RecordLoginFailure :one
UPDATE owner_accounts
   SET failed_login_count = CASE WHEN locked_until IS NOT NULL AND locked_until <= sqlc.arg(now)::timestamptz
                                 THEN 1 ELSE failed_login_count + 1 END,
       locked_until = CASE
           WHEN (CASE WHEN locked_until IS NOT NULL AND locked_until <= sqlc.arg(now)::timestamptz
                      THEN 1 ELSE failed_login_count + 1 END) >= sqlc.arg(max_failures)::int
           THEN sqlc.arg(lock_until)::timestamptz
           WHEN locked_until IS NOT NULL AND locked_until <= sqlc.arg(now)::timestamptz THEN NULL
           ELSE locked_until END,
       updated_at = sqlc.arg(now)::timestamptz
 WHERE account_id = sqlc.arg(account_id)
RETURNING failed_login_count, locked_until;

-- name: ResetLoginFailures :exec
UPDATE owner_accounts SET failed_login_count = 0, locked_until = NULL, updated_at = $2
 WHERE account_id = $1 AND (failed_login_count <> 0 OR locked_until IS NOT NULL);

-- name: TransitionAccountStatus :one
UPDATE owner_accounts
   SET status = sqlc.arg(status),
       status_reason = sqlc.narg(status_reason),
       status_changed_at = sqlc.arg(now),
       closure_requested_at = sqlc.narg(closure_requested_at),
       closure_effective_at = sqlc.narg(closure_effective_at),
       closed_at = sqlc.narg(closed_at),
       version = version + 1,
       updated_at = sqlc.arg(now)
 WHERE account_id = sqlc.arg(account_id) AND status = sqlc.arg(expected_status) AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: InsertStatusHistory :exec
INSERT INTO account_status_history (history_id, account_id, from_status, to_status, reason_code, actor_type,
    actor_domain, actor_id, calling_service, resolution_reference, note, account_version, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: ListStatusHistory :many
SELECT * FROM account_status_history WHERE account_id = $1 ORDER BY occurred_at, history_id;

-- name: SetEmailVerified :one
UPDATE owner_accounts
   SET email = sqlc.arg(email), email_verified_at = sqlc.arg(now)::timestamptz, version = version + 1, updated_at = sqlc.arg(now)::timestamptz
 WHERE account_id = sqlc.arg(account_id)
RETURNING *;

-- name: SetPhoneVerified :one
UPDATE owner_accounts
   SET phone_e164 = sqlc.arg(phone_e164)::text, phone_verified_at = sqlc.arg(now)::timestamptz, version = version + 1, updated_at = sqlc.arg(now)::timestamptz
 WHERE account_id = sqlc.arg(account_id)
RETURNING *;

-- name: UpdatePassword :one
UPDATE owner_accounts
   SET password_hash = $2, password_changed_at = $3, failed_login_count = 0, locked_until = NULL,
       version = version + 1, updated_at = $3
 WHERE account_id = $1
RETURNING *;

-- name: RehashPassword :exec
UPDATE owner_accounts SET password_hash = $2, updated_at = $3 WHERE account_id = $1;

-- name: UpdateProfile :one
UPDATE owner_accounts
   SET display_name = sqlc.arg(display_name), locale = sqlc.arg(locale), version = version + 1, updated_at = sqlc.arg(now)
 WHERE account_id = sqlc.arg(account_id) AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: SetMFAEnabled :one
UPDATE owner_accounts SET mfa_enabled = $2, version = version + 1, updated_at = $3
 WHERE account_id = $1
RETURNING *;

-- name: ClaimDueClosures :many
SELECT * FROM owner_accounts
 WHERE status = 'CLOSURE_PENDING' AND closure_effective_at <= sqlc.arg(now)::timestamptz
 ORDER BY closure_effective_at
 LIMIT sqlc.arg(row_limit)
 FOR UPDATE SKIP LOCKED;
