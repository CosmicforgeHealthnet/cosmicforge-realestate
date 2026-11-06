-- +goose Up
ALTER TABLE access_grant_references ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK(version>0);
ALTER TABLE access_grant_references ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE access_grant_references ADD CONSTRAINT access_reference_tenant_identity UNIQUE(organisation_id,id);
ALTER TABLE access_grant_references ADD CONSTRAINT access_reference_property_identity UNIQUE(organisation_id,property_id,id);
CREATE INDEX access_reference_scope ON access_grant_references(organisation_id,property_id,created_at DESC,id DESC);
CREATE TRIGGER access_reference_version BEFORE INSERT OR UPDATE ON access_grant_references FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER access_reference_retention BEFORE DELETE OR TRUNCATE ON access_grant_references FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE CONSTRAINT TRIGGER access_reference_evidence AFTER INSERT OR UPDATE ON access_grant_references DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('access_reference');
CREATE TABLE hospitality_access_configurations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,room_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 method text NOT NULL CHECK(method IN ('DISABLED','MANUAL_KEY','MANUAL_CARD','SMART_DEVICE')),provider text NOT NULL DEFAULT '',device_reference text NOT NULL DEFAULT '',account_reference text NOT NULL DEFAULT '',device_proof jsonb NOT NULL DEFAULT '{}',
 expires_at timestamptz NOT NULL,approval_id uuid,authorizing_membership_id uuid NOT NULL,authorizing_role_id uuid NOT NULL,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,room_id) REFERENCES room_status_current(organisation_id,property_id,room_id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),FOREIGN KEY(organisation_id,authorizing_membership_id) REFERENCES staff_memberships(organisation_id,id),FOREIGN KEY(organisation_id,authorizing_role_id) REFERENCES role_templates(organisation_id,id),
 CHECK(method='DISABLED' OR approval_id IS NOT NULL),CHECK((method='SMART_DEVICE')=(provider<>'' AND account_reference<>'')),CHECK(method='DISABLED' OR length(device_reference) BETWEEN 5 AND 300),UNIQUE(organisation_id,id),UNIQUE(organisation_id,room_id,version)
);
CREATE INDEX hospitality_access_configuration_latest ON hospitality_access_configurations(organisation_id,room_id,version DESC);
CREATE TRIGGER hospitality_access_configuration_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_access_configurations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE CONSTRAINT TRIGGER hospitality_access_configuration_evidence AFTER INSERT ON hospitality_access_configurations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('access_configuration');
CREATE TABLE hospitality_access_jobs (
 id uuid PRIMARY KEY,organisation_id uuid NOT NULL,property_id uuid NOT NULL,configuration_id uuid NOT NULL,
 request jsonb NOT NULL CHECK(jsonb_typeof(request)='object'),request_digest text NOT NULL CHECK(request_digest~'^[a-f0-9]{64}$'),idempotency_key text NOT NULL UNIQUE,
 delivery_state text NOT NULL CHECK(delivery_state IN ('READY','RUNNING','RETRY','WAITING_MANUAL','DEPENDENCY_BLOCKED','COMPLETE','CANCELLED','QUARANTINED')),attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),available_at timestamptz NOT NULL,review_at timestamptz NOT NULL DEFAULT now(),dispatched_at timestamptz,lease_owner text NOT NULL DEFAULT '',lease_generation bigint NOT NULL DEFAULT 0,lease_expires_at timestamptz,last_error_code text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,id) REFERENCES access_grant_references(organisation_id,property_id,id),FOREIGN KEY(organisation_id,configuration_id) REFERENCES hospitality_access_configurations(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE INDEX hospitality_access_job_due ON hospitality_access_jobs(available_at,id) WHERE delivery_state IN ('READY','RETRY','DEPENDENCY_BLOCKED','RUNNING');
CREATE INDEX hospitality_access_job_review ON hospitality_access_jobs(review_at,id) WHERE request->>'operation'='ISSUE';
CREATE INDEX hospitality_access_audit_lookup ON audit_events(organisation_id,target_id,action) WHERE target_type='access_reference';
CREATE TRIGGER hospitality_access_job_retention BEFORE DELETE OR TRUNCATE ON hospitality_access_jobs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE hospitality_access_observations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,access_reference_id uuid NOT NULL,reference_version bigint NOT NULL CHECK(reference_version>0),
 request_digest text NOT NULL CHECK(request_digest~'^[a-f0-9]{64}$'),state text NOT NULL CHECK(state IN ('PENDING','UNKNOWN','CONFIRMED','FAILED')),origin text NOT NULL CHECK(origin IN ('MANUAL_OBSERVATION','AUTHENTICATED_ADAPTER','SERVICE_SOURCE')),
 credential_reference text NOT NULL DEFAULT '',evidence_reference text NOT NULL,reason text NOT NULL,created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,access_reference_id) REFERENCES access_grant_references(organisation_id,property_id,id),CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),UNIQUE(organisation_id,id),UNIQUE(access_reference_id,reference_version)
);
CREATE TRIGGER hospitality_access_observation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_access_observations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_access_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_TABLE_NAME='access_grant_references' THEN
  IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.stay_id,NEW.assignment_id,NEW.operation,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.stay_id,OLD.assignment_id,OLD.operation,OLD.created_at) THEN RAISE EXCEPTION 'Access source identity is immutable' USING ERRCODE='23514';END IF;
 ELSE
  IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.configuration_id,NEW.request,NEW.request_digest,NEW.idempotency_key,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.configuration_id,OLD.request,OLD.request_digest,OLD.idempotency_key,OLD.created_at) THEN RAISE EXCEPTION 'Access dispatch must retain its original device, window, digest and idempotency key' USING ERRCODE='23514';END IF;
 END IF;RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER access_reference_identity BEFORE UPDATE ON access_grant_references FOR EACH ROW EXECUTE FUNCTION business_hospitality_access_identity();
