-- 20 Short-let ---------------------------------------------------------------

-- name: UpsertShortletSettings :one
INSERT INTO shortlet_settings (listing_id, property_id, max_guests, min_nights, max_nights, check_in_time, check_out_time, instant_book,
    guest_verification_required, payment_collection, cancellation_policy, house_rules, add_ons, turnover_buffer_days, damage_claim_window_hours,
    external_calendar_ref, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 1, $17, $17)
ON CONFLICT (listing_id) DO UPDATE SET max_guests = EXCLUDED.max_guests, min_nights = EXCLUDED.min_nights, max_nights = EXCLUDED.max_nights,
    check_in_time = EXCLUDED.check_in_time, check_out_time = EXCLUDED.check_out_time, instant_book = EXCLUDED.instant_book,
    guest_verification_required = EXCLUDED.guest_verification_required, payment_collection = EXCLUDED.payment_collection,
    cancellation_policy = EXCLUDED.cancellation_policy, house_rules = EXCLUDED.house_rules, add_ons = EXCLUDED.add_ons,
    turnover_buffer_days = EXCLUDED.turnover_buffer_days, damage_claim_window_hours = EXCLUDED.damage_claim_window_hours,
    external_calendar_ref = EXCLUDED.external_calendar_ref, version = shortlet_settings.version + 1, updated_at = EXCLUDED.updated_at
 WHERE shortlet_settings.version = sqlc.arg(expected_version)
RETURNING *;

-- name: GetShortletSettings :one
SELECT * FROM shortlet_settings WHERE listing_id = $1;

-- name: SettingsWithExternalCalendar :many
SELECT * FROM shortlet_settings WHERE external_calendar_ref IS NOT NULL ORDER BY updated_at LIMIT $1;

-- name: SupersedeRates :exec
UPDATE shortlet_rates SET superseded_at = $2 WHERE listing_id = $1 AND superseded_at IS NULL;

