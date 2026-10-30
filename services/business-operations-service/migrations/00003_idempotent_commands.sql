-- +goose Up
CREATE TABLE idempotency_records (
 id uuid PRIMARY KEY, scope text NOT NULL, caller_type text NOT NULL, caller_id text NOT NULL,
 idempotency_key text NOT NULL, request_hash text NOT NULL,
 status text NOT NULL CHECK(status IN ('IN_PROGRESS','COMPLETED','FAILED_RETRYABLE','FAILED_FINAL')),
 response_code integer, response_body bytea, resource_type text, resource_id text,
 attempts integer NOT NULL DEFAULT 1 CHECK(attempts>0), locked_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),expires_at timestamptz NOT NULL,
 CONSTRAINT idempotency_records_key_uniq UNIQUE(scope,caller_type,caller_id,idempotency_key),
 CONSTRAINT idempotency_records_key_len CHECK(char_length(idempotency_key) BETWEEN 1 AND 255)
);
CREATE INDEX idempotency_records_expires_idx ON idempotency_records(expires_at);
-- +goose Down
SELECT 1;
