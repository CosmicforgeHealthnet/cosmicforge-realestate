-- +goose Up
-- Section 16/19 analytics: asynchronous report jobs and materialised
-- command-centre snapshots (Annex H KPIs) with source provenance.
CREATE TABLE report_jobs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('COMMAND_CENTER','REPUTATION','CONVERSION','FINANCE','OPERATIONS')),date_from date NOT NULL,date_to date NOT NULL,
 state text NOT NULL DEFAULT 'QUEUED' CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 10),
 lease_owner text,lease_expires_at timestamptz,lease_generation bigint NOT NULL DEFAULT 0,snapshot_id uuid,failure_reason text NOT NULL DEFAULT '',
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_by_member uuid NOT NULL,created_by_role uuid NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(date_to>=date_from AND date_to<=date_from+731),CHECK(state<>'SUCCEEDED' OR snapshot_id IS NOT NULL),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE INDEX report_jobs_due ON report_jobs(state,created_at) WHERE state IN ('QUEUED','RUNNING');
CREATE TRIGGER report_jobs_guard BEFORE UPDATE OR DELETE ON report_jobs FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('QUEUED>RUNNING,RUNNING>RUNNING,RUNNING>SUCCEEDED,RUNNING>QUEUED,RUNNING>FAILED');
CREATE TABLE analytics_snapshots (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,kind text NOT NULL,date_from date NOT NULL,date_to date NOT NULL,
 metrics jsonb NOT NULL CHECK(jsonb_typeof(metrics)='object'),input_digest text NOT NULL CHECK(input_digest~'^[a-f0-9]{64}$'),definitions_version text NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE TRIGGER analytics_snapshots_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON analytics_snapshots FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
ALTER TABLE report_jobs ADD FOREIGN KEY(organisation_id,snapshot_id) REFERENCES analytics_snapshots(organisation_id,id);
-- +goose Down
SELECT 1;
