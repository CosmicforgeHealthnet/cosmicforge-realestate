-- name: InsertDecision :one
INSERT INTO verification_decisions (decision_id, verification_id, decision, decision_source, reason_code, notes, provider,
    provider_reference, decided_by_service, decided_by_actor_domain, decided_by_actor_id, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetDecision :one
SELECT * FROM verification_decisions WHERE verification_id = $1;

-- name: InsertHistory :exec
INSERT INTO verification_history (history_id, verification_id, from_status, to_status, substatus, decision_source, reason_code,
    calling_service, actor_domain, actor_id, provider, provider_reference, request_id, trace_id, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: ListHistory :many
SELECT * FROM verification_history WHERE verification_id = $1 ORDER BY occurred_at, history_id;
