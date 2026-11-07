-- +goose Up
-- Section 11: service recovery with exact approved remedies and verified
-- outcomes, and an optional policy-driven loyalty ledger. Loyalty points are
-- domain benefits, never a Payment Service wallet or stored money.
CREATE TABLE service_recovery_cases (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,reservation_id uuid,stay_id uuid,
 issue_type text NOT NULL CHECK(issue_type IN ('OVERBOOKING','ROOM_MOVE','SERVICE_FAILURE','BILLING_DISPUTE','ACCESS_FAILURE','DELAYED_CHECK_IN','CLEANLINESS','MAINTENANCE','INCORRECT_CHARGE','OTHER')),severity text NOT NULL CHECK(severity IN ('LOW','MEDIUM','HIGH','CRITICAL')),
 state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','ASSIGNED','INVESTIGATING','REMEDY_PROPOSED','PENDING_APPROVAL','APPROVED','EXECUTION_PENDING','RESOLVED','CLOSED','REJECTED','ESCALATED','CANCELLED')),
 summary text NOT NULL CHECK(length(summary) BETWEEN 5 AND 500),evidence_reference text NOT NULL CHECK(length(evidence_reference) BETWEEN 5 AND 300),assigned_to uuid REFERENCES business_accounts(id),sla_due_at timestamptz NOT NULL,
 remedy jsonb CHECK(remedy IS NULL OR jsonb_typeof(remedy)='array'),remedy_digest text CHECK(remedy_digest IS NULL OR remedy_digest~'^[a-f0-9]{64}$'),remedy_amount_minor bigint NOT NULL DEFAULT 0 CHECK(remedy_amount_minor BETWEEN 0 AND 1000000000000),currency text CHECK(currency IS NULL OR currency ~ '^[A-Z]{3}$'),approval_id uuid,
 outcome text,communication_reference text,follow_up_at timestamptz,resolved_at timestamptz,closed_at timestamptz,reopened_count integer NOT NULL DEFAULT 0 CHECK(reopened_count>=0),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,stay_id) REFERENCES stay_records(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK((remedy IS NULL)=(remedy_digest IS NULL)),CHECK(state NOT IN ('APPROVED','EXECUTION_PENDING','RESOLVED') OR remedy IS NOT NULL),CHECK(state<>'CLOSED' OR closed_at IS NOT NULL),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX service_recovery_queue ON service_recovery_cases(organisation_id,property_id,state,sla_due_at,id);
CREATE INDEX service_recovery_guest ON service_recovery_cases(organisation_id,guest_id,created_at DESC,id DESC);
CREATE TABLE service_recovery_actions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,case_id uuid NOT NULL,case_version bigint NOT NULL CHECK(case_version>0),action text NOT NULL,from_state text NOT NULL,to_state text NOT NULL,evidence_reference text,details jsonb NOT NULL CHECK(jsonb_typeof(details)='object'),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,case_id) REFERENCES service_recovery_cases(organisation_id,property_id,id),UNIQUE(case_id,case_version),UNIQUE(organisation_id,id)
);
CREATE TABLE service_recovery_compensation_refs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,case_id uuid NOT NULL,ordinal integer NOT NULL CHECK(ordinal BETWEEN 1 AND 10),
 kind text NOT NULL CHECK(kind IN ('ALTERNATIVE_ROOM','SERVICE_REPLACEMENT','COMPLIMENTARY_SERVICE','DISCOUNT','LOYALTY_BENEFIT','FUTURE_BOOKING_CREDIT','REFUND_REQUEST','FINANCIAL_ADJUSTMENT_REQUEST','OTHER_NONCASH')),
 amount_minor bigint NOT NULL DEFAULT 0 CHECK(amount_minor BETWEEN 0 AND 1000000000000),currency text CHECK(currency IS NULL OR currency ~ '^[A-Z]{3}$'),points bigint NOT NULL DEFAULT 0 CHECK(points BETWEEN 0 AND 100000000),remedy_digest text NOT NULL CHECK(remedy_digest~'^[a-f0-9]{64}$'),approval_id uuid,
 state text NOT NULL DEFAULT 'PENDING_EXECUTION' CHECK(state IN ('PENDING_EXECUTION','EXECUTED','PENDING_EXTERNAL_CONTRACT','DECLINED','CANCELLED')),execution_kind text CHECK(execution_kind IS NULL OR execution_kind IN ('FOLIO_ADJUSTMENT','LOYALTY_TRANSACTION','ROOM_MOVE','OPERATOR_EVIDENCE','EXTERNAL_DEPENDENCY')),execution_reference uuid,evidence_reference text,dependency text,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,case_id) REFERENCES service_recovery_cases(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK(kind NOT IN ('DISCOUNT','REFUND_REQUEST','FINANCIAL_ADJUSTMENT_REQUEST','FUTURE_BOOKING_CREDIT') OR amount_minor>0 AND currency IS NOT NULL),CHECK(kind<>'LOYALTY_BENEFIT' OR points>0),
 CHECK(state<>'EXECUTED' OR execution_kind IS NOT NULL AND (execution_reference IS NOT NULL OR execution_kind='OPERATOR_EVIDENCE' AND evidence_reference IS NOT NULL)),CHECK(state<>'EXECUTED' OR kind NOT IN ('REFUND_REQUEST','FINANCIAL_ADJUSTMENT_REQUEST')),
 UNIQUE(case_id,remedy_digest,ordinal),UNIQUE(organisation_id,id)
);
CREATE TRIGGER service_recovery_action_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON service_recovery_actions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER service_recovery_case_no_delete BEFORE DELETE OR TRUNCATE ON service_recovery_cases FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER service_recovery_compensation_no_delete BEFORE DELETE OR TRUNCATE ON service_recovery_compensation_refs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
-- Approved remedy terms are frozen; only execution state and evidence move.
CREATE FUNCTION business_recovery_compensation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.case_id,NEW.ordinal,NEW.kind,NEW.amount_minor,NEW.currency,NEW.points,NEW.remedy_digest,NEW.approval_id) IS DISTINCT FROM (OLD.case_id,OLD.ordinal,OLD.kind,OLD.amount_minor,OLD.currency,OLD.points,OLD.remedy_digest,OLD.approval_id) THEN RAISE EXCEPTION 'Approved remedy terms are immutable' USING ERRCODE='23514';END IF;
 IF OLD.state IN ('EXECUTED','DECLINED','CANCELLED') AND NEW.state IS DISTINCT FROM OLD.state THEN RAISE EXCEPTION 'A final remedy outcome is immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER service_recovery_compensation_terms BEFORE UPDATE ON service_recovery_compensation_refs FOR EACH ROW EXECUTE FUNCTION business_recovery_compensation_guard();
