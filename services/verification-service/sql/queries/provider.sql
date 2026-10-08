-- name: InsertProviderRequest :exec
INSERT INTO provider_requests (provider_request_id, attempt_id, verification_id, provider, operation, outcome, error_code, duration_ms, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: InsertProviderResult :one
INSERT INTO provider_results (provider_result_id, verification_id, attempt_id, provider, provider_event_id, outcome, reason_code, received_via, payload_sha256, received_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (provider, provider_event_id) DO NOTHING
RETURNING *;

-- name: MarkProviderResultApplied :exec
UPDATE provider_results SET applied = true WHERE provider_result_id = $1;

-- name: CountProviderResults :one
SELECT count(*) FROM provider_results WHERE verification_id = $1;
