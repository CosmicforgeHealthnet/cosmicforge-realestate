-- Sections 14-15: retain original nonfinancial provider operations across
-- timeouts/restarts. No credentials, document bytes or financial balances.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE owner_external_operations (
 operation_key text PRIMARY KEY,
 kind text NOT NULL CHECK(kind IN ('SMART_ACCESS_PROVISION','ESIGN_ENVELOPE')),
 aggregate_id uuid NOT NULL,
 request_hash text NOT NULL,
 state text NOT NULL CHECK(state IN ('UNKNOWN','CONFIRMED','MANUAL_REVIEW')),
 result jsonb,
 lease_token uuid,
 lease_until timestamptz,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 CHECK ((state='CONFIRMED')=(result IS NOT NULL))
);
CREATE FUNCTION guard_owner_external_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'UPDATE' THEN RAISE EXCEPTION 'external operation history is retained'; END IF;
 IF OLD.operation_key<>NEW.operation_key OR OLD.kind<>NEW.kind OR OLD.aggregate_id<>NEW.aggregate_id OR OLD.request_hash<>NEW.request_hash OR OLD.created_at<>NEW.created_at OR (OLD.state='CONFIRMED' AND (OLD.state<>NEW.state OR OLD.result IS DISTINCT FROM NEW.result)) THEN
  RAISE EXCEPTION 'original external operation identity/outcome is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_operation_identity BEFORE UPDATE OR DELETE ON owner_external_operations FOR EACH ROW EXECUTE FUNCTION guard_owner_external_operation();
CREATE TRIGGER external_operation_retention BEFORE TRUNCATE ON owner_external_operations FOR EACH STATEMENT EXECUTE FUNCTION guard_owner_external_operation();
CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events FOR EACH STATEMENT EXECUTE FUNCTION audit_events_append_only();
CREATE TRIGGER lifecycle_transitions_no_truncate BEFORE TRUNCATE ON lifecycle_transitions FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER property_history_no_truncate BEFORE TRUNCATE ON property_history FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN IF EXISTS(SELECT 1 FROM owner_external_operations) THEN RAISE EXCEPTION 'rollback prohibited with external operation history'; END IF; END $$;
DROP TRIGGER property_history_no_truncate ON property_history;
DROP TRIGGER lifecycle_transitions_no_truncate ON lifecycle_transitions;
DROP TRIGGER audit_events_no_truncate ON audit_events;
DROP TABLE owner_external_operations;
DROP FUNCTION guard_owner_external_operation();
-- +goose StatementEnd
