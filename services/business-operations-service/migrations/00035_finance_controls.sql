-- +goose Up
-- Section 15 enterprise finance boundary. Business Operations records
-- obligation context, coding, budgets, approvals and configuration intent.
-- Payment Service alone executes collection, refunds, payouts, beneficiary
-- validation and ledger/settlement. Amounts are exact integer minor units.

CREATE TABLE financial_profiles (
 organisation_id uuid PRIMARY KEY REFERENCES organisations(id),id uuid NOT NULL UNIQUE CHECK(substring(id::text,15,1)='7'),default_currency text NOT NULL CHECK(default_currency~'^[A-Z]{3}$'),
 current_payout_destination_id uuid,state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE')),version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER financial_profiles_guard BEFORE UPDATE OR DELETE ON financial_profiles FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('');

CREATE TABLE fiscal_periods (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL REFERENCES organisations(id),code text NOT NULL CHECK(code~'^[A-Z0-9][A-Z0-9_-]{1,39}$'),
 starts_on date NOT NULL,ends_on date NOT NULL,state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','CLOSING','CLOSED')),closed_by uuid REFERENCES business_accounts(id),closed_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(ends_on>starts_on),CHECK(state<>'CLOSED' OR closed_at IS NOT NULL),UNIQUE(organisation_id,code),UNIQUE(organisation_id,id)
);
-- Periods never overlap (writers hold the organisation lock).
-- +goose StatementBegin
CREATE FUNCTION business_fiscal_overlap() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM fiscal_periods p WHERE p.organisation_id=NEW.organisation_id AND p.id<>NEW.id AND daterange(p.starts_on,p.ends_on,'[]') && daterange(NEW.starts_on,NEW.ends_on,'[]')) THEN RAISE EXCEPTION 'Fiscal periods cannot overlap' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER fiscal_periods_overlap BEFORE INSERT ON fiscal_periods FOR EACH ROW EXECUTE FUNCTION business_fiscal_overlap();
CREATE TRIGGER fiscal_periods_guard BEFORE UPDATE OR DELETE ON fiscal_periods FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('OPEN>CLOSING,CLOSING>OPEN,CLOSING>CLOSED');

CREATE TABLE cost_centres (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL REFERENCES organisations(id),parent_id uuid,property_id uuid,code text NOT NULL CHECK(code~'^[A-Z0-9][A-Z0-9_-]{1,39}$'),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),project_reference text NOT NULL DEFAULT '' CHECK(length(project_reference)<=200),state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RETIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(parent_id IS NULL OR parent_id<>id),FOREIGN KEY(organisation_id,parent_id) REFERENCES cost_centres(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),
 UNIQUE(organisation_id,code),UNIQUE(organisation_id,id)
);
CREATE TRIGGER cost_centres_guard BEFORE UPDATE OR DELETE ON cost_centres FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('ACTIVE>RETIRED');

