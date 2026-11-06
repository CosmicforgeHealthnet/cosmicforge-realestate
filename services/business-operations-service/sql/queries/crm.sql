-- name: GetCRMGuest :one
SELECT (to_jsonb(g)-'encrypted_profile'-'profile_key_id')::jsonb AS record FROM guest_profiles g WHERE organisation_id=$1 AND id=$2;

-- name: GetCRMCorporate :one
SELECT (to_jsonb(c)-'encrypted_contacts'-'contact_key_id')::jsonb AS record FROM corporate_customer_profiles c WHERE organisation_id=$1 AND id=$2;

-- name: GetCRMCorporateContract :one
SELECT to_jsonb(c) FROM corporate_customer_contracts c WHERE organisation_id=$1 AND id=$2;

-- name: ListCRMStayProjections :many
SELECT to_jsonb(v)::jsonb AS record FROM stay_history_projection v WHERE organisation_id=$1 AND property_id=$2 AND guest_id=$3 ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;
