-- Section 13 closure: stay extensions, statutory calendar, valuations,
-- assisted use.

-- name: ClaimExtensionDays :execrows
-- Claims the added nights (HOLD) and the new turnover buffer (BLOCKED) for
-- an extension. Days still free, or the booking's own current buffer, can be
-- claimed; anything else held, booked or blocked refuses the extension.
INSERT INTO availability_calendar (listing_id, day, status, source, ref_id, updated_at)
SELECT sqlc.arg(listing_id)::uuid, d::date, CASE WHEN d::date < sqlc.arg(new_check_out)::date THEN 'HOLD' ELSE 'BLOCKED' END, 'BOOKING',
       sqlc.arg(extension_id)::uuid, sqlc.arg(now)::timestamptz
  FROM generate_series(sqlc.arg(from_day)::date, sqlc.arg(to_day)::date, interval '1 day') AS d
ON CONFLICT (listing_id, day) DO UPDATE SET status = EXCLUDED.status, source = EXCLUDED.source, ref_id = EXCLUDED.ref_id, updated_at = EXCLUDED.updated_at
 WHERE availability_calendar.status = 'AVAILABLE'
    OR (availability_calendar.status = 'BLOCKED' AND availability_calendar.ref_id = sqlc.arg(booking_id)::uuid);

-- name: RestoreExtensionDays :exec
-- Gives back what an unapplied extension claimed: the booking's previous
-- turnover buffer returns to the booking, everything else is freed.
UPDATE availability_calendar
   SET status = CASE WHEN day <= sqlc.arg(previous_buffer_end)::date THEN 'BLOCKED' ELSE 'AVAILABLE' END,
       source = CASE WHEN day <= sqlc.arg(previous_buffer_end)::date THEN 'BOOKING' ELSE 'SYSTEM' END,
       ref_id = CASE WHEN day <= sqlc.arg(previous_buffer_end)::date THEN sqlc.arg(booking_id)::uuid ELSE NULL END,
       updated_at = sqlc.arg(now)::timestamptz
 WHERE ref_id = sqlc.arg(extension_id)::uuid;

-- name: ApplyExtensionDays :exec
UPDATE availability_calendar SET status = CASE WHEN status = 'HOLD' THEN 'BOOKED' ELSE status END, ref_id = sqlc.arg(booking_id)::uuid,
       updated_at = sqlc.arg(now)::timestamptz
 WHERE ref_id = sqlc.arg(extension_id)::uuid;

-- name: ExtendBookingStay :exec
UPDATE shortlet_bookings SET check_out = $2, version = version + 1, updated_at = $3 WHERE id = $1;

-- name: InsertBookingExtension :one
INSERT INTO shortlet_booking_extensions (id, booking_id, listing_id, property_id, requested_by, awaiting, status, previous_check_out, new_check_out,
    buffer_end, previous_buffer_end, pricing_version_id, cost_snapshot, amount_minor, currency, payment_collection, respond_by, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'REQUESTED', $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 1, $17, $17)
RETURNING *;

-- name: GetBookingExtension :one
SELECT * FROM shortlet_booking_extensions WHERE id = $1;

-- name: GetBookingExtensionForUpdate :one
SELECT * FROM shortlet_booking_extensions WHERE id = $1 FOR UPDATE;

-- name: ListBookingExtensions :many
SELECT * FROM shortlet_booking_extensions WHERE booking_id = $1 ORDER BY created_at, id;

-- name: OpenBookingExtensions :many
SELECT * FROM shortlet_booking_extensions WHERE booking_id = $1 AND status IN ('REQUESTED','PAYMENT_PENDING') FOR UPDATE;

-- name: SetBookingExtensionStatus :one
UPDATE shortlet_booking_extensions SET status = $2, awaiting = $3, decline_reason = $4, applied_at = $5, respond_by = $6,
    version = version + 1, updated_at = $7
 WHERE id = $1 RETURNING *;

-- name: ExpiredBookingExtensions :many
SELECT id FROM shortlet_booking_extensions WHERE status IN ('REQUESTED','PAYMENT_PENDING') AND respond_by <= $1 ORDER BY respond_by LIMIT $2;

-- name: ExtensionsNeedingIntent :many
SELECT id FROM shortlet_booking_extensions
 WHERE status = 'PAYMENT_PENDING' AND payment_collection = 'PLATFORM' AND payment_intent_ref IS NULL ORDER BY updated_at LIMIT $1;