-- +goose StatementBegin
-- A case resolves only when every approved remedy reached a verified final
-- outcome; an accepted refund request is never a completed refund.
CREATE FUNCTION business_recovery_case_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.state='RESOLVED' AND OLD.state IS DISTINCT FROM 'RESOLVED' AND EXISTS(SELECT 1 FROM service_recovery_compensation_refs c WHERE c.organisation_id=NEW.organisation_id AND c.case_id=NEW.id AND c.remedy_digest=NEW.remedy_digest AND c.state NOT IN ('EXECUTED','DECLINED')) THEN RAISE EXCEPTION 'Every approved remedy needs a verified final outcome before resolution' USING ERRCODE='23514';END IF;
 IF NEW.state IN ('APPROVED','EXECUTION_PENDING') AND OLD.state='PENDING_APPROVAL' AND NEW.remedy_amount_minor>0 AND NEW.approval_id IS NULL THEN RAISE EXCEPTION 'Monetary remedies require an exact independent approval' USING ERRCODE='23514';END IF;
 IF (NEW.guest_id,NEW.property_id,NEW.created_by,NEW.created_at) IS DISTINCT FROM (OLD.guest_id,OLD.property_id,OLD.created_by,OLD.created_at) THEN RAISE EXCEPTION 'Case identity is immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER service_recovery_case_guard BEFORE UPDATE ON service_recovery_cases FOR EACH ROW EXECUTE FUNCTION business_recovery_case_guard();

