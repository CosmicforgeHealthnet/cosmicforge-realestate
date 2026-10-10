# Sections 7–10 schema catalog

Source: `services/payment-service/migrations/00006_remedies_customer_reliability.sql`. This forward migration adds 22 tables; prior migrations are retained unchanged. Every explicit column/check/foreign-key/primary-key/unique constraint, partial index, function and trigger is reproduced below. Primary/unique constraints create their normal PostgreSQL indexes. No second ledger is created.

## Generated trigger expansions

The `DO` block attaches history_immutable to immutable reference/fact/run tables; terms_immutable plus no_delete to evidence/case/refund terms; no_truncate to all retained remedy tables; and state_history to payment_intents, allocation_lines, payout_attempts, refund_attempts and financial_cases. Each trigger uses the existing immutable_financial_history or the new function shown below. Original readback history/jobs have explicit immutability/retention/term triggers. The deferred remedy balance trigger runs on journal_entries. Down refuses retained Section 7–10 history before dropping new structures and restoring prior enum checks.

## Complete forward definitions

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ledger_accounts DROP CONSTRAINT ledger_accounts_kind_check;
ALTER TABLE ledger_accounts ADD CONSTRAINT ledger_accounts_kind_check CHECK(kind IN ('PAYMENT_PENDING','PENDING_EARNINGS','PROTECTED','AVAILABLE','FROZEN','WITHDRAWAL_PENDING','REFUND_PENDING','DEPOSIT','PROVIDER_RECEIVABLE','SETTLED_CASH','REVENUE','PROCESSING_EXPENSE','RESERVE','SETTLEMENT_CONTROL','TREASURY','RECOVERABLE','PROVIDER_PAYABLE'));
ALTER TABLE payment_intents DROP CONSTRAINT payment_intents_state_check;
ALTER TABLE payment_intents ADD CONSTRAINT payment_intents_state_check CHECK(state IN ('REQUESTED','SUBMITTING','INITIALIZED','PENDING','PROCESSING','VERIFIED_SUCCESS','VERIFIED_FAILURE','UNKNOWN_OUTCOME','RECONCILIATION_EXCEPTION','CANCELLED'));
CREATE FUNCTION guard_remedy_asset() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind IN ('RECOVERABLE','PROVIDER_PAYABLE') AND (NEW.wallet_id IS NOT NULL OR NEW.normal_side<>CASE NEW.kind WHEN 'RECOVERABLE' THEN 'DEBIT' ELSE 'CREDIT' END) THEN RAISE EXCEPTION 'internal remedy account required'; END IF; RETURN NEW; END $$;
CREATE TRIGGER remedy_asset_dimensions BEFORE INSERT ON ledger_accounts FOR EACH ROW EXECUTE FUNCTION guard_remedy_asset();
CREATE TABLE remedy_policies (
 policy_id uuid PRIMARY KEY REFERENCES financial_policies, snapshot jsonb NOT NULL,
 fingerprint text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE remedy_evidence (
 evidence_id uuid PRIMARY KEY, source_service text NOT NULL, snapshot jsonb NOT NULL,
 fingerprint text NOT NULL, expires_at timestamptz NOT NULL, revoked boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE refundable_deposits (
 deposit_id uuid PRIMARY KEY, line_id uuid NOT NULL UNIQUE REFERENCES allocation_lines,
 snapshot jsonb NOT NULL, fingerprint text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE financial_cases (
 case_id uuid PRIMARY KEY, line_id uuid NOT NULL REFERENCES allocation_lines,
 operation_key text NOT NULL UNIQUE, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 contested_minor bigint NOT NULL CHECK(contested_minor>0),
 protected_minor bigint NOT NULL CHECK(protected_minor>=0), available_minor bigint NOT NULL CHECK(available_minor>=0),
 frozen_account_id uuid NOT NULL REFERENCES ledger_accounts,
 state text NOT NULL CHECK(state IN ('OPEN','EVIDENCE_PENDING','UNDER_REVIEW','MANUAL_APPROVAL_REQUIRED','DECIDED','PARTIAL_AWARD','RELEASE_TO_BENEFICIARY','RETURN_TO_PAYER','RESERVE_EXTENDED','REWORK_REQUIRED','LEGAL_ESCALATION','CLOSED')),
 CHECK(protected_minor::numeric+available_minor=contested_minor),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE financial_case_facts (
 fact_id uuid PRIMARY KEY, case_id uuid NOT NULL REFERENCES financial_cases,
 operation_key text NOT NULL UNIQUE, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 kind text NOT NULL CHECK(kind IN ('CLAIM','CHALLENGE','REVIEW','DECISION','NOTICE','OVERRIDE')),
 journal_id uuid REFERENCES journals, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE refund_attempts (
 refund_id uuid PRIMARY KEY, intent_id uuid NOT NULL REFERENCES payment_intents,
 line_id uuid NOT NULL REFERENCES allocation_lines, case_id uuid REFERENCES financial_cases,
 payer_wallet_id uuid NOT NULL REFERENCES wallets, provider_account_id uuid NOT NULL REFERENCES provider_accounts,
 pool_id uuid NOT NULL REFERENCES custody_pools, amount_minor bigint NOT NULL CHECK(amount_minor>0),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'), original_reference text NOT NULL,
 attempt_reference text NOT NULL UNIQUE, provider_refund_id text,
 operation_key text NOT NULL UNIQUE, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 source_account_id uuid NOT NULL REFERENCES ledger_accounts, pending_account_id uuid NOT NULL REFERENCES ledger_accounts,
 reservation_journal_id uuid NOT NULL REFERENCES journals, terminal_journal_id uuid REFERENCES journals,
 state text NOT NULL CHECK(state IN ('RESERVED','SUBMITTED','PROCESSING','UNKNOWN_OUTCOME','CONFIRMED_REFUNDED','CONFIRMED_FAILED','CANCELLED','REVIEW_REQUIRED')),
 updated_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE financial_reversals (
 reversal_id uuid PRIMARY KEY, intent_id uuid REFERENCES payment_intents, payout_id uuid REFERENCES payout_attempts,
 refund_id uuid REFERENCES refund_attempts, provider_account_id uuid NOT NULL REFERENCES provider_accounts,
 external_ref text NOT NULL, amount_minor bigint NOT NULL CHECK(amount_minor>0), snapshot jsonb NOT NULL,
 journal_id uuid NOT NULL REFERENCES journals, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(provider_account_id,external_ref), CHECK(num_nonnulls(intent_id,payout_id,refund_id)=1)
);
CREATE TABLE financial_recoverables (
 recoverable_id uuid PRIMARY KEY, reversal_id uuid NOT NULL REFERENCES financial_reversals,
 wallet_id uuid REFERENCES wallets, amount_minor bigint NOT NULL CHECK(amount_minor>0),
 reason text NOT NULL, snapshot jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE recoverable_actions (
 action_id uuid PRIMARY KEY,recoverable_id uuid NOT NULL REFERENCES financial_recoverables,
 wallet_id uuid REFERENCES wallets,operation_key text NOT NULL UNIQUE,fingerprint text NOT NULL,
 action text NOT NULL CHECK(action IN ('ASSIGN_BENEFICIARY','PLATFORM_LIABILITY','USE_RESERVE','RECOVER_FROM_PROCEEDS','LEGAL_ESCALATION')),
 amount_minor bigint NOT NULL CHECK(amount_minor>=0),snapshot jsonb NOT NULL,journal_id uuid REFERENCES journals,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE destination_versions (
 version_id uuid PRIMARY KEY, wallet_id uuid NOT NULL REFERENCES wallets,
 payout_account_id uuid NOT NULL REFERENCES payout_account_refs, predecessor_id uuid UNIQUE REFERENCES destination_versions,
 effective_at timestamptz NOT NULL, expires_at timestamptz NOT NULL, block_during_cooling boolean NOT NULL,
 snapshot jsonb NOT NULL, fingerprint text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(expires_at>effective_at)
);
CREATE TABLE destination_revocations (
 version_id uuid PRIMARY KEY REFERENCES destination_versions, evidence_ref text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE customer_topups (
 topup_id uuid PRIMARY KEY, operation_key text NOT NULL UNIQUE, wallet_id uuid NOT NULL REFERENCES wallets,
 intent_id uuid UNIQUE REFERENCES payment_intents, receipt_id uuid UNIQUE REFERENCES collection_receipts,
 pool_id uuid NOT NULL REFERENCES custody_pools, amount_minor bigint NOT NULL CHECK(amount_minor>0),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'), policy_id uuid NOT NULL REFERENCES financial_policies,
 expires_at timestamptz NOT NULL, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), CHECK(num_nonnulls(intent_id,receipt_id)=1)
);
CREATE TABLE customer_funding (
 topup_id uuid PRIMARY KEY REFERENCES customer_topups, journal_id uuid NOT NULL REFERENCES journals,
 snapshot jsonb NOT NULL, fingerprint text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE wallet_spends (
 spend_id uuid PRIMARY KEY, quote_id uuid NOT NULL UNIQUE REFERENCES locked_quotes,
 intent_id uuid NOT NULL UNIQUE REFERENCES payment_intents, wallet_id uuid NOT NULL REFERENCES wallets,
 operation_key text NOT NULL UNIQUE, fingerprint text NOT NULL, snapshot jsonb NOT NULL,
 journal_id uuid NOT NULL REFERENCES journals, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE unmatched_dva_credits (
 credit_id uuid PRIMARY KEY, provider_account_id uuid NOT NULL REFERENCES provider_accounts,
 external_ref text NOT NULL, amount_minor bigint NOT NULL CHECK(amount_minor>0), currency text NOT NULL,
 customer_ref text NOT NULL, snapshot jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(provider_account_id,external_ref)
);
CREATE TABLE unmatched_dva_resolutions (
 credit_id uuid PRIMARY KEY REFERENCES unmatched_dva_credits, snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE reconciliation_runs (
 run_id uuid PRIMARY KEY, provider_account_id uuid NOT NULL REFERENCES provider_accounts,
 snapshot jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE reconciliation_findings (
 finding_id uuid PRIMARY KEY, run_id uuid NOT NULL REFERENCES reconciliation_runs,
 kind text NOT NULL, aggregate_ref text NOT NULL, snapshot jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE reconciliation_resolutions (
 resolution_id uuid PRIMARY KEY,finding_id uuid NOT NULL UNIQUE REFERENCES reconciliation_findings,snapshot jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE financial_state_history (
 history_id uuid PRIMARY KEY DEFAULT (lpad(to_hex((extract(epoch FROM clock_timestamp())*1000)::bigint),12,'0')||'7'||substr(replace(gen_random_uuid()::text,'-',''),1,3)||'8'||substr(replace(gen_random_uuid()::text,'-',''),1,15))::uuid, aggregate_type text NOT NULL,
 aggregate_ref text NOT NULL, prior_state text, resulting_state text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION record_financial_state() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE id text; h uuid; prior text;
BEGIN
 id := to_jsonb(NEW)->>(CASE TG_TABLE_NAME WHEN 'payment_intents' THEN 'intent_id' WHEN 'allocation_lines' THEN 'line_id' WHEN 'payout_attempts' THEN 'payout_id' WHEN 'refund_attempts' THEN 'refund_id' ELSE 'case_id' END);
 IF TG_OP='INSERT' OR NEW.state IS DISTINCT FROM OLD.state THEN
  prior:=CASE WHEN TG_OP='UPDATE' THEN OLD.state ELSE NULL END;
 INSERT INTO financial_state_history(aggregate_type,aggregate_ref,prior_state,resulting_state) VALUES(TG_TABLE_NAME,id,prior,NEW.state) RETURNING history_id INTO h;
 INSERT INTO financial_audit(audit_id,origin_service,action,aggregate_ref,evidence) VALUES(h,'payment-service','financial.state_changed',id,jsonb_build_object('aggregate_type',TG_TABLE_NAME,'prior_state',prior,'resulting_state',NEW.state));
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION remedy_terms_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='remedy_evidence' THEN
  IF (to_jsonb(NEW)-'revoked') IS DISTINCT FROM (to_jsonb(OLD)-'revoked') OR OLD.revoked AND NOT NEW.revoked THEN RAISE EXCEPTION 'evidence immutable'; END IF;
 ELSIF TG_TABLE_NAME='financial_cases' THEN
  IF (to_jsonb(NEW)-ARRAY['state','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','updated_at']) OR OLD.state='CLOSED' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'case terms immutable'; END IF;
 ELSE
  IF (to_jsonb(NEW)-ARRAY['state','updated_at','terminal_journal_id','provider_refund_id']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','updated_at','terminal_journal_id','provider_refund_id']) OR OLD.state IN ('CONFIRMED_REFUNDED','CONFIRMED_FAILED','CANCELLED') AND NEW IS DISTINCT FROM OLD OR OLD.provider_refund_id IS NOT NULL AND OLD.provider_refund_id IS DISTINCT FROM NEW.provider_refund_id THEN RAISE EXCEPTION 'refund terms/terminal history immutable'; END IF;
  IF OLD.state<>'RESERVED' AND NEW.state='SUBMITTED' THEN RAISE EXCEPTION 'refund resubmission prohibited'; END IF;
 END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['remedy_policies','refundable_deposits','financial_case_facts','financial_reversals','financial_recoverables','destination_versions','destination_revocations','customer_topups','customer_funding','wallet_spends','unmatched_dva_credits','unmatched_dva_resolutions','reconciliation_runs','reconciliation_findings','financial_state_history','recoverable_actions','reconciliation_resolutions'] LOOP
  EXECUTE format('CREATE TRIGGER history_immutable BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION immutable_financial_history()',t);
 END LOOP;
 FOREACH t IN ARRAY ARRAY['remedy_evidence','financial_cases','refund_attempts'] LOOP
  EXECUTE format('CREATE TRIGGER terms_immutable BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION remedy_terms_immutable()',t);
  EXECUTE format('CREATE TRIGGER no_delete BEFORE DELETE ON %I FOR EACH ROW EXECUTE FUNCTION immutable_financial_history()',t);
 END LOOP;
 FOREACH t IN ARRAY ARRAY['remedy_policies','remedy_evidence','refundable_deposits','financial_cases','financial_case_facts','refund_attempts','financial_reversals','financial_recoverables','destination_versions','destination_revocations','customer_topups','customer_funding','wallet_spends','unmatched_dva_credits','unmatched_dva_resolutions','reconciliation_runs','reconciliation_findings','financial_state_history','recoverable_actions','reconciliation_resolutions'] LOOP
  EXECUTE format('CREATE TRIGGER no_truncate BEFORE TRUNCATE ON %I FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history()',t);
 END LOOP;
 FOREACH t IN ARRAY ARRAY['payment_intents','allocation_lines','payout_attempts','refund_attempts','financial_cases'] LOOP
  EXECUTE format('CREATE TRIGGER state_history AFTER INSERT OR UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION record_financial_state()',t);
 END LOOP;
END $$;
CREATE INDEX refund_recovery ON refund_attempts(updated_at) WHERE state NOT IN ('CONFIRMED_REFUNDED','CONFIRMED_FAILED','CANCELLED');
CREATE INDEX case_line_open ON financial_cases(line_id) WHERE state<>'CLOSED';
CREATE TABLE original_readback_jobs (
 aggregate_id uuid NOT NULL, kind text NOT NULL CHECK(kind IN ('COLLECTION','PAYOUT','REFUND')),
 provider_account_id uuid NOT NULL REFERENCES provider_accounts, original_reference text NOT NULL,
 state text NOT NULL CHECK(state IN ('PENDING','PROCESSING','COMPLETED','DEAD')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0), next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_token uuid, lease_until timestamptz, last_error_code text, updated_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(kind,aggregate_id)
);
CREATE TABLE original_readback_history (
 history_id uuid PRIMARY KEY, aggregate_id uuid NOT NULL, kind text NOT NULL,
 snapshot jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(kind,aggregate_id) REFERENCES original_readback_jobs
);
CREATE TRIGGER readback_history_immutable BEFORE UPDATE OR DELETE ON original_readback_history FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER readback_history_no_truncate BEFORE TRUNCATE ON original_readback_history FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER readback_jobs_no_delete BEFORE DELETE ON original_readback_jobs FOR EACH ROW EXECUTE FUNCTION immutable_financial_history();
CREATE TRIGGER readback_jobs_no_truncate BEFORE TRUNCATE ON original_readback_jobs FOR EACH STATEMENT EXECUTE FUNCTION immutable_financial_history();
CREATE FUNCTION guard_original_readback() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (NEW.aggregate_id,NEW.kind,NEW.provider_account_id,NEW.original_reference,NEW.created_at) IS DISTINCT FROM (OLD.aggregate_id,OLD.kind,OLD.provider_account_id,OLD.original_reference,OLD.created_at) THEN RAISE EXCEPTION 'original readback terms immutable'; END IF; RETURN NEW; END $$;
CREATE TRIGGER readback_terms BEFORE UPDATE ON original_readback_jobs FOR EACH ROW EXECUTE FUNCTION guard_original_readback();
CREATE FUNCTION guard_financial_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state=OLD.state THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='payout_attempts' THEN
  IF NEW.state IN ('CONFIRMED_PAID','CONFIRMED_FAILED') AND OLD.state NOT IN ('SUBMITTING','PROCESSING','OTP_REQUIRED','UNKNOWN_OUTCOME','REVIEW_REQUIRED') OR NEW.state='SUBMITTING' AND OLD.state<>'RESERVED' THEN RAISE EXCEPTION 'invalid payout transition'; END IF;
 ELSIF TG_TABLE_NAME='refund_attempts' THEN
  IF NEW.state='SUBMITTED' AND OLD.state<>'RESERVED' OR NEW.state IN ('CONFIRMED_REFUNDED','CONFIRMED_FAILED') AND OLD.state='RESERVED' AND OLD.snapshot->>'destination_strategy'<>'WALLET_CREDIT' OR NEW.state='CANCELLED' AND OLD.state<>'RESERVED' THEN RAISE EXCEPTION 'invalid refund transition'; END IF;
 ELSE
  IF OLD.state='REQUESTED' AND NEW.state NOT IN ('SUBMITTING','UNKNOWN_OUTCOME','CANCELLED') OR OLD.state='VERIFIED_SUCCESS' AND NEW.state<>'RECONCILIATION_EXCEPTION' OR OLD.state='CANCELLED' OR OLD.state='RECONCILIATION_EXCEPTION' OR OLD.state='VERIFIED_FAILURE' AND NEW.state<>'RECONCILIATION_EXCEPTION' THEN RAISE EXCEPTION 'invalid payment transition'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER valid_transition BEFORE UPDATE ON payment_intents FOR EACH ROW EXECUTE FUNCTION guard_financial_transition();
CREATE TRIGGER valid_transition BEFORE UPDATE ON payout_attempts FOR EACH ROW EXECUTE FUNCTION guard_financial_transition();
CREATE TRIGGER valid_transition BEFORE UPDATE ON refund_attempts FOR EACH ROW EXECUTE FUNCTION guard_financial_transition();
CREATE FUNCTION validate_remedy_balances() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM account_balances WHERE kind IN ('RECOVERABLE','PROVIDER_PAYABLE') AND balance_minor<0) THEN RAISE EXCEPTION 'negative remedy balance'; END IF;RETURN NEW;END $$;
CREATE CONSTRAINT TRIGGER remedy_balances AFTER INSERT ON journal_entries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION validate_remedy_balances();
-- +goose StatementEnd
```
