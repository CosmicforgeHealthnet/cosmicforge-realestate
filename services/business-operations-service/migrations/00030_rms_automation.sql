-- +goose Up
-- Section 8 RMS automation. APPROVAL_AUTOMATION and AUTOPILOT_WITH_LIMITS run
-- only under an explicit, independently approved activation record. The
-- service identity is recorded as such and never replaces a human approver.
-- Canonical rates, restrictions and promotions remain the only pricing authority.

-- Service-originated proposals and rate versions carry the explicit service
-- identity instead of borrowing a human account.
ALTER TABLE pricing_recommendations ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE pricing_recommendations ADD COLUMN created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE pricing_recommendations ADD CONSTRAINT pricing_recommendation_actor_identity CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service');
ALTER TABLE pricing_recommendations ADD COLUMN automation_run_id uuid;
ALTER TABLE rate_plan_versions ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE rate_plan_versions ADD COLUMN created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE rate_plan_versions ADD CONSTRAINT rate_plan_version_actor_identity CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service');

CREATE TABLE rms_automation_activations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,branch_id uuid NOT NULL,pool_id uuid NOT NULL,rate_plan_id uuid NOT NULL,policy_id uuid NOT NULL,
 mode text NOT NULL CHECK(mode IN ('APPROVAL_AUTOMATION','AUTOPILOT_WITH_LIMITS')),limits jsonb NOT NULL CHECK(jsonb_typeof(limits)='object' AND octet_length(limits::text)<=65536),limits_digest text NOT NULL CHECK(limits_digest~'^[a-f0-9]{64}$'),
 sponsor_account_id uuid NOT NULL REFERENCES business_accounts(id),sponsor_membership_id uuid NOT NULL,sponsor_role_id uuid NOT NULL,approval_id uuid NOT NULL,
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','PAUSED','REVOKED','EXPIRED')),state_reason text NOT NULL DEFAULT '' CHECK(length(state_reason)<=1000),
 effective_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,next_run_at timestamptz NOT NULL,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(expires_at>effective_at),CHECK(expires_at<=effective_at+interval '366 days'),
 FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES rms_policies(organisation_id,property_id,id),FOREIGN KEY(organisation_id,branch_id) REFERENCES branches(organisation_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE UNIQUE INDEX rms_automation_one_live ON rms_automation_activations(organisation_id,rate_plan_id) WHERE state IN ('ACTIVE','PAUSED');
CREATE INDEX rms_automation_due ON rms_automation_activations(state,next_run_at,id);

CREATE TABLE rms_automation_activation_history (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,activation_id uuid NOT NULL,activation_version bigint NOT NULL,prior_state text,state text NOT NULL,
 reason text NOT NULL,approval_id uuid,actor_id uuid REFERENCES business_accounts(id),actor_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(actor_id IS NOT NULL OR actor_service='business-operations-service'),FOREIGN KEY(organisation_id,property_id,activation_id) REFERENCES rms_automation_activations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(activation_id,activation_version)
);
CREATE TRIGGER rms_automation_activation_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rms_automation_activation_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- +goose StatementBegin
CREATE FUNCTION business_rms_activation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'RMS automation activations are retained' USING ERRCODE='23514';END IF;
 IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.branch_id,NEW.pool_id,NEW.rate_plan_id,NEW.policy_id,NEW.mode,NEW.limits,NEW.limits_digest,NEW.sponsor_account_id,NEW.sponsor_membership_id,NEW.sponsor_role_id,NEW.approval_id,NEW.effective_at,NEW.expires_at,NEW.reason,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.branch_id,OLD.pool_id,OLD.rate_plan_id,OLD.policy_id,OLD.mode,OLD.limits,OLD.limits_digest,OLD.sponsor_account_id,OLD.sponsor_membership_id,OLD.sponsor_role_id,OLD.approval_id,OLD.effective_at,OLD.expires_at,OLD.reason,OLD.created_at) OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'Approved automation limits are immutable; create a new activation' USING ERRCODE='23514';END IF;
 IF NEW.state<>OLD.state AND NOT ((OLD.state='ACTIVE' AND NEW.state IN ('PAUSED','REVOKED','EXPIRED')) OR (OLD.state='PAUSED' AND NEW.state IN ('ACTIVE','REVOKED','EXPIRED'))) THEN RAISE EXCEPTION 'Invalid RMS automation activation transition' USING ERRCODE='23514';END IF;
 IF NEW.state<>OLD.state AND NOT EXISTS(SELECT 1 FROM rms_automation_activation_history h WHERE h.activation_id=NEW.id AND h.activation_version=NEW.version AND h.prior_state=OLD.state AND h.state=NEW.state) THEN RAISE EXCEPTION 'Immutable activation history required' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER rms_automation_activation_guard BEFORE UPDATE OR DELETE ON rms_automation_activations FOR EACH ROW EXECUTE FUNCTION business_rms_activation_guard();