CREATE TABLE budgets (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,cost_centre_id uuid NOT NULL,fiscal_period_id uuid NOT NULL,budget_version integer NOT NULL CHECK(budget_version>0),
 currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),total_minor bigint NOT NULL CHECK(total_minor BETWEEN 0 AND 1000000000000000),state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','SUPERSEDED','CLOSED')),
 approval_id uuid,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(state='DRAFT' OR approval_id IS NOT NULL),FOREIGN KEY(organisation_id,cost_centre_id) REFERENCES cost_centres(organisation_id,id),FOREIGN KEY(organisation_id,fiscal_period_id) REFERENCES fiscal_periods(organisation_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(cost_centre_id,fiscal_period_id,budget_version),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX budgets_one_active ON budgets(cost_centre_id,fiscal_period_id) WHERE state='ACTIVE';
CREATE TRIGGER budgets_guard BEFORE UPDATE OR DELETE ON budgets FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>ACTIVE,ACTIVE>SUPERSEDED,ACTIVE>CLOSED');
CREATE TABLE budget_lines (
 organisation_id uuid NOT NULL,budget_id uuid NOT NULL,line_no integer NOT NULL CHECK(line_no BETWEEN 1 AND 200),
 category text NOT NULL CHECK(category IN ('OPERATIONS','MAINTENANCE','MARKETING','HOUSEKEEPING','UTILITIES','CAPEX','PROFESSIONAL_SERVICES','OTHER')),
 amount_minor bigint NOT NULL CHECK(amount_minor BETWEEN 0 AND 1000000000000000),approval_threshold_minor bigint NOT NULL CHECK(approval_threshold_minor>=0),
 PRIMARY KEY(budget_id,line_no),FOREIGN KEY(organisation_id,budget_id) REFERENCES budgets(organisation_id,id)
);
CREATE TRIGGER budget_lines_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON budget_lines FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_budget_total_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.state='ACTIVE' AND (SELECT coalesce(sum(amount_minor),0) FROM budget_lines WHERE budget_id=NEW.id)<>NEW.total_minor THEN RAISE EXCEPTION 'Budget total must equal its lines' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER budgets_total BEFORE UPDATE ON budgets FOR EACH ROW EXECUTE FUNCTION business_budget_total_guard();

CREATE TABLE financial_obligations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid,cost_centre_id uuid NOT NULL,
 direction text NOT NULL CHECK(direction IN ('RECEIVABLE','PAYABLE')),category text NOT NULL CHECK(length(category) BETWEEN 2 AND 60),
 counterparty_reference text NOT NULL CHECK(length(counterparty_reference) BETWEEN 3 AND 200),source_type text NOT NULL CHECK(source_type IN ('CONTRACT','EXPENSE_REQUEST','CHANNEL_SETTLEMENT','FOLIO','MANUAL')),source_reference text NOT NULL CHECK(length(source_reference) BETWEEN 3 AND 300),
 original_amount_minor bigint NOT NULL CHECK(original_amount_minor>0),amount_minor bigint NOT NULL CHECK(amount_minor>=0),currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),tax_policy_reference text NOT NULL DEFAULT '' CHECK(length(tax_policy_reference)<=200),
 due_on date NOT NULL,state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','APPROVED','PAYMENT_REQUESTED','UNKNOWN_PENDING_READBACK','SETTLED','CANCELLED','DISPUTED')),
 approval_id uuid,payment_reference uuid,settled_minor bigint NOT NULL DEFAULT 0 CHECK(settled_minor>=0),settlement_state text NOT NULL DEFAULT 'NOT_REQUESTED',last_readback_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(state IN ('DRAFT','CANCELLED') OR approval_id IS NOT NULL OR source_type='EXPENSE_REQUEST'),CHECK(settled_minor<=amount_minor),CHECK(state<>'SETTLED' OR settled_minor=amount_minor AND payment_reference IS NOT NULL),
 FOREIGN KEY(organisation_id,cost_centre_id) REFERENCES cost_centres(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 UNIQUE(organisation_id,source_type,source_reference),UNIQUE(organisation_id,id)
);
CREATE TRIGGER financial_obligations_guard BEFORE UPDATE OR DELETE ON financial_obligations FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>APPROVED,DRAFT>CANCELLED,APPROVED>APPROVED,APPROVED>PAYMENT_REQUESTED,APPROVED>CANCELLED,APPROVED>DISPUTED,PAYMENT_REQUESTED>PAYMENT_REQUESTED,PAYMENT_REQUESTED>SETTLED,PAYMENT_REQUESTED>UNKNOWN_PENDING_READBACK,PAYMENT_REQUESTED>DISPUTED,UNKNOWN_PENDING_READBACK>PAYMENT_REQUESTED,UNKNOWN_PENDING_READBACK>SETTLED,UNKNOWN_PENDING_READBACK>DISPUTED,DISPUTED>APPROVED,DISPUTED>CANCELLED,SETTLED>DISPUTED');
CREATE TABLE obligation_adjustments (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,obligation_id uuid NOT NULL,obligation_version bigint NOT NULL,delta_minor bigint NOT NULL CHECK(delta_minor<>0),
 resulting_amount_minor bigint NOT NULL CHECK(resulting_amount_minor>=0),approval_id uuid NOT NULL,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,obligation_id) REFERENCES financial_obligations(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(obligation_id,obligation_version)
);
CREATE TRIGGER obligation_adjustments_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON obligation_adjustments FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- Conservation: the live amount always equals original plus adjustments.
-- +goose StatementBegin
CREATE FUNCTION business_obligation_conservation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.amount_minor<>NEW.original_amount_minor+(SELECT coalesce(sum(delta_minor),0) FROM obligation_adjustments WHERE obligation_id=NEW.id) THEN RAISE EXCEPTION 'Obligation amount must equal original plus immutable adjustments' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER financial_obligations_conservation AFTER INSERT OR UPDATE ON financial_obligations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_obligation_conservation();

