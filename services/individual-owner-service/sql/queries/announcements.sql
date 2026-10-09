-- Customer notifications ------------------------------------------------------

-- name: InsertAnnouncement :one
INSERT INTO owner_announcements (id, property_id, sender_account_id, sender_role, audience, purpose, title, message, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: SetAnnouncementCounts :exec
UPDATE owner_announcements SET recipients = $2, skipped_no_account = $3 WHERE id = $1;

-- name: CountRecentAnnouncements :one
SELECT count(*) FROM owner_announcements WHERE property_id = $1 AND created_at > $2;

-- name: ListAnnouncements :many
SELECT * FROM owner_announcements WHERE property_id = $1 ORDER BY created_at DESC LIMIT 100;

-- name: AnnouncementDeliverySummary :many
SELECT status, count(*)::bigint AS n FROM customer_notifications WHERE announcement_id = $1 GROUP BY status;

-- name: InsertCustomerNotification :exec
INSERT INTO customer_notifications (id, announcement_id, property_id, recipient_domain, recipient_ref, relationship_type, relationship_id, purpose,
    variables, status, next_attempt_at, created_at, updated_at)
VALUES ($1, $2, $3, 'marketplace', $4, $5, $6, $7, $8, 'PENDING', $9, $9, $9)
ON CONFLICT DO NOTHING;

-- name: DueCustomerNotifications :many
SELECT * FROM customer_notifications WHERE status IN ('PENDING','BLOCKED') AND next_attempt_at <= $1
 ORDER BY next_attempt_at LIMIT $2 FOR UPDATE SKIP LOCKED;

-- name: SetCustomerNotificationStatus :exec
UPDATE customer_notifications SET status = $2, status_reason = $3, channels = $4, external_ref = coalesce($5, external_ref), attempts = attempts + $6,
    next_attempt_at = $7, updated_at = $8 WHERE id = $1;

-- name: ActiveTenantRecipients :many
SELECT id, customer_ref_id FROM tenancies WHERE property_id = $1 AND status IN ('ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING','MOVE_OUT_PENDING');

-- name: ActiveGuestRecipients :many
SELECT id, customer_ref_id FROM shortlet_bookings WHERE property_id = $1 AND status IN ('CONFIRMED','CHECKED_IN') AND check_out >= sqlc.arg(today)::date;

-- name: RelationshipStillActive :one
SELECT CASE sqlc.arg(relationship_type)::text
  WHEN 'tenancy' THEN EXISTS (SELECT 1 FROM tenancies WHERE id = sqlc.arg(relationship_id)::uuid AND customer_ref_id = sqlc.arg(recipient_ref)::uuid
                                AND status IN ('ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING','MOVE_OUT_PENDING'))
  WHEN 'shortlet_booking' THEN EXISTS (SELECT 1 FROM shortlet_bookings WHERE id = sqlc.arg(relationship_id)::uuid AND customer_ref_id = sqlc.arg(recipient_ref)::uuid
                                AND status IN ('CONFIRMED','CHECKED_IN'))
  ELSE false END::bool;

-- name: LeaseCustomerNotification :exec
UPDATE customer_notifications SET next_attempt_at = $2 WHERE id = $1;
