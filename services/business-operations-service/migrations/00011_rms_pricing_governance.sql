-- +goose Up
CREATE TABLE rms_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,branch_id uuid NOT NULL,pool_id uuid NOT NULL,rate_plan_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),state text NOT NULL CHECK(state IN ('ACTIVE','PAUSED')),definition jsonb NOT NULL,approval_id uuid,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),FOREIGN KEY(organisation_id,branch_id) REFERENCES branches(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(rate_plan_id,version)
);
CREATE INDEX rms_policy_scope ON rms_policies(organisation_id,property_id,rate_plan_id,version DESC);
CREATE TRIGGER rms_policies_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rms_policies FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE pricing_recommendations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,pool_id uuid NOT NULL,rate_plan_id uuid NOT NULL,policy_id uuid NOT NULL,forecast_id uuid NOT NULL,date_from date NOT NULL,date_to date NOT NULL,target_plan_version bigint NOT NULL CHECK(target_plan_version>0),change_digest text NOT NULL CHECK(change_digest~'^[a-f0-9]{64}$'),snapshot jsonb NOT NULL CHECK(octet_length(snapshot::text)<=2097152),expires_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','PENDING_APPROVAL','APPROVED','APPLIED','OVERRIDDEN','EXPIRED','ROLLED_BACK','REJECTED')),version bigint NOT NULL DEFAULT 1 CHECK(version>0),approval_id uuid,applied_versions jsonb NOT NULL DEFAULT '[]',created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES rms_policies(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,pool_id,forecast_id) REFERENCES forecast_runs(organisation_id,property_id,pool_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),CHECK(date_to>date_from),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX pricing_recommendation_scope ON pricing_recommendations(organisation_id,property_id,state,created_at DESC,id DESC);
CREATE TABLE pricing_decisions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,recommendation_id uuid NOT NULL,previous_version bigint NOT NULL,prior_state text NOT NULL,state text NOT NULL,action text NOT NULL,actor_id uuid REFERENCES business_accounts(id),actor_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),approval_id uuid,reason text NOT NULL,evidence jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(actor_id IS NOT NULL OR actor_service='business-operations-service'),FOREIGN KEY(organisation_id,property_id,recommendation_id) REFERENCES pricing_recommendations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(recommendation_id,previous_version)
);
CREATE TRIGGER pricing_decisions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON pricing_decisions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE pricing_simulations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,recommendation_id uuid NOT NULL,change_digest text NOT NULL,forecast_id uuid NOT NULL,result jsonb NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,recommendation_id) REFERENCES pricing_recommendations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,forecast_id) REFERENCES forecast_runs(organisation_id,property_id,id),UNIQUE(organisation_id,id)
);
CREATE INDEX pricing_simulation_scope ON pricing_simulations(organisation_id,property_id,created_at DESC,id DESC);
CREATE TRIGGER pricing_simulations_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON pricing_simulations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- +goose StatementBegin
CREATE FUNCTION business_pricing_recommendation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Pricing recommendation identity is retained' USING ERRCODE='23514';END IF;
 IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.pool_id,NEW.rate_plan_id,NEW.policy_id,NEW.forecast_id,NEW.date_from,NEW.date_to,NEW.target_plan_version,NEW.change_digest,NEW.snapshot,NEW.expires_at,NEW.created_by,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.pool_id,OLD.rate_plan_id,OLD.policy_id,OLD.forecast_id,OLD.date_from,OLD.date_to,OLD.target_plan_version,OLD.change_digest,OLD.snapshot,OLD.expires_at,OLD.created_by,OLD.created_at) OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'Immutable pricing proposal or invalid version' USING ERRCODE='23514';END IF;
 IF NOT ((OLD.state='DRAFT' AND NEW.state IN ('PENDING_APPROVAL','OVERRIDDEN','EXPIRED','REJECTED')) OR (OLD.state='PENDING_APPROVAL' AND NEW.state IN ('APPROVED','OVERRIDDEN','EXPIRED','REJECTED')) OR (OLD.state='APPROVED' AND NEW.state IN ('APPLIED','EXPIRED','REJECTED')) OR (OLD.state='APPLIED' AND NEW.state='ROLLED_BACK')) THEN RAISE EXCEPTION 'Invalid pricing recommendation transition' USING ERRCODE='23514';END IF;
 IF NOT EXISTS(SELECT 1 FROM pricing_decisions d WHERE d.organisation_id=NEW.organisation_id AND d.recommendation_id=NEW.id AND d.previous_version=OLD.version AND d.prior_state=OLD.state AND d.state=NEW.state AND d.approval_id IS NOT DISTINCT FROM NEW.approval_id) THEN RAISE EXCEPTION 'Immutable pricing decision required' USING ERRCODE='23514';END IF;
 IF (NEW.applied_versions IS DISTINCT FROM OLD.applied_versions AND NOT(OLD.state='APPROVED' AND NEW.state='APPLIED')) OR (NEW.approval_id IS DISTINCT FROM OLD.approval_id AND NOT((OLD.state='PENDING_APPROVAL' AND NEW.state='APPROVED') OR (OLD.state='APPLIED' AND NEW.state='ROLLED_BACK'))) THEN RAISE EXCEPTION 'Pricing application/approval lineage can only change at its owning transition' USING ERRCODE='23514';END IF;
 IF (NEW.state IN ('APPROVED','APPLIED','ROLLED_BACK') AND NEW.approval_id IS NULL) OR (NEW.state='APPLIED' AND jsonb_array_length(NEW.applied_versions)=0) THEN RAISE EXCEPTION 'Pricing authority/application lineage required' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER pricing_recommendation_guard BEFORE UPDATE OR DELETE ON pricing_recommendations FOR EACH ROW EXECUTE FUNCTION business_pricing_recommendation_guard();

-- +goose Down
-- Retain immutable pricing governance and historical decisions.
SELECT 1;