CREATE TABLE rms_automation_runs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,activation_id uuid NOT NULL,mode text NOT NULL CHECK(mode IN ('APPROVAL_AUTOMATION','AUTOPILOT_WITH_LIMITS')),
 trigger text NOT NULL CHECK(trigger IN ('SCHEDULED','MANUAL')),
 stage text NOT NULL DEFAULT 'QUEUED' CHECK(stage IN ('QUEUED','SOURCE_CAPTURED','FORECAST_READY','RECOMMENDED','APPROVAL_PENDING','APPROVED','APPLIED','NO_CHANGE','BLOCKED','REJECTED','EXPIRED','FAILED')),
 snapshot_id uuid,forecast_id uuid,recommendation_id uuid,approval_id uuid,change_digest text CHECK(change_digest IS NULL OR change_digest~'^[a-f0-9]{64}$'),
 blocked_code text NOT NULL DEFAULT '',evidence jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(evidence)='object' AND octet_length(evidence::text)<=262144),
 requested_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(requested_by IS NOT NULL OR created_by_service='business-operations-service'),
 FOREIGN KEY(organisation_id,property_id,activation_id) REFERENCES rms_automation_activations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,snapshot_id) REFERENCES demand_snapshots(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,forecast_id) REFERENCES forecast_runs(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,recommendation_id) REFERENCES pricing_recommendations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE UNIQUE INDEX rms_automation_one_open_run ON rms_automation_runs(activation_id) WHERE stage IN ('QUEUED','SOURCE_CAPTURED','FORECAST_READY','RECOMMENDED','APPROVAL_PENDING','APPROVED');
CREATE INDEX rms_automation_run_scope ON rms_automation_runs(organisation_id,property_id,created_at DESC,id DESC);
CREATE UNIQUE INDEX rms_automation_run_recommendation ON rms_automation_runs(recommendation_id) WHERE recommendation_id IS NOT NULL;
ALTER TABLE pricing_recommendations ADD CONSTRAINT pricing_recommendation_automation_run FOREIGN KEY(organisation_id,property_id,automation_run_id) REFERENCES rms_automation_runs(organisation_id,property_id,id);

-- +goose StatementBegin
CREATE FUNCTION business_rms_run_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'RMS automation runs are retained' USING ERRCODE='23514';END IF;
 IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.activation_id,NEW.mode,NEW.trigger,NEW.requested_by,NEW.created_by_service,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.activation_id,OLD.mode,OLD.trigger,OLD.requested_by,OLD.created_by_service,OLD.created_at) OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'Run identity is immutable' USING ERRCODE='23514';END IF;
 IF OLD.stage IN ('APPLIED','NO_CHANGE','BLOCKED','REJECTED','EXPIRED','FAILED') THEN RAISE EXCEPTION 'Terminal automation runs are immutable' USING ERRCODE='23514';END IF;
 IF (OLD.snapshot_id IS NOT NULL AND NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id) OR (OLD.forecast_id IS NOT NULL AND NEW.forecast_id IS DISTINCT FROM OLD.forecast_id) OR (OLD.recommendation_id IS NOT NULL AND NEW.recommendation_id IS DISTINCT FROM OLD.recommendation_id) OR (OLD.approval_id IS NOT NULL AND NEW.approval_id IS DISTINCT FROM OLD.approval_id) OR (OLD.change_digest IS NOT NULL AND NEW.change_digest IS DISTINCT FROM OLD.change_digest) THEN RAISE EXCEPTION 'Run lineage is append-only' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER rms_automation_run_guard BEFORE UPDATE OR DELETE ON rms_automation_runs FOR EACH ROW EXECUTE FUNCTION business_rms_run_guard();

