-- Extend the existing wallet/ledger. Never merge wallets or rewrite history.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE wallet_purposes (
 wallet_id uuid PRIMARY KEY REFERENCES wallets,
 identity_id uuid NOT NULL REFERENCES financial_identities, domain text NOT NULL,
 purpose text NOT NULL, legal_entity text NOT NULL, currency text NOT NULL,
 UNIQUE(identity_id,domain,purpose,legal_entity,currency)
);
INSERT INTO wallet_purposes
 SELECT wallet_id,identity_id,domain,
 CASE WHEN n=1 THEN purpose ELSE 'LEGACY_RETAINED:'||wallet_id::text END,legal_entity,currency
 FROM (SELECT w.*,CASE WHEN role IN ('CUSTOMER','TENANT') THEN 'PERSONAL' ELSE 'EARNINGS' END AS purpose,
 row_number() OVER(PARTITION BY identity_id,domain,legal_entity,currency,CASE WHEN role IN ('CUSTOMER','TENANT') THEN 'PERSONAL' ELSE 'EARNINGS' END ORDER BY created_at,wallet_id) n FROM wallets w) q;
CREATE TABLE wallet_roles (
 wallet_id uuid NOT NULL REFERENCES wallets, role text NOT NULL,
 registration_ref text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(wallet_id,role)
);
INSERT INTO wallet_roles SELECT w.wallet_id,w.role,b.registration_ref,now() FROM wallets w JOIN identity_bindings b ON b.identity_id=w.identity_id AND b.domain=w.domain;
CREATE TABLE wallet_controls (
 wallet_id uuid PRIMARY KEY REFERENCES wallets, state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RESTRICTED','CLOSED')),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE wallet_transfer_policies (
 policy_id uuid PRIMARY KEY, pool_id uuid NOT NULL REFERENCES custody_pools,
 version text NOT NULL, snapshot jsonb NOT NULL, fingerprint text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE wallet_transfers (
 transfer_id uuid PRIMARY KEY, source_wallet_id uuid NOT NULL REFERENCES wallets,
 destination_wallet_id uuid NOT NULL REFERENCES wallets, pool_id uuid NOT NULL REFERENCES custody_pools,
 policy_id uuid NOT NULL REFERENCES wallet_transfer_policies,
 actor_id uuid NOT NULL, actor_domain text NOT NULL, source_service text NOT NULL,
 legal_entity text NOT NULL, currency text NOT NULL, amount_minor bigint NOT NULL CHECK(amount_minor>0),
 purpose text NOT NULL CHECK(purpose='CUSTOMER_REMITTANCE'),
 operation_key text NOT NULL UNIQUE, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 state text NOT NULL DEFAULT 'REQUESTED' CHECK(state IN ('REQUESTED','POSTED')),
 journal_id uuid UNIQUE REFERENCES journals, created_at timestamptz NOT NULL DEFAULT now(), posted_at timestamptz,
 CHECK(source_wallet_id<>destination_wallet_id),
 CHECK((state='POSTED')=(journal_id IS NOT NULL)),CHECK((state='POSTED')=(posted_at IS NOT NULL))
);
CREATE INDEX wallet_transfer_source_history ON wallet_transfers(source_wallet_id,transfer_id);
CREATE INDEX wallet_transfer_destination_history ON wallet_transfers(destination_wallet_id,transfer_id);
CREATE TABLE wallet_transfer_history (
 history_id uuid PRIMARY KEY, transfer_id uuid NOT NULL REFERENCES wallet_transfers,
 state text NOT NULL, evidence jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE financial_capability_evidence (
 evidence_id uuid PRIMARY KEY, capability text NOT NULL, stage text NOT NULL,
 legal_entity text NOT NULL, provider_account_id uuid REFERENCES provider_accounts,
 jurisdiction text NOT NULL, version text NOT NULL, status text NOT NULL,
 snapshot jsonb NOT NULL, fingerprint text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(stage IN ('IMPLEMENTATION','LOCAL_TEST','PROVIDER_TEST','ACCOUNT','COMMERCIAL','REGULATORY','SAFEGUARDING','JURISDICTION','STAGING','PRODUCTION')),
 CHECK(status IN ('NOT_RUN','CERTIFIED','APPROVED','BLOCKED','REVOKED'))
);
CREATE FUNCTION guard_wallet_purpose() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM wallets w WHERE w.wallet_id=NEW.wallet_id AND w.identity_id=NEW.identity_id AND w.domain=NEW.domain AND w.legal_entity=NEW.legal_entity AND w.currency=NEW.currency) THEN RAISE EXCEPTION 'wallet ownership dimensions mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_purpose_dimensions BEFORE INSERT ON wallet_purposes FOR EACH ROW EXECUTE FUNCTION guard_wallet_purpose();
CREATE FUNCTION guard_wallet_transfer() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF OLD.state<>'REQUESTED' OR NEW.state<>'POSTED' OR (to_jsonb(OLD)-ARRAY['state','journal_id','posted_at'])<>(to_jsonb(NEW)-ARRAY['state','journal_id','posted_at']) THEN RAISE EXCEPTION 'transfer history/terms immutable' USING ERRCODE='23514'; END IF;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM wallets s JOIN wallets d ON d.wallet_id=NEW.destination_wallet_id JOIN custody_pools p ON p.pool_id=NEW.pool_id WHERE s.wallet_id=NEW.source_wallet_id AND s.legal_entity=NEW.legal_entity AND d.legal_entity=NEW.legal_entity AND p.legal_entity=NEW.legal_entity AND s.currency=NEW.currency AND d.currency=NEW.currency AND p.currency=NEW.currency) THEN RAISE EXCEPTION 'transfer dimensions mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.state='POSTED' AND NOT EXISTS(SELECT 1 FROM journals j WHERE j.journal_id=NEW.journal_id AND j.operation_ref=NEW.operation_key AND j.pool_id=NEW.pool_id AND j.legal_entity=NEW.legal_entity AND j.currency=NEW.currency AND (SELECT count(*) FROM journal_entries e WHERE e.journal_id=j.journal_id)=2 AND EXISTS(SELECT 1 FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE e.journal_id=j.journal_id AND a.wallet_id=NEW.source_wallet_id AND a.kind='AVAILABLE' AND a.dimension_ref='' AND e.side='DEBIT' AND e.amount_minor=NEW.amount_minor) AND EXISTS(SELECT 1 FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE e.journal_id=j.journal_id AND a.wallet_id=NEW.destination_wallet_id AND a.kind='AVAILABLE' AND a.dimension_ref='' AND e.side='CREDIT' AND e.amount_minor=NEW.amount_minor)) THEN RAISE EXCEPTION 'transfer journal does not conserve original value' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_transfer_terms BEFORE INSERT OR UPDATE ON wallet_transfers FOR EACH ROW EXECUTE FUNCTION guard_wallet_transfer();
CREATE TRIGGER wallet_transfer_no_delete BEFORE DELETE ON wallet_transfers FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_controls_no_delete BEFORE DELETE ON wallet_controls FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_controls_no_truncate BEFORE TRUNCATE ON wallet_controls EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_purposes_immutable BEFORE UPDATE OR DELETE ON wallet_purposes FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_roles_immutable BEFORE UPDATE OR DELETE ON wallet_roles FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_transfer_policies_immutable BEFORE UPDATE OR DELETE ON wallet_transfer_policies FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_transfer_history_immutable BEFORE UPDATE OR DELETE ON wallet_transfer_history FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER capability_evidence_immutable BEFORE UPDATE OR DELETE ON financial_capability_evidence FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_purposes_no_truncate BEFORE TRUNCATE ON wallet_purposes EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_roles_no_truncate BEFORE TRUNCATE ON wallet_roles EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_transfers_no_truncate BEFORE TRUNCATE ON wallet_transfers EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_transfer_history_no_truncate BEFORE TRUNCATE ON wallet_transfer_history EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER wallet_transfer_policies_no_truncate BEFORE TRUNCATE ON wallet_transfer_policies EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER capability_evidence_no_truncate BEFORE TRUNCATE ON financial_capability_evidence EXECUTE FUNCTION immutable_financial_history();
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM wallet_transfers) OR EXISTS(SELECT 1 FROM financial_capability_evidence) OR EXISTS(SELECT 1 FROM wallet_controls) OR EXISTS(SELECT 1 FROM wallet_transfer_policies) OR EXISTS(SELECT 1 FROM wallets) THEN RAISE EXCEPTION 'rollback prohibited with wallet/transfer history'; END IF; END $$;
DROP TABLE financial_capability_evidence,wallet_transfer_history,wallet_transfers,wallet_transfer_policies,wallet_controls,wallet_roles,wallet_purposes;
DROP FUNCTION guard_wallet_transfer(),guard_wallet_purpose();
-- +goose StatementEnd
