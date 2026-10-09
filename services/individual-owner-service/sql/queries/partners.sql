-- 28–30 Financing, insurance and smart access --------------------------------

-- name: UpsertFinanceOffer :one
INSERT INTO finance_offers (id, account_id, listing_id, product_type, partner_ref, external_offer_ref, status, disclosure, consent_id, expires_at,
    created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'OFFERED', $7, $8, $9, $10, $10)
ON CONFLICT (partner_ref, external_offer_ref) DO UPDATE SET disclosure = EXCLUDED.disclosure, expires_at = EXCLUDED.expires_at,
    consent_id = EXCLUDED.consent_id, updated_at = EXCLUDED.updated_at
 WHERE finance_offers.status = 'OFFERED' AND finance_offers.account_id = EXCLUDED.account_id
RETURNING *;

-- name: ListFinanceOffers :many
SELECT * FROM finance_offers WHERE account_id = $1 ORDER BY created_at DESC LIMIT 100;

-- name: GetFinanceOfferForUpdate :one
SELECT * FROM finance_offers WHERE id = $1 FOR UPDATE;

-- name: SetFinanceOfferDecision :exec
UPDATE finance_offers SET status = $2, owner_payout_preference = $3, decided_at = $4, updated_at = $4 WHERE id = $1;

-- name: InsertPolicy :one
INSERT INTO insurance_policies (id, account_id, property_id, source, product_type, insurer_name, policy_ref, coverage_minor, premium_minor, currency,
    starts_on, ends_on, status, document_media_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 1, $15, $15)
RETURNING *;

-- name: GetPolicy :one
SELECT * FROM insurance_policies WHERE id = $1;

-- name: ListPolicies :many
SELECT * FROM insurance_policies
 WHERE property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                          AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR property_id = sqlc.narg(property_id)::uuid)
 ORDER BY ends_on DESC LIMIT 200;

-- name: PoliciesToRemind :many
SELECT * FROM insurance_policies WHERE status = 'ACTIVE' AND ends_on <= sqlc.arg(horizon)::date ORDER BY ends_on LIMIT sqlc.arg(max_rows);

-- name: MarkPolicyExpiring :exec
UPDATE insurance_policies SET status = 'EXPIRING', renewal_reminded_at = $2, version = version + 1, updated_at = $2 WHERE id = $1 AND status = 'ACTIVE';

-- name: ExpirePolicies :execrows
UPDATE insurance_policies SET status = 'EXPIRED', version = version + 1, updated_at = sqlc.arg(now)::timestamptz
 WHERE status IN ('ACTIVE','EXPIRING') AND ends_on < sqlc.arg(today)::date;

-- name: InsertInsuranceClaim :one
INSERT INTO insurance_claims (id, policy_id, property_id, incident_date, description, claimed_minor, currency, status, insurer_claim_ref,
    evidence_media_ids, related_ref_type, related_ref_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'SUBMITTED', $8, $9, $10, $11, 1, $12, $12)
RETURNING *;

-- name: GetInsuranceClaimForUpdate :one
SELECT * FROM insurance_claims WHERE id = $1 FOR UPDATE;

-- name: ListInsuranceClaims :many
SELECT c.* FROM insurance_claims c
 WHERE c.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(policy_id)::uuid IS NULL OR c.policy_id = sqlc.narg(policy_id)::uuid)
 ORDER BY c.created_at DESC LIMIT 200;

-- name: UpdateInsuranceClaim :one
UPDATE insurance_claims SET status = sqlc.arg(status)::text, insurer_claim_ref = coalesce(sqlc.narg(insurer_claim_ref)::text, insurer_claim_ref),
    approved_minor = coalesce(sqlc.narg(approved_minor)::bigint, approved_minor), paid_minor = sqlc.arg(paid_minor)::bigint,
    evidence_media_ids = sqlc.arg(evidence_media_ids)::uuid[], status_note = sqlc.narg(status_note)::text,
    version = version + 1, updated_at = sqlc.arg(now)::timestamptz
 WHERE id = sqlc.arg(id)::uuid AND version = sqlc.arg(version)::bigint
RETURNING *;

-- name: InsertAccessDevice :one
INSERT INTO access_devices (id, property_id, unit_id, device_type, label, provider_device_ref, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'ACTIVE', $7, $7)
RETURNING *;

-- name: GetAccessDevice :one
SELECT * FROM access_devices WHERE id = $1;

-- name: ListAccessDevices :many
SELECT * FROM access_devices WHERE property_id = $1 AND status = 'ACTIVE' ORDER BY created_at;

