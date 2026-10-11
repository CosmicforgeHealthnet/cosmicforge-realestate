-- +goose Up
-- +goose StatementBegin
-- Source-domain projection only: no authoritative balances or paid-rent inference.
CREATE TABLE financial_core_collections (
 obligation_id uuid PRIMARY KEY REFERENCES rent_obligations(id),
 quote_id uuid NOT NULL UNIQUE,
 intent_id uuid NOT NULL UNIQUE,
 amount_minor bigint NOT NULL CHECK(amount_minor>0),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 snapshot jsonb NOT NULL,
 observed_state text NOT NULL,
 settlement_state text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION guard_financial_core_collection() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR TG_OP='TRUNCATE' THEN RAISE EXCEPTION 'financial observation retained'; END IF;
 IF (to_jsonb(OLD)-ARRAY['observed_state','settlement_state','updated_at'])<>(to_jsonb(NEW)-ARRAY['observed_state','settlement_state','updated_at']) THEN RAISE EXCEPTION 'accepted financial binding immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER financial_core_collection_terms BEFORE UPDATE OR DELETE ON financial_core_collections FOR EACH ROW EXECUTE FUNCTION guard_financial_core_collection();
CREATE TRIGGER financial_core_collection_retained BEFORE TRUNCATE ON financial_core_collections EXECUTE FUNCTION guard_financial_core_collection();
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM financial_core_collections) THEN RAISE EXCEPTION 'rollback prohibited with financial observations'; END IF; END $$;
DROP TABLE financial_core_collections;
DROP FUNCTION guard_financial_core_collection();
-- +goose StatementEnd
