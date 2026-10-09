-- 21 Manager delegation -------------------------------------------------------

-- name: InsertManagerProposal :one
INSERT INTO manager_proposals (id, property_id, manager_domain, manager_ref_id, manager_display_name, role_type, capabilities,
    fee_type, fee_bps, fee_amount_minor, fee_currency, proposed_by, status, message, expires_at, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'OWNER', 'SUBMITTED', $12, $13, 1, $14, $14)
RETURNING *;

-- name: GetManagerProposal :one
SELECT * FROM manager_proposals WHERE id = $1;

-- name: GetManagerProposalForUpdate :one
SELECT * FROM manager_proposals WHERE id = $1 FOR UPDATE;

-- name: ListManagerProposals :many
SELECT * FROM manager_proposals WHERE property_id = $1 ORDER BY created_at DESC, id DESC LIMIT 100;

-- name: SetManagerProposalStatus :execrows
UPDATE manager_proposals SET status = $3, version = version + 1, updated_at = $4 WHERE id = $1 AND status = $2;

-- name: ExpireManagerProposals :execrows
UPDATE manager_proposals SET status = 'EXPIRED', version = version + 1, updated_at = $1 WHERE status = 'SUBMITTED' AND expires_at <= $1;

-- name: InsertManagerAssignment :one
INSERT INTO manager_assignments (id, property_id, proposal_id, manager_domain, manager_ref_id, manager_display_name, role_type,
    status, starts_at, ends_at, current_permission_version, report_schedule, created_by_account, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'PROPOSED', $8, $9, 1, $10, $11, 1, $12, $12)
RETURNING *;

-- name: GetManagerAssignment :one
SELECT * FROM manager_assignments WHERE id = $1;

-- name: GetManagerAssignmentForUpdate :one
SELECT * FROM manager_assignments WHERE id = $1 FOR UPDATE;

-- name: ListAssignmentsForProperty :many
SELECT * FROM manager_assignments WHERE property_id = $1 ORDER BY created_at DESC, id DESC LIMIT 100;

-- name: ListAssignmentsForAccount :many
SELECT a.* FROM manager_assignments a
 WHERE a.property_id IN (SELECT pa.property_id FROM property_access pa WHERE pa.account_id = sqlc.arg(account_id)
                            AND pa.role IN ('OWNER','CO_OWNER','AUTHORIZED_REPRESENTATIVE','ESTATE_EXECUTOR'))
   AND (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status)::text)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (a.created_at, a.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY a.created_at DESC, a.id DESC
 LIMIT sqlc.arg(max_rows);

-- name: LiveAssignmentsForProperty :many
SELECT * FROM manager_assignments WHERE property_id = $1 AND status IN ('PROPOSED','ACCEPTED','ACTIVE','SUSPENDED','EXPIRING');

-- name: UpdateAssignmentTerms :one
UPDATE manager_assignments SET ends_at = $3, report_schedule = $4, current_permission_version = $5, version = version + 1, updated_at = $6
 WHERE id = $1 AND version = $2
RETURNING *;

-- name: SetAssignmentRevocation :exec
UPDATE manager_assignments SET revoked_at = $2, revoke_reason = $3 WHERE id = $1;

-- name: InsertManagerPermission :one
INSERT INTO manager_permissions (id, assignment_id, version_no, capabilities, spend_limit_minor, emergency_limit_minor, currency,
    emergency_rule, created_by_account, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetManagerPermission :one
SELECT * FROM manager_permissions WHERE assignment_id = $1 AND version_no = $2;

-- name: ListManagerPermissions :many
SELECT * FROM manager_permissions WHERE assignment_id = $1 ORDER BY version_no DESC;

-- name: DueAssignmentActivations :many
SELECT id FROM manager_assignments WHERE status = 'ACCEPTED' AND starts_at <= $1 ORDER BY starts_at LIMIT $2;

-- name: DueAssignmentExpiring :many
SELECT id FROM manager_assignments WHERE status = 'ACTIVE' AND ends_at <= $1 ORDER BY ends_at LIMIT $2;

-- name: DueAssignmentExpired :many
SELECT id FROM manager_assignments WHERE status IN ('ACTIVE','EXPIRING','SUSPENDED','ACCEPTED') AND ends_at <= $1 ORDER BY ends_at LIMIT $2;

-- name: InsertManagerActivity :one
INSERT INTO manager_activity_refs (id, assignment_id, property_id, activity_type, ref_service, ref_type, ref_id, summary,
    actor_domain, actor_id, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: ListManagerActivity :many
SELECT * FROM manager_activity_refs
 WHERE assignment_id = sqlc.arg(assignment_id)
   AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (occurred_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY occurred_at DESC, id DESC
 LIMIT sqlc.arg(max_rows);
