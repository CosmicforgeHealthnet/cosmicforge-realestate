-- +goose Up
CREATE TABLE hospitality_maintenance_cursors (
 organisation_id uuid NOT NULL,folio_id uuid NOT NULL,next_check_at timestamptz NOT NULL,
 PRIMARY KEY(organisation_id,folio_id),FOREIGN KEY(organisation_id,folio_id) REFERENCES folios(organisation_id,id)
);
CREATE INDEX hospitality_maintenance_due ON hospitality_maintenance_cursors(next_check_at,folio_id);
ALTER TABLE hospitality_history ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE hospitality_history ADD COLUMN created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE hospitality_history ADD CONSTRAINT hospitality_history_actor CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service');
ALTER TABLE night_audits ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE night_audits ADD COLUMN created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE night_audits ADD CONSTRAINT night_audit_actor CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service');
CREATE TABLE folio_close_reviews (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,folio_id uuid NOT NULL,folio_version bigint NOT NULL CHECK(folio_version>0),posting_digest text NOT NULL,automatic_close boolean NOT NULL,authorizing_membership_id uuid NOT NULL,authorizing_role_id uuid NOT NULL,expires_at timestamptz NOT NULL,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,authorizing_membership_id) REFERENCES staff_memberships(organisation_id,id),FOREIGN KEY(organisation_id,authorizing_role_id) REFERENCES role_templates(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX folio_review_current ON folio_close_reviews(organisation_id,property_id,folio_id,created_at DESC,id DESC);
CREATE TABLE folio_close_review_revocations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,review_id uuid NOT NULL,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,review_id) REFERENCES folio_close_reviews(organisation_id,property_id,id),UNIQUE(review_id)
);
CREATE TABLE hospitality_operation_exceptions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,source_type text NOT NULL,source_id uuid NOT NULL,source_digest text NOT NULL,code text NOT NULL,state text NOT NULL CHECK(state IN ('OPEN','RESOLVED')),evidence jsonb NOT NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),UNIQUE(organisation_id,id),UNIQUE(organisation_id,source_type,source_id,source_digest,code)
);
CREATE INDEX hospitality_exception_queue ON hospitality_operation_exceptions(organisation_id,property_id,state,created_at,id);
CREATE TRIGGER folio_review_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON folio_close_reviews FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER folio_review_revocation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON folio_close_review_revocations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER operational_exception_version BEFORE INSERT OR UPDATE ON hospitality_operation_exceptions FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER operational_exception_identity BEFORE UPDATE ON hospitality_operation_exceptions FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER operational_exception_retention BEFORE DELETE OR TRUNCATE ON hospitality_operation_exceptions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_exception_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (to_jsonb(NEW)-ARRAY['state','version','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','version','updated_at']) OR OLD.state<>'OPEN' OR NEW.state<>'RESOLVED' THEN
 RAISE EXCEPTION 'Hospitality exception evidence is immutable; only verified resolution is allowed' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER operational_exception_evidence BEFORE UPDATE ON hospitality_operation_exceptions FOR EACH ROW EXECUTE FUNCTION business_hospitality_exception_guard();
-- +goose Down
SELECT 1;
