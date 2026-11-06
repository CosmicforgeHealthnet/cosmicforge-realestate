-- +goose Up
CREATE TABLE hospitality_arrival_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),mode text NOT NULL CHECK(mode IN ('MANUAL','AUTOMATIC_NO_SHOW')),
 allowed_sources text[] NOT NULL CHECK(cardinality(allowed_sources) BETWEEN 1 AND 4 AND allowed_sources<@ARRAY['DIRECT','WALK_IN','CORPORATE','GROUP']::text[]),max_reservations_per_run integer NOT NULL CHECK(max_reservations_per_run BETWEEN 1 AND 50),max_reservation_minor bigint NOT NULL CHECK(max_reservation_minor>=0),max_total_minor bigint NOT NULL CHECK(max_total_minor>=max_reservation_minor),currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),
 effective_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,authorizing_membership_id uuid NOT NULL,authorizing_role_id uuid NOT NULL,approval_id uuid,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 2000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,authorizing_membership_id) REFERENCES staff_memberships(organisation_id,id),FOREIGN KEY(organisation_id,authorizing_role_id) REFERENCES role_templates(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),CHECK(expires_at>effective_at AND expires_at<=effective_at+interval '7 days'),CHECK(mode='MANUAL' OR approval_id IS NOT NULL),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(organisation_id,property_id,version)
);
CREATE INDEX hospitality_arrival_policy_latest ON hospitality_arrival_policies(organisation_id,property_id,version DESC);
CREATE TABLE hospitality_arrival_revocations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,policy_id uuid NOT NULL,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 2000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES hospitality_arrival_policies(organisation_id,property_id,id),UNIQUE(policy_id)
);
CREATE TABLE hospitality_arrival_runs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,policy_id uuid NOT NULL,reservation_id uuid NOT NULL,reservation_version bigint NOT NULL CHECK(reservation_version>0),source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),
 state text NOT NULL CHECK(state IN ('MARKED_NO_SHOW','REVIEW_REQUIRED','AUTHORITY_REVOKED','BUDGET_EXHAUSTED','SOURCE_REVIEW_REQUIRED')),original_amount_minor bigint NOT NULL CHECK(original_amount_minor>=0),currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),evidence jsonb NOT NULL CHECK(jsonb_typeof(evidence)='object'),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES hospitality_arrival_policies(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),CHECK(created_by_service='business-operations-service'),UNIQUE(organisation_id,id),UNIQUE(policy_id,reservation_id,source_digest,state)
);
CREATE UNIQUE INDEX hospitality_single_automatic_no_show ON hospitality_arrival_runs(organisation_id,reservation_id) WHERE state='MARKED_NO_SHOW';
CREATE INDEX hospitality_arrival_run_list ON hospitality_arrival_runs(organisation_id,property_id,created_at DESC,id DESC);
CREATE TABLE hospitality_arrival_cursors (
 organisation_id uuid NOT NULL,property_id uuid NOT NULL,reservation_id uuid NOT NULL,next_check_at timestamptz NOT NULL,consecutive_failures integer NOT NULL DEFAULT 0 CHECK(consecutive_failures>=0),state text NOT NULL DEFAULT 'READY' CHECK(state IN ('READY','QUARANTINED')),last_error_code text NOT NULL DEFAULT '',
 PRIMARY KEY(organisation_id,reservation_id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id)
);
CREATE INDEX hospitality_arrival_due ON hospitality_arrival_cursors(next_check_at,reservation_id);
CREATE TRIGGER hospitality_arrival_policy_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_arrival_policies FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER hospitality_arrival_revocation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_arrival_revocations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER hospitality_arrival_run_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_arrival_runs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE CONSTRAINT TRIGGER hospitality_arrival_policy_evidence AFTER INSERT ON hospitality_arrival_policies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('hospitality_arrival_policy');
CREATE CONSTRAINT TRIGGER hospitality_arrival_revocation_evidence AFTER INSERT ON hospitality_arrival_revocations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('hospitality_arrival_revocation');
CREATE CONSTRAINT TRIGGER hospitality_arrival_run_evidence AFTER INSERT ON hospitality_arrival_runs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('hospitality_arrival_run');
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_no_show_evidence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.state='MARKED_NO_SHOW' AND NOT EXISTS(SELECT 1 FROM reservations r JOIN reservation_versions v ON v.organisation_id=r.organisation_id AND v.reservation_id=r.id AND v.reservation_version=NEW.reservation_version WHERE r.organisation_id=NEW.organisation_id AND r.id=NEW.reservation_id AND r.state='NO_SHOW' AND v.action='NO_SHOW' AND v.created_by IS NULL AND v.created_by_service='business-operations-service') THEN RAISE EXCEPTION 'Automatic no-show requires actual service-owned canonical command evidence' USING ERRCODE='23514';END IF;
 IF NEW.state='MARKED_NO_SHOW' AND NOT EXISTS(SELECT 1 FROM reservations r JOIN hospitality_arrival_policies p ON p.organisation_id=r.organisation_id AND p.property_id=r.property_id AND p.id=NEW.policy_id WHERE r.organisation_id=NEW.organisation_id AND r.id=NEW.reservation_id AND r.amount_minor=NEW.original_amount_minor AND r.currency=NEW.currency AND p.currency=r.currency AND p.mode='AUTOMATIC_NO_SHOW' AND r.source=ANY(p.allowed_sources) AND coalesce((r.policy_snapshot->>'automatic_no_show')::boolean,false) AND r.amount_minor<=p.max_reservation_minor AND (SELECT coalesce(sum(run.original_amount_minor),0) FROM hospitality_arrival_runs run WHERE run.organisation_id=p.organisation_id AND run.policy_id=p.id AND run.state='MARKED_NO_SHOW')<=p.max_total_minor AND NOT EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=r.organisation_id AND s.reservation_id=r.id)) THEN RAISE EXCEPTION 'Automatic no-show must preserve accepted opt-in, source, unoccupied stay and approved exposure limits' USING ERRCODE='23514';END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER hospitality_arrival_canonical_evidence AFTER INSERT ON hospitality_arrival_runs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_no_show_evidence();
-- +goose Down
SELECT 1;