CREATE TRIGGER hospitality_access_job_identity BEFORE UPDATE ON hospitality_access_jobs FOR EACH ROW EXECUTE FUNCTION business_hospitality_access_identity();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_access_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM id FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 IF TG_TABLE_NAME='access_grant_references' THEN
  IF NOT EXISTS(SELECT 1 FROM stay_records s JOIN room_assignments a ON a.organisation_id=s.organisation_id AND a.reservation_id=s.reservation_id WHERE s.organisation_id=NEW.organisation_id AND s.id=NEW.stay_id AND s.property_id=NEW.property_id AND a.id=NEW.assignment_id AND a.property_id=NEW.property_id) THEN RAISE EXCEPTION 'Access must name a physical assignment of the same actual stay reservation' USING ERRCODE='23514';END IF;
 ELSE
  IF NOT EXISTS(SELECT 1 FROM access_grant_references a JOIN room_assignments physical ON physical.organisation_id=a.organisation_id AND physical.id=a.assignment_id JOIN hospitality_access_configurations c ON c.organisation_id=a.organisation_id AND c.id=NEW.configuration_id WHERE a.organisation_id=NEW.organisation_id AND a.property_id=NEW.property_id AND a.id=NEW.id AND c.property_id=a.property_id AND c.room_id=physical.room_id AND c.method<>'DISABLED' AND NEW.request->>'id'=a.id::text AND NEW.request->>'configuration_id'=c.id::text AND NEW.request->>'operation'=a.operation AND NEW.request->>'stay_id'=a.stay_id::text AND NEW.request->>'assignment_id'=a.assignment_id::text AND NEW.request->'device'->>'organisation_id'=a.organisation_id::text AND NEW.request->'device'->>'property_id'=a.property_id::text AND NEW.request->'device'->>'room_id'=physical.room_id::text AND NEW.request->'device'->>'provider'=c.provider AND NEW.request->'device'->>'device_reference'=c.device_reference AND NEW.request->>'account_reference'=c.account_reference AND (NEW.request->>'starts_at')::timestamptz<(NEW.request->>'ends_at')::timestamptz AND (NEW.request->>'ends_at')::timestamptz<=c.expires_at) THEN RAISE EXCEPTION 'Access command must bind the exact accepted tenant, physical device and window' USING ERRCODE='23514';END IF;
  IF NEW.request->>'operation'='REVOKE' AND NOT EXISTS(SELECT 1 FROM hospitality_access_jobs issue JOIN access_grant_references a ON a.organisation_id=issue.organisation_id AND a.id=issue.id WHERE issue.organisation_id=NEW.organisation_id AND issue.id::text=NEW.request->>'issue_id' AND issue.configuration_id=NEW.configuration_id AND a.operation='ISSUE' AND a.assignment_id::text=NEW.request->>'assignment_id') THEN RAISE EXCEPTION 'Revocation must retain its exact original issue binding' USING ERRCODE='23514';END IF;
 END IF;RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER access_reference_source BEFORE INSERT OR UPDATE ON access_grant_references FOR EACH ROW EXECUTE FUNCTION business_hospitality_access_source();
CREATE TRIGGER hospitality_access_job_source BEFORE INSERT ON hospitality_access_jobs FOR EACH ROW EXECUTE FUNCTION business_hospitality_access_source();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_access_observation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM hospitality_access_observations o JOIN hospitality_access_jobs j ON j.organisation_id=o.organisation_id AND j.id=o.access_reference_id JOIN hospitality_history h ON h.organisation_id=o.organisation_id AND h.property_id=o.property_id AND h.entity_id=o.access_reference_id AND h.entity_type='access_reference' AND h.entity_version=o.reference_version AND h.change_context->>'request_digest'=o.request_digest AND h.change_context->>'state'=o.state AND h.change_context->>'origin'=o.origin JOIN commercial_history c ON c.organisation_id=h.organisation_id AND c.property_id=h.property_id AND c.entity_id=h.entity_id AND c.entity_type=h.entity_type AND c.version=h.entity_version AND c.operation=h.action AND c.snapshot=h.change_context JOIN audit_events a ON a.organisation_id=c.organisation_id AND a.target_type=c.entity_type AND a.target_id=c.entity_id AND a.action=c.operation AND a.change_context=c.snapshot AND a.change_context->>'reference_version'=NEW.version::text AND a.actor_id IS NOT DISTINCT FROM h.created_by WHERE o.organisation_id=NEW.organisation_id AND o.access_reference_id=NEW.id AND o.reference_version=NEW.version AND o.state=NEW.state AND o.credential_reference=NEW.provider_reference AND o.request_digest=j.request_digest) THEN RAISE EXCEPTION 'Access state requires per-version immutable observation, history and security audit in the same transaction' USING ERRCODE='23514';END IF;RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER access_reference_observation AFTER UPDATE ON access_grant_references DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_access_observation_guard();
-- +goose Down
-- Keep physical access/security evidence. Roll back only to a compatible image.
SELECT 1;