CREATE TABLE expense_requests (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,cost_centre_id uuid NOT NULL,budget_id uuid NOT NULL,budget_line_no integer NOT NULL,property_id uuid,
 description text NOT NULL CHECK(length(description) BETWEEN 5 AND 1000),counterparty_reference text NOT NULL CHECK(length(counterparty_reference) BETWEEN 3 AND 200),
 amount_minor bigint NOT NULL CHECK(amount_minor>0),currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),requires_approval boolean NOT NULL,
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','APPROVED','OBLIGATION_CREATED','EXPIRED','CANCELLED')),approval_id uuid,approved_amount_minor bigint,expires_at timestamptz,obligation_id uuid,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(state NOT IN ('APPROVED','OBLIGATION_CREATED') OR approved_amount_minor=amount_minor AND expires_at IS NOT NULL),CHECK(NOT requires_approval OR state IN ('DRAFT','CANCELLED','EXPIRED') OR approval_id IS NOT NULL),
 CHECK((state='OBLIGATION_CREATED')=(obligation_id IS NOT NULL)),FOREIGN KEY(organisation_id,cost_centre_id) REFERENCES cost_centres(organisation_id,id),FOREIGN KEY(budget_id,budget_line_no) REFERENCES budget_lines(budget_id,line_no),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),FOREIGN KEY(organisation_id,obligation_id) REFERENCES financial_obligations(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE TRIGGER expense_requests_guard BEFORE UPDATE OR DELETE ON expense_requests FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>APPROVED,DRAFT>CANCELLED,APPROVED>OBLIGATION_CREATED,APPROVED>EXPIRED,APPROVED>CANCELLED');
-- Approved amount and identity are immutable once approved; material
-- changes require a new request and new approval.
-- +goose StatementBegin
CREATE FUNCTION business_expense_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.state<>'DRAFT' AND (NEW.amount_minor,NEW.currency,NEW.budget_id,NEW.budget_line_no,NEW.cost_centre_id,NEW.counterparty_reference) IS DISTINCT FROM (OLD.amount_minor,OLD.currency,OLD.budget_id,OLD.budget_line_no,OLD.cost_centre_id,OLD.counterparty_reference) THEN
  RAISE EXCEPTION 'Approved expense terms are immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER expense_requests_immutable BEFORE UPDATE ON expense_requests FOR EACH ROW EXECUTE FUNCTION business_expense_immutable();

CREATE TABLE payout_destination_refs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL REFERENCES organisations(id),payment_authority_id uuid NOT NULL,label text NOT NULL CHECK(length(label) BETWEEN 1 AND 120),
 currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),state text NOT NULL DEFAULT 'PENDING_VERIFICATION' CHECK(state IN ('PENDING_VERIFICATION','ACTIVE','REJECTED','REVOKED')),
 payment_validation text NOT NULL DEFAULT 'UNVERIFIED' CHECK(payment_validation IN ('UNVERIFIED','VERIFIED','REJECTED','EXPIRED')),validated_at timestamptz,validation_expires_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(state<>'ACTIVE' OR payment_validation='VERIFIED' AND validated_at IS NOT NULL),UNIQUE(organisation_id,payment_authority_id),UNIQUE(organisation_id,id)
);
CREATE TRIGGER payout_destination_refs_guard BEFORE UPDATE OR DELETE ON payout_destination_refs FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('PENDING_VERIFICATION>ACTIVE,PENDING_VERIFICATION>REJECTED,PENDING_VERIFICATION>PENDING_VERIFICATION,ACTIVE>ACTIVE,ACTIVE>REVOKED,ACTIVE>PENDING_VERIFICATION,REJECTED>PENDING_VERIFICATION');
ALTER TABLE financial_profiles ADD FOREIGN KEY(organisation_id,current_payout_destination_id) REFERENCES payout_destination_refs(organisation_id,id);

