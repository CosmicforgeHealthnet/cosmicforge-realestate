-- +goose Up
CREATE TABLE forecast_models (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),model_key text NOT NULL,version integer NOT NULL CHECK(version>0),definition jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(model_key,version)
);
CREATE TRIGGER forecast_models_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON forecast_models FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
INSERT INTO forecast_models(id,model_key,version,definition) VALUES('019a5fb0-0000-7000-8000-000000000070','canonical-weekday-demand-v1',1,'{"algorithm":"same-weekday-capacity-weighted-recorded-demand","algorithm_version":1,"night_unit_scale":1000,"confidence_method":"OBSERVED_CAPACITY_DAY_COVERAGE_NOT_CALIBRATED","is_calibrated_probability":false,"minimum_comparable_days":3,"maximum_horizon_days":90,"maximum_history_days":366}');
CREATE TABLE demand_snapshots (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,pool_id uuid NOT NULL,date_from date NOT NULL,date_to date NOT NULL,as_of timestamptz NOT NULL,currency text NOT NULL,source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),dataset jsonb NOT NULL CHECK(octet_length(dataset::text)<=2097152),provenance jsonb NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(date_to>date_from),CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(organisation_id,property_id,pool_id,id)
);
CREATE INDEX demand_snapshot_scope ON demand_snapshots(organisation_id,property_id,pool_id,as_of DESC,id DESC);
CREATE TRIGGER demand_snapshots_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON demand_snapshots FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE booking_pace_snapshots (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,pool_id uuid NOT NULL,current_snapshot_id uuid NOT NULL,previous_snapshot_id uuid,state text NOT NULL CHECK(state IN ('COMPARABLE','NO_EARLIER_OBSERVATION')),result jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,pool_id,current_snapshot_id) REFERENCES demand_snapshots(organisation_id,property_id,pool_id,id),FOREIGN KEY(organisation_id,property_id,pool_id,previous_snapshot_id) REFERENCES demand_snapshots(organisation_id,property_id,pool_id,id),UNIQUE(organisation_id,id),UNIQUE(current_snapshot_id),CHECK(previous_snapshot_id IS DISTINCT FROM current_snapshot_id)
);
CREATE INDEX booking_pace_snapshot_scope ON booking_pace_snapshots(organisation_id,property_id,pool_id,created_at DESC,id DESC);
CREATE TRIGGER booking_pace_snapshots_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON booking_pace_snapshots FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE forecast_runs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,pool_id uuid NOT NULL,model_id uuid NOT NULL REFERENCES forecast_models(id),source_snapshot_id uuid NOT NULL,source_as_of timestamptz NOT NULL,source_digest text NOT NULL,date_from date NOT NULL,date_to date NOT NULL,configuration jsonb NOT NULL,result jsonb NOT NULL,status text NOT NULL CHECK(status IN ('BASELINE_READY','INSUFFICIENT_DATA')),supersedes_id uuid,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(date_to>date_from),CHECK(result->>'status'=status),CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),FOREIGN KEY(organisation_id,property_id,pool_id,source_snapshot_id) REFERENCES demand_snapshots(organisation_id,property_id,pool_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(organisation_id,property_id,pool_id,id),FOREIGN KEY(organisation_id,property_id,pool_id,supersedes_id) REFERENCES forecast_runs(organisation_id,property_id,pool_id,id)
);
CREATE INDEX forecast_run_scope ON forecast_runs(organisation_id,property_id,pool_id,created_at DESC,id DESC);
CREATE TRIGGER forecast_runs_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON forecast_runs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- +goose StatementBegin
CREATE FUNCTION business_forecast_source_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM demand_snapshots s JOIN forecast_models m ON m.id=NEW.model_id WHERE s.organisation_id=NEW.organisation_id AND s.property_id=NEW.property_id AND s.pool_id=NEW.pool_id AND s.id=NEW.source_snapshot_id AND s.source_digest=NEW.source_digest AND s.as_of=NEW.source_as_of AND s.date_from=NEW.date_from AND s.date_to=NEW.date_to AND NEW.result->>'model'=m.model_key AND (NEW.result->>'algorithm_version')::integer=m.version AND NEW.result->>'is_calibrated_probability'='false') THEN RAISE EXCEPTION 'Forecast must retain its exact owned source and registered model' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER forecast_source_guard BEFORE INSERT ON forecast_runs FOR EACH ROW EXECUTE FUNCTION business_forecast_source_guard();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION business_reservation_room_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.product_id,NEW.pool_id,NEW.quote_id,NEW.hold_id,NEW.date_from,NEW.date_to,NEW.quantity,NEW.adults,NEW.children,NEW.amount_minor,NEW.accommodation_minor,NEW.currency,NEW.snapshot,NEW.accepted_at,NEW.financial_group_id) IS DISTINCT FROM (OLD.product_id,OLD.pool_id,OLD.quote_id,OLD.hold_id,OLD.date_from,OLD.date_to,OLD.quantity,OLD.adults,OLD.children,OLD.amount_minor,OLD.accommodation_minor,OLD.currency,OLD.snapshot,OLD.accepted_at,OLD.financial_group_id) THEN RAISE EXCEPTION 'Accepted room price and allocation history is immutable; append a modification' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd

-- +goose Down
-- Captured source facts/model versions and reproducible forecasts are retained.
SELECT 1;
