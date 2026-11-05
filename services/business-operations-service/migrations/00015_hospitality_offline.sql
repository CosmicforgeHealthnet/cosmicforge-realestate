-- +goose Up
CREATE TABLE offline_operation_receipts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,account_id uuid NOT NULL REFERENCES business_accounts(id),session_id uuid NOT NULL REFERENCES business_sessions(id),client_operation_id uuid NOT NULL CHECK(substring(client_operation_id::text,15,1)='7'),action text NOT NULL CHECK(action IN ('FOLIO_POST','ASSIGN_ROOM','CHECK_IN')),target_id uuid NOT NULL,original_action_at timestamptz NOT NULL,request_digest text NOT NULL,state text NOT NULL CHECK(state IN ('APPLIED','RECONCILIATION_REQUIRED')),result jsonb NOT NULL,failure_code text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,account_id,client_operation_id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE offline_reconciliation_tasks (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,receipt_id uuid NOT NULL,classification text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,receipt_id) REFERENCES offline_operation_receipts(organisation_id,property_id,id),UNIQUE(receipt_id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE offline_reconciliation_resolutions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,task_id uuid NOT NULL,disposition text NOT NULL CHECK(disposition IN ('RETRY_ONLINE','CANCELLED')),evidence_reference text NOT NULL,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,task_id) REFERENCES offline_reconciliation_tasks(organisation_id,property_id,id),UNIQUE(task_id)
);
CREATE TRIGGER offline_receipt_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON offline_operation_receipts FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER offline_task_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON offline_reconciliation_tasks FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER offline_resolution_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON offline_reconciliation_resolutions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose Down
SELECT 1;
