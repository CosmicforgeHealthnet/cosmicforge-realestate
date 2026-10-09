-- name: CreateTOTPCredential :exec
INSERT INTO mfa_totp_credentials (credential_id, account_id, status, secret_ciphertext, key_id, created_at)
VALUES ($1, $2, 'PENDING', $3, $4, $5);

-- name: DiscardPendingTOTP :exec
UPDATE mfa_totp_credentials SET status = 'DISABLED', disabled_at = $2
 WHERE account_id = $1 AND status = 'PENDING';

-- name: GetTOTPCredential :one
SELECT * FROM mfa_totp_credentials WHERE account_id = $1 AND status = $2 FOR UPDATE;

-- name: ActivateTOTP :execrows
UPDATE mfa_totp_credentials SET status = 'ACTIVE', confirmed_at = $2, last_used_step = $3
 WHERE credential_id = $1 AND status = 'PENDING';

-- name: DisableActiveTOTP :execrows
UPDATE mfa_totp_credentials SET status = 'DISABLED', disabled_at = $2
 WHERE account_id = $1 AND status = 'ACTIVE';

-- name: AdvanceTOTPStep :execrows
UPDATE mfa_totp_credentials SET last_used_step = $2
 WHERE credential_id = $1 AND (last_used_step IS NULL OR last_used_step < $2);

-- name: InsertRecoveryCode :exec
INSERT INTO mfa_recovery_codes (code_id, account_id, batch_id, code_hash, created_at)
VALUES ($1, $2, $3, $4, $5);

-- name: InvalidateRecoveryCodes :exec
UPDATE mfa_recovery_codes SET invalidated_at = $2
 WHERE account_id = $1 AND used_at IS NULL AND invalidated_at IS NULL;

-- name: UseRecoveryCode :execrows
UPDATE mfa_recovery_codes SET used_at = $3
 WHERE account_id = $1 AND code_hash = $2 AND used_at IS NULL AND invalidated_at IS NULL;

-- name: CountUsableRecoveryCodes :one
SELECT count(*) FROM mfa_recovery_codes WHERE account_id = $1 AND used_at IS NULL AND invalidated_at IS NULL;
