-- 13 Templates (controlled legal configuration) -----------------------------

-- name: InsertAgreementTemplate :one
INSERT INTO agreement_templates (id, code, version_no, country, agreement_kind, title, document_media_id, clauses, status, policy_version,
    created_by_actor_domain, created_by_actor_id, created_at)
VALUES ($1, $2, (SELECT coalesce(max(t.version_no), 0) + 1 FROM agreement_templates t WHERE t.code = $2), $3, $4, $5, $6, $7, 'ACTIVE', $8, $9, $10, $11)
RETURNING *;

-- name: GetAgreementTemplate :one
SELECT * FROM agreement_templates WHERE id = $1;

-- name: ListAgreementTemplates :many
SELECT * FROM agreement_templates
 WHERE (sqlc.narg(country)::text IS NULL OR country = sqlc.narg(country)::text)
   AND (sqlc.narg(agreement_kind)::text IS NULL OR agreement_kind = sqlc.narg(agreement_kind)::text)
   AND (sqlc.arg(include_retired)::boolean OR status = 'ACTIVE')
 ORDER BY code, version_no DESC
 LIMIT 200;

-- name: RetireAgreementTemplate :one
UPDATE agreement_templates SET status = 'RETIRED', retired_at = $2 WHERE id = $1 AND status = 'ACTIVE' RETURNING *;

-- name: InsertNoticeTemplate :one
INSERT INTO notice_templates (id, code, version_no, country, notice_type, title, min_notice_days, document_media_id, status, policy_version,
    created_by_actor_domain, created_by_actor_id, created_at)
VALUES ($1, $2, (SELECT coalesce(max(t.version_no), 0) + 1 FROM notice_templates t WHERE t.code = $2), $3, $4, $5, $6, $7, 'ACTIVE', $8, $9, $10, $11)
RETURNING *;

-- name: GetNoticeTemplate :one
SELECT * FROM notice_templates WHERE id = $1;

-- name: ListNoticeTemplates :many
SELECT * FROM notice_templates
 WHERE (sqlc.narg(country)::text IS NULL OR country = sqlc.narg(country)::text)
   AND (sqlc.narg(notice_type)::text IS NULL OR notice_type = sqlc.narg(notice_type)::text)
   AND (sqlc.arg(include_retired)::boolean OR status = 'ACTIVE')
 ORDER BY code, version_no DESC
 LIMIT 200;

-- name: RetireNoticeTemplate :one
UPDATE notice_templates SET status = 'RETIRED', retired_at = $2 WHERE id = $1 AND status = 'ACTIVE' RETURNING *;

-- 13 Agreements -----------------------------------------------------------

-- name: InsertAgreement :one
INSERT INTO agreements (id, property_id, unit_id, listing_id, offer_id, parent_agreement_id, renewal_case_id, tenancy_id, agreement_kind, source,
    status, customer_ref_id, template_id, pricing_version_id, loi_id, current_version_no, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'DRAFT', $11, $12, $13, $14, 0, 1, $15, $15)
RETURNING *;

-- name: GetAgreement :one
SELECT * FROM agreements WHERE id = $1;

-- name: GetAgreementForUpdate :one
SELECT * FROM agreements WHERE id = $1 FOR UPDATE;

-- name: GetAgreementByOffer :one
SELECT * FROM agreements WHERE offer_id = $1 AND status <> 'CANCELLED';

