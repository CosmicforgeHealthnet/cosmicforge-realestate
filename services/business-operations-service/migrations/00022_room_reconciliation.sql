-- +goose Up
ALTER TABLE hospitality_operation_exceptions ADD COLUMN room_id uuid;
ALTER TABLE hospitality_operation_exceptions ADD CONSTRAINT hospitality_room_exception_identity CHECK(room_id IS NOT DISTINCT FROM CASE WHEN source_type='room' THEN source_id END);
ALTER TABLE hospitality_operation_exceptions ADD CONSTRAINT hospitality_room_exception_parent FOREIGN KEY(organisation_id,property_id,room_id) REFERENCES room_status_current(organisation_id,property_id,room_id);
ALTER TABLE hospitality_operation_exceptions ADD CONSTRAINT hospitality_room_exception_code CHECK(source_type<>'room' OR code='ROOM_SAFETY_REVIEW_REQUIRED');
CREATE TABLE room_reconciliation_reports (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,room_id uuid NOT NULL,source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),
 state text NOT NULL CHECK(state IN ('CONSISTENT','DERIVED_OCCUPANCY','DERIVED_DEPARTURE','ASSIGNMENTS_RELEASED','REVIEW_REQUIRED','OPERATOR_OBSERVATION','SOURCE_REVIEW_REQUIRED')),proof jsonb NOT NULL CHECK(jsonb_typeof(proof)='object'),changes jsonb NOT NULL CHECK(jsonb_typeof(changes)='object'),approval_id uuid,reason text NOT NULL,created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,room_id) REFERENCES room_status_current(organisation_id,property_id,room_id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),CHECK(state<>'OPERATOR_OBSERVATION' OR created_by IS NOT NULL AND approval_id IS NOT NULL),UNIQUE(organisation_id,id),UNIQUE(organisation_id,room_id,source_digest,state)
);
CREATE INDEX room_reconciliation_list ON room_reconciliation_reports(organisation_id,property_id,created_at DESC,id DESC);
CREATE TABLE hospitality_room_cursors (
 organisation_id uuid NOT NULL,room_id uuid NOT NULL,next_check_at timestamptz NOT NULL,consecutive_failures integer NOT NULL DEFAULT 0 CHECK(consecutive_failures>=0),state text NOT NULL DEFAULT 'READY' CHECK(state IN ('READY','QUARANTINED')),last_error_code text NOT NULL DEFAULT '',
 PRIMARY KEY(organisation_id,room_id),FOREIGN KEY(organisation_id,room_id) REFERENCES room_status_current(organisation_id,room_id)
);
CREATE INDEX hospitality_room_due ON hospitality_room_cursors(next_check_at,room_id);
CREATE INDEX hospitality_room_exception_open ON hospitality_operation_exceptions(organisation_id,room_id) WHERE state='OPEN' AND source_type='room';
CREATE INDEX room_assignment_source_lookup ON room_assignments(organisation_id,room_id,state,id);
CREATE INDEX hospitality_source_evidence ON hospitality_history(organisation_id,entity_type,entity_id,entity_version,action,created_at DESC);
CREATE TRIGGER room_reconciliation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON room_reconciliation_reports FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE CONSTRAINT TRIGGER room_reconciliation_evidence AFTER INSERT ON room_reconciliation_reports DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('room_reconciliation_report');
-- Preserve the established computed operational_hold field and all owning
-- calendar/commitment rules. A client cannot clear a source-derived hold flag.
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_has_hold(org uuid,pool uuid) RETURNS boolean LANGUAGE sql VOLATILE AS $$
 SELECT EXISTS(SELECT 1 FROM inventory_pool_members m JOIN room_status_current s ON s.organisation_id=m.organisation_id AND s.room_id=m.node_id WHERE m.organisation_id=org AND m.pool_id=pool AND s.state IN ('BLOCKED','OUT_OF_ORDER'))
 OR EXISTS(SELECT 1 FROM inventory_pool_members m JOIN hospitality_operation_exceptions ex ON ex.organisation_id=m.organisation_id AND ex.room_id=m.node_id WHERE m.organisation_id=org AND m.pool_id=pool AND ex.code='ROOM_SAFETY_REVIEW_REQUIRED' AND ex.state='OPEN');
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION business_hospitality_pool_hold() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 NEW.operational_hold:=business_hospitality_has_hold(NEW.organisation_id,NEW.id);RETURN NEW;END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION business_hospitality_propagate_hold() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 UPDATE inventory_pools p SET version=version+1,updated_at=now() WHERE p.organisation_id=NEW.organisation_id AND EXISTS(SELECT 1 FROM inventory_pool_members m WHERE m.organisation_id=p.organisation_id AND m.pool_id=p.id AND m.node_id=NEW.room_id) AND p.operational_hold IS DISTINCT FROM business_hospitality_has_hold(p.organisation_id,p.id);
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE TRIGGER hospitality_room_exception_hold AFTER INSERT OR UPDATE ON hospitality_operation_exceptions FOR EACH ROW WHEN(NEW.room_id IS NOT NULL) EXECUTE FUNCTION business_hospitality_propagate_hold();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_room_exception_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 NEW.room_id:=CASE WHEN NEW.source_type='room' THEN NEW.source_id END;
 IF NEW.source_type='room' THEN PERFORM id FROM organisations WHERE id=NEW.organisation_id FOR UPDATE; END IF;RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER hospitality_room_exception_fence BEFORE INSERT OR UPDATE ON hospitality_operation_exceptions FOR EACH ROW EXECUTE FUNCTION business_hospitality_room_exception_fence();
-- +goose Down
SELECT 1;
