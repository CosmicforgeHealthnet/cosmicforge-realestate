-- 02 Profile and contacts ----------------------------------------------------

-- name: GetOwnerProfile :one
SELECT * FROM owner_profiles WHERE account_id = $1;

-- name: InsertOwnerProfile :one
INSERT INTO owner_profiles (account_id, preferred_name, bio, avatar_media_id, public_profile, languages, timezone,
    accessibility_needs, preferred_channel, marketing_opt_in, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11, $11)
RETURNING *;

-- name: UpdateOwnerProfile :one
UPDATE owner_profiles
   SET preferred_name = $3, bio = $4, avatar_media_id = $5, public_profile = $6, languages = $7, timezone = $8,
       accessibility_needs = $9, preferred_channel = $10, marketing_opt_in = $11, version = version + 1, updated_at = $12
 WHERE account_id = $1 AND version = $2
RETURNING *;

-- name: InsertOwnerContact :one
INSERT INTO owner_contacts (id, account_id, contact_type, label, name, relationship, email, phone_e164, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListOwnerContacts :many
SELECT * FROM owner_contacts WHERE account_id = $1 AND removed_at IS NULL ORDER BY created_at, id;

-- name: CountOwnerContacts :one
SELECT count(*) FROM owner_contacts WHERE account_id = $1 AND removed_at IS NULL;

-- name: RemoveOwnerContact :execrows
UPDATE owner_contacts SET removed_at = $3 WHERE id = $1 AND account_id = $2 AND removed_at IS NULL;

-- 03 Banking, payouts and tax ----------------------------------------------

-- name: InsertPayoutAccount :one
INSERT INTO payout_accounts (id, account_id, status, bank_code, bank_name, account_name, account_number_last4,
    account_fingerprint, currency, payment_beneficiary_ref, name_match, created_at, updated_at)
VALUES ($1, $2, 'PENDING_VERIFICATION', $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
RETURNING *;

-- name: ListPayoutAccounts :many
SELECT * FROM payout_accounts WHERE account_id = $1 AND status <> 'REMOVED' ORDER BY created_at, id;

-- name: GetPayoutAccountForUpdate :one
SELECT * FROM payout_accounts WHERE id = $1 AND account_id = $2 FOR UPDATE;

-- name: GetPayoutAccount :one
SELECT * FROM payout_accounts WHERE id = $1 AND account_id = $2;

-- name: SetPayoutAccountVerification :one
UPDATE payout_accounts
   SET status = sqlc.arg(status), name_match = sqlc.narg(name_match), payment_beneficiary_ref = coalesce(sqlc.narg(beneficiary_ref), payment_beneficiary_ref),
       verified_at = sqlc.narg(verified_at), cooling_off_until = sqlc.narg(cooling_off_until),
       version = version + 1, updated_at = sqlc.arg(now)
 WHERE id = sqlc.arg(id) AND version = sqlc.arg(version)
RETURNING *;

-- name: ActivateCooledPayoutAccounts :many
UPDATE payout_accounts
   SET status = 'ACTIVE', activated_at = sqlc.arg(now)::timestamptz, version = version + 1, updated_at = sqlc.arg(now)::timestamptz
 WHERE id IN (SELECT id FROM payout_accounts WHERE status = 'COOLING_OFF' AND cooling_off_until <= sqlc.arg(now)::timestamptz
              ORDER BY cooling_off_until LIMIT sqlc.arg(max_rows) FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: RemovePayoutAccount :one
UPDATE payout_accounts SET status = 'REMOVED', removed_at = $3, version = version + 1, updated_at = $3
 WHERE id = $1 AND version = $2 AND status <> 'REMOVED'
RETURNING *;

-- name: GetPayoutProfile :one
SELECT * FROM payout_profiles WHERE account_id = $1;

-- name: GetPayoutProfileForUpdate :one
SELECT * FROM payout_profiles WHERE account_id = $1 FOR UPDATE;

-- name: InsertPayoutProfile :one
INSERT INTO payout_profiles (account_id, default_payout_account_id, rent_payout_account_id, sale_payout_account_id,
    shortlet_payout_account_id, version, updated_at)
VALUES ($1, $2, $3, $4, $5, 1, $6)
RETURNING *;

-- name: UpdatePayoutProfile :one
UPDATE payout_profiles
   SET default_payout_account_id = $3, rent_payout_account_id = $4, sale_payout_account_id = $5,
       shortlet_payout_account_id = $6, version = version + 1, updated_at = $7
 WHERE account_id = $1 AND version = $2
RETURNING *;

-- name: InsertPayoutChangeRequest :one
INSERT INTO payout_change_requests (id, account_id, payout_account_id, change_type, status, step_up_method,
    requested_session, cooling_off_until, applied_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ApplyDuePayoutChanges :many
UPDATE payout_change_requests SET status = 'APPLIED', applied_at = sqlc.arg(now)::timestamptz
 WHERE id IN (SELECT id FROM payout_change_requests WHERE status = 'PENDING_COOLING_OFF' AND cooling_off_until <= sqlc.arg(now)::timestamptz
              ORDER BY cooling_off_until LIMIT sqlc.arg(max_rows) FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: ListPayoutChangeRequests :many
SELECT * FROM payout_change_requests WHERE account_id = $1 ORDER BY created_at DESC, id DESC LIMIT 50;

-- name: GetTaxProfile :one
SELECT * FROM tax_profiles WHERE account_id = $1;

-- name: InsertTaxProfile :one
INSERT INTO tax_profiles (account_id, tax_id_type, tax_id_last4, tax_id_fingerprint, residency_country,
    reporting_preferences, policy_version, version, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8)
RETURNING *;

-- name: UpdateTaxProfile :one
UPDATE tax_profiles
   SET tax_id_type = $3, tax_id_last4 = $4, tax_id_fingerprint = $5, residency_country = $6,
       reporting_preferences = $7, policy_version = $8, version = version + 1, updated_at = $9
 WHERE account_id = $1 AND version = $2
RETURNING *;
