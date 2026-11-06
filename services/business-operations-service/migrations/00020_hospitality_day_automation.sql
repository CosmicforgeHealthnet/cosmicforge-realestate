-- +goose Up
ALTER TABLE hospitality_business_days ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE hospitality_business_days ADD COLUMN created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE hospitality_business_days ADD CONSTRAINT hospitality_day_actor CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service');
ALTER TABLE business_day_closes ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE business_day_closes ADD COLUMN created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE business_day_closes ADD CONSTRAINT hospitality_close_actor CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service');
CREATE TABLE hospitality_day_automation_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 mode text NOT NULL CHECK(mode IN ('MANUAL','AUDIT_ONLY','CLOSE_IF_CLEAR')),hospitality_policy_version bigint NOT NULL CHECK(hospitality_policy_version>=0),timezone text NOT NULL,
 effective_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,max_days_per_run integer NOT NULL CHECK(max_days_per_run BETWEEN 1 AND 7),
 authorizing_membership_id uuid NOT NULL,authorizing_role_id uuid NOT NULL,approval_id uuid,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 2000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,authorizing_membership_id) REFERENCES staff_memberships(organisation_id,id),FOREIGN KEY(organisation_id,authorizing_role_id) REFERENCES role_templates(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK(expires_at>effective_at AND expires_at<=effective_at+interval '7 days'),CHECK(mode='MANUAL' OR approval_id IS NOT NULL),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(organisation_id,property_id,version)
);
CREATE INDEX hospitality_automation_latest ON hospitality_day_automation_policies(organisation_id,property_id,version DESC);
CREATE TABLE hospitality_day_automation_revocations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,policy_id uuid NOT NULL,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 2000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES hospitality_day_automation_policies(organisation_id,property_id,id),UNIQUE(policy_id)
);
CREATE TABLE hospitality_day_automation_runs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,policy_id uuid NOT NULL,business_day_id uuid,
 source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),state text NOT NULL CHECK(state IN ('AUDITED','BLOCKED','CLOSED','AUTHORITY_REVOKED','POLICY_STALE','SOURCE_REVIEW_REQUIRED')),audit_id uuid,close_id uuid,evidence jsonb NOT NULL CHECK(jsonb_typeof(evidence)='object'),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES hospitality_day_automation_policies(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,business_day_id) REFERENCES hospitality_business_days(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,audit_id) REFERENCES night_audits(organisation_id,property_id,id),FOREIGN KEY(organisation_id,close_id) REFERENCES business_day_closes(organisation_id,id),
 CHECK(created_by_service='business-operations-service'),CHECK(business_day_id IS NOT NULL OR state='SOURCE_REVIEW_REQUIRED'),CHECK((state='CLOSED')=(close_id IS NOT NULL)),CHECK(state NOT IN ('AUDITED','BLOCKED','CLOSED') OR audit_id IS NOT NULL),UNIQUE(organisation_id,id),UNIQUE NULLS NOT DISTINCT(policy_id,business_day_id,source_digest,state)
);
CREATE INDEX hospitality_automation_run_list ON hospitality_day_automation_runs(organisation_id,property_id,created_at DESC,id DESC);
CREATE TABLE hospitality_day_automation_cursors (
 organisation_id uuid NOT NULL,property_id uuid NOT NULL,next_check_at timestamptz NOT NULL,consecutive_failures integer NOT NULL DEFAULT 0 CHECK(consecutive_failures>=0),last_error_code text NOT NULL DEFAULT '',state text NOT NULL DEFAULT 'READY' CHECK(state IN ('READY','QUARANTINED')),
 PRIMARY KEY(organisation_id,property_id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id)
);
CREATE INDEX hospitality_automation_due ON hospitality_day_automation_cursors(next_check_at,property_id);
CREATE TRIGGER hospitality_automation_policy_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_day_automation_policies FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER hospitality_automation_revocation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_day_automation_revocations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER hospitality_automation_run_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_day_automation_runs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_automation_evidence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM hospitality_history h WHERE h.organisation_id=NEW.organisation_id AND h.property_id=NEW.property_id AND h.entity_id=NEW.id AND h.entity_type=TG_ARGV[0] AND h.entity_version=1) THEN RAISE EXCEPTION 'Hospitality automation requires transactional immutable evidence' USING ERRCODE='23514';END IF;
 IF TG_TABLE_NAME='hospitality_day_automation_runs' THEN
  IF NEW.close_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM business_day_closes c WHERE c.organisation_id=NEW.organisation_id AND c.property_id=NEW.property_id AND c.id=NEW.close_id AND c.business_day_id=NEW.business_day_id AND c.audit_id=NEW.audit_id AND c.created_by IS NULL) THEN RAISE EXCEPTION 'Automatic close evidence must identify its actual service-owned day and audit' USING ERRCODE='23514';END IF;
 END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER hospitality_automation_policy_evidence AFTER INSERT ON hospitality_day_automation_policies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('hospitality_day_automation_policy');
CREATE CONSTRAINT TRIGGER hospitality_automation_revocation_evidence AFTER INSERT ON hospitality_day_automation_revocations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('hospitality_day_automation_revocation');
CREATE CONSTRAINT TRIGGER hospitality_automation_run_evidence AFTER INSERT ON hospitality_day_automation_runs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('hospitality_day_automation_run');
-- +goose Down
SELECT 1;
