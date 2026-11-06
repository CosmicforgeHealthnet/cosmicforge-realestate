-- +goose Up
CREATE TABLE stay_history_projection (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,reservation_id uuid NOT NULL,
 source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),snapshot jsonb NOT NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_by_service text NOT NULL DEFAULT 'business-operations-service' CHECK(created_by_service='business-operations-service'),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,guest_id) REFERENCES guest_profiles(organisation_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(organisation_id,guest_id,reservation_id)
);
CREATE INDEX guest_history_visible ON stay_history_projection(organisation_id,property_id,guest_id,created_at DESC,id DESC);
CREATE TABLE guest_stay_history (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,projection_id uuid NOT NULL,projection_version bigint NOT NULL CHECK(projection_version>0),
 source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),snapshot jsonb NOT NULL,source_event_id uuid,source_event_version bigint CHECK(source_event_version>0),source_event_type text,source_aggregate_type text,source_aggregate_id uuid,CHECK((source_event_id IS NULL)=(source_event_version IS NULL)),CHECK(source_event_id IS NULL OR substring(source_event_id::text,15,1)='7'),CHECK((source_event_id IS NULL)=(source_event_type IS NULL)),CHECK((source_event_id IS NULL)=(source_aggregate_type IS NULL)),CHECK((source_event_id IS NULL)=(source_aggregate_id IS NULL)),
 created_by_service text NOT NULL DEFAULT 'business-operations-service' CHECK(created_by_service='business-operations-service'),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,projection_id) REFERENCES stay_history_projection(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(projection_id,projection_version)
);
CREATE INDEX guest_history_evidence ON guest_stay_history(organisation_id,property_id,projection_id,projection_version DESC);
CREATE TABLE crm_history_cursors (
 organisation_id uuid NOT NULL,reservation_id uuid NOT NULL,next_check_at timestamptz NOT NULL,failure_count integer NOT NULL DEFAULT 0 CHECK(failure_count>=0),last_error_code text,last_failed_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organisation_id,reservation_id),FOREIGN KEY(organisation_id,reservation_id) REFERENCES reservations(organisation_id,id)
);
CREATE INDEX crm_history_due ON crm_history_cursors(next_check_at,reservation_id);
CREATE TRIGGER stay_projection_version BEFORE INSERT OR UPDATE ON stay_history_projection FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER stay_projection_identity BEFORE UPDATE ON stay_history_projection FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER stay_projection_retention BEFORE DELETE OR TRUNCATE ON stay_history_projection FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER guest_stay_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON guest_stay_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_stay_projection_evidence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM guest_stay_history h WHERE h.organisation_id=NEW.organisation_id AND h.projection_id=NEW.id AND h.projection_version=NEW.version AND h.source_digest=NEW.source_digest AND h.snapshot=NEW.snapshot) THEN
 RAISE EXCEPTION 'Guest stay projection requires immutable matching source evidence' USING ERRCODE='23514';END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER guest_stay_projection_evidence AFTER INSERT OR UPDATE ON stay_history_projection DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stay_projection_evidence();
-- +goose Down
SELECT 1;
