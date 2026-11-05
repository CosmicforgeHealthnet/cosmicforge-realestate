-- name: GetCRMGuest :one
SELECT (to_jsonb(g)-'encrypted_profile'-'profile_key_id')::jsonb AS record FROM guest_profiles g WHERE organisation_id=$1 AND id=$2;

-- name: GetCRMCorporate :one
SELECT (to_jsonb(c)-'encrypted_contacts'-'contact_key_id')::jsonb AS record FROM corporate_customer_profiles c WHERE organisation_id=$1 AND id=$2;

-- name: GetCRMCorporateContract :one
SELECT to_jsonb(c) FROM corporate_customer_contracts c WHERE organisation_id=$1 AND id=$2;