CREATE TABLE rms_automation_run_stages (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,run_id uuid NOT NULL,stage text NOT NULL,code text NOT NULL DEFAULT '',
 evidence jsonb NOT NULL CHECK(jsonb_typeof(evidence)='object' AND octet_length(evidence::text)<=262144),actor_id uuid REFERENCES business_accounts(id),actor_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(actor_id IS NOT NULL OR actor_service='business-operations-service'),FOREIGN KEY(organisation_id,property_id,run_id) REFERENCES rms_automation_runs(organisation_id,property_id,id)
);
CREATE INDEX rms_automation_run_stage_scope ON rms_automation_run_stages(organisation_id,run_id,created_at,id);
CREATE TRIGGER rms_automation_run_stages_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rms_automation_run_stages FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- Durable leased RMS jobs. A lease generation fences delayed replicas; the
-- dedupe key makes scheduling idempotent across restarts.
CREATE TABLE rms_automation_jobs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,activation_id uuid,run_id uuid,recommendation_id uuid,
 kind text NOT NULL CHECK(kind IN ('CAPTURE_DEMAND','GENERATE_FORECAST','GENERATE_RECOMMENDATION','SUBMIT_APPROVAL','MONITOR_APPROVAL','APPLY_RATE','MEASURE_OUTCOME','CHECK_FRESHNESS','DETECT_DRIFT','MONITOR_LIMITS','RECONCILE_PUBLICATION')),
 dedupe_key text NOT NULL CHECK(length(dedupe_key) BETWEEN 1 AND 300),state text NOT NULL DEFAULT 'QUEUED' CHECK(state IN ('QUEUED','RUNNING','RETRY','SUCCEEDED','BLOCKED','QUARANTINED','CANCELLED')),
 available_at timestamptz NOT NULL DEFAULT now(),attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),lease_owner text,lease_generation bigint NOT NULL DEFAULT 0,lease_expires_at timestamptz,last_error text,
 result jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(result)='object'),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,property_id,activation_id) REFERENCES rms_automation_activations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,run_id) REFERENCES rms_automation_runs(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,recommendation_id) REFERENCES pricing_recommendations(organisation_id,property_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,dedupe_key)
);
CREATE INDEX rms_automation_job_work ON rms_automation_jobs(state,available_at,id);
CREATE INDEX rms_automation_job_scope ON rms_automation_jobs(organisation_id,property_id,created_at DESC,id DESC);
CREATE TRIGGER rms_automation_jobs_version_guard BEFORE INSERT OR UPDATE ON rms_automation_jobs FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();

CREATE TABLE rms_outcome_measurements (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,recommendation_id uuid NOT NULL,run_id uuid,
 status text NOT NULL CHECK(status IN ('MEASURED','PENDING_STAY_DATES','INSUFFICIENT_DATA')),measured_at timestamptz NOT NULL,source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),
 result jsonb NOT NULL CHECK(jsonb_typeof(result)='object' AND octet_length(result::text)<=262144),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),FOREIGN KEY(organisation_id,property_id,recommendation_id) REFERENCES pricing_recommendations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,run_id) REFERENCES rms_automation_runs(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(recommendation_id,source_digest)
);
CREATE INDEX rms_outcome_scope ON rms_outcome_measurements(organisation_id,property_id,created_at DESC,id DESC);
CREATE TRIGGER rms_outcome_measurements_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rms_outcome_measurements FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE rms_monitoring_findings (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,activation_id uuid,recommendation_id uuid,
 kind text NOT NULL CHECK(kind IN ('FORECAST_DRIFT','SOURCE_STALE','MODEL_INELIGIBLE','POLICY_LIMIT_BREACH','PUBLICATION_UNKNOWN','OUTCOME_DEGRADED','AUTHORITY_REVOKED','POLICY_EXCEPTION')),
 severity text NOT NULL CHECK(severity IN ('INFO','WARNING','CRITICAL')),state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','RESOLVED')),
 action_taken text NOT NULL DEFAULT 'NONE' CHECK(action_taken IN ('NONE','AUTOMATION_PAUSED','ROLLBACK_REVIEW_QUEUED')),dedupe_key text NOT NULL CHECK(length(dedupe_key) BETWEEN 1 AND 300),
 evidence jsonb NOT NULL CHECK(jsonb_typeof(evidence)='object' AND octet_length(evidence::text)<=262144),resolution text,resolved_by uuid REFERENCES business_accounts(id),resolved_by_service text,resolved_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK((state='RESOLVED')=(resolved_at IS NOT NULL AND resolution IS NOT NULL)),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,property_id,activation_id) REFERENCES rms_automation_activations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,recommendation_id) REFERENCES pricing_recommendations(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,dedupe_key)
);
CREATE INDEX rms_finding_scope ON rms_monitoring_findings(organisation_id,property_id,state,created_at DESC,id DESC);
CREATE TRIGGER rms_monitoring_findings_version_guard BEFORE INSERT OR UPDATE ON rms_monitoring_findings FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();

