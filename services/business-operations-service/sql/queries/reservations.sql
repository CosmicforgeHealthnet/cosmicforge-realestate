-- name: GetCanonicalReservation :one
SELECT to_jsonb(r) AS record FROM reservations r WHERE organisation_id=$1 AND id=$2;
-- name: LockCanonicalReservation :one
SELECT id,organisation_id,property_id,state,version FROM reservations WHERE organisation_id=$1 AND id=$2 FOR UPDATE;
-- name: ReservationExpiryWork :many
SELECT id,organisation_id,property_id,version FROM reservations WHERE state IN ('INQUIRY','HOLD','PENDING_PAYMENT','PENDING_APPROVAL') AND hold_expires_at<=sqlc.arg(as_of)::timestamptz ORDER BY hold_expires_at,id LIMIT sqlc.arg(batch_size)::int;
-- name: ReservationActiveRooms :many
SELECT * FROM reservation_rooms WHERE organisation_id=$1 AND reservation_id=$2 AND state='ACTIVE' ORDER BY pool_id,date_from,id;
-- name: ReservationSourceReference :one
SELECT * FROM reservation_source_refs WHERE organisation_id=$1 AND connection_id=$2 AND provider=$3 AND external_reservation_id=$4 AND external_version=$5;