-- name: InsertRate :one
INSERT INTO shortlet_rates (id, listing_id, start_date, end_date, nightly_rate_minor, min_nights, label, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ActiveRates :many
SELECT * FROM shortlet_rates WHERE listing_id = $1 AND superseded_at IS NULL ORDER BY start_date;

-- name: SetCalendarRange :execrows
INSERT INTO availability_calendar (listing_id, day, status, source, ref_id, updated_at)
SELECT sqlc.arg(listing_id)::uuid, d::date, sqlc.arg(status)::text, sqlc.arg(source)::text, sqlc.narg(ref_id)::uuid, sqlc.arg(now)::timestamptz
  FROM generate_series(sqlc.arg(from_day)::date, sqlc.arg(to_day)::date, interval '1 day') AS d
ON CONFLICT (listing_id, day) DO UPDATE SET status = EXCLUDED.status, source = EXCLUDED.source, ref_id = EXCLUDED.ref_id, updated_at = EXCLUDED.updated_at
 WHERE availability_calendar.status = 'AVAILABLE'
    OR (EXCLUDED.source = 'OWNER' AND availability_calendar.status = 'BLOCKED' AND availability_calendar.source IN ('OWNER','EXTERNAL'))
    OR (EXCLUDED.ref_id IS NOT NULL AND availability_calendar.ref_id = EXCLUDED.ref_id);

-- name: ListCalendarRange :many
SELECT * FROM availability_calendar WHERE listing_id = $1 AND day BETWEEN sqlc.arg(from_day)::date AND sqlc.arg(to_day)::date ORDER BY day;

-- name: ReleaseBookingDays :exec
UPDATE availability_calendar SET status = 'AVAILABLE', source = 'SYSTEM', ref_id = NULL, updated_at = $2 WHERE ref_id = $1;

-- name: ConfirmBookingDays :exec
UPDATE availability_calendar SET status = 'BOOKED', updated_at = $2 WHERE ref_id = $1 AND status = 'HOLD';

-- name: ExternalConflicts :many
SELECT day, status, ref_id FROM availability_calendar
 WHERE listing_id = $1 AND day >= sqlc.arg(from_day)::date AND day < sqlc.arg(to_day)::date AND status IN ('HOLD','BOOKED');

-- name: InsertBooking :one
INSERT INTO shortlet_bookings (id, listing_id, property_id, customer_ref_id, status, check_in, check_out, guests, pricing_version_id, cost_snapshot,
    total_minor, currency, payment_collection, cancellation_policy, hold_expires_at, conversation_ref, external_ref, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'HOLD', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 1, $17, $17)
RETURNING *;

-- name: GetBooking :one
SELECT * FROM shortlet_bookings WHERE id = $1;

-- name: GetBookingForUpdate :one
SELECT * FROM shortlet_bookings WHERE id = $1 FOR UPDATE;

-- name: GetBookingByExternalRef :one
SELECT * FROM shortlet_bookings WHERE listing_id = $1 AND external_ref = $2;

-- name: ListBookingsForAccount :many
SELECT b.* FROM shortlet_bookings b
 WHERE b.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(listing_id)::uuid IS NULL OR b.listing_id = sqlc.narg(listing_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR b.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (b.created_at, b.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY b.created_at DESC, b.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: SetBookingHold :exec
UPDATE shortlet_bookings SET hold_expires_at = $2, updated_at = $3 WHERE id = $1;

-- name: SetBookingIntent :exec
UPDATE shortlet_bookings SET payment_intent_ref = $2, last_payment_status = $3, last_checked_at = $4, updated_at = $4 WHERE id = $1;

-- name: SetBookingPayment :exec
UPDATE shortlet_bookings SET paid_minor = greatest(paid_minor, $2), last_payment_status = $3, last_checked_at = $4 WHERE id = $1;

-- name: SetBookingCancelled :exec
UPDATE shortlet_bookings SET cancelled_by = $2, cancel_reason = $3, refund_due_minor = $4, hold_expires_at = NULL, updated_at = $5 WHERE id = $1;

-- name: SetBookingRefund :exec
UPDATE shortlet_bookings SET refund_request_ref = coalesce(sqlc.narg(refund_request_ref)::text, refund_request_ref), refund_status = sqlc.arg(refund_status)::text,
    refunded_minor = sqlc.arg(refunded_minor)::bigint, last_checked_at = sqlc.arg(now)::timestamptz WHERE id = sqlc.arg(id)::uuid;

-- name: SetBookingStay :exec
UPDATE shortlet_bookings SET checked_in_at = coalesce(sqlc.narg(checked_in_at)::timestamptz, checked_in_at),
    checked_out_at = coalesce(sqlc.narg(checked_out_at)::timestamptz, checked_out_at),
    access_grant_ref = coalesce(sqlc.narg(access_grant_ref)::uuid, access_grant_ref), hold_expires_at = NULL, updated_at = sqlc.arg(now)::timestamptz
 WHERE id = sqlc.arg(id)::uuid;

-- name: ExpiredHolds :many
SELECT id FROM shortlet_bookings WHERE status IN ('HOLD','PENDING_APPROVAL','PENDING_PAYMENT') AND hold_expires_at <= $1 ORDER BY hold_expires_at LIMIT $2;

-- name: BookingsNeedingIntent :many
SELECT id FROM shortlet_bookings WHERE status = 'PENDING_PAYMENT' AND payment_collection = 'PLATFORM' AND payment_intent_ref IS NULL
 ORDER BY created_at LIMIT $1;

-- name: BookingsToReconcile :many
SELECT id FROM shortlet_bookings WHERE payment_intent_ref IS NOT NULL AND status = 'PENDING_PAYMENT'
   AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(checked_before)::timestamptz)
 ORDER BY last_checked_at NULLS FIRST LIMIT sqlc.arg(max_rows);

-- name: BookingsNeedingRefund :many
SELECT id FROM shortlet_bookings WHERE status = 'CANCELLED' AND refund_due_minor > 0 AND payment_intent_ref IS NOT NULL
   AND (refund_status IS NULL OR refund_status = 'FAILED' OR (refund_status = 'PENDING' AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(checked_before)::timestamptz)))
 ORDER BY updated_at LIMIT sqlc.arg(max_rows);

-- name: BookingsToComplete :many
SELECT b.id FROM shortlet_bookings b JOIN shortlet_settings s ON s.listing_id = b.listing_id
 WHERE b.status = 'CHECKED_OUT' AND b.checked_out_at + make_interval(hours => s.damage_claim_window_hours) <= sqlc.arg(now)::timestamptz
   AND NOT EXISTS (SELECT 1 FROM damage_claims c WHERE c.booking_id = b.id AND c.status IN ('OPEN','GUEST_ACCEPTED','DISPUTED','RESPONSE_OVERDUE','RESOLVED'))
 ORDER BY b.checked_out_at LIMIT sqlc.arg(max_rows);

-- name: CustomerBookings :many
SELECT * FROM shortlet_bookings WHERE customer_ref_id = $1 ORDER BY check_in DESC LIMIT 100;

-- name: InsertGuest :exec
INSERT INTO shortlet_guests (id, booking_id, is_primary, customer_ref_id, display_name, verification_ref, verification_status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8);

-- name: ListGuests :many
SELECT * FROM shortlet_guests WHERE booking_id = $1 ORDER BY is_primary DESC, created_at;

-- name: SetGuestVerification :execrows
UPDATE shortlet_guests SET verification_ref = $3, verification_status = $4, updated_at = $5 WHERE id = $1 AND booking_id = $2;

-- name: InsertTurnoverJob :one
INSERT INTO turnover_jobs (id, listing_id, property_id, booking_id, job_type, status, scheduled_for, assignee_type, assignee_ref_id, checklist, notes,
    version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'SCHEDULED', $6, $7, $8, $9, $10, 1, $11, $11)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetTurnoverJob :one
SELECT * FROM turnover_jobs WHERE id = $1;

-- name: ListTurnoverJobs :many
SELECT * FROM turnover_jobs WHERE listing_id = $1 AND (sqlc.narg(booking_id)::uuid IS NULL OR booking_id = sqlc.narg(booking_id)::uuid)
 ORDER BY scheduled_for DESC LIMIT 200;

-- name: UpdateTurnoverJob :one
UPDATE turnover_jobs SET status = $3, notes = $4, evidence_media_ids = $5, checklist = $6, completed_at = $7, version = version + 1, updated_at = $8
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: CancelBookingTurnover :exec
UPDATE turnover_jobs SET status = 'CANCELLED', version = version + 1, updated_at = $2 WHERE booking_id = $1 AND status = 'SCHEDULED';

-- name: InsertDamageClaim :one
INSERT INTO damage_claims (id, booking_id, property_id, status, items, claimed_minor, currency, respond_by, version, created_at, updated_at)
VALUES ($1, $2, $3, 'OPEN', $4, $5, $6, $7, 1, $8, $8)
RETURNING *;

-- name: GetDamageClaimForUpdate :one
SELECT * FROM damage_claims WHERE id = $1 FOR UPDATE;

-- name: ListDamageClaims :many
SELECT * FROM damage_claims WHERE booking_id = $1 ORDER BY created_at;

-- name: UpdateDamageClaim :one
UPDATE damage_claims SET status = $2, guest_response_note = coalesce($3, guest_response_note), guest_evidence_media_ids = $4, awarded_minor = $5,
    settlement = $6, settled_at = $7, version = version + 1, updated_at = $8
 WHERE id = $1
RETURNING *;

-- name: SetClaimIntent :exec
UPDATE damage_claims SET payment_intent_ref = $2, last_payment_status = $3, updated_at = $4 WHERE id = $1;

-- name: SetClaimPayment :exec
UPDATE damage_claims SET paid_minor = greatest(paid_minor, $2), last_payment_status = $3, updated_at = $4 WHERE id = $1;

-- name: ClaimsNeedingIntent :many
SELECT id FROM damage_claims WHERE status IN ('GUEST_ACCEPTED','RESOLVED') AND settlement = 'PLATFORM' AND payment_intent_ref IS NULL
 ORDER BY updated_at LIMIT $1;

-- name: ClaimsToReconcile :many
SELECT id FROM damage_claims WHERE status IN ('GUEST_ACCEPTED','RESOLVED') AND payment_intent_ref IS NOT NULL ORDER BY updated_at LIMIT $1;

-- name: ExpiredClaimResponses :many
SELECT id FROM damage_claims WHERE status = 'OPEN' AND respond_by <= $1 ORDER BY respond_by LIMIT $2;

-- name: CancelledBookingIntents :many
SELECT id FROM shortlet_bookings WHERE status IN ('CANCELLED','NO_SHOW') AND payment_intent_ref IS NOT NULL
   AND coalesce(last_payment_status, '') IN ('REQUIRES_PAYMENT','PROCESSING','PARTIALLY_PAID')
   AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(checked_before)::timestamptz)
 ORDER BY last_checked_at NULLS FIRST LIMIT sqlc.arg(max_rows);

-- name: AddBookingRefundDue :exec
UPDATE shortlet_bookings SET refund_due_minor = refund_due_minor + sqlc.arg(amount_minor)::bigint, refund_status = NULL, updated_at = sqlc.arg(now)::timestamptz
 WHERE id = sqlc.arg(id)::uuid;

-- name: GetBookingByIntent :one
SELECT id FROM shortlet_bookings WHERE payment_intent_ref = $1;

-- name: GetClaimByIntent :one
SELECT id FROM damage_claims WHERE payment_intent_ref = $1;