-- name: ListAgreementsForAccount :many
SELECT a.* FROM agreements a
 WHERE a.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR a.property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (a.created_at, a.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY a.created_at DESC, a.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: SetAgreementVersionNo :exec
UPDATE agreements SET current_version_no = $2, template_id = coalesce($3, template_id), updated_at = $4 WHERE id = $1;

-- name: SetAgreementSigning :exec
UPDATE agreements SET signing_method = $2, signing_version_no = $3, esign_envelope_ref = $4, signing_started_at = $5, updated_at = $5 WHERE id = $1;

-- name: SetAgreementEnvelope :exec
UPDATE agreements SET esign_envelope_ref = $2, updated_at = $3 WHERE id = $1;

-- name: SetAgreementFullySigned :exec
UPDATE agreements SET fully_signed_at = $2, updated_at = $2 WHERE id = $1;

-- name: SetAgreementEffective :exec
UPDATE agreements SET effective_at = $2, tenancy_id = coalesce(sqlc.narg(tenancy_id)::uuid, tenancy_id), updated_at = $2 WHERE id = $1;

-- name: SetAgreementCancelled :exec
UPDATE agreements SET cancelled_at = $2, cancel_reason = $3, updated_at = $2 WHERE id = $1;

-- name: AgreementsAwaitingEsign :many
SELECT id FROM agreements WHERE status = 'SIGNING' AND signing_method = 'ESIGN' AND esign_envelope_ref IS NOT NULL
 ORDER BY updated_at LIMIT $1;

-- name: AgreementsAwaitingEnvelope :many
SELECT id FROM agreements WHERE status = 'SIGNING' AND signing_method = 'ESIGN' AND esign_envelope_ref IS NULL
 ORDER BY updated_at LIMIT $1;

-- name: InsertAgreementVersion :one
INSERT INTO agreement_versions (id, agreement_id, version_no, terms, document_media_id, template_id, template_version_no, change_note,
    actor_domain, actor_id, created_at)
VALUES ($1, $2, (SELECT coalesce(max(v.version_no), 0) + 1 FROM agreement_versions v WHERE v.agreement_id = $2), $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetAgreementVersion :one
SELECT * FROM agreement_versions WHERE agreement_id = $1 AND version_no = $2;

-- name: ListAgreementVersions :many
SELECT * FROM agreement_versions WHERE agreement_id = $1 ORDER BY version_no DESC LIMIT 100;

-- name: InsertAgreementParticipant :one
INSERT INTO agreement_participants (id, agreement_id, role, party_domain, party_ref_id, display_name, signing_email, sign_required, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListAgreementParticipants :many
SELECT * FROM agreement_participants WHERE agreement_id = $1 AND removed_at IS NULL ORDER BY created_at, id;

-- name: RemoveAgreementParticipant :execrows
UPDATE agreement_participants SET removed_at = $3 WHERE id = $1 AND agreement_id = $2 AND removed_at IS NULL;

-- name: CustomerAgreementRoles :many
SELECT role FROM agreement_participants
 WHERE agreement_id = $1 AND party_domain = 'marketplace' AND party_ref_id = $2 AND removed_at IS NULL;

-- name: InsertSignatureRequest :one
INSERT INTO signature_requests (id, agreement_id, agreement_version_no, participant_id, method, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 1, $7, $7)
RETURNING *;

-- name: ListSignatureRequests :many
SELECT * FROM signature_requests WHERE agreement_id = $1 AND agreement_version_no = $2 ORDER BY created_at, id;

-- name: GetSignatureRequestForUpdate :one
SELECT * FROM signature_requests WHERE agreement_id = $1 AND agreement_version_no = $2 AND participant_id = $3 FOR UPDATE;

-- name: UpdateSignatureRequest :one
UPDATE signature_requests SET status = sqlc.arg(status)::text, provider_signer_ref = coalesce(sqlc.narg(provider_signer_ref)::text, provider_signer_ref),
    evidence_media_id = coalesce(sqlc.narg(evidence_media_id)::uuid, evidence_media_id),
    certificate_media_id = coalesce(sqlc.narg(certificate_media_id)::uuid, certificate_media_id),
    attested_by = coalesce(sqlc.narg(attested_by)::text, attested_by), signed_at = coalesce(sqlc.narg(signed_at)::timestamptz, signed_at),
    declined_reason = coalesce(sqlc.narg(declined_reason)::text, declined_reason), version = version + 1, updated_at = sqlc.arg(updated_at)::timestamptz
 WHERE id = sqlc.arg(id)::uuid
RETURNING *;

-- name: VoidSignatureRequests :exec
UPDATE signature_requests SET status = 'VOIDED', version = version + 1, updated_at = $3
 WHERE agreement_id = $1 AND agreement_version_no = $2 AND status NOT IN ('SIGNED','VOIDED');

-- 14 Tenancy --------------------------------------------------------------

-- name: InsertTenancy :one
INSERT INTO tenancies (id, property_id, unit_id, listing_id, agreement_id, customer_ref_id, tenant_display_name, status, start_date, end_date,
    rent_minor, currency, frequency, due_day, rent_collection, deposit_handling, late_fee_grace_days, late_fee_minor, recurring_fees_minor,
    escalation_after_months, escalation_bps, rent_free_months, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'PENDING_MOVE_IN', $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, 1, $22, $22)
RETURNING *;

-- name: GetTenancy :one
SELECT * FROM tenancies WHERE id = $1;

-- name: GetTenancyForUpdate :one
SELECT * FROM tenancies WHERE id = $1 FOR UPDATE;

-- name: ListTenanciesForAccount :many
SELECT t.* FROM tenancies t
 WHERE t.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR t.property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (t.created_at, t.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY t.created_at DESC, t.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: CountOccupyingTenancies :one
SELECT count(*) FROM tenancies
 WHERE property_id = $1 AND coalesce(unit_id, '00000000-0000-0000-0000-000000000000'::uuid) = coalesce(sqlc.narg(unit_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
   AND status IN ('ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING','MOVE_OUT_PENDING') AND id <> sqlc.arg(exclude_id);

-- name: SetTenancyMovedIn :exec
UPDATE tenancies SET move_in_at = $2, updated_at = $2 WHERE id = $1;

-- name: SetTenancyTerms :exec
UPDATE tenancies SET end_date = $2, rent_minor = $3, recurring_fees_minor = $4, escalation_after_months = $5, escalation_bps = $6,
    schedule_anchor = $7, schedule_materialized_to = NULL, renewal_reminder_sent_at = NULL, updated_at = $8 WHERE id = $1;

-- name: SetTenancyTermination :exec
UPDATE tenancies SET termination_effective_date = $2, updated_at = $3 WHERE id = $1;

-- name: SetTenancyEnded :exec
UPDATE tenancies SET ended_at = $2, end_reason = $3, updated_at = $2 WHERE id = $1;

-- name: SetTenancyDisputeFlag :exec
UPDATE tenancies SET dispute_flag = $2, updated_at = $3 WHERE id = $1;

-- name: SetTenancyScheduleTo :exec
UPDATE tenancies SET schedule_materialized_to = $2 WHERE id = $1;

-- name: TenanciesNeedingSchedule :many
SELECT * FROM tenancies
 WHERE rent_collection = 'PLATFORM' AND status IN ('PENDING_MOVE_IN','ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING')
   AND (schedule_materialized_to IS NULL OR schedule_materialized_to < sqlc.arg(horizon)::date)
   AND (end_date IS NULL OR schedule_materialized_to IS NULL OR schedule_materialized_to < end_date)
 ORDER BY schedule_materialized_to NULLS FIRST
 LIMIT sqlc.arg(max_rows);

-- name: TenanciesForRenewalReminder :many
UPDATE tenancies SET renewal_reminder_sent_at = sqlc.arg(now)::timestamptz
 WHERE id IN (SELECT t.id FROM tenancies t WHERE t.status = 'ACTIVE' AND t.end_date IS NOT NULL AND t.end_date <= sqlc.arg(window_end)::date
                AND t.renewal_reminder_sent_at IS NULL ORDER BY t.end_date LIMIT sqlc.arg(max_rows) FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: CustomerTenancies :many
SELECT t.* FROM tenancies t
 WHERE t.customer_ref_id = $1
    OR t.agreement_id IN (SELECT p.agreement_id FROM agreement_participants p WHERE p.party_domain = 'marketplace' AND p.party_ref_id = $1 AND p.removed_at IS NULL)
 ORDER BY t.created_at DESC
 LIMIT 100;

-- name: InsertRentObligation :one
INSERT INTO rent_obligations (id, tenancy_id, property_id, kind, period_start, period_end, due_date, amount_minor, currency, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'SCHEDULED', 1, $10, $10)
ON CONFLICT (tenancy_id, kind, period_start) DO NOTHING
RETURNING *;

-- name: GetRentObligation :one
SELECT * FROM rent_obligations WHERE id = $1;

-- name: GetRentObligationForUpdate :one
SELECT * FROM rent_obligations WHERE id = $1 FOR UPDATE;

-- name: GetRentObligationByIntent :one
SELECT * FROM rent_obligations WHERE payment_intent_ref = $1;

-- name: ListRentObligations :many
SELECT * FROM rent_obligations WHERE tenancy_id = $1
   AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
 ORDER BY due_date, kind LIMIT 500;

-- name: SetObligationIntent :exec
UPDATE rent_obligations SET payment_intent_ref = $2, last_payment_status = $3, last_checked_at = $4, updated_at = $4 WHERE id = $1;

-- name: SetObligationPayment :exec
UPDATE rent_obligations SET paid_minor = $2, refunded_minor = $3, last_payment_status = $4, last_checked_at = $5, updated_at = $5 WHERE id = $1;

-- name: SetObligationReason :exec
UPDATE rent_obligations SET status_reason = $2, updated_at = $3 WHERE id = $1;

-- name: SetObligationReminder :exec
UPDATE rent_obligations SET reminder_sent_at = $2 WHERE id = $1;

-- name: InsertObligationHistory :exec
INSERT INTO rent_obligation_status_history (id, obligation_id, from_status, to_status, source, payment_ref, source_event_id, paid_minor, reason, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: ListObligationHistory :many
SELECT * FROM rent_obligation_status_history WHERE obligation_id = $1 ORDER BY occurred_at, id;

-- name: ObligationsToOpen :many
SELECT o.id FROM rent_obligations o WHERE o.status = 'SCHEDULED' AND o.due_date <= sqlc.arg(open_until)::date
 ORDER BY o.due_date LIMIT sqlc.arg(max_rows);

-- name: ObligationsNeedingIntent :many
SELECT o.id FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id
 WHERE o.status IN ('DUE','OVERDUE','PARTIAL') AND o.payment_intent_ref IS NULL AND t.rent_collection = 'PLATFORM'
 ORDER BY o.due_date LIMIT $1;

-- name: ObligationsToMarkOverdue :many
SELECT o.id FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id
 WHERE o.status IN ('DUE','PARTIAL') AND o.due_date + t.late_fee_grace_days < sqlc.arg(today)::date
 ORDER BY o.due_date LIMIT sqlc.arg(max_rows);

-- name: ObligationsToReconcile :many
SELECT id FROM rent_obligations
 WHERE payment_intent_ref IS NOT NULL AND status IN ('DUE','PARTIAL','OVERDUE','DISPUTED','REFUND_PENDING')
   AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(checked_before)::timestamptz)
 ORDER BY last_checked_at NULLS FIRST LIMIT sqlc.arg(max_rows);

-- name: FutureObligationsAfter :many
SELECT id FROM rent_obligations WHERE tenancy_id = $1 AND status = 'SCHEDULED' AND period_start > $2;

-- name: OutstandingObligations :one
SELECT count(*)::int AS open_count, coalesce(sum(amount_minor - paid_minor), 0)::bigint AS outstanding_minor
  FROM rent_obligations WHERE tenancy_id = $1 AND status IN ('DUE','PARTIAL','OVERDUE','DISPUTED');

-- name: InitialObligationsSettled :one
SELECT NOT EXISTS (SELECT 1 FROM rent_obligations o JOIN tenancies t ON t.id = o.tenancy_id
  WHERE o.tenancy_id = sqlc.arg(tenancy_id) AND o.kind = 'RENT'
    AND o.period_start <= greatest(t.start_date, sqlc.arg(today)::date)
    AND o.status NOT IN ('PAID','WAIVED'))::boolean AS settled;

-- name: InsertDeposit :one
INSERT INTO deposit_obligations (id, tenancy_id, property_id, amount_minor, currency, status, release_due_days, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'REQUIRED', $6, 1, $7, $7)
RETURNING *;

-- name: GetDepositByTenancy :one
SELECT * FROM deposit_obligations WHERE tenancy_id = $1;

-- name: GetDepositForUpdate :one
SELECT * FROM deposit_obligations WHERE id = $1 FOR UPDATE;

-- name: SetDepositIntent :exec
UPDATE deposit_obligations SET payment_intent_ref = $2, last_checked_at = $3, updated_at = $3 WHERE id = $1;

-- name: SetDepositFunding :exec
UPDATE deposit_obligations SET funded_minor = $2, last_payment_status = $3, last_checked_at = $4, updated_at = $4 WHERE id = $1;

-- name: ObligationsToCancelIntent :many
SELECT id FROM rent_obligations
 WHERE status = 'WAIVED' AND payment_intent_ref IS NOT NULL
   AND coalesce(last_payment_status, '') NOT IN ('CANCELLED','FAILED','SUCCEEDED','REFUNDED','REFUND_PENDING')
 ORDER BY updated_at LIMIT $1;

-- name: DepositsToCancelIntent :many
SELECT d.id FROM deposit_obligations d JOIN tenancies t ON t.id = d.tenancy_id
 WHERE t.status = 'ENDED' AND d.status = 'REQUIRED' AND d.payment_intent_ref IS NOT NULL
   AND coalesce(d.last_payment_status, '') NOT IN ('CANCELLED','FAILED')
 ORDER BY d.updated_at LIMIT $1;

-- name: SetDepositDeductions :exec
UPDATE deposit_obligations SET deductions = $2, deduction_total_minor = $3, proposed_at = $4, response_due_at = $5, tenant_response = NULL,
    disputed_minor = 0, updated_at = $4 WHERE id = $1;

-- name: SetDepositTenantResponse :exec
UPDATE deposit_obligations SET tenant_response = $2, disputed_minor = $3, updated_at = $4 WHERE id = $1;

-- name: GetDepositByIntent :one
SELECT * FROM deposit_obligations WHERE payment_intent_ref = $1;

-- name: SetDepositReleaseRequest :exec
UPDATE deposit_obligations SET release_request_ref = $2, release_requested_at = $3, updated_at = $3 WHERE id = $1;

-- name: SetDepositResolution :exec
UPDATE deposit_obligations SET resolution_owner_minor = $2, resolved_at = $3, release_request_ref = NULL, updated_at = $3 WHERE id = $1;

-- name: DepositsReadyForRelease :many
SELECT id FROM deposit_obligations
 WHERE release_request_ref IS NULL AND (
       status = 'RELEASE_PENDING'
    OR (status = 'DEDUCTION_PROPOSED' AND tenant_response = 'ACCEPTED')
    OR (status = 'DISPUTED' AND (released_minor = 0 OR resolved_at IS NOT NULL)))
 ORDER BY updated_at LIMIT $1;

-- name: SetDepositReleased :exec
UPDATE deposit_obligations SET released_minor = $2, last_checked_at = $3, updated_at = $3 WHERE id = $1;

-- name: DepositsNeedingIntent :many
SELECT d.id FROM deposit_obligations d JOIN tenancies t ON t.id = d.tenancy_id
 WHERE d.status = 'REQUIRED' AND d.payment_intent_ref IS NULL AND t.status <> 'ENDED' ORDER BY d.created_at LIMIT $1;

-- name: DepositsToReconcile :many
SELECT id FROM deposit_obligations
 WHERE ((status IN ('REQUIRED','FUNDED') AND payment_intent_ref IS NOT NULL) OR (status IN ('RELEASE_PENDING','DEDUCTION_PROPOSED','DISPUTED') AND release_request_ref IS NOT NULL))
   AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(checked_before)::timestamptz)
 ORDER BY last_checked_at NULLS FIRST LIMIT sqlc.arg(max_rows);

-- name: DepositsAwaitingResponse :many
SELECT id FROM deposit_obligations WHERE status = 'DEDUCTION_PROPOSED' AND tenant_response IS NULL AND response_due_at <= $1 LIMIT $2;

-- name: InsertChecklist :one
INSERT INTO move_in_checklists (id, tenancy_id, property_id, phase, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'OPEN', 1, $5, $5)
ON CONFLICT (tenancy_id, phase) DO NOTHING
RETURNING *;

-- name: GetChecklist :one
SELECT * FROM move_in_checklists WHERE tenancy_id = $1 AND phase = $2;

-- name: GetChecklistForUpdate :one
SELECT * FROM move_in_checklists WHERE tenancy_id = $1 AND phase = $2 FOR UPDATE;

-- name: UpdateChecklistState :one
UPDATE move_in_checklists SET status = $3, submitted_at = $4, owner_ack_at = $5, tenant_ack_at = $6, locked_at = $7, version = version + 1, updated_at = $8,
    tenant_ack_evidence_media_id = coalesce(sqlc.narg(tenant_ack_evidence_media_id)::uuid, tenant_ack_evidence_media_id)
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: InsertConditionItem :one
INSERT INTO condition_items (id, checklist_id, property_id, room, item, condition, notes, media_ids, supersedes_id, addendum, recorded_by_party,
    actor_domain, actor_id, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: GetConditionItem :one
SELECT * FROM condition_items WHERE id = $1;

-- name: ListConditionItems :many
SELECT * FROM condition_items WHERE checklist_id = $1 ORDER BY recorded_at, id LIMIT 1000;

-- name: InsertKeyItem :one
INSERT INTO key_access_inventory (id, tenancy_id, property_id, item_type, label, quantity, access_grant_ref, issued_at, notes, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, $10)
RETURNING *;

-- name: ListKeyItems :many
SELECT * FROM key_access_inventory WHERE tenancy_id = $1 ORDER BY created_at, id;

-- name: ReturnKeyItem :one
UPDATE key_access_inventory SET returned_at = $3, returned_quantity = $4, notes = coalesce($5, notes), version = version + 1, updated_at = $3
 WHERE id = $1 AND tenancy_id = $2 AND returned_at IS NULL
RETURNING *;

-- name: UnreturnedKeyItems :one
SELECT count(*) FROM key_access_inventory WHERE tenancy_id = $1 AND returned_at IS NULL;

-- 15 Renewal / notices / move-out ----------------------------------------

-- name: InsertRenewalCase :one
INSERT INTO renewal_cases (id, tenancy_id, property_id, status, current_round, respond_by, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, 1, $5, 1, $6, $6)
RETURNING *;

-- name: GetRenewalCase :one
SELECT * FROM renewal_cases WHERE id = $1;

-- name: GetRenewalCaseForUpdate :one
SELECT * FROM renewal_cases WHERE id = $1 FOR UPDATE;

-- name: ListRenewalCases :many
SELECT * FROM renewal_cases WHERE tenancy_id = $1 ORDER BY created_at DESC LIMIT 50;

-- name: SetRenewalRound :exec
UPDATE renewal_cases SET current_round = $2, respond_by = $3, updated_at = $4 WHERE id = $1;

-- name: SetRenewalAddendum :exec
UPDATE renewal_cases SET addendum_agreement_id = $2, updated_at = $3 WHERE id = $1;

-- name: InsertRenewalTerms :one
INSERT INTO renewal_terms (id, case_id, round_no, proposed_by, rent_minor, currency, term_months, start_date, change_explanation, market_context,
    actor_domain, actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: ListRenewalTerms :many
SELECT * FROM renewal_terms WHERE case_id = $1 ORDER BY round_no;

-- name: LatestRenewalTerms :one
SELECT * FROM renewal_terms WHERE case_id = $1 ORDER BY round_no DESC LIMIT 1;

-- name: DueRenewalExpiries :many
SELECT id FROM renewal_cases WHERE status IN ('PROPOSED','COUNTERED') AND respond_by <= $1 ORDER BY respond_by LIMIT $2;

-- name: InsertNotice :one
INSERT INTO notices (id, tenancy_id, property_id, issued_by, notice_type, template_id, status, summary, document_media_id, requested_effective_date,
    delivery_channel, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'DRAFT', $7, $8, $9, $10, 1, $11, $11)
RETURNING *;

-- name: GetNotice :one
SELECT * FROM notices WHERE id = $1;

-- name: GetNoticeForUpdate :one
SELECT * FROM notices WHERE id = $1 FOR UPDATE;

-- name: ListNotices :many
SELECT * FROM notices WHERE tenancy_id = $1 ORDER BY created_at DESC, id DESC LIMIT 200;

-- name: SetNoticeIssued :exec
UPDATE notices SET issued_at = $2, effective_date = $3, policy_version = $4, updated_at = $2 WHERE id = $1;

-- name: SetNoticeDelivered :exec
UPDATE notices SET delivered_at = $2, delivery_evidence_media_id = coalesce($3, delivery_evidence_media_id), delivery_ref = coalesce($4, delivery_ref),
    failure_reason = NULL, updated_at = $2 WHERE id = $1;

-- name: SetNoticeFailed :exec
UPDATE notices SET failure_reason = $2, updated_at = $3 WHERE id = $1;

-- name: SetNoticeAcknowledged :exec
UPDATE notices SET acknowledged_at = $2, updated_at = $2 WHERE id = $1;

-- name: SetNoticeDisputed :exec
UPDATE notices SET disputed_at = $2, dispute_reason = $3, updated_at = $2 WHERE id = $1;

-- name: InsertMoveOutCase :one
INSERT INTO move_out_cases (id, tenancy_id, property_id, status, initiated_by, notice_id, move_out_date, version, created_at, updated_at)
VALUES ($1, $2, $3, 'OPEN', $4, $5, $6, 1, $7, $7)
RETURNING *;

-- name: GetMoveOutByTenancy :one
SELECT * FROM move_out_cases WHERE tenancy_id = $1;

-- name: GetMoveOutForUpdate :one
SELECT * FROM move_out_cases WHERE tenancy_id = $1 FOR UPDATE;

-- name: SetMoveOutInspection :exec
UPDATE move_out_cases SET inspection_at = $2, updated_at = $3 WHERE id = $1;

-- name: SetMoveOutInspected :exec
UPDATE move_out_cases SET inspected_at = $2, updated_at = $2 WHERE id = $1;

-- name: SetMoveOutKeysReturned :exec
UPDATE move_out_cases SET keys_returned_at = $2, updated_at = $2 WHERE id = $1;

-- name: SetMoveOutCompleted :exec
UPDATE move_out_cases SET owner_confirmed_at = $2, return_to_market = $3, completed_at = $2, updated_at = $2 WHERE id = $1;

-- 16 Utilities & service charges ----------------------------------------

-- name: InsertUtilityAccount :one
INSERT INTO utility_accounts (id, property_id, unit_id, utility_type, provider_name, account_ref_last4, meter_number, meter_type, responsibility,
    status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'ACTIVE', 1, $10, $10)
RETURNING *;

-- name: GetUtilityAccount :one
SELECT * FROM utility_accounts WHERE id = $1;

-- name: ListUtilityAccounts :many
SELECT * FROM utility_accounts WHERE property_id = $1 ORDER BY created_at, id;

-- name: UpdateUtilityAccount :one
UPDATE utility_accounts SET provider_name = $3, account_ref_last4 = $4, meter_number = $5, meter_type = $6, responsibility = $7, status = $8,
    version = version + 1, updated_at = $9
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: InsertAllocationRule :one
INSERT INTO utility_allocation_rules (id, utility_account_id, version_no, method, shares, actor_domain, actor_id, created_at)
VALUES ($1, $2, (SELECT coalesce(max(r.version_no), 0) + 1 FROM utility_allocation_rules r WHERE r.utility_account_id = $2), $3, $4, $5, $6, $7)
RETURNING *;

-- name: SetUtilityRuleVersion :exec
UPDATE utility_accounts SET allocation_rule_version = $2, version = version + 1, updated_at = $3 WHERE id = $1;

-- name: GetAllocationRule :one
SELECT * FROM utility_allocation_rules WHERE utility_account_id = $1 AND version_no = $2;

-- name: InsertMeterReading :one
INSERT INTO meter_readings (id, utility_account_id, property_id, tenancy_id, reading_type, value, unit, read_at, media_id, corrects_id,
    recorded_by_party, actor_domain, actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: GetMeterReading :one
SELECT * FROM meter_readings WHERE id = $1;

-- name: ListMeterReadings :many
SELECT * FROM meter_readings WHERE property_id = $1
   AND (sqlc.narg(utility_account_id)::uuid IS NULL OR utility_account_id = sqlc.narg(utility_account_id)::uuid)
 ORDER BY read_at DESC, id DESC LIMIT 500;

-- name: InsertUtilityBill :one
INSERT INTO utility_bills (id, utility_account_id, property_id, period_start, period_end, amount_minor, currency, bill_media_id, status,
    version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'RECORDED', 1, $9, $9)
RETURNING *;

-- name: GetUtilityBillForUpdate :one
SELECT * FROM utility_bills WHERE id = $1 FOR UPDATE;

-- name: ListUtilityBills :many
SELECT * FROM utility_bills WHERE property_id = $1
   AND (sqlc.narg(utility_account_id)::uuid IS NULL OR utility_account_id = sqlc.narg(utility_account_id)::uuid)
 ORDER BY period_start DESC, id DESC LIMIT 500;

-- name: UpdateUtilityBillState :one
UPDATE utility_bills SET status = $3, allocation = $4, allocation_rule_version = $5, original_allocation = $6, dispute_reason = $7,
    version = version + 1, updated_at = $8
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: InsertBudget :one
INSERT INTO service_charge_budgets (id, property_id, period_start, period_end, currency, status, categories, total_expected_minor, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'DRAFT', $6, $7, 1, $8, $8)
RETURNING *;

-- name: GetBudget :one
SELECT * FROM service_charge_budgets WHERE id = $1;

-- name: ListBudgets :many
SELECT * FROM service_charge_budgets WHERE property_id = $1 ORDER BY period_start DESC LIMIT 100;

-- name: UpdateBudget :one
UPDATE service_charge_budgets SET period_end = $3, categories = $4, total_expected_minor = $5, status = $6, approved_at = $7, closed_at = $8,
    version = version + 1, updated_at = $9
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: InsertServiceChargeItem :one
INSERT INTO service_charge_items (id, budget_id, property_id, category, item_type, amount_minor, description, evidence_media_ids, adjusts_id,
    incurred_on, actor_domain, actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetServiceChargeItem :one
SELECT * FROM service_charge_items WHERE id = $1;

-- name: ListServiceChargeItems :many
SELECT * FROM service_charge_items WHERE budget_id = $1 ORDER BY created_at, id LIMIT 2000;

-- name: BudgetTotals :many
SELECT category, item_type, sum(amount_minor)::bigint AS total_minor
  FROM service_charge_items WHERE budget_id = $1
 GROUP BY category, item_type ORDER BY category, item_type;
