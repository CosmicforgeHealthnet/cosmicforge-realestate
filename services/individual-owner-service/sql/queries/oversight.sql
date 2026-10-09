-- 25–27 Disputes, privacy, reports and market insights ----------------------

-- name: InsertDispute :one
INSERT INTO domain_disputes (id, account_id, property_id, target_type, target_id, category, status, summary, desired_outcome, urgent, version,
    created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'OPEN', $7, $8, $9, 1, $10, $10)
RETURNING *;

-- name: GetDispute :one
SELECT * FROM domain_disputes WHERE id = $1;

-- name: GetDisputeForUpdate :one
SELECT * FROM domain_disputes WHERE id = $1 FOR UPDATE;

-- name: ListDisputesForAccount :many
SELECT * FROM domain_disputes WHERE account_id = $1 AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
 ORDER BY created_at DESC LIMIT 200;

-- name: ListDisputesForAdmin :many
SELECT * FROM domain_disputes WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (created_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY urgent DESC, created_at DESC, id DESC LIMIT sqlc.arg(max_rows);

-- name: UpdateDisputeStatus :one
UPDATE domain_disputes SET status = sqlc.arg(status)::text, admin_case_ref = coalesce(sqlc.narg(admin_case_ref)::text, admin_case_ref),
    information_request = sqlc.narg(information_request)::text, resolution_code = sqlc.narg(resolution_code)::text,
    resolution_note = sqlc.narg(resolution_note)::text, resolved_at = sqlc.narg(resolved_at)::timestamptz,
    version = version + 1, updated_at = sqlc.arg(now)::timestamptz
 WHERE id = sqlc.arg(id)::uuid
RETURNING *;

-- name: InsertDisputeEvidence :exec
INSERT INTO dispute_evidence (id, dispute_id, media_id, note, submitted_by, created_at) VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListDisputeEvidence :many
SELECT * FROM dispute_evidence WHERE dispute_id = $1 ORDER BY created_at;

-- name: InsertConsent :one
INSERT INTO consents (id, account_id, purpose, decision, policy_version, expires_at, source, actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: CurrentConsents :many
SELECT DISTINCT ON (purpose) * FROM consents WHERE account_id = $1 ORDER BY purpose, created_at DESC, id DESC;

-- name: CurrentConsent :one
SELECT * FROM consents WHERE account_id = $1 AND purpose = $2 ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: InsertDataSubjectRequest :one
INSERT INTO data_subject_requests (id, account_id, request_type, status, details, due_at, version, created_at, updated_at)
VALUES ($1, $2, $3, 'SUBMITTED', $4, $5, 1, $6, $6)
RETURNING *;

-- name: GetDataSubjectRequest :one
SELECT * FROM data_subject_requests WHERE id = $1;

-- name: ListDataSubjectRequests :many
SELECT * FROM data_subject_requests WHERE account_id = $1 ORDER BY created_at DESC LIMIT 100;

-- name: ListOpenDataSubjectRequests :many
SELECT * FROM data_subject_requests WHERE status IN ('SUBMITTED','VERIFIED_REQUESTER','PROCESSING') ORDER BY due_at LIMIT 200;

-- name: SetDataSubjectVerified :exec
UPDATE data_subject_requests SET verified_at = $2, updated_at = $2 WHERE id = $1;

-- name: SetDataSubjectDecision :exec
UPDATE data_subject_requests SET decision_reason = $2, retained_categories = $3, report_id = coalesce(sqlc.narg(report_id)::uuid, report_id),
    decided_at = $4, updated_at = $4 WHERE id = $1;

-- name: DataSubjectRequestsToProcess :many
SELECT id FROM data_subject_requests WHERE status = 'VERIFIED_REQUESTER' ORDER BY created_at LIMIT $1;

-- name: DataSubjectRequestsAwaitingReport :many
SELECT d.id, d.report_id FROM data_subject_requests d JOIN report_snapshots r ON r.id = d.report_id
 WHERE d.status = 'PROCESSING' AND r.status IN ('READY','FAILED') ORDER BY d.created_at LIMIT $1;

-- name: DataSubjectRequestsToClose :many
SELECT id FROM data_subject_requests WHERE status IN ('FULFILLED','PARTIALLY_FULFILLED','DENIED_WITH_REASON') AND decided_at <= $1 ORDER BY decided_at LIMIT $2;

-- name: InsertReport :one
INSERT INTO report_snapshots (id, account_id, report_type, period_start, period_end, property_id, status, data_subject_request_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, 'QUEUED', $7, $8)
RETURNING *;

-- name: GetReport :one
SELECT * FROM report_snapshots WHERE id = $1;

-- name: ListReports :many
SELECT * FROM report_snapshots WHERE account_id = $1 ORDER BY created_at DESC LIMIT 100;

-- name: QueuedReports :many
SELECT * FROM report_snapshots WHERE status = 'QUEUED' ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED;

-- name: CompleteReport :exec
UPDATE report_snapshots SET status = 'READY', summary = $2, media_id = $3, attempts = attempts + 1, completed_at = $4 WHERE id = $1 AND status = 'QUEUED';

-- name: FailReportAttempt :exec
UPDATE report_snapshots SET attempts = attempts + 1, failure_reason = $2,
    status = CASE WHEN attempts + 1 >= sqlc.arg(max_attempts)::integer THEN 'FAILED' ELSE status END,
    completed_at = CASE WHEN attempts + 1 >= sqlc.arg(max_attempts)::integer THEN sqlc.arg(now)::timestamptz ELSE completed_at END
 WHERE id = $1 AND status = 'QUEUED';

-- name: InsertInsight :one
INSERT INTO market_insights (id, account_id, property_id, listing_id, insight_type, model_name, model_version, inputs, result, confidence,
    generated_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: LatestInsight :one
SELECT * FROM market_insights WHERE property_id = $1 AND insight_type = $2
   AND coalesce(listing_id, '00000000-0000-0000-0000-000000000000'::uuid) = coalesce(sqlc.narg(listing_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
   AND expires_at > sqlc.arg(now)::timestamptz
 ORDER BY generated_at DESC LIMIT 1;

-- name: UpsertDashboardPreferences :one
INSERT INTO dashboard_preferences (account_id, widgets, default_property_id, updated_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (account_id) DO UPDATE SET widgets = EXCLUDED.widgets, default_property_id = EXCLUDED.default_property_id, updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: AnonymizeShortletGuests :execrows
UPDATE shortlet_guests g SET display_name = 'Guest', verification_ref = NULL, customer_ref_id = NULL, updated_at = sqlc.arg(now)::timestamptz
  FROM shortlet_bookings b
 WHERE b.id = g.booking_id AND g.display_name <> 'Guest'
   AND b.status IN ('COMPLETED','CANCELLED','NO_SHOW') AND b.check_out < sqlc.arg(before)::date
   AND NOT EXISTS (SELECT 1 FROM damage_claims c WHERE c.booking_id = b.id AND c.status IN ('OPEN','GUEST_ACCEPTED','DISPUTED','RESPONSE_OVERDUE','RESOLVED'))
   AND NOT EXISTS (SELECT 1 FROM domain_disputes d WHERE d.target_type = 'shortlet_booking' AND d.target_id = b.id
                     AND d.status IN ('OPEN','UNDER_REVIEW','AWAITING_INFORMATION'));

-- name: SetDataSubjectReport :exec
UPDATE data_subject_requests SET report_id = $2, updated_at = $3 WHERE id = $1;
