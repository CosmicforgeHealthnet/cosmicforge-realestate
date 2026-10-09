-- 22 Maintenance, artisans and preventive care -----------------------------

-- name: InsertMaintenanceRequest :one
INSERT INTO maintenance_requests (id, property_id, unit_id, asset_id, tenancy_id, preventive_schedule_id, due_on, warranty_of_request_id, title,
    description, category, priority, responsibility, reported_by_type, reported_by_ref, status, evidence_media_ids, budget_minor, currency,
    cfmeet_context_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 'OPEN', $16, $17, $18, $19, 1, $20, $20)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetMaintenanceRequest :one
SELECT * FROM maintenance_requests WHERE id = $1;

-- name: GetMaintenanceRequestForUpdate :one
SELECT * FROM maintenance_requests WHERE id = $1 FOR UPDATE;

-- name: ListMaintenanceForAccount :many
SELECT m.* FROM maintenance_requests m
 WHERE m.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR m.property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR m.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (m.created_at, m.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY m.created_at DESC, m.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: UpdateMaintenanceTriage :exec
UPDATE maintenance_requests SET priority = sqlc.arg(priority)::text, responsibility = sqlc.arg(responsibility)::text,
    responsibility_note = sqlc.narg(responsibility_note)::text, budget_minor = sqlc.narg(budget_minor)::bigint,
    cfmeet_context_id = coalesce(sqlc.narg(cfmeet_context_id)::text, cfmeet_context_id), version = version + 1,
    updated_at = sqlc.arg(now)::timestamptz
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetMaintenanceSourcing :exec
UPDATE maintenance_requests SET sourcing = $2, updated_at = $3 WHERE id = $1;

-- name: SetMaintenanceCancelReason :exec
UPDATE maintenance_requests SET cancel_reason = $2, updated_at = $3 WHERE id = $1;

-- name: InsertMaintenanceQuote :one
INSERT INTO maintenance_quotes (id, request_id, source, provider_ref, provider_display_name, external_quote_ref, scope, amount_minor, currency,
    earliest_start_at, valid_until, warranty_days, evidence_media_ids, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'SUBMITTED', $14, $14)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetMaintenanceQuoteByExternal :one
SELECT * FROM maintenance_quotes WHERE external_quote_ref = $1;

-- name: ListMaintenanceQuotes :many
SELECT * FROM maintenance_quotes WHERE request_id = $1 ORDER BY amount_minor, created_at;

-- name: GetMaintenanceQuote :one
SELECT * FROM maintenance_quotes WHERE id = $1 AND request_id = $2;

-- name: SetQuoteStatus :exec
UPDATE maintenance_quotes SET status = $2, updated_at = $3 WHERE id = $1;

-- name: RejectOtherQuotes :exec
UPDATE maintenance_quotes SET status = 'REJECTED', updated_at = $3 WHERE request_id = $1 AND id <> $2 AND status = 'SUBMITTED';

-- name: ExpireQuotes :execrows
UPDATE maintenance_quotes SET status = 'EXPIRED', updated_at = $1 WHERE status = 'SUBMITTED' AND valid_until IS NOT NULL AND valid_until <= $1;

-- name: InsertMaintenanceApproval :exec
INSERT INTO maintenance_approvals (id, request_id, quote_id, decision, amount_minor, currency, approved_via, approver_ref, assignment_id, limit_minor,
    emergency, note, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: ListMaintenanceApprovals :many
SELECT * FROM maintenance_approvals WHERE request_id = $1 ORDER BY created_at;

-- name: LatestManagerPermission :one
SELECT * FROM manager_permissions WHERE assignment_id = $1 ORDER BY version_no DESC LIMIT 1;

-- name: InsertMaintenanceJob :one
INSERT INTO maintenance_jobs (id, request_id, quote_id, provider_ref, status, amount_minor, currency, payment_collection, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'ASSIGNED', $5, $6, $7, 1, $8, $8)
RETURNING *;

-- name: GetActiveJob :one
SELECT * FROM maintenance_jobs WHERE request_id = $1 AND status <> 'CANCELLED';

-- name: GetMaintenanceJob :one
SELECT * FROM maintenance_jobs WHERE id = $1;

-- name: GetMaintenanceJobForUpdate :one
SELECT * FROM maintenance_jobs WHERE id = $1 FOR UPDATE;

-- name: UpdateMaintenanceJob :exec
UPDATE maintenance_jobs SET status = sqlc.arg(status)::text,
    external_job_ref = coalesce(sqlc.narg(external_job_ref)::text, external_job_ref),
    invoiced_minor = coalesce(sqlc.narg(invoiced_minor)::bigint, invoiced_minor),
    provider_payment_ack = provider_payment_ack OR sqlc.arg(payment_ack)::boolean,
    rework_count = rework_count + sqlc.arg(rework_inc)::integer,
    accepted_at = coalesce(sqlc.narg(accepted_at)::timestamptz, accepted_at),
    version = version + 1, updated_at = sqlc.arg(now)::timestamptz
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetJobOfflinePayment :exec
UPDATE maintenance_jobs SET offline_payment_evidence = $2, paid_minor = $3, last_payment_status = $4, version = version + 1, updated_at = $5 WHERE id = $1;

-- name: InsertJobEvent :one
INSERT INTO maintenance_job_events (id, job_id, event_type, source, external_event_id, note, evidence_media_ids, amount_minor, occurred_at, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: ListJobEvents :many
SELECT * FROM maintenance_job_events WHERE job_id = $1 ORDER BY occurred_at, recorded_at;

-- name: JobsNeedingIntent :many
SELECT id FROM maintenance_jobs WHERE status = 'ACCEPTED' AND payment_collection = 'PLATFORM' AND payment_intent_ref IS NULL ORDER BY accepted_at LIMIT $1;

-- name: JobsToReconcile :many
SELECT id FROM maintenance_jobs WHERE payment_intent_ref IS NOT NULL AND coalesce(last_payment_status, '') NOT IN ('SUCCEEDED','CANCELLED','REFUNDED')
   AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(checked_before)::timestamptz)
 ORDER BY last_checked_at NULLS FIRST LIMIT sqlc.arg(max_rows);

-- name: SetJobIntent :exec
UPDATE maintenance_jobs SET payment_intent_ref = $2, last_payment_status = $3, last_checked_at = $4 WHERE id = $1;

-- name: SetJobPayment :exec
UPDATE maintenance_jobs SET paid_minor = greatest(paid_minor, $2), last_payment_status = $3, last_checked_at = $4 WHERE id = $1;

-- name: GetJobByIntent :one
SELECT id FROM maintenance_jobs WHERE payment_intent_ref = $1;

-- name: InsertWarranty :exec
INSERT INTO maintenance_warranties (id, request_id, job_id, provider_ref, starts_on, ends_on, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'ACTIVE', $7, $7)
ON CONFLICT DO NOTHING;

-- name: GetWarrantyForRequest :one
SELECT * FROM maintenance_warranties WHERE request_id = $1;

-- name: ClaimWarranty :execrows
UPDATE maintenance_warranties SET status = 'CLAIMED', claim_request_id = $2, updated_at = $3 WHERE id = $1 AND status = 'ACTIVE';

-- name: ExpireWarranties :execrows
UPDATE maintenance_warranties SET status = 'EXPIRED', updated_at = sqlc.arg(now)::timestamptz WHERE status = 'ACTIVE' AND ends_on < sqlc.arg(today)::date;

-- name: MarkAssetServiced :exec
UPDATE property_assets SET last_serviced_on = $2, version = version + 1, updated_at = $3 WHERE id = $1;

-- name: InsertPreventiveSchedule :one
INSERT INTO preventive_schedules (id, property_id, asset_id, title, category, interval_days, lead_days, next_due_on, priority, status, version,
    created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'ACTIVE', 1, $10, $10)
RETURNING *;

-- name: ListPreventiveSchedules :many
SELECT * FROM preventive_schedules WHERE property_id = $1 AND status <> 'RETIRED' ORDER BY next_due_on;

-- name: GetPreventiveSchedule :one
SELECT * FROM preventive_schedules WHERE id = $1;

-- name: UpdatePreventiveSchedule :one
UPDATE preventive_schedules SET title = $3, interval_days = $4, lead_days = $5, next_due_on = $6, priority = $7, status = $8, version = version + 1,
    updated_at = $9
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: DuePreventiveSchedules :many
SELECT * FROM preventive_schedules WHERE status = 'ACTIVE' AND next_due_on - lead_days <= sqlc.arg(today)::date
 ORDER BY next_due_on LIMIT sqlc.arg(max_rows) FOR UPDATE SKIP LOCKED;

-- name: AdvancePreventiveSchedule :exec
UPDATE preventive_schedules SET next_due_on = $2, last_generated_on = $3, version = version + 1, updated_at = $4 WHERE id = $1;

-- 23 Professional services ----------------------------------------------------

-- name: InsertAppointment :one
INSERT INTO professional_appointments (id, owner_account_id, property_id, sale_id, purpose, scope, professional_ref, professional_label, status,
    currency, due_by, cfmeet_context_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'REQUESTED', $9, $10, $11, 1, $12, $12)
RETURNING *;

-- name: GetAppointment :one
SELECT * FROM professional_appointments WHERE id = $1;

-- name: GetAppointmentForUpdate :one
SELECT * FROM professional_appointments WHERE id = $1 FOR UPDATE;

-- name: ListAppointmentsForAccount :many
SELECT a.* FROM professional_appointments a
 WHERE (a.owner_account_id = sqlc.arg(account_id)
        OR a.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                                AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz)))
   AND (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status)::text)
 ORDER BY a.created_at DESC LIMIT 200;

