-- Owner owns registration delivery; payment-service owns wallet/ledger data.
-- +goose Up
CREATE TABLE wallet_registration_delivery (
 account_id uuid PRIMARY KEY REFERENCES owner_accounts(account_id),
 registration_ref text NOT NULL UNIQUE, attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(), lease_token uuid,
 lease_until timestamptz, delivered_at timestamptz, last_error_code text,
 created_at timestamptz NOT NULL DEFAULT now()
);
-- Existing accounts receive the same controlled registration adapter.
INSERT INTO wallet_registration_delivery(account_id,registration_ref)
 SELECT account_id,'owner-registration:'||account_id::text FROM owner_accounts WHERE status<>'CLOSED';
CREATE INDEX wallet_registration_due ON wallet_registration_delivery(next_attempt_at) WHERE delivered_at IS NULL;
-- +goose Down
DROP TABLE wallet_registration_delivery;
