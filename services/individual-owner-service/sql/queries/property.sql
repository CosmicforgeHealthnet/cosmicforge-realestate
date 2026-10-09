-- 05 Properties --------------------------------------------------------------

-- name: InsertProperty :one
INSERT INTO properties (id, owner_account_id, status, category, property_type, title, description, timezone, bedrooms,
    bathrooms, floor_area_sqm, year_built, external_ref, import_id, version, created_at, updated_at)
VALUES ($1, $2, 'DRAFT', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 1, $14, $14)
RETURNING *;

-- name: GetProperty :one
SELECT * FROM properties WHERE id = $1;

-- name: GetPropertyForUpdate :one
SELECT * FROM properties WHERE id = $1 FOR UPDATE;

-- name: GetPropertyByExternalRef :one
SELECT * FROM properties WHERE owner_account_id = $1 AND external_ref = $2;

-- name: ListAccessibleProperties :many
SELECT p.* FROM properties p
 WHERE p.id IN (SELECT pa.property_id FROM property_access pa
                 WHERE pa.account_id = sqlc.arg(account_id)
                   AND (pa.valid_from IS NULL OR pa.valid_from <= sqlc.arg(now)::timestamptz)
                   AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(status)::text IS NULL OR p.status = sqlc.narg(status)::text)
   AND (sqlc.narg(category)::text IS NULL OR p.category = sqlc.narg(category)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (p.created_at, p.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY p.created_at DESC, p.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: ListAccessiblePropertyIDs :many
SELECT DISTINCT pa.property_id FROM property_access pa
  JOIN properties p ON p.id = pa.property_id
 WHERE pa.account_id = sqlc.arg(account_id) AND p.status <> 'ARCHIVED'
   AND (pa.valid_from IS NULL OR pa.valid_from <= sqlc.arg(now)::timestamptz)
   AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz);

-- name: UpdatePropertyDetails :one
UPDATE properties
   SET property_type = $3, title = $4, description = $5, timezone = $6, bedrooms = $7, bathrooms = $8,
       floor_area_sqm = $9, year_built = $10, version = version + 1, updated_at = $11
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: SetPropertyRestriction :one
UPDATE properties SET restriction_reason = $2, updated_at = $3 WHERE id = $1 RETURNING *;

-- name: SetPropertyOwner :one
UPDATE properties SET owner_account_id = $2, version = version + 1, updated_at = $3 WHERE id = $1 RETURNING *;

-- name: TouchProperty :exec
UPDATE properties SET version = version + 1, updated_at = $2 WHERE id = $1;

-- name: UpsertPropertyAddress :one
INSERT INTO property_addresses (property_id, line1, line2, city, state_region, postal_code, country_code, latitude,
    longitude, visibility, area_label, fingerprint, geocode_source, geocoded_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (property_id) DO UPDATE SET line1 = EXCLUDED.line1, line2 = EXCLUDED.line2, city = EXCLUDED.city,
    state_region = EXCLUDED.state_region, postal_code = EXCLUDED.postal_code, country_code = EXCLUDED.country_code,
    latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude, visibility = EXCLUDED.visibility,
    area_label = EXCLUDED.area_label, fingerprint = EXCLUDED.fingerprint, geocode_source = EXCLUDED.geocode_source,
    geocoded_at = EXCLUDED.geocoded_at, updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: GetPropertyAddress :one
SELECT * FROM property_addresses WHERE property_id = $1;

-- name: FindPropertiesByFingerprint :many
SELECT p.id, p.owner_account_id, p.status FROM property_addresses a JOIN properties p ON p.id = a.property_id
 WHERE a.fingerprint = $1 AND p.id <> $2 AND p.status <> 'ARCHIVED'
 ORDER BY p.created_at LIMIT 5;

-- name: DeletePropertyFeatures :exec
DELETE FROM property_features WHERE property_id = $1;

-- name: InsertPropertyFeature :exec
INSERT INTO property_features (property_id, feature_key, category, value, updated_at) VALUES ($1, $2, $3, $4, $5);

-- name: ListPropertyFeatures :many
SELECT * FROM property_features WHERE property_id = $1 ORDER BY category, feature_key;

-- name: InsertUnit :one
INSERT INTO property_units (id, property_id, parent_unit_id, unit_type, label, floor_number, bedrooms, bathrooms,
    floor_area_sqm, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'ACTIVE', $10, $10)
RETURNING *;

-- name: ListUnits :many
SELECT * FROM property_units WHERE property_id = $1 AND (sqlc.arg(include_archived)::bool OR status = 'ACTIVE')
 ORDER BY created_at, id;

-- name: GetUnit :one
SELECT * FROM property_units WHERE id = $1 AND property_id = $2;

-- name: UpdateUnit :one
UPDATE property_units SET label = $3, floor_number = $4, bedrooms = $5, bathrooms = $6, floor_area_sqm = $7,
    status = $8, updated_at = $9, version = version + 1
 WHERE id = $1 AND property_id = $2 AND version = $10
RETURNING *;

-- name: CountActiveChildUnits :one
SELECT count(*) FROM property_units WHERE parent_unit_id = $1 AND status = 'ACTIVE';

-- name: InsertParcel :one
INSERT INTO land_parcels (id, property_id, parcel_ref, title_type, area_sqm, zoning, access_notes, boundary_geojson,
    status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'ACTIVE', $9, $9)
RETURNING *;

-- name: ListParcels :many
SELECT * FROM land_parcels WHERE property_id = $1 AND status = 'ACTIVE' ORDER BY created_at, id;

-- name: GetParcel :one
SELECT * FROM land_parcels WHERE id = $1 AND property_id = $2;

-- name: UpdateParcel :one
UPDATE land_parcels SET parcel_ref = $3, title_type = $4, area_sqm = $5, zoning = $6, access_notes = $7,
    boundary_geojson = $8, status = $9, updated_at = $10, version = version + 1
 WHERE id = $1 AND property_id = $2 AND version = $11
RETURNING *;

-- name: InsertAsset :one
INSERT INTO property_assets (id, property_id, unit_id, asset_type, name, make, model, serial_number, installed_on,
    warranty_until, service_interval_days, last_serviced_on, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'ACTIVE', $13, $13)
RETURNING *;

-- name: ListAssets :many
SELECT * FROM property_assets WHERE property_id = $1 AND (sqlc.arg(include_retired)::bool OR status = 'ACTIVE')
 ORDER BY created_at, id;

-- name: GetAsset :one
SELECT * FROM property_assets WHERE id = $1 AND property_id = $2;

-- name: UpdateAsset :one
UPDATE property_assets SET unit_id = $3, name = $4, make = $5, model = $6, serial_number = $7, installed_on = $8,
    warranty_until = $9, service_interval_days = $10, last_serviced_on = $11, status = $12, updated_at = $13, version = version + 1
 WHERE id = $1 AND property_id = $2 AND version = $14
RETURNING *;

-- name: InsertPropertyMedia :one
INSERT INTO property_media_links (id, property_id, media_id, purpose, media_status, position, caption, geotag_lat,
    geotag_lon, captured_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
ON CONFLICT (property_id, media_id, purpose) DO NOTHING
RETURNING *;

-- name: ListPropertyMedia :many
SELECT * FROM property_media_links WHERE property_id = $1 ORDER BY purpose, position, created_at;

-- name: GetPropertyMedia :one
SELECT * FROM property_media_links WHERE id = $1 AND property_id = $2;

-- name: DeletePropertyMedia :execrows
DELETE FROM property_media_links WHERE id = $1 AND property_id = $2;

-- name: SetPropertyMediaStatus :many
UPDATE property_media_links SET media_status = $2, updated_at = $3 WHERE media_id = $1 AND media_status <> $2
RETURNING property_id;

-- name: SetAuthorityDocumentStatus :many
UPDATE authority_documents SET media_status = $2, updated_at = $3 WHERE media_id = $1 AND media_status <> $2
RETURNING property_id;

-- name: CountReadyPropertyPhotos :one
SELECT count(*) FROM property_media_links WHERE property_id = $1 AND purpose = 'PHOTO' AND media_status = 'READY';

-- name: ListPropertyHistory :many
SELECT * FROM property_history
 WHERE property_id = sqlc.arg(property_id)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (occurred_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY occurred_at DESC, id DESC
 LIMIT sqlc.arg(max_rows);

-- name: ListLifecycleTransitions :many
SELECT * FROM lifecycle_transitions WHERE aggregate_type = $1 AND aggregate_id = $2 ORDER BY occurred_at, id;

-- name: InsertPropertyImport :one
INSERT INTO property_imports (id, account_id, status, rows, total_rows, created_at)
VALUES ($1, $2, 'RECEIVED', $3, $4, $5)
RETURNING *;

-- name: GetPropertyImport :one
SELECT * FROM property_imports WHERE id = $1 AND account_id = $2;

-- name: ClaimPropertyImport :one
UPDATE property_imports AS pi SET status = 'PROCESSING', lease_owner = sqlc.arg(owner), lease_expires_at = sqlc.arg(lease_until),
    attempts = pi.attempts + 1
 WHERE pi.id = (SELECT q.id FROM property_imports q
              WHERE (q.status = 'RECEIVED' OR (q.status = 'PROCESSING' AND q.lease_expires_at < sqlc.arg(now)::timestamptz))
                AND q.attempts < sqlc.arg(max_attempts)
              ORDER BY q.created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: CompletePropertyImport :execrows
UPDATE property_imports SET status = $3, created_count = $4, matched_count = $5, error_count = $6, errors = $7,
    completed_at = $8, lease_owner = NULL, lease_expires_at = NULL
 WHERE id = $1 AND lease_owner = $2 AND status = 'PROCESSING';

-- name: FailExhaustedPropertyImports :execrows
UPDATE property_imports SET status = 'FAILED', completed_at = $2, lease_owner = NULL, lease_expires_at = NULL,
    errors = '[{"row":0,"code":"IMPORT_ATTEMPTS_EXHAUSTED"}]'::jsonb
 WHERE status IN ('RECEIVED','PROCESSING') AND attempts >= $1 AND (lease_expires_at IS NULL OR lease_expires_at < $2);

-- Authorization facts ------------------------------------------------------

-- name: PropertyAuthorityState :one
SELECT p.status, p.owner_account_id,
       EXISTS (SELECT 1 FROM ownership_records r WHERE r.property_id = p.id AND r.is_primary AND r.status IN ('VERIFIED','TRANSFER_PENDING')
                  AND (r.expires_at IS NULL OR r.expires_at > sqlc.arg(now)::timestamptz))::bool AS authority_verified
  FROM properties p WHERE p.id = sqlc.arg(property_id);

-- name: PropertyAccessForAccount :many
SELECT role, valid_from, valid_until, capabilities FROM property_access WHERE property_id = $1 AND account_id = $2;

-- name: PropertyPrincipalAccounts :many
SELECT DISTINCT account_id::uuid FROM property_access
 WHERE property_id = sqlc.arg(property_id) AND role IN ('OWNER','CO_OWNER')
   AND (valid_until IS NULL OR valid_until > sqlc.arg(now)::timestamptz);

-- name: ManagerAccess :many
SELECT a.id, a.starts_at, a.ends_at, a.status, mp.capabilities
  FROM manager_assignments a
  JOIN manager_permissions mp ON mp.assignment_id = a.id AND mp.version_no = a.current_permission_version
 WHERE a.property_id = $1 AND a.manager_domain = $2 AND a.manager_ref_id = $3 AND a.status IN ('ACTIVE','EXPIRING');

-- 04 Ownership -------------------------------------------------------------

-- name: InsertOwnershipRecord :one
INSERT INTO ownership_records (id, property_id, claimant_account_id, claim_type, status, is_primary,
    conflict_with_record_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8, $8)
RETURNING *;

-- name: GetOwnershipRecord :one
SELECT * FROM ownership_records WHERE id = $1;

-- name: GetPrimaryOwnershipRecord :one
SELECT * FROM ownership_records WHERE property_id = $1 AND is_primary;

-- name: GetPrimaryOwnershipRecordForUpdate :one
SELECT * FROM ownership_records WHERE property_id = $1 AND is_primary FOR UPDATE;

-- name: ListOwnershipRecords :many
SELECT * FROM ownership_records WHERE property_id = $1 ORDER BY created_at, id;

-- name: SetOwnershipClaim :exec
UPDATE ownership_records SET claim_type = $2, updated_at = $3 WHERE id = $1;

-- name: SetOwnershipVerification :exec
UPDATE ownership_records SET verification_id = $2, updated_at = $3 WHERE id = $1;

-- name: SetOwnershipDecision :exec
UPDATE ownership_records SET verified_at = $2, expires_at = $3, reason_code = $4, updated_at = $5 WHERE id = $1;

-- name: DemoteOwnershipRecord :exec
UPDATE ownership_records SET is_primary = false, updated_at = $2 WHERE id = $1;

-- name: GetPrimaryRecordByVerification :one
SELECT * FROM ownership_records WHERE verification_id = $1;

-- name: ListOwnershipConflicts :many
SELECT * FROM ownership_records
 WHERE status = 'CONFLICT'
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (created_at, id) > (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY created_at, id LIMIT sqlc.arg(max_rows);

-- name: DueOwnershipExpiries :many
SELECT id, property_id FROM ownership_records
 WHERE status = 'VERIFIED' AND expires_at IS NOT NULL AND expires_at <= $1
 ORDER BY expires_at LIMIT $2;

-- name: InsertOwnershipParty :one
INSERT INTO ownership_parties (id, ownership_record_id, property_id, party_role, account_id, invite_email, display_name,
    share_bps, capabilities, evidence_media_id, status, valid_from, valid_until, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)
RETURNING *;

-- name: ListOwnershipParties :many
SELECT * FROM ownership_parties WHERE property_id = $1 AND status IN ('INVITED','ACTIVE') ORDER BY created_at, id;

-- name: GetOwnershipParty :one
SELECT * FROM ownership_parties WHERE id = $1;

-- name: GetOwnershipPartyForUpdate :one
SELECT * FROM ownership_parties WHERE id = $1 FOR UPDATE;

-- name: AcceptOwnershipParty :one
UPDATE ownership_parties SET account_id = $2, status = 'ACTIVE', valid_from = coalesce(valid_from, $3), updated_at = $3
 WHERE id = $1 AND status = 'INVITED'
RETURNING *;

-- name: EndOwnershipParty :one
UPDATE ownership_parties SET status = $2, revoked_at = CASE WHEN $2 = 'REVOKED' THEN $3::timestamptz ELSE revoked_at END, updated_at = $3
 WHERE id = $1 AND status IN ('INVITED','ACTIVE')
RETURNING *;

-- name: SumOwnershipShares :one
SELECT coalesce(sum(share_bps), 0)::bigint FROM ownership_parties
 WHERE ownership_record_id = $1 AND status IN ('INVITED','ACTIVE') AND party_role IN ('OWNER','CO_OWNER');

-- name: ListInvitationsForEmail :many
SELECT op.*, p.title AS property_title FROM ownership_parties op JOIN properties p ON p.id = op.property_id
 WHERE op.invite_email = $1 AND op.status = 'INVITED' ORDER BY op.created_at;

-- name: ExpireRepresentativeParties :many
UPDATE ownership_parties SET status = 'EXPIRED', updated_at = $1
 WHERE status = 'ACTIVE' AND valid_until IS NOT NULL AND valid_until <= $1
RETURNING id, property_id;

-- name: TransferPartyRecord :exec
UPDATE ownership_parties SET ownership_record_id = $2, updated_at = $3
 WHERE property_id = $1 AND status IN ('INVITED','ACTIVE') AND party_role IN ('AUTHORIZED_REPRESENTATIVE','ESTATE_EXECUTOR','BENEFICIAL_OWNER');

-- name: EndPrincipalParties :exec
UPDATE ownership_parties SET status = 'REVOKED', revoked_at = $2, updated_at = $2
 WHERE ownership_record_id = $1 AND status IN ('INVITED','ACTIVE');

-- name: InsertOwnershipConsent :one
INSERT INTO ownership_consents (id, property_id, party_id, action_type, target_type, target_id, requested_by_account,
    decision, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'PENDING', $8, $9)
ON CONFLICT (party_id, action_type, target_id) WHERE decision = 'PENDING' DO NOTHING
RETURNING *;

-- name: ListConsentsForTarget :many
SELECT c.*, op.account_id AS party_account_id, op.display_name AS party_name, op.share_bps AS party_share_bps
  FROM ownership_consents c JOIN ownership_parties op ON op.id = c.party_id
 WHERE c.property_id = $1 AND c.action_type = $2 AND c.target_id = $3
 ORDER BY c.created_at DESC, c.id DESC;

-- name: GetOwnershipConsentForUpdate :one
SELECT c.*, op.account_id AS party_account_id FROM ownership_consents c JOIN ownership_parties op ON op.id = c.party_id
 WHERE c.id = $1 FOR UPDATE OF c;

-- name: DecideOwnershipConsent :one
UPDATE ownership_consents SET decision = $2, decision_note = $3, decided_at = $4
 WHERE id = $1 AND decision = 'PENDING'
RETURNING *;

-- name: ListPendingConsentsForAccount :many
SELECT c.*, p.title AS property_title FROM ownership_consents c
  JOIN ownership_parties op ON op.id = c.party_id
  JOIN properties p ON p.id = c.property_id
 WHERE op.account_id = $1 AND op.status = 'ACTIVE' AND c.decision = 'PENDING' AND c.expires_at > $2
 ORDER BY c.created_at, c.id;

-- name: ListPropertyConsents :many
SELECT * FROM ownership_consents WHERE property_id = $1 ORDER BY created_at DESC, id DESC LIMIT 100;

-- name: ExpireOwnershipConsents :execrows
UPDATE ownership_consents SET decision = 'EXPIRED', decided_at = $1 WHERE decision = 'PENDING' AND expires_at <= $1;

-- name: InsertAuthorityDocument :one
INSERT INTO authority_documents (id, ownership_record_id, property_id, media_id, document_type, media_status,
    uploaded_by_account, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
ON CONFLICT (ownership_record_id, media_id) DO NOTHING
RETURNING *;

-- name: ListAuthorityDocuments :many
SELECT * FROM authority_documents WHERE ownership_record_id = $1 ORDER BY created_at, id;

-- name: SetAuthorityEvidence :exec
UPDATE authority_documents SET verification_evidence_id = $2, updated_at = $3 WHERE id = $1;

-- name: InsertChangeCase :one
INSERT INTO ownership_change_cases (id, property_id, case_type, from_record_id, to_account_id, to_email, status,
    requested_by_account, reason, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 'OPEN', $7, $8, 1, $9, $9)
RETURNING *;

-- name: InsertSaleChangeCase :one
INSERT INTO ownership_change_cases (id, property_id, case_type, from_record_id, to_email, status, requested_by_account, reason, sale_id,
    version, created_at, updated_at)
VALUES ($1, $2, 'SALE', $3, $4, 'OPEN', $5, $6, $7, 1, $8, $8)
RETURNING *;

-- name: GetLiveSaleChangeCase :one
SELECT * FROM ownership_change_cases WHERE sale_id = $1 AND status NOT IN ('REJECTED','CANCELLED');

-- name: GetChangeCase :one
SELECT * FROM ownership_change_cases WHERE id = $1;

-- name: GetOpenChangeCase :one
SELECT * FROM ownership_change_cases WHERE property_id = $1 AND status IN ('OPEN','EVIDENCE_PENDING','UNDER_REVIEW','APPROVED');

-- name: GetLatestChangeCase :one
SELECT * FROM ownership_change_cases WHERE property_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: SetChangeCaseRecipient :exec
UPDATE ownership_change_cases SET to_account_id = $2, updated_at = $3 WHERE id = $1;

-- name: SetChangeCaseOutcome :exec
UPDATE ownership_change_cases SET to_record_id = $2, decision_note = $3, updated_at = $4 WHERE id = $1;

-- name: ListTransfersForEmail :many
SELECT * FROM ownership_change_cases WHERE to_email = $1 AND status = 'OPEN' ORDER BY created_at;

-- 06 Verification links --------------------------------------------------------

-- name: InsertVerificationLink :one
INSERT INTO verification_links (id, verification_id, verification_type, subject_type, subject_id, account_id,
    property_id, ownership_record_id, status, last_synced_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
ON CONFLICT (verification_id) DO UPDATE SET last_synced_at = EXCLUDED.last_synced_at
RETURNING *;

-- name: GetVerificationLink :one
SELECT * FROM verification_links WHERE verification_id = $1;

-- name: GetVerificationLinkForUpdate :one
SELECT * FROM verification_links WHERE verification_id = $1 FOR UPDATE;

-- name: ListVerificationLinks :many
SELECT * FROM verification_links WHERE subject_type = $1 AND subject_id = $2 ORDER BY created_at DESC, id DESC LIMIT 50;

-- name: LatestVerificationLink :one
SELECT * FROM verification_links WHERE subject_type = $1 AND subject_id = $2 AND verification_type = $3
 ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: UpdateVerificationLink :one
UPDATE verification_links SET status = $2, reason_code = $3, decided_at = $4, expires_at = $5, last_event_at = $6, last_synced_at = $7
 WHERE verification_id = $1
RETURNING *;

-- name: StaleOpenVerificationLinks :many
SELECT * FROM verification_links WHERE status IN ('SUBMITTED','PROCESSING','MANUAL_REVIEW') AND last_synced_at < $1
 ORDER BY last_synced_at LIMIT $2;

-- name: IdentityVerified :one
SELECT EXISTS (SELECT 1 FROM verification_links WHERE subject_type = 'USER' AND subject_id = sqlc.arg(account_id)
    AND verification_type = 'IDENTITY' AND status = 'VERIFIED' AND (expires_at IS NULL OR expires_at > sqlc.arg(now)::timestamptz))::bool;

-- name: GetDashboardPreferences :one
SELECT * FROM dashboard_preferences WHERE account_id = $1;

-- name: GetPrimaryOwnerParty :one
SELECT * FROM ownership_parties WHERE ownership_record_id = $1 AND party_role = 'OWNER' AND status = 'ACTIVE' ORDER BY created_at LIMIT 1;

-- name: SetPartyShare :exec
UPDATE ownership_parties SET share_bps = $2, updated_at = $3 WHERE id = $1;

-- name: MovePartiesToRecord :exec
UPDATE ownership_parties SET ownership_record_id = $2, updated_at = $3 WHERE ownership_record_id = $1 AND status IN ('INVITED','ACTIVE');

-- name: WithdrawPendingConsentsForParty :exec
UPDATE ownership_consents SET decision = 'WITHDRAWN', decided_at = $2 WHERE party_id = $1 AND decision = 'PENDING';

-- name: ActivePrincipalParties :many
SELECT * FROM ownership_parties
 WHERE property_id = $1 AND status = 'ACTIVE' AND account_id IS NOT NULL AND party_role IN ('OWNER','CO_OWNER')
 ORDER BY created_at, id;