-- name: SetAppointmentQuote :exec
UPDATE professional_appointments SET external_ref = coalesce(sqlc.narg(external_ref)::text, external_ref),
    quote_amount_minor = sqlc.arg(quote_amount_minor)::bigint, quote_valid_until = sqlc.narg(quote_valid_until)::timestamptz,
    licence_ref = sqlc.narg(licence_ref)::text, conflict_declared = sqlc.arg(conflict_declared)::boolean,
    conflict_note = sqlc.narg(conflict_note)::text, updated_at = sqlc.arg(now)::timestamptz
 WHERE id = sqlc.arg(id)::uuid;

-- name: SetAppointmentFee :exec
UPDATE professional_appointments SET fee_collection = $2, updated_at = $3 WHERE id = $1;

-- name: SetAppointmentGrant :exec
UPDATE professional_appointments SET data_room_grant_id = $2, updated_at = $3 WHERE id = $1;

-- name: SetAppointmentReport :exec
UPDATE professional_appointments SET report_media_id = $2, report_signed = $3, report_submitted_at = $4, updated_at = $4 WHERE id = $1;

-- name: SetAppointmentFeedback :exec
UPDATE professional_appointments SET rating = $2, feedback = $3, updated_at = $4 WHERE id = $1;

-- name: SetAppointmentCancel :exec
UPDATE professional_appointments SET cancel_reason = $2, updated_at = $3 WHERE id = $1;