CREATE TABLE payout_change_requests (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL REFERENCES organisations(id),from_destination_id uuid,to_destination_id uuid NOT NULL,
 state text NOT NULL DEFAULT 'COOLING_OFF' CHECK(state IN ('COOLING_OFF','EFFECTIVE','BLOCKED','CANCELLED')),approval_id uuid NOT NULL,cooling_off_until timestamptz NOT NULL,effective_at timestamptz,blocked_reason text NOT NULL DEFAULT '',
 reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(cooling_off_until>created_at),CHECK(state<>'EFFECTIVE' OR effective_at>=cooling_off_until),
 FOREIGN KEY(organisation_id,from_destination_id) REFERENCES payout_destination_refs(organisation_id,id),FOREIGN KEY(organisation_id,to_destination_id) REFERENCES payout_destination_refs(organisation_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX payout_change_one_live ON payout_change_requests(organisation_id) WHERE state='COOLING_OFF';
CREATE TRIGGER payout_change_requests_guard BEFORE UPDATE OR DELETE ON payout_change_requests FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('COOLING_OFF>EFFECTIVE,COOLING_OFF>BLOCKED,COOLING_OFF>CANCELLED');

CREATE TABLE revenue_split_rules (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,contract_reference text NOT NULL CHECK(length(contract_reference) BETWEEN 3 AND 200),
 currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),shares jsonb NOT NULL CHECK(jsonb_typeof(shares)='array' AND jsonb_array_length(shares) BETWEEN 1 AND 20),shares_digest text NOT NULL CHECK(shares_digest~'^[a-f0-9]{64}$'),
 effective_from date NOT NULL,effective_to date,state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','SUPERSEDED','RETIRED')),approval_id uuid,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(effective_to IS NULL OR effective_to>effective_from),CHECK(state='DRAFT' OR approval_id IS NOT NULL),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE UNIQUE INDEX revenue_split_one_active ON revenue_split_rules(organisation_id,property_id,contract_reference) WHERE state='ACTIVE';
CREATE TRIGGER revenue_split_rules_guard BEFORE UPDATE OR DELETE ON revenue_split_rules FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>ACTIVE,DRAFT>RETIRED,ACTIVE>SUPERSEDED,ACTIVE>RETIRED');
-- +goose StatementBegin
CREATE FUNCTION business_split_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.shares,NEW.shares_digest,NEW.currency,NEW.contract_reference,NEW.effective_from) IS DISTINCT FROM (OLD.shares,OLD.shares_digest,OLD.currency,OLD.contract_reference,OLD.effective_from) THEN RAISE EXCEPTION 'Split terms are immutable; create a new rule' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER revenue_split_rules_immutable BEFORE UPDATE ON revenue_split_rules FOR EACH ROW EXECUTE FUNCTION business_split_immutable();

CREATE TABLE channel_settlement_expectations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,reservation_id uuid,
 provider_remittance_reference text NOT NULL CHECK(length(provider_remittance_reference) BETWEEN 3 AND 200),collection_model text NOT NULL CHECK(collection_model IN ('CHANNEL_COLLECT','VIRTUAL_CARD','PLATFORM_COLLECT')),
 gross_minor bigint NOT NULL CHECK(gross_minor>=0),commission_minor bigint NOT NULL CHECK(commission_minor>=0),promotion_minor bigint NOT NULL CHECK(promotion_minor>=0),fees_minor bigint NOT NULL CHECK(fees_minor>=0),tax_minor bigint NOT NULL CHECK(tax_minor>=0),
 expected_net_minor bigint NOT NULL,currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),expected_on date NOT NULL,
 state text NOT NULL DEFAULT 'EXPECTED' CHECK(state IN ('EXPECTED','MATCHED','MISMATCH','LATE','RESOLVED','CANCELLED')),actual_net_minor bigint,actual_source_reference text,reconciled_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(expected_net_minor=gross_minor-commission_minor-promotion_minor-fees_minor-tax_minor AND expected_net_minor>=0),CHECK(state NOT IN ('MATCHED','MISMATCH') OR actual_net_minor IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),
 UNIQUE(organisation_id,connection_id,provider_remittance_reference),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TRIGGER channel_settlement_expectations_guard BEFORE UPDATE OR DELETE ON channel_settlement_expectations FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('EXPECTED>MATCHED,EXPECTED>MISMATCH,EXPECTED>LATE,EXPECTED>CANCELLED,LATE>MATCHED,LATE>MISMATCH,LATE>RESOLVED,MISMATCH>RESOLVED');

CREATE TABLE financial_exception_cases (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL REFERENCES organisations(id),property_id uuid,
 kind text NOT NULL CHECK(kind IN ('SETTLEMENT_MISMATCH','SETTLEMENT_LATE','PAYMENT_UNKNOWN','PAYMENT_REVERSAL','BUDGET_OVERRUN','DUPLICATE_SETTLEMENT')),
 source_type text NOT NULL CHECK(source_type IN ('CHANNEL_SETTLEMENT','FINANCIAL_OBLIGATION','EXPENSE_REQUEST','BUDGET')),source_id uuid NOT NULL,
 amount_delta_minor bigint,currency text CHECK(currency IS NULL OR currency~'^[A-Z]{3}$'),state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','INVESTIGATING','RESOLVED','WRITTEN_OFF')),
 resolution text NOT NULL DEFAULT '' CHECK(length(resolution)<=2000),version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK(state NOT IN ('RESOLVED','WRITTEN_OFF') OR length(resolution)>=5),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX financial_exceptions_one_open ON financial_exception_cases(organisation_id,source_type,source_id,kind) WHERE state IN ('OPEN','INVESTIGATING');
CREATE TRIGGER financial_exception_cases_guard BEFORE UPDATE OR DELETE ON financial_exception_cases FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('OPEN>INVESTIGATING,OPEN>RESOLVED,OPEN>WRITTEN_OFF,INVESTIGATING>RESOLVED,INVESTIGATING>WRITTEN_OFF');

-- +goose Down
SELECT 1;
