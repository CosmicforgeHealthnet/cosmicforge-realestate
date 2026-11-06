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

-- name: GetFolioFiscalReview :one
SELECT to_jsonb(v)::jsonb AS record FROM folio_fiscal_reviews v WHERE organisation_id=$1 AND id=$2;

-- name: ListFolioFiscalReviews :many
SELECT to_jsonb(v)::jsonb AS record FROM folio_fiscal_reviews v WHERE organisation_id=$1 AND folio_id=$2 ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: GetFolioItemsWithFiscalDetails :one
SELECT coalesce(jsonb_agg(to_jsonb(i)||jsonb_build_object('fiscal_review',(SELECT to_jsonb(v) FROM folio_fiscal_reviews v WHERE v.organisation_id=i.organisation_id AND v.folio_item_id=i.id ORDER BY review_version DESC LIMIT 1)) ORDER BY i.created_at,i.id),'[]'::jsonb)::jsonb AS record FROM folio_items i WHERE i.organisation_id=$1 AND i.folio_id=$2;

-- name: GetLatestHospitalityDayAutomation :one
SELECT to_jsonb(p)::jsonb AS record FROM hospitality_day_automation_policies p WHERE organisation_id=$1 AND property_id=$2 ORDER BY version DESC LIMIT 1;