-- name: ExtensionsToReconcile :many
SELECT id FROM shortlet_booking_extensions
 WHERE status = 'PAYMENT_PENDING' AND payment_intent_ref IS NOT NULL
   AND (last_checked_at IS NULL OR last_checked_at <= sqlc.arg(checked_before)::timestamptz)
 ORDER BY last_checked_at NULLS FIRST LIMIT sqlc.arg(max_rows);

-- name: SetExtensionIntent :exec
UPDATE shortlet_booking_extensions SET payment_intent_ref = $2, last_payment_status = $3, last_checked_at = $4, updated_at = $4 WHERE id = $1;

-- name: SetExtensionPayment :exec
UPDATE shortlet_booking_extensions SET paid_minor = $2, last_payment_status = $3, last_checked_at = $4, updated_at = $4 WHERE id = $1;

-- name: GetExtensionByIntent :one
SELECT id FROM shortlet_booking_extensions WHERE payment_intent_ref = $1;

-- name: AppliedExtensionTotals :one
SELECT coalesce(sum(amount_minor), 0)::bigint AS amount_minor, coalesce(sum(paid_minor), 0)::bigint AS paid_minor
  FROM shortlet_booking_extensions WHERE booking_id = $1 AND status = 'APPLIED';

-- name: InsertStatutoryObligation :one
INSERT INTO statutory_obligations (id, account_id, property_id, obligation_type, title, jurisdiction, authority, reference, due_on, recurrence,
    estimated_amount_minor, currency, reminder_days, status, notes, previous_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'UPCOMING', $14, $15, 1, $16, $16)
RETURNING *;

-- name: GetStatutoryObligationForUpdate :one
SELECT * FROM statutory_obligations WHERE id = $1 FOR UPDATE;

-- name: ListStatutoryObligations :many
SELECT * FROM statutory_obligations
 WHERE account_id = sqlc.arg(account_id)
   AND (sqlc.narg(property_id)::uuid IS NULL OR property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
 ORDER BY due_on, id LIMIT 500;

-- name: CloseStatutoryObligation :one
UPDATE statutory_obligations SET status = $2, completed_at = $3, evidence_media_ids = $4, notes = coalesce(sqlc.narg(notes)::text, notes),
    version = version + 1, updated_at = $5
 WHERE id = $1 RETURNING *;

-- name: StatutoryRemindersDue :many
SELECT * FROM statutory_obligations
 WHERE status = 'UPCOMING' AND reminded_at IS NULL AND due_on - reminder_days <= sqlc.arg(today)::date
 ORDER BY due_on LIMIT sqlc.arg(max_rows);

-- name: MarkStatutoryReminded :exec
UPDATE statutory_obligations SET reminded_at = $2 WHERE id = $1 AND reminded_at IS NULL;

-- name: InsertPropertyValuation :one
INSERT INTO property_valuations (id, property_id, value_minor, currency, valued_on, basis, professional_appointment_id, evidence_media_ids, note,
    recorded_by_account, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: ListPropertyValuations :many
SELECT * FROM property_valuations WHERE property_id = $1 ORDER BY valued_on DESC, created_at DESC LIMIT 100;

-- name: InsertAssistanceAuthorization :one
INSERT INTO assistance_authorizations (id, account_id, scopes, property_ids, channel, reason, status, expires_at, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'ACTIVE', $7, 1, $8, $8)
RETURNING *;

-- name: GetAssistanceAuthorizationForUpdate :one
SELECT * FROM assistance_authorizations WHERE id = $1 FOR UPDATE;

-- name: ListAssistanceAuthorizations :many
SELECT * FROM assistance_authorizations WHERE account_id = $1 ORDER BY created_at DESC, id DESC LIMIT 100;

-- name: ActiveAssistanceAuthorizations :many
SELECT * FROM assistance_authorizations WHERE account_id = $1 AND status = 'ACTIVE' AND expires_at > $2 ORDER BY created_at DESC;

-- name: RevokeAssistanceAuthorization :one
UPDATE assistance_authorizations SET status = 'REVOKED', revoked_at = $2, revoke_reason = $3, version = version + 1, updated_at = $2
 WHERE id = $1 AND status = 'ACTIVE' RETURNING *;

-- name: ExpireAssistanceAuthorizations :execrows
UPDATE assistance_authorizations SET status = 'EXPIRED', version = version + 1, updated_at = $1 WHERE status = 'ACTIVE' AND expires_at <= $1;

-- name: InsertAssistedActivity :one
INSERT INTO assisted_activities (id, account_id, authorization_id, support_service, support_actor_domain, support_actor_id, activity_type, scope,
    property_id, media_ids, note, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: ListAssistedActivities :many
SELECT * FROM assisted_activities WHERE account_id = $1 ORDER BY created_at DESC, id DESC LIMIT 200;
