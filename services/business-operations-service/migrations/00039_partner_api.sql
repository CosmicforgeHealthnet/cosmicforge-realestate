-- +goose Up
-- Annex D.36 partner API runtime. Additive only. Partner requests authenticate
-- by the SHA-256 fingerprint of a presented key that is held in the partner's
-- secret manager (Business never stores the raw key). Per-credential request
-- windows enforce the integration client's rate limit across every replica.

CREATE UNIQUE INDEX api_credentials_fingerprint ON api_credentials ((attributes->>'fingerprint_sha256')) WHERE state IN ('ACTIVE','ROTATING');
CREATE INDEX api_credentials_next_fingerprint ON api_credentials ((attributes->>'next_fingerprint_sha256')) WHERE state='ROTATING';

CREATE TABLE partner_api_rate_windows (
 organisation_id uuid NOT NULL,
 credential_id uuid NOT NULL,
 window_start timestamptz NOT NULL,
 request_count integer NOT NULL CHECK(request_count>0),
 PRIMARY KEY(credential_id,window_start),
 FOREIGN KEY(organisation_id,credential_id) REFERENCES api_credentials(organisation_id,id) ON DELETE CASCADE
);
CREATE INDEX partner_api_rate_windows_age ON partner_api_rate_windows(window_start);

-- +goose Down
SELECT 1;