-- Governed external/market inputs. Nothing here is fabricated: every row carries
-- its source and licence reference and is used only where present.
CREATE TABLE rms_market_inputs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('HOLIDAY','LOCAL_EVENT','COMPETITOR_RATE','MARKET_DEMAND_INDEX','CHANNEL_COMMISSION','OPERATING_COST')),channel text NOT NULL DEFAULT '',
 date_from date NOT NULL,date_to date NOT NULL,value_minor bigint,value_bps bigint,currency text,label text NOT NULL CHECK(length(label) BETWEEN 1 AND 200),
 source_reference text NOT NULL CHECK(length(source_reference) BETWEEN 5 AND 300),licence_reference text NOT NULL CHECK(length(licence_reference) BETWEEN 5 AND 300),evidence_digest text NOT NULL CHECK(evidence_digest~'^[a-f0-9]{64}$'),
 observed_at timestamptz NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(date_to>date_from),CHECK(date_to<=date_from+366),CHECK(value_minor IS NULL OR value_minor>=0),CHECK(value_bps IS NULL OR value_bps BETWEEN -100000 AND 100000),CHECK(currency IS NULL OR currency~'^[A-Z]{3}$'),
 CHECK(kind NOT IN ('COMPETITOR_RATE','OPERATING_COST') OR (value_minor IS NOT NULL AND currency IS NOT NULL)),CHECK(kind NOT IN ('CHANNEL_COMMISSION','MARKET_DEMAND_INDEX') OR value_bps IS NOT NULL),CHECK(kind<>'CHANNEL_COMMISSION' OR channel<>''),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,kind,channel,date_from,date_to,evidence_digest)
);
CREATE INDEX rms_market_input_scope ON rms_market_inputs(organisation_id,property_id,kind,date_from,date_to);
CREATE TRIGGER rms_market_inputs_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rms_market_inputs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE rms_analytics_snapshots (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,scope_kind text NOT NULL CHECK(scope_kind IN ('PROPERTY','BRANCH','PORTFOLIO')),scope_id uuid NOT NULL,property_ids uuid[] NOT NULL CHECK(cardinality(property_ids) BETWEEN 1 AND 500),
 model_id uuid NOT NULL REFERENCES forecast_models(id),date_from date NOT NULL,date_to date NOT NULL,as_of timestamptz NOT NULL,input_digest text NOT NULL CHECK(input_digest~'^[a-f0-9]{64}$'),result_digest text NOT NULL CHECK(result_digest~'^[a-f0-9]{64}$'),
 result jsonb NOT NULL CHECK(jsonb_typeof(result)='object' AND octet_length(result::text)<=2097152),provenance jsonb NOT NULL CHECK(jsonb_typeof(provenance)='object'),
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(date_to>date_from),CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),FOREIGN KEY(organisation_id) REFERENCES organisations(id),UNIQUE(organisation_id,id)
);
CREATE INDEX rms_analytics_scope ON rms_analytics_snapshots(organisation_id,scope_kind,scope_id,created_at DESC,id DESC);
CREATE TRIGGER rms_analytics_snapshots_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rms_analytics_snapshots FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE rms_advanced_recommendations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,pool_id uuid NOT NULL,rate_plan_id uuid NOT NULL,policy_id uuid NOT NULL,forecast_id uuid NOT NULL,analytics_id uuid,
 kind text NOT NULL CHECK(kind IN ('DERIVED_RATE','RATE_RANGE','MIN_LOS','MAX_LOS','BOOKING_WINDOW','EARLY_BOOKER','LAST_MINUTE','LONG_STAY','CHANNEL_ALLOCATION','CHANNEL_PRICING','OVERBOOKING')),
 application text NOT NULL CHECK(application IN ('CANONICAL_RATE_VERSION','CANONICAL_RESTRICTION','CANONICAL_PROMOTION','ADVISORY')),advisory_reason text NOT NULL DEFAULT '',
 date_from date NOT NULL,date_to date NOT NULL,target_plan_version bigint NOT NULL CHECK(target_plan_version>0),change_digest text NOT NULL CHECK(change_digest~'^[a-f0-9]{64}$'),
 proposal jsonb NOT NULL CHECK(jsonb_typeof(proposal)='object' AND octet_length(proposal::text)<=262144),expires_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','PENDING_APPROVAL','APPROVED','APPLIED','REJECTED','EXPIRED','ACKNOWLEDGED')),approval_id uuid,applied_reference jsonb NOT NULL DEFAULT '{}',
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(date_to>date_from),CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),CHECK((application='ADVISORY')=(advisory_reason<>'')),CHECK(application<>'ADVISORY' OR state IN ('DRAFT','ACKNOWLEDGED','EXPIRED','REJECTED')),
 CHECK(state NOT IN ('APPROVED','APPLIED') OR approval_id IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES rms_policies(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,pool_id,forecast_id) REFERENCES forecast_runs(organisation_id,property_id,pool_id,id),
 FOREIGN KEY(organisation_id,analytics_id) REFERENCES rms_analytics_snapshots(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX rms_advanced_scope ON rms_advanced_recommendations(organisation_id,property_id,state,created_at DESC,id DESC);
CREATE TABLE rms_advanced_decisions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,recommendation_id uuid NOT NULL,previous_version bigint NOT NULL,prior_state text NOT NULL,state text NOT NULL,action text NOT NULL,
 actor_id uuid NOT NULL REFERENCES business_accounts(id),approval_id uuid,reason text NOT NULL,evidence jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,recommendation_id) REFERENCES rms_advanced_recommendations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(recommendation_id,previous_version)
);
CREATE TRIGGER rms_advanced_decisions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rms_advanced_decisions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- +goose StatementBegin
CREATE FUNCTION business_rms_advanced_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Advanced recommendations are retained' USING ERRCODE='23514';END IF;
 IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.pool_id,NEW.rate_plan_id,NEW.policy_id,NEW.forecast_id,NEW.analytics_id,NEW.kind,NEW.application,NEW.advisory_reason,NEW.date_from,NEW.date_to,NEW.target_plan_version,NEW.change_digest,NEW.proposal,NEW.expires_at,NEW.created_by,NEW.created_by_service,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.pool_id,OLD.rate_plan_id,OLD.policy_id,OLD.forecast_id,OLD.analytics_id,OLD.kind,OLD.application,OLD.advisory_reason,OLD.date_from,OLD.date_to,OLD.target_plan_version,OLD.change_digest,OLD.proposal,OLD.expires_at,OLD.created_by,OLD.created_by_service,OLD.created_at) OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'Immutable advanced proposal or invalid version' USING ERRCODE='23514';END IF;
 IF NOT ((OLD.state='DRAFT' AND NEW.state IN ('PENDING_APPROVAL','REJECTED','EXPIRED','ACKNOWLEDGED')) OR (OLD.state='PENDING_APPROVAL' AND NEW.state IN ('APPROVED','REJECTED','EXPIRED')) OR (OLD.state='APPROVED' AND NEW.state IN ('APPLIED','REJECTED','EXPIRED'))) THEN RAISE EXCEPTION 'Invalid advanced recommendation transition' USING ERRCODE='23514';END IF;
 IF NOT EXISTS(SELECT 1 FROM rms_advanced_decisions d WHERE d.recommendation_id=NEW.id AND d.previous_version=OLD.version AND d.prior_state=OLD.state AND d.state=NEW.state) THEN RAISE EXCEPTION 'Immutable advanced decision required' USING ERRCODE='23514';END IF;
 IF NEW.applied_reference IS DISTINCT FROM OLD.applied_reference AND NOT (OLD.state='APPROVED' AND NEW.state='APPLIED') THEN RAISE EXCEPTION 'Application lineage changes only when applied' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER rms_advanced_guard BEFORE UPDATE OR DELETE ON rms_advanced_recommendations FOR EACH ROW EXECUTE FUNCTION business_rms_advanced_guard();

