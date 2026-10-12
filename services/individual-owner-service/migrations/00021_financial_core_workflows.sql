-- +goose Up
-- +goose StatementBegin
-- Business bindings and readback observations only. Payment Core owns all money.
-- Existing bindings are retained verbatim. Missing historical issuer context
-- remains a review prerequisite instead of assigning it to a later property owner.
ALTER TABLE financial_core_collections ADD COLUMN owner_account_id uuid REFERENCES owner_accounts(account_id);
ALTER TABLE financial_core_collections ADD COLUMN business_context jsonb NOT NULL DEFAULT '{}';
ALTER TABLE financial_core_collections ADD COLUMN quote_snapshot jsonb NOT NULL DEFAULT '{}';
CREATE TABLE financial_core_business_bindings (
 business_type text NOT NULL CHECK(business_type IN ('deposit_obligation','closing_milestone','shortlet_booking','shortlet_booking_extension','maintenance_job','professional_appointment')),
 business_id uuid NOT NULL,
 property_id uuid NOT NULL REFERENCES properties(id),
 owner_account_id uuid NOT NULL REFERENCES owner_accounts(account_id),
 quote_id uuid NOT NULL UNIQUE,
 intent_id uuid NOT NULL UNIQUE,
 amount_minor bigint NOT NULL CHECK(amount_minor>0),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 business_context jsonb NOT NULL,
 quote_snapshot jsonb NOT NULL,
 intent_snapshot jsonb NOT NULL,
 observed_state text NOT NULL,
 settlement_state text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(business_type,business_id)
);
CREATE FUNCTION guard_core_business_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR TG_OP='TRUNCATE' THEN RAISE EXCEPTION 'financial business binding retained'; END IF;
 IF (to_jsonb(OLD)-ARRAY['observed_state','settlement_state','updated_at'])<>(to_jsonb(NEW)-ARRAY['observed_state','settlement_state','updated_at']) THEN RAISE EXCEPTION 'financial business terms immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER core_business_terms BEFORE UPDATE OR DELETE ON financial_core_business_bindings FOR EACH ROW EXECUTE FUNCTION guard_core_business_binding();
CREATE TRIGGER core_business_retained BEFORE TRUNCATE ON financial_core_business_bindings FOR EACH STATEMENT EXECUTE FUNCTION guard_core_business_binding();

CREATE TABLE financial_core_resource_links (
 resource_type text NOT NULL CHECK(resource_type IN ('DEPOSIT','CASE','REFUND')),
 resource_id uuid NOT NULL,
 intent_id uuid NOT NULL,
 snapshot jsonb NOT NULL,
 state text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(resource_type,resource_id)
);
CREATE FUNCTION guard_core_resource_link() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR TG_OP='TRUNCATE' THEN RAISE EXCEPTION 'financial resource link retained'; END IF;
 IF (to_jsonb(OLD)-ARRAY['state','updated_at'])<>(to_jsonb(NEW)-ARRAY['state','updated_at']) THEN RAISE EXCEPTION 'financial resource identity immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER core_resource_identity BEFORE UPDATE OR DELETE ON financial_core_resource_links FOR EACH ROW EXECUTE FUNCTION guard_core_resource_link();
CREATE TRIGGER core_resource_retained BEFORE TRUNCATE ON financial_core_resource_links FOR EACH STATEMENT EXECUTE FUNCTION guard_core_resource_link();

-- External reads run outside a DB transaction. A renewable owner-domain lease
-- prevents old in-flight readbacks from overwriting a later observation.
CREATE TABLE financial_core_readback_leases (
 intent_id uuid PRIMARY KEY,
 claim_id uuid,
 expires_at timestamptz,
 last_attempt_at timestamptz
);
CREATE TABLE financial_core_payout_preferences (
 owner_account_id uuid PRIMARY KEY REFERENCES owner_accounts(account_id),
 payout_account_id uuid NOT NULL,
 updated_at timestamptz NOT NULL,
 version bigint NOT NULL DEFAULT 1
);
CREATE TABLE financial_core_allocation_lines (
 line_id uuid PRIMARY KEY,
 original_intent_id uuid NOT NULL,
 allocation_id uuid NOT NULL,
 award boolean NOT NULL,
 snapshot jsonb NOT NULL
);
CREATE TRIGGER core_line_identity BEFORE UPDATE OR DELETE ON financial_core_allocation_lines FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER core_line_retained BEFORE TRUNCATE ON financial_core_allocation_lines FOR EACH STATEMENT EXECUTE FUNCTION guard_core_resource_link();
CREATE TABLE financial_core_wallet_links (
 wallet_id uuid PRIMARY KEY,
 owner_account_id uuid NOT NULL REFERENCES owner_accounts(account_id),
 legal_entity text NOT NULL,
 currency char(3) NOT NULL
);
CREATE TRIGGER core_wallet_identity BEFORE UPDATE OR DELETE ON financial_core_wallet_links FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER core_wallet_retained BEFORE TRUNCATE ON financial_core_wallet_links FOR EACH STATEMENT EXECUTE FUNCTION guard_core_resource_link();
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM financial_core_business_bindings) OR EXISTS(SELECT 1 FROM financial_core_resource_links) OR EXISTS(SELECT 1 FROM financial_core_wallet_links) OR EXISTS(SELECT 1 FROM financial_core_allocation_lines) OR EXISTS(SELECT 1 FROM financial_core_payout_preferences) OR EXISTS(SELECT 1 FROM financial_core_collections WHERE owner_account_id IS NOT NULL) THEN RAISE EXCEPTION 'rollback prohibited with financial business history'; END IF; END $$;
DROP TABLE financial_core_payout_preferences;
DROP TABLE financial_core_wallet_links;
DROP TABLE financial_core_allocation_lines;
DROP TABLE financial_core_readback_leases;
DROP TABLE financial_core_resource_links;
DROP FUNCTION guard_core_resource_link();
DROP TABLE financial_core_business_bindings;
DROP FUNCTION guard_core_business_binding();
ALTER TABLE financial_core_collections DROP COLUMN quote_snapshot;
ALTER TABLE financial_core_collections DROP COLUMN business_context;
ALTER TABLE financial_core_collections DROP COLUMN owner_account_id;
-- +goose StatementEnd
