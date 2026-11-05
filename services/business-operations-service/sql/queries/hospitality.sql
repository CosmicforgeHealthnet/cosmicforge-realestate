-- name: GetPhysicalRoomAssignment :one
SELECT to_jsonb(a)::jsonb AS record FROM room_assignments a WHERE organisation_id=$1 AND id=$2;

-- name: GetHospitalityStay :one
SELECT to_jsonb(s)::jsonb AS record FROM stay_records s WHERE organisation_id=$1 AND id=$2;

-- name: GetHospitalityFolio :one
SELECT to_jsonb(f)::jsonb AS record FROM folios f WHERE organisation_id=$1 AND id=$2;

-- name: GetHospitalityGroup :one
SELECT to_jsonb(g)::jsonb AS record FROM reservation_groups g WHERE organisation_id=$1 AND id=$2;

-- name: GetLatestFolioCloseReview :one
SELECT to_jsonb(r)::jsonb AS record FROM folio_close_reviews r WHERE organisation_id=$1 AND folio_id=$2 ORDER BY created_at DESC,id DESC LIMIT 1;
