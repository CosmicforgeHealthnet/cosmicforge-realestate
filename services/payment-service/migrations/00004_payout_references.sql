-- Verified account references are not entitlements or executable withdrawals.
-- +goose Up
CREATE TABLE payout_account_refs (
 payout_account_id uuid PRIMARY KEY, wallet_id uuid NOT NULL REFERENCES wallets,
 beneficiary_ref text NOT NULL, verification_ref text NOT NULL,
 jurisdiction text NOT NULL, expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(wallet_id,beneficiary_ref)
);
CREATE TRIGGER payout_refs_immutable BEFORE UPDATE OR DELETE ON payout_account_refs
 FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER payout_refs_no_truncate BEFORE TRUNCATE ON payout_account_refs FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM payout_account_refs) THEN
 RAISE EXCEPTION 'verified financial references require retention'; END IF; END $$;
-- +goose StatementEnd
DROP TABLE payout_account_refs;
