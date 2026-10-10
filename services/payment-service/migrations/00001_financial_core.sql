-- payment_db is owned exclusively by payment-service. References to domain
-- accounts/evidence are opaque IDs; no cross-service database access.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE financial_identities (
 identity_id uuid PRIMARY KEY, legal_party_ref text NOT NULL UNIQUE,
 party_kind text NOT NULL CHECK(party_kind IN ('PERSON','ORGANIZATION')),
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RESTRICTED','CLOSED')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE identity_bindings (
 domain text NOT NULL, account_id uuid NOT NULL, identity_id uuid NOT NULL REFERENCES financial_identities,
 registration_ref text NOT NULL, link_evidence_ref text,
 PRIMARY KEY(domain,account_id), UNIQUE(domain,identity_id)
);
CREATE TABLE wallets (
 wallet_id uuid PRIMARY KEY, identity_id uuid NOT NULL REFERENCES financial_identities,
 domain text NOT NULL, role text NOT NULL,
 legal_entity text NOT NULL, currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(identity_id,domain,role,legal_entity,currency)
);
CREATE TABLE provider_accounts (
 provider_account_id uuid PRIMARY KEY, provider text NOT NULL,
 legal_entity text NOT NULL, merchant_ref text NOT NULL, mode text NOT NULL CHECK(mode IN ('test','live')),
 config jsonb NOT NULL CHECK(jsonb_typeof(config)='object'), config_version text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(provider,legal_entity,merchant_ref,mode)
);
CREATE TABLE custody_pools (
 pool_id uuid PRIMARY KEY, provider_account_id uuid NOT NULL REFERENCES provider_accounts,
 legal_entity text NOT NULL, currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 label text NOT NULL, custody_approved boolean NOT NULL DEFAULT false,
 restricted boolean NOT NULL DEFAULT true, reserve_minor bigint NOT NULL DEFAULT 0 CHECK(reserve_minor>=0),
 UNIQUE(provider_account_id,currency,label)
);
CREATE TABLE ledger_accounts (
 account_id uuid PRIMARY KEY, wallet_id uuid REFERENCES wallets, pool_id uuid NOT NULL REFERENCES custody_pools,
 legal_entity text NOT NULL, currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 kind text NOT NULL CHECK(kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','PROVIDER_RECEIVABLE','SETTLED_CASH','REVENUE','PROCESSING_EXPENSE','RESERVE','SETTLEMENT_CONTROL','TREASURY')),
 normal_side text NOT NULL CHECK(normal_side IN ('DEBIT','CREDIT')),
 dimension_ref text NOT NULL DEFAULT '',
 UNIQUE NULLS NOT DISTINCT(wallet_id,pool_id,kind,dimension_ref)
);
CREATE TABLE journals (
 journal_id uuid PRIMARY KEY, operation_ref text NOT NULL UNIQUE, fingerprint text NOT NULL,
 legal_entity text NOT NULL, currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'), pool_id uuid NOT NULL REFERENCES custody_pools,
 origin_service text NOT NULL, policy_version text NOT NULL, authorization_snapshot jsonb NOT NULL CHECK(jsonb_typeof(authorization_snapshot)='object'),
 reverses_id uuid UNIQUE REFERENCES journals, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE journal_entries (
 entry_id uuid PRIMARY KEY, journal_id uuid NOT NULL REFERENCES journals,
 account_id uuid NOT NULL REFERENCES ledger_accounts, side text NOT NULL CHECK(side IN ('DEBIT','CREDIT')),
 amount_minor bigint NOT NULL CHECK(amount_minor>0), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX journal_entries_account_idx ON journal_entries(account_id,created_at,entry_id);
CREATE INDEX journals_history_idx ON journals(created_at,journal_id);
CREATE VIEW account_balances AS SELECT a.*, COALESCE(SUM(CASE WHEN e.side=a.normal_side THEN e.amount_minor::numeric ELSE -e.amount_minor::numeric END),0) AS balance_minor
 FROM ledger_accounts a LEFT JOIN journal_entries e USING(account_id) GROUP BY a.account_id;
CREATE FUNCTION immutable_financial_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'financial history is immutable' USING ERRCODE='23514'; END $$;
CREATE TRIGGER immutable_journals BEFORE UPDATE OR DELETE ON journals FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER immutable_entries BEFORE UPDATE OR DELETE ON journal_entries FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER immutable_accounts BEFORE UPDATE OR DELETE ON ledger_accounts FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER immutable_bindings BEFORE UPDATE OR DELETE ON identity_bindings FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER immutable_wallets BEFORE UPDATE OR DELETE ON wallets FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE FUNCTION validate_journal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE j journals; n bigint; net numeric;
BEGIN
 SELECT * INTO j FROM journals WHERE journal_id=NEW.journal_id;
 SELECT count(*),COALESCE(sum(CASE WHEN side='DEBIT' THEN amount_minor::numeric ELSE -amount_minor::numeric END),0) INTO n,net FROM journal_entries WHERE journal_id=j.journal_id;
 IF n<2 OR net<>0 THEN RAISE EXCEPTION 'unbalanced journal' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE e.journal_id=j.journal_id AND (a.currency<>j.currency OR a.legal_entity<>j.legal_entity OR a.pool_id<>j.pool_id)) THEN RAISE EXCEPTION 'journal dimension mismatch' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM account_balances WHERE pool_id=j.pool_id AND kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','SETTLED_CASH','PROVIDER_RECEIVABLE') AND balance_minor<0) THEN RAISE EXCEPTION 'negative controlled financial balance' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER journal_balanced AFTER INSERT ON journals DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_journal();
CREATE FUNCTION guard_entry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Entries may only be appended in the journal's creating transaction.
 IF NOT EXISTS(SELECT 1 FROM journals WHERE journal_id=NEW.journal_id AND xmin::text=pg_current_xact_id()::text) THEN RAISE EXCEPTION 'posted journal is sealed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER entries_sealed BEFORE INSERT ON journal_entries FOR EACH ROW EXECUTE FUNCTION guard_entry();
CREATE FUNCTION validate_account_dimensions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.kind IN ('PROVIDER_RECEIVABLE','SETTLED_CASH','PROCESSING_EXPENSE','REVENUE','RESERVE','SETTLEMENT_CONTROL','TREASURY') AND NEW.wallet_id IS NOT NULL THEN RAISE EXCEPTION 'platform accounting is not a public wallet' USING ERRCODE='23514'; END IF;
 IF NEW.kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT') AND NEW.wallet_id IS NULL THEN RAISE EXCEPTION 'public liability requires wallet identity' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM custody_pools p WHERE p.pool_id=NEW.pool_id AND p.currency=NEW.currency AND p.legal_entity=NEW.legal_entity) OR
 (NEW.wallet_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM wallets w WHERE w.wallet_id=NEW.wallet_id AND w.currency=NEW.currency AND w.legal_entity=NEW.legal_entity)) THEN RAISE EXCEPTION 'account dimension mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.kind IN ('PROVIDER_RECEIVABLE','SETTLED_CASH','PROCESSING_EXPENSE') AND NEW.normal_side<>'DEBIT' THEN RAISE EXCEPTION 'invalid normal side' USING ERRCODE='23514'; END IF;
 IF NEW.kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','REVENUE','RESERVE','SETTLEMENT_CONTROL') AND NEW.normal_side<>'CREDIT' THEN RAISE EXCEPTION 'invalid normal side' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER account_dimensions BEFORE INSERT ON ledger_accounts FOR EACH ROW EXECUTE FUNCTION validate_account_dimensions();
CREATE TABLE payment_intents (
 intent_id uuid PRIMARY KEY, source_service text NOT NULL, wallet_id uuid NOT NULL REFERENCES wallets,
 operation_key text NOT NULL, fingerprint text NOT NULL,
 amount_minor bigint NOT NULL CHECK(amount_minor>0), currency text NOT NULL, legal_entity text NOT NULL,
 category text NOT NULL, country text NOT NULL, evidence_ref text NOT NULL, policy_version text NOT NULL,
 provider_account_id uuid NOT NULL REFERENCES provider_accounts, pool_id uuid NOT NULL REFERENCES custody_pools,
 route_snapshot jsonb NOT NULL, provider_reference text NOT NULL UNIQUE,
 state text NOT NULL CHECK(state IN ('REQUESTED','SUBMITTING','INITIALIZED','PENDING','PROCESSING','VERIFIED_SUCCESS','VERIFIED_FAILURE','UNKNOWN_OUTCOME','RECONCILIATION_EXCEPTION')),
 settlement_state text NOT NULL DEFAULT 'PENDING' CHECK(settlement_state IN ('PENDING','SETTLED','EXCEPTION')),
 checkout_url text, credited_journal_id uuid REFERENCES journals,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(source_service,operation_key)
);
CREATE FUNCTION immutable_route() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.provider_account_id,NEW.pool_id,NEW.route_snapshot,NEW.provider_reference,NEW.amount_minor,NEW.currency,NEW.legal_entity,NEW.wallet_id,NEW.source_service,NEW.fingerprint,NEW.evidence_ref,NEW.policy_version,NEW.category,NEW.country,NEW.operation_key) IS DISTINCT FROM
 (OLD.provider_account_id,OLD.pool_id,OLD.route_snapshot,OLD.provider_reference,OLD.amount_minor,OLD.currency,OLD.legal_entity,OLD.wallet_id,OLD.source_service,OLD.fingerprint,OLD.evidence_ref,OLD.policy_version,OLD.category,OLD.country,OLD.operation_key) THEN RAISE EXCEPTION 'intent route and terms immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER intent_route BEFORE UPDATE ON payment_intents FOR EACH ROW EXECUTE FUNCTION immutable_route();
