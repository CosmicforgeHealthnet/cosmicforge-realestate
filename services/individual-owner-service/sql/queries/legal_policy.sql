-- Versioned jurisdiction legal policy ------------------------------------------

-- name: InsertJurisdictionPolicy :one
INSERT INTO jurisdiction_policies (id, country, region, version_no, status, rules, summary, created_by_actor_id, created_at)
VALUES (sqlc.arg(id), sqlc.arg(country), sqlc.narg(region),
    (SELECT coalesce(max(p.version_no), 0) + 1 FROM jurisdiction_policies p
      WHERE p.country = sqlc.arg(country) AND coalesce(p.region, '') = coalesce(sqlc.narg(region)::text, '')),
    'DRAFT', sqlc.arg(rules), sqlc.arg(summary), sqlc.arg(created_by_actor_id), sqlc.arg(created_at))
RETURNING *;

-- name: GetJurisdictionPolicyForUpdate :one
SELECT * FROM jurisdiction_policies WHERE id = $1 FOR UPDATE;

-- name: ListJurisdictionPolicies :many
SELECT * FROM jurisdiction_policies
 WHERE (sqlc.narg(country)::text IS NULL OR country = sqlc.narg(country)::text)
 ORDER BY country, coalesce(region, ''), version_no DESC LIMIT 200;

-- name: ApprovedPolicyForUpdate :one
SELECT * FROM jurisdiction_policies
 WHERE country = sqlc.arg(country) AND coalesce(region, '') = coalesce(sqlc.narg(region)::text, '') AND status = 'APPROVED'
 FOR UPDATE;

-- name: ApproveJurisdictionPolicy :one
UPDATE jurisdiction_policies SET status = 'APPROVED', legal_approval_ref = $2, approved_by_actor_id = $3, approved_at = $4
 WHERE id = $1 AND status = 'DRAFT'
RETURNING *;

-- name: RetireJurisdictionPolicy :one
UPDATE jurisdiction_policies SET status = 'RETIRED', retired_at = $2 WHERE id = $1 AND status = 'APPROVED'
RETURNING *;

-- name: EffectiveJurisdictionPolicy :one
-- The region-specific approved version wins over the country-wide one.
SELECT * FROM jurisdiction_policies
 WHERE country = sqlc.arg(country) AND status = 'APPROVED' AND (region IS NULL OR region = upper(sqlc.arg(region)::text))
 ORDER BY region NULLS LAST LIMIT 1;
