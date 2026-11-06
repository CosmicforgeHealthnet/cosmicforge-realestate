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

-- name: GetLatestHospitalityArrivalPolicy :one
SELECT to_jsonb(p)::jsonb AS record FROM hospitality_arrival_policies p WHERE organisation_id=$1 AND property_id=$2 ORDER BY version DESC LIMIT 1;

-- name: GetRoomSafetyProof :one
SELECT jsonb_build_object(
 'organisation_id',s.organisation_id,'property_id',s.property_id,'room_id',s.room_id,'status_id',s.id,
 'room_state',s.state,'room_version',s.version,'status_updated_at',s.updated_at,
 'node_kind',n.kind,'node_state',n.state,'node_version',n.version,'node_property_id',business_graph_property(n.organisation_id,n.id),'node_capacity',coalesce((n.attributes->>'capacity')::integer,0),
 'active_stay_count',(SELECT count(*) FROM stay_records st JOIN room_assignments a ON a.organisation_id=st.organisation_id AND a.id=st.assignment_id WHERE st.organisation_id=s.organisation_id AND a.room_id=s.room_id AND st.state='ACTIVE'),
 'occupied_assignment_count',(SELECT count(*) FROM room_assignments a WHERE a.organisation_id=s.organisation_id AND a.room_id=s.room_id AND a.state='OCCUPIED'),
 'effective_assignment_count',(SELECT count(*) FROM room_assignments a WHERE a.organisation_id=s.organisation_id AND a.room_id=s.room_id AND a.state IN ('ASSIGNED','OCCUPIED')),
 'active_stays',(SELECT coalesce(jsonb_agg(to_jsonb(source) ORDER BY source.id),'[]'::jsonb) FROM (
  SELECT st.id,st.version,a.id AS assignment_id,a.state AS assignment_state,a.version AS assignment_version,
  root.id AS reservation_id,rr.id AS reservation_room_id,root.version AS reservation_version,root.state AS reservation_state,root.allocation_state,
  a.date_from::text,a.date_to::text,rr.adults+rr.children AS occupants,root.policy_snapshot->>'check_out_local' AS checkout_local,root.policy_snapshot->>'acceptance_timezone' AS acceptance_timezone,
  EXISTS(SELECT 1 FROM hospitality_history h WHERE h.organisation_id=st.organisation_id AND h.entity_type='stay' AND h.entity_id=st.id AND h.action='CHECKED_IN' AND h.entity_version<=st.version) AS has_arrival_evidence
  FROM stay_records st JOIN room_assignments a ON a.organisation_id=st.organisation_id AND a.id=st.assignment_id JOIN reservations root ON root.organisation_id=a.organisation_id AND root.id=a.reservation_id AND root.id=st.reservation_id JOIN reservation_rooms rr ON rr.organisation_id=a.organisation_id AND rr.id=a.reservation_room_id
  WHERE st.organisation_id=s.organisation_id AND a.room_id=s.room_id AND st.state='ACTIVE' ORDER BY st.id LIMIT 10
 ) source),
 'assignments',(SELECT coalesce(jsonb_agg(to_jsonb(source) ORDER BY source.id),'[]'::jsonb) FROM (
  SELECT a.id,a.version,a.state,root.id AS reservation_id,root.version AS reservation_version,root.state AS reservation_state,
  EXISTS(SELECT 1 FROM stay_records st WHERE st.organisation_id=a.organisation_id AND st.assignment_id=a.id) AS has_any_stay,
  EXISTS(SELECT 1 FROM reservation_versions v WHERE v.organisation_id=root.organisation_id AND v.reservation_id=root.id AND v.reservation_version<=root.version AND v.action IN ('CANCELLED','REJECTED','EXPIRED','NO_SHOW') AND v.snapshot->'reservation'->>'state'=root.state) AS has_terminal_evidence
  FROM room_assignments a JOIN reservations root ON root.organisation_id=a.organisation_id AND root.id=a.reservation_id WHERE a.organisation_id=s.organisation_id AND a.room_id=s.room_id AND a.state IN ('ASSIGNED','OCCUPIED') ORDER BY a.id LIMIT 500
 ) source),
 'latest_departure',(SELECT jsonb_build_object('stay_id',st.id,'stay_version',st.version,'assignment_id',a.id,'assignment_state',a.state,'checked_out_at',st.checked_out_at,'recorded_at',(SELECT h.created_at FROM hospitality_history h WHERE h.organisation_id=st.organisation_id AND h.entity_type='stay' AND h.entity_id=st.id AND h.entity_version=st.version AND h.action='CHECKED_OUT' ORDER BY h.created_at DESC,h.id DESC LIMIT 1),
  'has_departure_evidence',EXISTS(SELECT 1 FROM hospitality_history h WHERE h.organisation_id=st.organisation_id AND h.entity_type='stay' AND h.entity_id=st.id AND h.entity_version=st.version AND h.action='CHECKED_OUT'))
  FROM stay_records st JOIN room_assignments a ON a.organisation_id=st.organisation_id AND a.id=st.assignment_id AND a.reservation_id=st.reservation_id WHERE st.organisation_id=s.organisation_id AND a.room_id=s.room_id AND st.state='DEPARTED' ORDER BY st.checked_out_at DESC,st.id DESC LIMIT 1)
)::jsonb AS record
FROM room_status_current s JOIN inventory_nodes n ON n.organisation_id=s.organisation_id AND n.id=s.room_id
WHERE s.organisation_id=$1 AND s.room_id=$2;

-- name: GetRoomReconciliationReport :one
SELECT to_jsonb(r)::jsonb AS record FROM room_reconciliation_reports r WHERE organisation_id=$1 AND id=$2;