CREATE TABLE loyalty_programs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,program_key uuid NOT NULL,version bigint NOT NULL CHECK(version>0),name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 state text NOT NULL CHECK(state IN ('ACTIVE','SUSPENDED','RETIRED')),definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=16384),effective_at timestamptz NOT NULL,approval_id uuid,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(program_key,version),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE loyalty_rules (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,program_key uuid NOT NULL,rule_key uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 kind text NOT NULL CHECK(kind IN ('EARN_PER_NIGHT','EARN_PER_SPEND','QUALIFYING_STAY_BONUS')),state text NOT NULL CHECK(state IN ('ACTIVE','RETIRED')),definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=8192),effective_at timestamptz NOT NULL,approval_id uuid,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(rule_key,version),UNIQUE(organisation_id,id)
);
CREATE INDEX loyalty_rule_current ON loyalty_rules(organisation_id,program_key,rule_key,version DESC);
CREATE TABLE loyalty_accounts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,program_key uuid NOT NULL,guest_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','SUSPENDED','REVIEW_REQUIRED','CLOSED')),tier text NOT NULL DEFAULT 'BASE' CHECK(length(tier) BETWEEN 1 AND 40),
 consent_id uuid NOT NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),FOREIGN KEY(organisation_id,consent_id) REFERENCES consents(organisation_id,id),UNIQUE(organisation_id,program_key,guest_id),UNIQUE(organisation_id,id)
);
CREATE TABLE loyalty_rewards (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,program_key uuid NOT NULL,reward_key uuid NOT NULL,version bigint NOT NULL CHECK(version>0),name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 kind text NOT NULL CHECK(kind IN ('COMPLIMENTARY_SERVICE','LATE_CHECKOUT','UPGRADE_ELIGIBILITY','FUTURE_STAY_BENEFIT')),points_cost bigint NOT NULL CHECK(points_cost BETWEEN 1 AND 100000000),state text NOT NULL CHECK(state IN ('ACTIVE','RETIRED')),validity_days integer NOT NULL CHECK(validity_days BETWEEN 1 AND 730),reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(reward_key,version),UNIQUE(organisation_id,id)
);
CREATE TABLE loyalty_transactions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,account_id uuid NOT NULL,kind text NOT NULL CHECK(kind IN ('ACCRUAL','REDEMPTION','REVERSAL','EXPIRY','RECOVERY_BENEFIT')),points bigint NOT NULL CHECK(points<>0 AND points BETWEEN -100000000 AND 100000000),
 source_kind text NOT NULL CHECK(source_kind IN ('STAY','REDEMPTION','TRANSACTION','RECOVERY_COMPENSATION','EXPIRY')),source_id uuid NOT NULL,source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),rule_ids uuid[] NOT NULL DEFAULT '{}',program_version bigint,expires_at timestamptz,evidence jsonb NOT NULL CHECK(jsonb_typeof(evidence)='object'),
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),CHECK(kind NOT IN ('ACCRUAL','RECOVERY_BENEFIT') OR points>0),CHECK(kind NOT IN ('REDEMPTION','EXPIRY') OR points<0),
 FOREIGN KEY(organisation_id,account_id) REFERENCES loyalty_accounts(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(account_id,kind,source_kind,source_id),UNIQUE(organisation_id,id)
);
CREATE INDEX loyalty_ledger ON loyalty_transactions(organisation_id,account_id,created_at,id);
CREATE TABLE loyalty_redemptions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,account_id uuid NOT NULL,reward_id uuid NOT NULL,transaction_id uuid NOT NULL,points bigint NOT NULL CHECK(points>0),request_reference uuid NOT NULL,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,account_id) REFERENCES loyalty_accounts(organisation_id,id),FOREIGN KEY(organisation_id,reward_id) REFERENCES loyalty_rewards(organisation_id,id),FOREIGN KEY(organisation_id,transaction_id) REFERENCES loyalty_transactions(organisation_id,id),UNIQUE(transaction_id),UNIQUE(account_id,request_reference),UNIQUE(organisation_id,id)
);
CREATE TABLE loyalty_entitlements (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,account_id uuid NOT NULL,redemption_id uuid NOT NULL,reward_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('AVAILABLE','CONSUMED','EXPIRED','REVOKED')),valid_until timestamptz NOT NULL,consumed_reference text,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,account_id) REFERENCES loyalty_accounts(organisation_id,id),FOREIGN KEY(organisation_id,redemption_id) REFERENCES loyalty_redemptions(organisation_id,id),FOREIGN KEY(organisation_id,reward_id) REFERENCES loyalty_rewards(organisation_id,id),UNIQUE(redemption_id),UNIQUE(organisation_id,id)
);
CREATE TABLE loyalty_reversals (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,account_id uuid NOT NULL,original_transaction_id uuid NOT NULL,reversal_transaction_id uuid NOT NULL,reason_code text NOT NULL CHECK(reason_code IN ('STAY_NO_LONGER_QUALIFYING','FINANCIAL_BACKING_REVERSED','FRAUD_REVIEW','REDEMPTION_CANCELLED')),source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,account_id) REFERENCES loyalty_accounts(organisation_id,id),FOREIGN KEY(organisation_id,original_transaction_id) REFERENCES loyalty_transactions(organisation_id,id),FOREIGN KEY(organisation_id,reversal_transaction_id) REFERENCES loyalty_transactions(organisation_id,id),UNIQUE(original_transaction_id),UNIQUE(reversal_transaction_id),UNIQUE(organisation_id,id)
);
CREATE TRIGGER loyalty_program_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON loyalty_programs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER loyalty_rule_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON loyalty_rules FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER loyalty_reward_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON loyalty_rewards FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER loyalty_transaction_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON loyalty_transactions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER loyalty_redemption_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON loyalty_redemptions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER loyalty_reversal_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON loyalty_reversals FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER loyalty_account_no_delete BEFORE DELETE OR TRUNCATE ON loyalty_accounts FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER loyalty_entitlement_no_delete BEFORE DELETE OR TRUNCATE ON loyalty_entitlements FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
-- Ledger guards: accrual needs an actual completed stay for the same guest;
-- redemptions cannot overdraw; reversals mirror exactly one original.
CREATE FUNCTION business_loyalty_ledger_guard() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE balance numeric;BEGIN
 PERFORM id FROM loyalty_accounts WHERE organisation_id=NEW.organisation_id AND id=NEW.account_id FOR UPDATE;
 IF NEW.kind='ACCRUAL' AND NOT EXISTS(SELECT 1 FROM loyalty_accounts a JOIN stay_records s ON s.organisation_id=a.organisation_id AND s.guest_id=a.guest_id AND s.id=NEW.source_id JOIN reservations r ON r.organisation_id=s.organisation_id AND r.id=s.reservation_id WHERE a.organisation_id=NEW.organisation_id AND a.id=NEW.account_id AND a.state='ACTIVE' AND NEW.source_kind='STAY' AND s.state='DEPARTED' AND r.state='COMPLETED') THEN RAISE EXCEPTION 'Loyalty accrual requires an actual completed stay of the enrolled guest' USING ERRCODE='23514';END IF;
 IF NEW.kind='EXPIRY' AND NOT EXISTS(SELECT 1 FROM loyalty_transactions o WHERE o.organisation_id=NEW.organisation_id AND o.account_id=NEW.account_id AND o.id=NEW.source_id AND NEW.source_kind='EXPIRY' AND o.kind IN ('ACCRUAL','RECOVERY_BENEFIT') AND o.expires_at<=now() AND -NEW.points<=o.points) THEN RAISE EXCEPTION 'Expiry must retire an actual expired earning entry' USING ERRCODE='23514';END IF;
 IF NEW.kind='REVERSAL' AND NOT EXISTS(SELECT 1 FROM loyalty_transactions o WHERE o.organisation_id=NEW.organisation_id AND o.account_id=NEW.account_id AND o.id=NEW.source_id AND NEW.source_kind='TRANSACTION' AND o.kind IN ('ACCRUAL','RECOVERY_BENEFIT','REDEMPTION') AND o.points=-NEW.points) THEN RAISE EXCEPTION 'A reversal mirrors exactly one original ledger entry' USING ERRCODE='23514';END IF;
 IF NEW.kind='RECOVERY_BENEFIT' AND NOT EXISTS(SELECT 1 FROM service_recovery_compensation_refs c JOIN service_recovery_cases k ON k.organisation_id=c.organisation_id AND k.id=c.case_id JOIN loyalty_accounts a ON a.organisation_id=k.organisation_id AND a.guest_id=k.guest_id WHERE c.organisation_id=NEW.organisation_id AND c.id=NEW.source_id AND NEW.source_kind='RECOVERY_COMPENSATION' AND a.id=NEW.account_id AND c.kind='LOYALTY_BENEFIT' AND c.points=NEW.points AND c.remedy_digest=k.remedy_digest AND k.state IN ('APPROVED','EXECUTION_PENDING')) THEN RAISE EXCEPTION 'A recovery benefit needs its exact approved compensation' USING ERRCODE='23514';END IF;
 IF NEW.kind='REDEMPTION' THEN
  SELECT coalesce(sum(points),0) INTO balance FROM loyalty_transactions WHERE organisation_id=NEW.organisation_id AND account_id=NEW.account_id;
  IF balance+NEW.points<0 OR NOT EXISTS(SELECT 1 FROM loyalty_accounts WHERE organisation_id=NEW.organisation_id AND id=NEW.account_id AND state='ACTIVE') THEN RAISE EXCEPTION 'Redemption requires an active account with sufficient unexpired points' USING ERRCODE='23514';END IF;
 END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER loyalty_ledger_guard BEFORE INSERT ON loyalty_transactions FOR EACH ROW EXECUTE FUNCTION business_loyalty_ledger_guard();
-- +goose Down
SELECT 1;