-- Pricing proposals keep their automation lineage and service identity immutable.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION business_pricing_recommendation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Pricing recommendation identity is retained' USING ERRCODE='23514';END IF;
 IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.pool_id,NEW.rate_plan_id,NEW.policy_id,NEW.forecast_id,NEW.date_from,NEW.date_to,NEW.target_plan_version,NEW.change_digest,NEW.snapshot,NEW.expires_at,NEW.created_by,NEW.created_by_service,NEW.automation_run_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.pool_id,OLD.rate_plan_id,OLD.policy_id,OLD.forecast_id,OLD.date_from,OLD.date_to,OLD.target_plan_version,OLD.change_digest,OLD.snapshot,OLD.expires_at,OLD.created_by,OLD.created_by_service,OLD.automation_run_id,OLD.created_at) OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'Immutable pricing proposal or invalid version' USING ERRCODE='23514';END IF;
 IF NOT ((OLD.state='DRAFT' AND NEW.state IN ('PENDING_APPROVAL','OVERRIDDEN','EXPIRED','REJECTED')) OR (OLD.state='PENDING_APPROVAL' AND NEW.state IN ('APPROVED','OVERRIDDEN','EXPIRED','REJECTED')) OR (OLD.state='APPROVED' AND NEW.state IN ('APPLIED','EXPIRED','REJECTED')) OR (OLD.state='APPLIED' AND NEW.state='ROLLED_BACK')) THEN RAISE EXCEPTION 'Invalid pricing recommendation transition' USING ERRCODE='23514';END IF;
 IF NOT EXISTS(SELECT 1 FROM pricing_decisions d WHERE d.organisation_id=NEW.organisation_id AND d.recommendation_id=NEW.id AND d.previous_version=OLD.version AND d.prior_state=OLD.state AND d.state=NEW.state AND d.approval_id IS NOT DISTINCT FROM NEW.approval_id) THEN RAISE EXCEPTION 'Immutable pricing decision required' USING ERRCODE='23514';END IF;
 IF (NEW.applied_versions IS DISTINCT FROM OLD.applied_versions AND NOT(OLD.state='APPROVED' AND NEW.state='APPLIED')) OR (NEW.approval_id IS DISTINCT FROM OLD.approval_id AND NOT((OLD.state='PENDING_APPROVAL' AND NEW.state='APPROVED') OR (OLD.state='APPLIED' AND NEW.state='ROLLED_BACK'))) THEN RAISE EXCEPTION 'Pricing application/approval lineage can only change at its owning transition' USING ERRCODE='23514';END IF;
 IF (NEW.state IN ('APPROVED','APPLIED','ROLLED_BACK') AND NEW.approval_id IS NULL) OR (NEW.state='APPLIED' AND jsonb_array_length(NEW.applied_versions)=0) THEN RAISE EXCEPTION 'Pricing authority/application lineage required' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

INSERT INTO forecast_models(id,model_key,version,definition) VALUES('019a5fb0-0000-7000-8000-000000000071','canonical-pace-cancellation-v1',1,'{"algorithm":"weekday-capacity-weighted-demand-with-observed-pickup-and-cancellation-adjustment","algorithm_version":1,"night_unit_scale":1000,"features":["confirmed_committed_nights","physical_calendar","accepted_accommodation","booking_lead_time","pickup_pace","cancellations","no_shows","occupancy","channel_source","weekday","month_season","comparable_period","governed_market_inputs"],"confidence_method":"OBSERVED_COVERAGE_AND_COMPARABLE_COUNT_NOT_CALIBRATED","is_calibrated_probability":false,"maximum_horizon_days":90,"maximum_history_days":366,"reproducibility":"SHA-256 of canonical input JSON and model version determines the result"}');

-- +goose Down
-- Retain approved automation authority, run lineage, measurements and decisions.
SELECT 1;
