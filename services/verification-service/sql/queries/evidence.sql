-- name: AddEvidence :one
INSERT INTO verification_evidence_refs (evidence_id, verification_id, media_id, evidence_type, media_checksum_sha256, attached_by_service, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (verification_id, media_id) DO NOTHING
RETURNING *;

-- name: GetEvidenceByMedia :one
SELECT * FROM verification_evidence_refs WHERE verification_id = $1 AND media_id = $2;

-- name: ListEvidence :many
SELECT * FROM verification_evidence_refs WHERE verification_id = $1 ORDER BY created_at, evidence_id;