-- name: AppointmentsNeedingIntent :many
SELECT id FROM professional_appointments WHERE status IN ('APPOINTED','IN_PROGRESS','REPORT_SUBMITTED') AND fee_collection = 'PLATFORM'
   AND payment_intent_ref IS NULL ORDER BY updated_at LIMIT $1;

-- name: AppointmentsToReconcile :many
SELECT id FROM professional_appointments WHERE payment_intent_ref IS NOT NULL AND coalesce(last_payment_status, '') NOT IN ('SUCCEEDED','CANCELLED','REFUNDED')
   AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(checked_before)::timestamptz)
 ORDER BY last_checked_at NULLS FIRST LIMIT sqlc.arg(max_rows);

-- name: SetAppointmentIntent :exec
UPDATE professional_appointments SET payment_intent_ref = $2, last_payment_status = $3, last_checked_at = $4 WHERE id = $1;

-- name: SetAppointmentPayment :exec
UPDATE professional_appointments SET paid_minor = greatest(paid_minor, $2), last_payment_status = $3, last_checked_at = $4 WHERE id = $1;

-- name: GetAppointmentByIntent :one
SELECT id FROM professional_appointments WHERE payment_intent_ref = $1;

-- name: InsertFinding :exec
INSERT INTO professional_findings (id, appointment_id, category, severity, summary, media_id, external_finding_ref, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT DO NOTHING;

-- name: ListFindings :many
SELECT * FROM professional_findings WHERE appointment_id = $1 ORDER BY created_at;

-- name: UseFinding :execrows
UPDATE professional_findings SET used_in_type = $3, used_in_id = $4 WHERE id = $1 AND appointment_id = $2;
