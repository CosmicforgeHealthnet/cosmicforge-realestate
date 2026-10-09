-- 17/18 Commercial and land terms ------------------------------------------

-- name: InsertCommercialTerms :one
INSERT INTO commercial_terms (id, property_id, unit_id, version_no, permitted_uses, zoning_class, terms, actor_domain, actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: LatestCommercialTerms :one
SELECT * FROM commercial_terms
 WHERE property_id = $1 AND coalesce(unit_id, '00000000-0000-0000-0000-000000000000'::uuid) = coalesce(sqlc.narg(unit_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
 ORDER BY version_no DESC LIMIT 1;

-- name: ListCommercialTermsVersions :many
SELECT * FROM commercial_terms WHERE property_id = $1 ORDER BY created_at DESC, version_no DESC LIMIT 100;

-- name: InsertLandTerms :one
INSERT INTO land_terms (id, property_id, parcel_id, version_no, permitted_uses, terms, evidence_media_ids, actor_domain, actor_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: LatestLandTerms :one
SELECT * FROM land_terms
 WHERE property_id = $1 AND coalesce(parcel_id, '00000000-0000-0000-0000-000000000000'::uuid) = coalesce(sqlc.narg(parcel_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
 ORDER BY version_no DESC LIMIT 1;

-- name: InsertLOI :one
INSERT INTO commercial_lois (id, property_id, unit_id, listing_id, applicant_ref_id, business_name, business_verification_ref, status, current_round,
    respond_by, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'SUBMITTED', 1, $8, 1, $9, $9)
RETURNING *;

-- name: GetLOI :one
SELECT * FROM commercial_lois WHERE id = $1;

-- name: GetLOIForUpdate :one
SELECT * FROM commercial_lois WHERE id = $1 FOR UPDATE;

-- name: ListLOIsForAccount :many
SELECT l.* FROM commercial_lois l
 WHERE l.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR l.property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR l.status = sqlc.narg(status)::text)
 ORDER BY l.created_at DESC, l.id DESC
 LIMIT 200;

-- name: SetLOIRound :exec
UPDATE commercial_lois SET current_round = $2, respond_by = $3, updated_at = $4 WHERE id = $1;

-- name: SetLOIOutcome :exec
UPDATE commercial_lois SET agreement_id = coalesce(sqlc.narg(agreement_id)::uuid, agreement_id), decline_reason = coalesce(sqlc.narg(decline_reason)::text, decline_reason),
    updated_at = sqlc.arg(updated_at)::timestamptz WHERE id = sqlc.arg(id)::uuid;

-- name: InsertLOIRound :one
INSERT INTO commercial_loi_rounds (id, loi_id, round_no, proposed_by, terms, note, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListLOIRounds :many
SELECT * FROM commercial_loi_rounds WHERE loi_id = $1 ORDER BY round_no;

-- name: LatestLOIRound :one
SELECT * FROM commercial_loi_rounds WHERE loi_id = $1 ORDER BY round_no DESC LIMIT 1;

-- name: DueLOIExpiries :many
SELECT id FROM commercial_lois WHERE status IN ('SUBMITTED','COUNTERED') AND respond_by <= $1 ORDER BY respond_by LIMIT $2;

-- 19 Sale transactions -------------------------------------------------------

-- name: InsertSale :one
INSERT INTO sale_transactions (id, property_id, unit_id, listing_id, sale_kind, status, currency, asking_price_minor, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'INQUIRY', $6, $7, 1, $8, $8)
RETURNING *;

-- name: GetSale :one
SELECT * FROM sale_transactions WHERE id = $1;

-- name: GetSaleForUpdate :one
SELECT * FROM sale_transactions WHERE id = $1 FOR UPDATE;

-- name: LiveSaleForSpace :one
SELECT * FROM sale_transactions
 WHERE property_id = $1 AND coalesce(unit_id, '00000000-0000-0000-0000-000000000000'::uuid) = coalesce(sqlc.narg(unit_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
   AND status NOT IN ('COMPLETED','CANCELLED','FAILED')
 FOR UPDATE;

-- name: ListSalesForAccount :many
SELECT t.* FROM sale_transactions t
 WHERE t.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND (pa.valid_until IS NULL OR pa.valid_until > sqlc.arg(now)::timestamptz))
   AND (sqlc.narg(property_id)::uuid IS NULL OR t.property_id = sqlc.narg(property_id)::uuid)
   AND (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (t.created_at, t.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY t.created_at DESC, t.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: SetSaleAccepted :exec
UPDATE sale_transactions SET accepted_offer_id = $2, buyer_ref_id = $3, agreed_price_minor = $4, target_closing_date = $5, updated_at = $6 WHERE id = $1;

-- name: SetSaleListing :exec
UPDATE sale_transactions SET listing_id = $2, updated_at = $3 WHERE id = $1;

-- name: SetSaleDisputeOrigin :exec
UPDATE sale_transactions SET status_before_dispute = $2, updated_at = $3 WHERE id = $1;

-- name: SetSaleEnded :exec
UPDATE sale_transactions SET end_reason = $2, completed_at = $3, updated_at = $4 WHERE id = $1;

-- name: InsertSaleOffer :one
INSERT INTO sale_offers (id, sale_id, buyer_ref_id, buyer_display_name, parent_offer_id, made_by, amount_minor, currency, financing, conditions,
    proof_of_funds_ref, status, expires_at, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'SUBMITTED', $12, 1, $13, $13)
RETURNING *;

-- name: GetSaleOffer :one
SELECT * FROM sale_offers WHERE id = $1;

-- name: GetSaleOfferForUpdate :one
SELECT * FROM sale_offers WHERE id = $1 FOR UPDATE;

-- name: ListSaleOffers :many
SELECT * FROM sale_offers WHERE sale_id = $1 ORDER BY created_at, id;

-- name: SetSaleOfferStatus :one
UPDATE sale_offers SET status = $3, decided_at = $4, decline_reason = coalesce($5, decline_reason), version = version + 1, updated_at = $4
 WHERE id = $1 AND status = $2
RETURNING *;

-- name: SupersedeOpenOffers :exec
UPDATE sale_offers SET status = 'SUPERSEDED', decided_at = $3, version = version + 1, updated_at = $3
 WHERE sale_id = $1 AND id <> $2 AND status IN ('SUBMITTED','COUNTERED');

-- name: DueSaleOfferExpiries :many
SELECT id FROM sale_offers WHERE status = 'SUBMITTED' AND expires_at <= $1 ORDER BY expires_at LIMIT $2;

-- name: OpenBuyerOffer :one
SELECT count(*) FROM sale_offers WHERE sale_id = $1 AND buyer_ref_id = $2 AND status IN ('SUBMITTED','COUNTERED');

-- name: InsertDataRoom :one
INSERT INTO data_rooms (id, sale_id, property_id, status, created_at)
VALUES ($1, $2, $3, 'OPEN', $4)
ON CONFLICT (sale_id) DO NOTHING
RETURNING *;

-- name: GetDataRoomBySale :one
SELECT * FROM data_rooms WHERE sale_id = $1;

-- name: GetDataRoom :one
SELECT * FROM data_rooms WHERE id = $1;

-- name: CloseDataRoom :exec
UPDATE data_rooms SET status = 'CLOSED', closed_at = $2 WHERE id = $1 AND status = 'OPEN';

-- name: InsertDataRoomDocument :one
INSERT INTO data_room_documents (id, data_room_id, media_id, category, title, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (data_room_id, media_id) DO NOTHING
RETURNING *;

-- name: ListDataRoomDocuments :many
SELECT * FROM data_room_documents WHERE data_room_id = $1 AND removed_at IS NULL ORDER BY created_at, id;

-- name: GetDataRoomDocument :one
SELECT * FROM data_room_documents WHERE id = $1 AND data_room_id = $2 AND removed_at IS NULL;

-- name: RemoveDataRoomDocument :execrows
UPDATE data_room_documents SET removed_at = $3 WHERE id = $1 AND data_room_id = $2 AND removed_at IS NULL;

-- name: InsertDataRoomGrant :one
INSERT INTO data_room_grants (id, data_room_id, grantee_domain, grantee_ref_id, grantee_label, purpose, categories, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: ListDataRoomGrants :many
SELECT * FROM data_room_grants WHERE data_room_id = $1 ORDER BY created_at, id;

-- name: RevokeDataRoomGrant :execrows
UPDATE data_room_grants SET revoked_at = $3 WHERE id = $1 AND data_room_id = $2 AND revoked_at IS NULL;

-- name: RevokeAllDataRoomGrants :exec
UPDATE data_room_grants SET revoked_at = $2 WHERE data_room_id = $1 AND revoked_at IS NULL;

-- name: ActiveDataRoomGrant :one
SELECT * FROM data_room_grants
 WHERE data_room_id = $1 AND grantee_domain = $2 AND grantee_ref_id = $3 AND revoked_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz
 ORDER BY expires_at DESC LIMIT 1
 FOR UPDATE;

-- name: TouchDataRoomGrant :exec
UPDATE data_room_grants SET access_count = access_count + 1, last_accessed_at = $2 WHERE id = $1;

-- name: InsertDataRoomAccess :exec
INSERT INTO data_room_access_log (id, grant_id, document_id, accessed_at) VALUES ($1, $2, $3, $4);

-- name: InsertDueDiligenceItem :one
INSERT INTO due_diligence_items (id, sale_id, category, title, raised_by, status, blocking, finding, evidence_media_ids, professional_appointment_ref,
    version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'OPEN', $6, $7, $8, $9, 1, $10, $10)
RETURNING *;

-- name: ListDueDiligenceItems :many
SELECT * FROM due_diligence_items WHERE sale_id = $1 ORDER BY created_at, id;

-- name: GetDueDiligenceItem :one
SELECT * FROM due_diligence_items WHERE id = $1 AND sale_id = $2;

-- name: UpdateDueDiligenceItem :one
UPDATE due_diligence_items SET status = $3, blocking = $4, finding = $5, evidence_media_ids = $6, resolution_note = $7, version = version + 1, updated_at = $8
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: OpenBlockingDiligence :one
SELECT count(*) FROM due_diligence_items WHERE sale_id = $1 AND blocking AND status IN ('OPEN','IN_PROGRESS','ISSUE_FOUND');

-- name: InsertMilestone :one
INSERT INTO closing_milestones (id, sale_id, code, title, kind, required_for_completion, sequence, settlement, amount_minor, status, due_date,
    version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'PENDING', $10, 1, $11, $11)
RETURNING *;

-- name: ListMilestones :many
SELECT * FROM closing_milestones WHERE sale_id = $1 ORDER BY sequence, created_at;

-- name: GetMilestoneForUpdate :one
SELECT * FROM closing_milestones WHERE sale_id = $1 AND code = $2 FOR UPDATE;

-- name: GetMilestoneByID :one
SELECT * FROM closing_milestones WHERE id = $1;

-- name: UpdateMilestone :one
UPDATE closing_milestones SET status = $2, evidence_media_ids = $3, attested_by = $4, buyer_confirmed_at = $5, satisfied_at = $6, waived_reason = $7,
    paid_minor = $8, version = version + 1, updated_at = $9
 WHERE id = $1
RETURNING *;

-- name: SetMilestoneIntent :exec
UPDATE closing_milestones SET payment_intent_ref = $2, last_payment_status = $3, last_checked_at = $4, updated_at = $4 WHERE id = $1;

-- name: SetMilestonePayment :exec
UPDATE closing_milestones SET paid_minor = $2, last_payment_status = $3, last_checked_at = $4 WHERE id = $1;

-- name: MilestonesNeedingIntent :many
SELECT m.id FROM closing_milestones m JOIN sale_transactions t ON t.id = m.sale_id
 WHERE m.kind = 'PAYMENT' AND m.settlement = 'PLATFORM' AND m.status = 'PENDING' AND m.payment_intent_ref IS NULL
   AND t.status IN ('OFFER_ACCEPTED','DUE_DILIGENCE','CONTRACTING','CONDITIONS_PENDING','CLOSING')
 ORDER BY m.created_at LIMIT $1;

-- name: MilestonesToReconcile :many
SELECT id FROM closing_milestones
 WHERE payment_intent_ref IS NOT NULL AND status = 'PENDING'
   AND (last_checked_at IS NULL OR last_checked_at < sqlc.arg(checked_before)::timestamptz)
 ORDER BY last_checked_at NULLS FIRST LIMIT sqlc.arg(max_rows);

-- name: MilestonesToCancel :many
SELECT id FROM closing_milestones
 WHERE status = 'FAILED' AND payment_intent_ref IS NOT NULL
   AND coalesce(last_payment_status, '') NOT IN ('CANCELLED','FAILED','SUCCEEDED','REFUNDED','REFUND_PENDING')
 ORDER BY updated_at LIMIT $1;

-- name: GetMilestoneByIntent :one
SELECT * FROM closing_milestones WHERE payment_intent_ref = $1;

-- name: FailOpenMilestones :exec
UPDATE closing_milestones SET status = 'FAILED', version = version + 1, updated_at = $2
 WHERE sale_id = $1 AND status IN ('PENDING','AWAITING_CONFIRMATION');

-- name: UnmetRequiredMilestones :many
SELECT code FROM closing_milestones WHERE sale_id = $1 AND required_for_completion AND status NOT IN ('SATISFIED','WAIVED') ORDER BY sequence;

-- name: BuyerSales :many
SELECT t.* FROM sale_transactions t WHERE t.buyer_ref_id = $1 OR t.id IN (SELECT o.sale_id FROM sale_offers o WHERE o.buyer_ref_id = $1)
 ORDER BY t.created_at DESC LIMIT 100;
