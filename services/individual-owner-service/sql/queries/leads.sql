-- 10 Leads ----------------------------------------------------------------

-- name: InsertInquiry :one
INSERT INTO inquiries (id, property_id, listing_id, customer_ref_id, customer_display_name, channel, status, conversation_ref,
    intended_move_in, occupants, term_months, external_ref, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'NEW', $7, $8, $9, $10, $11, 1, $12, $12)
RETURNING *;

-- name: GetInquiry :one
SELECT * FROM inquiries WHERE id = $1;

-- name: GetInquiryByExternalRef :one
SELECT * FROM inquiries WHERE listing_id = $1 AND external_ref = $2;

-- name: ListInquiriesForAccount :many
SELECT i.* FROM inquiries i
 WHERE i.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(listing_id)::uuid IS NULL OR i.listing_id = sqlc.narg(listing_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR i.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (i.created_at, i.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY i.created_at DESC, i.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: UpdateInquiryWork :one
UPDATE inquiries SET priority = $3, follow_up_at = $4, version = version + 1, updated_at = $5
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: SetInquiryQualification :exec
UPDATE inquiries SET intended_move_in = $2, occupants = $3, term_months = $4, budget_within_range = $5, has_pets = $6, updated_at = $7
 WHERE id = $1;

-- name: SetInquiryAcknowledged :exec
UPDATE inquiries SET acknowledged_at = coalesce(acknowledged_at, $2), updated_at = $2 WHERE id = $1;

-- name: SetInquiryCloseReason :exec
UPDATE inquiries SET close_reason = $2, follow_up_at = NULL, updated_at = $3 WHERE id = $1;

-- name: InsertLeadActivity :one
INSERT INTO lead_activities (id, inquiry_id, property_id, activity_type, note, ref_type, ref_id, actor_domain, actor_id, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ListLeadActivities :many
SELECT * FROM lead_activities WHERE inquiry_id = $1 ORDER BY occurred_at DESC, id DESC LIMIT 200;

-- name: DueInquiryFollowUps :many
UPDATE inquiries SET follow_up_at = NULL
 WHERE id IN (SELECT q.id FROM inquiries q WHERE q.follow_up_at <= sqlc.arg(now)::timestamptz AND q.status NOT IN ('CLOSED_WON','CLOSED_LOST')
              ORDER BY q.follow_up_at LIMIT sqlc.arg(max_rows) FOR UPDATE SKIP LOCKED)
RETURNING *;

-- 11 Viewings -------------------------------------------------------------

-- name: InsertViewingSlot :one
INSERT INTO viewing_slots (id, property_id, listing_id, starts_at, ends_at, mode, capacity, host_type, host_ref_id, status, notes, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'OPEN', $10, 1, $11, $11)
RETURNING *;

-- name: GetViewingSlotForUpdate :one
SELECT * FROM viewing_slots WHERE id = $1 FOR UPDATE;

-- name: ListViewingSlots :many
SELECT * FROM viewing_slots WHERE listing_id = $1 AND ends_at >= $2 ORDER BY starts_at LIMIT 200;

-- name: OverlappingSlots :one
SELECT count(*) FROM viewing_slots WHERE listing_id = $1 AND status = 'OPEN' AND starts_at < $3 AND ends_at > $2;

-- name: AdjustSlotBooking :execrows
UPDATE viewing_slots SET booked = booked + sqlc.arg(delta)::int, version = version + 1, updated_at = sqlc.arg(now)
 WHERE id = sqlc.arg(id) AND status = 'OPEN' AND booked + sqlc.arg(delta)::int BETWEEN 0 AND capacity;

-- name: CancelViewingSlot :execrows
UPDATE viewing_slots SET status = 'CANCELLED', version = version + 1, updated_at = $2 WHERE id = $1 AND status = 'OPEN';

-- name: InsertViewing :one
INSERT INTO viewing_requests (id, property_id, listing_id, slot_id, inquiry_id, customer_ref_id, customer_display_name, mode, status,
    starts_at, ends_at, host_type, host_ref_id, occupied, conversation_ref, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'REQUESTED', $9, $10, $11, $12, $13, $14, 1, $15, $15)
RETURNING *;

-- name: GetViewing :one
SELECT * FROM viewing_requests WHERE id = $1;

-- name: GetViewingForUpdate :one
SELECT * FROM viewing_requests WHERE id = $1 FOR UPDATE;

-- name: ListViewingsForAccount :many
SELECT v.* FROM viewing_requests v
 WHERE v.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(listing_id)::uuid IS NULL OR v.listing_id = sqlc.narg(listing_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR v.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (v.starts_at, v.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY v.starts_at DESC, v.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: SetViewingSchedule :exec
UPDATE viewing_requests SET starts_at = $2, ends_at = $3, slot_id = $4, proposed_starts_at = NULL, proposed_ends_at = NULL, updated_at = $5 WHERE id = $1;

-- name: SetViewingProposal :exec
UPDATE viewing_requests SET proposed_starts_at = $2, proposed_ends_at = $3, slot_id = $4, updated_at = $5 WHERE id = $1;

-- name: SetViewingHost :exec
UPDATE viewing_requests SET host_type = $2, host_ref_id = $3, updated_at = $4 WHERE id = $1;

-- name: SetViewingAccessCode :exec
UPDATE viewing_requests SET access_code_sealed = $2, access_code_key_id = $3, access_code_expires_at = $4, updated_at = $5 WHERE id = $1;

-- name: SetViewingOccupantConsent :exec
UPDATE viewing_requests SET occupant_consent_at = $2, occupant_consent_ref = $3, updated_at = $2 WHERE id = $1;

-- name: SetViewingNotes :one
UPDATE viewing_requests SET owner_notes = $3, version = version + 1, updated_at = $4 WHERE id = $1 AND version = $2 RETURNING *;

-- name: SetViewingCancellation :exec
UPDATE viewing_requests SET cancel_reason = $2, cancelled_by = $3, access_code_sealed = NULL, access_code_key_id = NULL, updated_at = $4 WHERE id = $1;

-- name: InsertViewingAttendance :one
INSERT INTO viewing_attendance (id, viewing_id, party, outcome, rating, feedback, safety_flag, actor_domain, actor_id, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: ListViewingAttendance :many
SELECT * FROM viewing_attendance WHERE viewing_id = $1 ORDER BY occurred_at, id;

-- name: DueViewingReminders :many
UPDATE viewing_requests SET reminder_sent_at = sqlc.arg(now)::timestamptz
 WHERE id IN (SELECT q.id FROM viewing_requests q WHERE q.status = 'CONFIRMED' AND q.reminder_sent_at IS NULL
                AND q.starts_at <= sqlc.arg(remind_before)::timestamptz AND q.starts_at > sqlc.arg(now)::timestamptz
              ORDER BY q.starts_at LIMIT sqlc.arg(max_rows) FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: StaleViewings :many
SELECT id FROM viewing_requests WHERE status IN ('REQUESTED','RESCHEDULED') AND ends_at < $1 ORDER BY ends_at LIMIT $2;

-- 12 Applications -----------------------------------------------------------

-- name: InsertScreeningPolicy :one
INSERT INTO screening_policies (id, property_id, version_no, criteria, policy_version, created_by_actor_domain, created_by_actor_id, created_at)
VALUES ($1, $2, (SELECT coalesce(max(version_no), 0) + 1 FROM screening_policies WHERE property_id = $2), $3, $4, $5, $6, $7)
RETURNING *;

-- name: LatestScreeningPolicy :one
SELECT * FROM screening_policies WHERE property_id = $1 ORDER BY version_no DESC LIMIT 1;

-- name: GetScreeningPolicy :one
SELECT * FROM screening_policies WHERE id = $1;

-- name: ListScreeningPolicies :many
SELECT * FROM screening_policies WHERE property_id = $1 ORDER BY version_no DESC LIMIT 50;

-- name: InsertApplication :one
INSERT INTO rental_applications (id, property_id, listing_id, unit_id, inquiry_id, customer_ref_id, status, desired_move_in, term_months,
    occupants, has_pets, screening_policy_id, submitted_at, expires_at, applicant_consent_at, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'SUBMITTED', $7, $8, $9, $10, $11, $12, $13, $12, 1, $12, $12)
RETURNING *;

-- name: GetApplication :one
SELECT * FROM rental_applications WHERE id = $1;

-- name: GetApplicationForUpdate :one
SELECT * FROM rental_applications WHERE id = $1 FOR UPDATE;

-- name: ListApplicationsForAccount :many
SELECT a.* FROM rental_applications a
 WHERE a.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(listing_id)::uuid IS NULL OR a.listing_id = sqlc.narg(listing_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (a.created_at, a.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY a.created_at DESC, a.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: SetApplicationDeadline :exec
UPDATE rental_applications SET expires_at = $2, respond_by = $3, updated_at = $4 WHERE id = $1;

-- name: InsertParticipant :one
INSERT INTO application_participants (id, application_id, role, customer_ref_id, display_name, relationship, employment_status,
    declared_monthly_income_minor, income_currency, identity_verification_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: ListParticipants :many
SELECT * FROM application_participants WHERE application_id = $1 ORDER BY created_at, id;

-- name: InsertEvidenceGrant :one
INSERT INTO application_evidence_grants (id, application_id, participant_id, evidence_type, purpose, status, media_id, request_note, due_at,
    granted_at, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: GetEvidenceGrantForUpdate :one
SELECT * FROM application_evidence_grants WHERE id = $1 AND application_id = $2 FOR UPDATE;

-- name: FulfilEvidenceGrant :one
UPDATE application_evidence_grants SET status = 'GRANTED', media_id = $2, granted_at = $3, expires_at = $4
 WHERE id = $1 AND status = 'REQUESTED'
RETURNING *;

-- name: TouchEvidenceGrant :exec
UPDATE application_evidence_grants SET access_count = access_count + 1, last_accessed_at = $2 WHERE id = $1;

-- name: ListEvidenceGrants :many
SELECT * FROM application_evidence_grants WHERE application_id = $1 ORDER BY created_at, id;

-- name: CountOpenEvidenceRequests :one
SELECT count(*) FROM application_evidence_grants WHERE application_id = $1 AND status = 'REQUESTED';

-- name: ExpireEvidenceGrants :execrows
UPDATE application_evidence_grants SET status = 'EXPIRED' WHERE status = 'GRANTED' AND expires_at <= $1;

-- name: RevokeApplicationGrants :exec
UPDATE application_evidence_grants SET status = 'REVOKED', revoked_at = $2 WHERE application_id = $1 AND status IN ('REQUESTED','GRANTED');

-- name: InsertScreeningResult :exec
INSERT INTO screening_results (id, application_id, policy_id, criterion, outcome, source, note, actor_domain, actor_id, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: ListScreeningResults :many
SELECT * FROM screening_results WHERE application_id = $1 ORDER BY recorded_at, id;

-- name: InsertApplicationDecision :one
INSERT INTO application_decisions (id, application_id, decision, reason_codes, conditions, note, screening_policy_id, policy_version,
    human_review, actor_domain, actor_id, decided_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: ListApplicationDecisions :many
SELECT * FROM application_decisions WHERE application_id = $1 ORDER BY decided_at, id;

-- name: DueApplicationExpiries :many
SELECT id FROM rental_applications
 WHERE status IN ('SUBMITTED','RECEIVED','INFO_REQUESTED','UNDER_REVIEW','WAITLISTED') AND expires_at <= $1
 ORDER BY expires_at LIMIT $2;

-- name: OpenApplicationsForListing :many
SELECT id FROM rental_applications WHERE listing_id = $1 AND status IN ('SUBMITTED','RECEIVED','UNDER_REVIEW','INFO_REQUESTED','WAITLISTED','APPROVED','CONDITIONAL');

-- name: InsertRentalOffer :one
INSERT INTO rental_offers (id, application_id, property_id, listing_id, unit_id, customer_ref_id, status, pricing_version_id, cost_snapshot,
    start_date, term_months, conditions, expires_at, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'DRAFT', $7, $8, $9, $10, $11, $12, 1, $13, $13)
RETURNING *;

-- name: GetRentalOffer :one
SELECT * FROM rental_offers WHERE id = $1;

-- name: GetRentalOfferForUpdate :one
SELECT * FROM rental_offers WHERE id = $1 FOR UPDATE;

-- name: ListOffersForApplication :many
SELECT * FROM rental_offers WHERE application_id = $1 ORDER BY created_at DESC;

-- name: UpdateRentalOfferTerms :one
UPDATE rental_offers SET start_date = $3, term_months = $4, conditions = $5, expires_at = $6, cost_snapshot = $7, version = version + 1, updated_at = $8
 WHERE id = $1 AND version = $2 AND status = 'DRAFT'
RETURNING *;

-- name: SetRentalOfferTimes :exec
UPDATE rental_offers SET sent_at = coalesce($2, sent_at), responded_at = coalesce($3, responded_at), withdraw_reason = coalesce($4, withdraw_reason),
    updated_at = $5 WHERE id = $1;

-- name: DueOfferExpiries :many
SELECT id FROM rental_offers WHERE status = 'SENT' AND expires_at <= $1 ORDER BY expires_at LIMIT $2;
