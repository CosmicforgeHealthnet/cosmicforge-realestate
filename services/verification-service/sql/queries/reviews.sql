-- name: CreateManualReview :one
INSERT INTO manual_reviews (review_id, verification_id, status, reason, created_at, updated_at)
VALUES ($1, $2, 'OPEN', $3, $4, $4)
RETURNING *;

-- name: GetActiveReviewForUpdate :one
SELECT * FROM manual_reviews WHERE verification_id = $1 AND status <> 'DECIDED' FOR UPDATE;

-- name: AssignReview :one
UPDATE manual_reviews SET status = 'ASSIGNED', assigned_admin_id = $2, assigned_at = $3, updated_at = $3
 WHERE review_id = $1 RETURNING *;

-- name: DecideReview :exec
UPDATE manual_reviews SET status = 'DECIDED', decided_at = $2, updated_at = $2,
       assigned_admin_id = coalesce(assigned_admin_id, $3), assigned_at = coalesce(assigned_at, $2)
 WHERE review_id = $1;

-- name: ListReviewQueue :many
SELECT r.review_id, r.verification_id, r.status, r.reason, r.assigned_admin_id, r.assigned_at, r.created_at,
       c.verification_type, c.subject_type, c.subject_domain, c.subject_id
  FROM manual_reviews r JOIN verification_cases c ON c.verification_id = r.verification_id
 WHERE r.status = ANY(sqlc.arg(statuses)::text[])
   AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
        OR (r.created_at, r.review_id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
 ORDER BY r.created_at, r.review_id
 LIMIT sqlc.arg(row_limit);
