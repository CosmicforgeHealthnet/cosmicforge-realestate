-- Sections 4–6: all tables are owned by payment-service; domain facts are references.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE financial_policies (
 policy_id uuid PRIMARY KEY, policy_key text NOT NULL, version text NOT NULL,
 jurisdiction text NOT NULL, currency text NOT NULL, contract_type text NOT NULL, product text NOT NULL,
 effective_from timestamptz NOT NULL, effective_until timestamptz,
 active boolean NOT NULL, snapshot jsonb NOT NULL, fingerprint text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(policy_key,version,jurisdiction,contract_type,product),
 CHECK(effective_until IS NULL OR effective_until>effective_from)
);
CREATE TABLE beneficiary_authorities (
 authority_id uuid PRIMARY KEY, source_service text NOT NULL, business_ref text NOT NULL,
 wallet_id uuid REFERENCES wallets, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 expires_at timestamptz NOT NULL, revoked boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE policy_deactivations (
 decision_id uuid PRIMARY KEY, policy_id uuid NOT NULL REFERENCES financial_policies,
 effective_at timestamptz NOT NULL, evidence_ref text NOT NULL, snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE locked_quotes (
 quote_id uuid PRIMARY KEY, source_service text NOT NULL, source_domain text NOT NULL,
 business_ref text NOT NULL, operation_key text NOT NULL, fingerprint text NOT NULL,
 payer_wallet_id uuid NOT NULL REFERENCES wallets, policy_id uuid NOT NULL REFERENCES financial_policies,
 amount_minor bigint NOT NULL CHECK(amount_minor>0), currency text NOT NULL,
 snapshot jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_service,operation_key)
);
ALTER TABLE payment_intents ADD COLUMN locked_quote_id uuid REFERENCES locked_quotes;
CREATE UNIQUE INDEX intent_locked_quote_unique ON payment_intents(locked_quote_id) WHERE locked_quote_id IS NOT NULL;
CREATE TABLE transaction_funding (
 funding_id uuid PRIMARY KEY, intent_id uuid NOT NULL UNIQUE REFERENCES payment_intents,
 journal_id uuid NOT NULL REFERENCES journals, snapshot jsonb NOT NULL, fingerprint text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE allocation_snapshots (
 allocation_id uuid PRIMARY KEY, intent_id uuid NOT NULL REFERENCES payment_intents,
 version integer NOT NULL CHECK(version>0), supersedes_id uuid UNIQUE REFERENCES allocation_snapshots,
 fingerprint text NOT NULL, snapshot jsonb NOT NULL, journal_id uuid NOT NULL REFERENCES journals,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(intent_id,version)
);
CREATE TABLE allocation_lines (
 line_id uuid PRIMARY KEY, allocation_id uuid NOT NULL REFERENCES allocation_snapshots DEFERRABLE INITIALLY DEFERRED,
 authority_id uuid REFERENCES beneficiary_authorities, wallet_id uuid REFERENCES wallets,
 amount_minor bigint NOT NULL CHECK(amount_minor>0), snapshot jsonb NOT NULL,
 protected_account_id uuid NOT NULL REFERENCES ledger_accounts,
 available_account_id uuid REFERENCES ledger_accounts,
 state text NOT NULL CHECK(state IN ('PENDING','PROTECTED','HELD','DISPUTED','RELEASE_ELIGIBLE','RELEASED','AVAILABLE','WITHDRAWAL_RESERVED','PAID_OUT','CORRECTED','REVERSED','REVIEW_REQUIRED')),
 released_minor bigint NOT NULL DEFAULT 0 CHECK(released_minor>=0 AND released_minor<=amount_minor),
 reserved_minor bigint NOT NULL DEFAULT 0 CHECK(reserved_minor>=0 AND reserved_minor<=released_minor),
 paid_minor bigint NOT NULL DEFAULT 0 CHECK(paid_minor>=0 AND paid_minor<=reserved_minor)
);
CREATE TABLE lifecycle_decisions (
 decision_id uuid PRIMARY KEY, aggregate_id uuid NOT NULL, operation_key text NOT NULL UNIQUE,
 action text NOT NULL, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 journal_id uuid REFERENCES journals, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE payout_attempts (
 payout_id uuid PRIMARY KEY, line_id uuid NOT NULL REFERENCES allocation_lines,
 wallet_id uuid NOT NULL REFERENCES wallets, payout_account_id uuid NOT NULL REFERENCES payout_account_refs,
 provider_account_id uuid NOT NULL REFERENCES provider_accounts, pool_id uuid NOT NULL REFERENCES custody_pools,
 operation_key text NOT NULL UNIQUE, fingerprint text NOT NULL, amount_minor bigint NOT NULL CHECK(amount_minor>0),
 currency text NOT NULL, provider_reference text NOT NULL UNIQUE, recipient_ref text NOT NULL,
 route_snapshot jsonb NOT NULL, authorization_snapshot jsonb NOT NULL,
 state text NOT NULL CHECK(state IN ('RESERVED','SUBMITTING','PROCESSING','OTP_REQUIRED','UNKNOWN_OUTCOME','CONFIRMED_PAID','CONFIRMED_FAILED','REVIEW_REQUIRED')),
 reservation_journal_id uuid NOT NULL REFERENCES journals, terminal_journal_id uuid REFERENCES journals,
 updated_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE payout_destination_bindings (
 binding_id uuid PRIMARY KEY, payout_account_id uuid NOT NULL REFERENCES payout_account_refs,
 provider_account_id uuid NOT NULL REFERENCES provider_accounts, recipient_ref text NOT NULL,
 evidence_ref text NOT NULL, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 expires_at timestamptz NOT NULL, UNIQUE(payout_account_id,provider_account_id)
);
CREATE INDEX payout_recovery_idx ON payout_attempts(updated_at) WHERE state NOT IN ('CONFIRMED_PAID','CONFIRMED_FAILED');
CREATE TABLE rent_agreements (
 agreement_id uuid PRIMARY KEY, source_service text NOT NULL, agreement_ref text NOT NULL,
 version text NOT NULL, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 effective_from timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_service,agreement_ref,version)
);
CREATE TABLE rent_obligations (
 obligation_id uuid PRIMARY KEY, agreement_id uuid NOT NULL REFERENCES rent_agreements,
 agreement_ref text NOT NULL, period_ref text NOT NULL, source_service text NOT NULL,
 stream text NOT NULL CHECK(stream IN ('RENT','DEPOSIT')), quote_id uuid NOT NULL UNIQUE REFERENCES locked_quotes,
 due_at timestamptz NOT NULL, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(source_service,agreement_ref,period_ref,stream)
);
CREATE UNIQUE INDEX rent_initial_once ON rent_obligations(source_service,agreement_ref) WHERE stream='RENT' AND (snapshot->>'initial')='true';
CREATE FUNCTION lifecycle_terms_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='allocation_lines' THEN
  IF (NEW.line_id,NEW.allocation_id,NEW.authority_id,NEW.wallet_id,NEW.amount_minor,NEW.snapshot,NEW.protected_account_id,NEW.available_account_id) IS DISTINCT FROM (OLD.line_id,OLD.allocation_id,OLD.authority_id,OLD.wallet_id,OLD.amount_minor,OLD.snapshot,OLD.protected_account_id,OLD.available_account_id) THEN RAISE EXCEPTION 'allocation terms immutable'; END IF;
 ELSIF TG_TABLE_NAME='beneficiary_authorities' THEN
  IF (NEW.authority_id,NEW.source_service,NEW.business_ref,NEW.wallet_id,NEW.fingerprint,NEW.snapshot,NEW.expires_at) IS DISTINCT FROM (OLD.authority_id,OLD.source_service,OLD.business_ref,OLD.wallet_id,OLD.fingerprint,OLD.snapshot,OLD.expires_at) OR (OLD.revoked AND NOT NEW.revoked) THEN RAISE EXCEPTION 'authority terms immutable'; END IF;
 ELSE
  IF (NEW.payout_id,NEW.line_id,NEW.wallet_id,NEW.payout_account_id,NEW.provider_account_id,NEW.pool_id,NEW.operation_key,NEW.fingerprint,NEW.amount_minor,NEW.currency,NEW.provider_reference,NEW.recipient_ref,NEW.route_snapshot,NEW.authorization_snapshot,NEW.reservation_journal_id) IS DISTINCT FROM (OLD.payout_id,OLD.line_id,OLD.wallet_id,OLD.payout_account_id,OLD.provider_account_id,OLD.pool_id,OLD.operation_key,OLD.fingerprint,OLD.amount_minor,OLD.currency,OLD.provider_reference,OLD.recipient_ref,OLD.route_snapshot,OLD.authorization_snapshot,OLD.reservation_journal_id) OR (OLD.state IN ('CONFIRMED_PAID','CONFIRMED_FAILED') AND NEW IS DISTINCT FROM OLD) THEN RAISE EXCEPTION 'payout terms/terminal history immutable'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER line_terms BEFORE UPDATE ON allocation_lines FOR EACH ROW EXECUTE FUNCTION lifecycle_terms_immutable();
CREATE TRIGGER authority_terms BEFORE UPDATE ON beneficiary_authorities FOR EACH ROW EXECUTE FUNCTION lifecycle_terms_immutable();
CREATE TRIGGER payout_terms BEFORE UPDATE ON payout_attempts FOR EACH ROW EXECUTE FUNCTION lifecycle_terms_immutable();
CREATE FUNCTION quote_intent_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.locked_quote_id IS DISTINCT FROM OLD.locked_quote_id THEN RAISE EXCEPTION 'canonical quote immutable'; END IF; RETURN NEW; END $$;
CREATE TRIGGER intent_quote_terms BEFORE UPDATE ON payment_intents FOR EACH ROW EXECUTE FUNCTION quote_intent_immutable();
CREATE FUNCTION guard_allocation_line() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM allocation_snapshots WHERE allocation_id=NEW.allocation_id AND xmin::text<>pg_current_xact_id()::text) THEN RAISE EXCEPTION 'accepted allocation is sealed'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER allocation_line_sealed BEFORE INSERT ON allocation_lines FOR EACH ROW EXECUTE FUNCTION guard_allocation_line();
CREATE FUNCTION validate_allocation_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected bigint; actual numeric;
BEGIN
 SELECT amount_minor INTO expected FROM payment_intents WHERE intent_id=NEW.intent_id;
 SELECT sum(amount_minor::numeric) INTO actual FROM allocation_lines WHERE allocation_id=NEW.allocation_id;
 IF actual IS DISTINCT FROM expected::numeric OR (SELECT count(*) FROM allocation_lines WHERE allocation_id=NEW.allocation_id)<>jsonb_array_length(NEW.snapshot->'lines') THEN RAISE EXCEPTION 'allocation does not reconcile'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER allocation_reconciled AFTER INSERT ON allocation_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_allocation_snapshot();
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['financial_policies','locked_quotes','transaction_funding','allocation_snapshots','lifecycle_decisions','rent_agreements','rent_obligations','payout_destination_bindings','policy_deactivations'] LOOP
  EXECUTE format('CREATE TRIGGER history_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION immutable_financial_history()',t);
 END LOOP;
 FOREACH t IN ARRAY ARRAY['financial_policies','beneficiary_authorities','locked_quotes','transaction_funding','allocation_snapshots','allocation_lines','lifecycle_decisions','payout_attempts','rent_agreements','rent_obligations','payout_destination_bindings','policy_deactivations'] LOOP
  EXECUTE format('CREATE TRIGGER history_no_truncate BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history()',t);
 END LOOP;
 FOREACH t IN ARRAY ARRAY['beneficiary_authorities','allocation_lines','payout_attempts'] LOOP
  EXECUTE format('CREATE TRIGGER history_no_delete BEFORE DELETE ON %I FOR EACH ROW EXECUTE FUNCTION immutable_financial_history()',t);
 END LOOP;
END $$;
CREATE OR REPLACE FUNCTION validate_journal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE j journals; n bigint; net numeric;
BEGIN
 SELECT * INTO j FROM journals WHERE journal_id=NEW.journal_id;
 SELECT count(*),COALESCE(sum(CASE WHEN side='DEBIT' THEN amount_minor::numeric ELSE -amount_minor::numeric END),0) INTO n,net FROM journal_entries WHERE journal_id=j.journal_id;
 IF n<2 OR net<>0 THEN RAISE EXCEPTION 'unbalanced journal' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE e.journal_id=j.journal_id AND (a.currency<>j.currency OR a.legal_entity<>j.legal_entity OR a.pool_id<>j.pool_id)) THEN RAISE EXCEPTION 'journal dimension mismatch' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM account_balances WHERE pool_id=j.pool_id AND kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','SETTLEMENT_CONTROL','RESERVE','SETTLED_CASH','PROVIDER_RECEIVABLE') AND balance_minor<0) THEN RAISE EXCEPTION 'negative controlled financial balance' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM locked_quotes) OR EXISTS(SELECT 1 FROM financial_policies) OR EXISTS(SELECT 1 FROM beneficiary_authorities) OR EXISTS(SELECT 1 FROM rent_agreements) THEN RAISE EXCEPTION 'lifecycle history requires retention'; END IF; END $$;
DROP TABLE rent_obligations,rent_agreements,payout_destination_bindings,payout_attempts,lifecycle_decisions,allocation_lines,allocation_snapshots,transaction_funding;
ALTER TABLE payment_intents DROP COLUMN locked_quote_id;
DROP TRIGGER intent_quote_terms ON payment_intents;
DROP TABLE locked_quotes,beneficiary_authorities,policy_deactivations,financial_policies;
CREATE OR REPLACE FUNCTION validate_journal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE j journals; n bigint; net numeric;
BEGIN
 SELECT * INTO j FROM journals WHERE journal_id=NEW.journal_id;
 SELECT count(*),COALESCE(sum(CASE WHEN side='DEBIT' THEN amount_minor::numeric ELSE -amount_minor::numeric END),0) INTO n,net FROM journal_entries WHERE journal_id=j.journal_id;
 IF n<2 OR net<>0 THEN RAISE EXCEPTION 'unbalanced journal' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM journal_entries e JOIN ledger_accounts a USING(account_id) WHERE e.journal_id=j.journal_id AND (a.currency<>j.currency OR a.legal_entity<>j.legal_entity OR a.pool_id<>j.pool_id)) THEN RAISE EXCEPTION 'journal dimension mismatch' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM account_balances WHERE pool_id=j.pool_id AND kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','SETTLED_CASH','PROVIDER_RECEIVABLE') AND balance_minor<0) THEN RAISE EXCEPTION 'negative controlled financial balance' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
DROP FUNCTION lifecycle_terms_immutable(),quote_intent_immutable(),guard_allocation_line(),validate_allocation_snapshot();
-- +goose StatementEnd
