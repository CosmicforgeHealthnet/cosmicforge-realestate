-- +goose Up
CREATE TABLE collection_receipts (
 receipt_id uuid PRIMARY KEY, provider_account_id uuid NOT NULL REFERENCES provider_accounts,
 external_ref text NOT NULL, wallet_id uuid NOT NULL REFERENCES wallets,
 journal_id uuid NOT NULL REFERENCES journals, amount_minor bigint NOT NULL CHECK(amount_minor>0),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'), evidence jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(provider_account_id,external_ref)
);
CREATE TRIGGER receipt_immutable BEFORE UPDATE OR DELETE ON collection_receipts FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER receipts_no_truncate BEFORE TRUNCATE ON collection_receipts FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history();
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM collection_receipts) THEN RAISE EXCEPTION 'rollback prohibited with collected funds'; END IF; END $$;
DROP TABLE collection_receipts;
-- +goose StatementEnd