-- name: InsertAccessGrant :one
INSERT INTO access_grants (id, property_id, device_id, purpose, relation_type, relation_id, principal_domain, principal_ref, principal_label,
    starts_at, ends_at, status, requested_by, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'REQUESTED', $12, 1, $13, $13)
RETURNING *;

-- name: GetAccessGrant :one
SELECT * FROM access_grants WHERE id = $1;

-- name: ListAccessGrants :many
SELECT g.* FROM access_grants g
 WHERE g.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR g.property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR g.status = sqlc.narg(status)::text)
 ORDER BY g.created_at DESC LIMIT 200;

-- name: SetGrantCredential :exec
UPDATE access_grants SET credential_ref = $2, updated_at = $3 WHERE id = $1;

-- name: SetGrantEndReason :exec
UPDATE access_grants SET end_reason = $2, updated_at = $3 WHERE id = $1;

-- name: MarkCredentialRevoked :exec
UPDATE access_grants SET credential_revoked_at = $2 WHERE id = $1 AND credential_revoked_at IS NULL;

-- name: GrantsForRelation :many
SELECT id FROM access_grants WHERE relation_type = $1 AND relation_id = $2 AND status IN ('REQUESTED','APPROVED','ISSUED','ACTIVE');

-- name: GrantsToProvision :many
SELECT g.id FROM access_grants g JOIN access_devices d ON d.id = g.device_id
 WHERE g.status = 'APPROVED' AND d.provider_device_ref IS NOT NULL AND g.credential_ref IS NULL AND g.ends_at > sqlc.arg(now)::timestamptz
 ORDER BY g.starts_at LIMIT sqlc.arg(max_rows);

-- name: GrantsToActivate :many
SELECT id FROM access_grants WHERE status = 'ISSUED' AND starts_at <= sqlc.arg(now)::timestamptz AND ends_at > sqlc.arg(now)::timestamptz
 ORDER BY starts_at LIMIT sqlc.arg(max_rows);

-- name: GrantsToEnd :many
SELECT id FROM access_grants WHERE status IN ('REQUESTED','APPROVED','ISSUED','ACTIVE') AND ends_at <= sqlc.arg(now)::timestamptz
 ORDER BY ends_at LIMIT sqlc.arg(max_rows);

-- name: GrantsWithEndedRelation :many
SELECT g.id FROM access_grants g
 WHERE g.status IN ('REQUESTED','APPROVED','ISSUED','ACTIVE') AND (
       (g.relation_type = 'viewing_request' AND EXISTS (SELECT 1 FROM viewing_requests v WHERE v.id = g.relation_id AND v.status IN ('COMPLETED','CANCELLED','NO_SHOW')))
    OR (g.relation_type = 'tenancy' AND EXISTS (SELECT 1 FROM tenancies t WHERE t.id = g.relation_id AND t.status = 'ENDED'))
    OR (g.relation_type = 'shortlet_booking' AND EXISTS (SELECT 1 FROM shortlet_bookings b WHERE b.id = g.relation_id
          AND b.status IN ('CHECKED_OUT','COMPLETED','CANCELLED','NO_SHOW','DISPUTED')))
    OR (g.relation_type = 'manager_assignment' AND EXISTS (SELECT 1 FROM manager_assignments a WHERE a.id = g.relation_id
          AND a.status IN ('SUSPENDED','REVOKED','EXPIRED','TERMINATED')))
    OR (g.relation_type = 'maintenance_job' AND EXISTS (SELECT 1 FROM maintenance_jobs j WHERE j.id = g.relation_id AND j.status IN ('ACCEPTED','CANCELLED'))))
 LIMIT sqlc.arg(max_rows);

-- name: GrantsToRevokeCredential :many
SELECT g.id FROM access_grants g
 WHERE g.status IN ('EXPIRED','REVOKED','COMPROMISED') AND g.credential_ref IS NOT NULL AND g.credential_revoked_at IS NULL
 ORDER BY g.updated_at LIMIT $1;

-- name: InsertAccessEvent :exec
INSERT INTO access_events (id, grant_id, device_id, property_id, event_type, actor, note, occurred_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListAccessEvents :many
SELECT e.* FROM access_events e
 WHERE e.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR e.property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(grant_id)::uuid IS NULL OR e.grant_id = sqlc.narg(grant_id)::uuid)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (e.occurred_at, e.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY e.occurred_at DESC, e.id DESC LIMIT sqlc.arg(max_rows);