CREATE TABLE provider_inbox (
 inbox_id uuid PRIMARY KEY, provider_account_id uuid NOT NULL REFERENCES provider_accounts,
 digest text NOT NULL, event_type text NOT NULL, external_ref text NOT NULL,
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','PROCESSING','PROCESSED','DEAD')),
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz,
 lease_token uuid, last_error text, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(provider_account_id,digest)
);
CREATE INDEX provider_inbox_due ON provider_inbox(next_attempt_at) WHERE status IN ('PENDING','PROCESSING');
CREATE TABLE provider_observations (
 observation_id uuid PRIMARY KEY, provider_account_id uuid NOT NULL REFERENCES provider_accounts,
 operation text NOT NULL, external_ref text NOT NULL, outcome text NOT NULL, metadata jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER observations_immutable BEFORE UPDATE OR DELETE ON provider_observations FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TABLE settlement_observations (
 observation_id uuid PRIMARY KEY, pool_id uuid NOT NULL REFERENCES custody_pools, external_ref text NOT NULL,
 amount_minor bigint NOT NULL CHECK(amount_minor>=0), fee_minor bigint NOT NULL CHECK(fee_minor>=0),
 status text NOT NULL, evidence jsonb NOT NULL, evidence_fingerprint text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(pool_id,external_ref,evidence_fingerprint)
);
CREATE TRIGGER settlements_immutable BEFORE UPDATE OR DELETE ON settlement_observations FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TABLE reconciliation_exceptions (
 exception_id uuid PRIMARY KEY, pool_id uuid NOT NULL REFERENCES custody_pools, reason text NOT NULL,
 expected_minor numeric NOT NULL, observed_minor numeric NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), resolved_at timestamptz
);
CREATE TABLE verification_refs (
 identity_id uuid PRIMARY KEY REFERENCES financial_identities, verification_ref text NOT NULL, state text NOT NULL,
 expires_at timestamptz NOT NULL, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE virtual_accounts (
 virtual_account_id uuid PRIMARY KEY, wallet_id uuid NOT NULL REFERENCES wallets,
 provider_account_id uuid NOT NULL REFERENCES provider_accounts, consent_ref text NOT NULL, consent_version text NOT NULL,
 consent_at timestamptz NOT NULL, consent_expires_at timestamptz NOT NULL, verification_ref text NOT NULL, fingerprint text NOT NULL,
 state text NOT NULL CHECK(state IN ('REQUESTED','CUSTOMER_UNKNOWN','VALIDATION_REQUIRED','ASSIGNING','ASSIGNMENT_UNKNOWN','ACTIVE','RESTRICTED','REJECTED','INACTIVE')),
 customer_ref text, external_ref text, bank_code text, account_mask text,
 account_digest text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(wallet_id,provider_account_id), UNIQUE(provider_account_id,customer_ref), UNIQUE(provider_account_id,external_ref), UNIQUE(provider_account_id,bank_code,account_digest)
);
CREATE TABLE financial_audit (
 audit_id uuid PRIMARY KEY, origin_service text NOT NULL, action text NOT NULL, aggregate_ref text NOT NULL,
 evidence jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER audit_immutable BEFORE UPDATE OR DELETE ON financial_audit FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER journals_no_truncate BEFORE TRUNCATE ON journals FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER entries_no_truncate BEFORE TRUNCATE ON journal_entries FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER accounts_no_truncate BEFORE TRUNCATE ON ledger_accounts FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER audit_no_truncate BEFORE TRUNCATE ON financial_audit FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM journals) OR EXISTS(SELECT 1 FROM wallets) OR EXISTS(SELECT 1 FROM financial_audit) THEN RAISE EXCEPTION 'rollback prohibited with financial history; restore through approved recovery'; END IF; END $$;
DROP TABLE financial_audit,virtual_accounts,verification_refs,reconciliation_exceptions,settlement_observations,provider_observations,provider_inbox,payment_intents,journal_entries,journals CASCADE;
DROP VIEW IF EXISTS account_balances;
DROP TABLE ledger_accounts,custody_pools,provider_accounts,wallets,identity_bindings,financial_identities;
DROP FUNCTION immutable_financial_history(),validate_journal(),guard_entry(),validate_account_dimensions(),immutable_route();
-- +goose StatementEnd
