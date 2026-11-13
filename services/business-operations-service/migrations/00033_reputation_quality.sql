-- +goose Up
-- Section 13 reviews, reputation and quality loop. Every source version is
-- retained as immutable evidence; ratings count once per review; topic
-- signals are quality inputs that never rewrite the original review.

ALTER TABLE messaging_connections ADD COLUMN supports_reviews boolean NOT NULL DEFAULT false;
ALTER TABLE messaging_connections ADD COLUMN supports_review_replies boolean NOT NULL DEFAULT false;

CREATE TABLE review_refs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,unit_id uuid,reservation_id uuid,stay_id uuid,
 source text NOT NULL CHECK(source IN ('MARKETPLACE_VERIFIED_STAY','BOOKING_COM','AIRBNB','DIRECT_FEEDBACK','LEGACY_IMPORT')),connection_id uuid,
 external_review_id text NOT NULL CHECK(length(external_review_id) BETWEEN 1 AND 200),source_version bigint NOT NULL CHECK(source_version>0),
 verification_status text NOT NULL CHECK(verification_status IN ('VERIFIED_STAY','UNVERIFIED','LEGACY_REFERENCE')),
 rating_value integer NOT NULL,rating_scale integer NOT NULL CHECK(rating_scale BETWEEN 1 AND 100),rating_bps integer NOT NULL CHECK(rating_bps BETWEEN 0 AND 10000),
 content_digest text NOT NULL CHECK(content_digest~'^[a-f0-9]{64}$'),content_excerpt text CHECK(content_excerpt IS NULL OR length(content_excerpt)<=2000),content_reference text NOT NULL CHECK(length(content_reference) BETWEEN 1 AND 500),
 language text NOT NULL DEFAULT 'en' CHECK(language~'^[a-z]{2}$'),reviewed_at timestamptz NOT NULL,
 moderation_status text NOT NULL DEFAULT 'PUBLISHED' CHECK(moderation_status IN ('PUBLISHED','PENDING_MODERATION','HIDDEN_BY_PROVIDER','REMOVED_BY_PROVIDER')),
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','TOMBSTONED')),legal_hold boolean NOT NULL DEFAULT false,
 response_state text NOT NULL DEFAULT 'NONE' CHECK(response_state IN ('NONE','DRAFT','PENDING_APPROVAL','APPROVED','SUBMITTING','PUBLISHED','UNKNOWN_PENDING_READBACK','FAILED','RETRACT_REQUESTED','RETRACTED','CANCELLED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK(rating_value BETWEEN 0 AND rating_scale),
 CHECK(source<>'MARKETPLACE_VERIFIED_STAY' OR verification_status='VERIFIED_STAY' AND reservation_id IS NOT NULL),CHECK(verification_status<>'VERIFIED_STAY' OR reservation_id IS NOT NULL OR stay_id IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,stay_id) REFERENCES stay_records(organisation_id,property_id,id),FOREIGN KEY(organisation_id,unit_id) REFERENCES inventory_nodes(organisation_id,id),
 UNIQUE(organisation_id,source,external_review_id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX review_refs_property ON review_refs(organisation_id,property_id,reviewed_at DESC,id);
CREATE TRIGGER review_refs_guard BEFORE UPDATE OR DELETE ON review_refs FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('ACTIVE>TOMBSTONED');

CREATE TABLE review_versions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,review_id uuid NOT NULL,source_version bigint NOT NULL CHECK(source_version>0),
 rating_value integer NOT NULL,rating_scale integer NOT NULL,content_digest text NOT NULL,content_excerpt text,moderation_status text NOT NULL,tombstoned boolean NOT NULL DEFAULT false,
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=32768),received_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,review_id) REFERENCES review_refs(organisation_id,id),UNIQUE(review_id,source_version)
);
CREATE TRIGGER review_versions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON review_versions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE review_dimension_ratings (
 organisation_id uuid NOT NULL,review_id uuid NOT NULL,source_version bigint NOT NULL,dimension text NOT NULL CHECK(dimension IN ('CLEANLINESS','LOCATION','VALUE','COMMUNICATION','CHECK_IN','ACCURACY','STAFF','COMFORT','FACILITIES')),
 rating_value integer NOT NULL,rating_scale integer NOT NULL CHECK(rating_scale BETWEEN 1 AND 100),rating_bps integer NOT NULL CHECK(rating_bps BETWEEN 0 AND 10000),
 PRIMARY KEY(review_id,source_version,dimension),FOREIGN KEY(organisation_id,review_id) REFERENCES review_refs(organisation_id,id),CHECK(rating_value BETWEEN 0 AND rating_scale)
);
CREATE TRIGGER review_dimension_ratings_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON review_dimension_ratings FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE review_responses (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,review_id uuid NOT NULL,body text NOT NULL CHECK(length(body) BETWEEN 10 AND 4000),
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','PENDING_APPROVAL','APPROVED','SUBMITTING','PUBLISHED','UNKNOWN_PENDING_READBACK','FAILED','RETRACT_REQUESTED','RETRACTED','CANCELLED')),
 requires_approval boolean NOT NULL,approval_id uuid,provider_response_id text,attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 20),next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_owner text,lease_expires_at timestamptz,lease_generation bigint NOT NULL DEFAULT 0,outcome_reason text NOT NULL DEFAULT '',published_at timestamptz,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_by_member uuid NOT NULL,created_by_role uuid NOT NULL,submitted_by uuid REFERENCES business_accounts(id),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(state<>'PUBLISHED' OR provider_response_id IS NOT NULL AND published_at IS NOT NULL),CHECK(NOT requires_approval OR state IN ('DRAFT','PENDING_APPROVAL','CANCELLED') OR approval_id IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id,review_id) REFERENCES review_refs(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX review_responses_one_live ON review_responses(review_id) WHERE state NOT IN ('CANCELLED','FAILED','RETRACTED');
CREATE TRIGGER review_responses_guard BEFORE UPDATE OR DELETE ON review_responses FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>PENDING_APPROVAL,DRAFT>APPROVED,DRAFT>CANCELLED,PENDING_APPROVAL>APPROVED,PENDING_APPROVAL>CANCELLED,APPROVED>SUBMITTING,APPROVED>CANCELLED,SUBMITTING>PUBLISHED,SUBMITTING>UNKNOWN_PENDING_READBACK,SUBMITTING>FAILED,SUBMITTING>APPROVED,UNKNOWN_PENDING_READBACK>SUBMITTING,UNKNOWN_PENDING_READBACK>PUBLISHED,UNKNOWN_PENDING_READBACK>APPROVED,UNKNOWN_PENDING_READBACK>FAILED,PUBLISHED>RETRACT_REQUESTED,RETRACT_REQUESTED>RETRACTED,RETRACT_REQUESTED>PUBLISHED');
CREATE TABLE review_response_attempts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,response_id uuid NOT NULL,attempt integer NOT NULL,lease_generation bigint NOT NULL,kind text NOT NULL CHECK(kind IN ('SUBMIT','READBACK','RETRACT')),
 outcome text NOT NULL CHECK(outcome IN ('ACCEPTED','AMBIGUOUS','REJECTED','NOT_FOUND')),provider_response_id text,detail text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,response_id) REFERENCES review_responses(organisation_id,id),UNIQUE(response_id,attempt,kind,lease_generation)
);
CREATE TRIGGER review_response_attempts_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON review_response_attempts FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE review_topic_signals (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,review_id uuid NOT NULL,source_version bigint NOT NULL,
 topic text NOT NULL CHECK(topic IN ('CLEANLINESS','MAINTENANCE','CHECK_IN_DELAY','SERVICE_QUALITY','STAFF_BEHAVIOUR','PRICING','ROOM_CONDITION','ACCESS','NOISE','SAFETY','CANCELLATION_REFUND')),
 sentiment text NOT NULL CHECK(sentiment IN ('NEGATIVE','NEUTRAL','POSITIVE')),confidence_bps integer NOT NULL CHECK(confidence_bps BETWEEN 0 AND 10000),engine text NOT NULL,model_version text NOT NULL,
 evidence_terms jsonb NOT NULL CHECK(jsonb_typeof(evidence_terms)='array'),state text NOT NULL DEFAULT 'MACHINE' CHECK(state IN ('MACHINE','CONFIRMED','CORRECTED','REJECTED')),
 corrected_sentiment text CHECK(corrected_sentiment IS NULL OR corrected_sentiment IN ('NEGATIVE','NEUTRAL','POSITIVE')),reviewed_by uuid REFERENCES business_accounts(id),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,review_id) REFERENCES review_refs(organisation_id,property_id,id),UNIQUE(review_id,source_version,topic,engine,model_version),UNIQUE(organisation_id,id)
);
CREATE TRIGGER review_topic_signals_guard BEFORE UPDATE OR DELETE ON review_topic_signals FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('MACHINE>CONFIRMED,MACHINE>CORRECTED,MACHINE>REJECTED');

CREATE TABLE quality_issues (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,topic text NOT NULL,
 state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','TASKED','MONITORING','CLOSED')),severity text NOT NULL DEFAULT 'MEDIUM' CHECK(severity IN ('LOW','MEDIUM','HIGH','CRITICAL')),
 signal_count integer NOT NULL DEFAULT 0 CHECK(signal_count>=0),first_signal_at timestamptz NOT NULL,last_signal_at timestamptz NOT NULL,window_days integer NOT NULL DEFAULT 30 CHECK(window_days BETWEEN 1 AND 365),
 monitoring_until timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE UNIQUE INDEX quality_issues_one_live ON quality_issues(organisation_id,property_id,topic) WHERE state IN ('OPEN','TASKED','MONITORING');
CREATE TRIGGER quality_issues_guard BEFORE UPDATE OR DELETE ON quality_issues FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('OPEN>TASKED,OPEN>CLOSED,TASKED>MONITORING,TASKED>OPEN,MONITORING>OPEN,MONITORING>CLOSED');
CREATE TABLE quality_issue_signals (
 organisation_id uuid NOT NULL,issue_id uuid NOT NULL,signal_id uuid NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(signal_id),
 FOREIGN KEY(organisation_id,issue_id) REFERENCES quality_issues(organisation_id,id),FOREIGN KEY(organisation_id,signal_id) REFERENCES review_topic_signals(organisation_id,id)
);
CREATE TRIGGER quality_issue_signals_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON quality_issue_signals FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE review_action_tasks (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,issue_id uuid,review_id uuid,
 team text NOT NULL CHECK(team IN ('HOUSEKEEPING','MAINTENANCE','FRONT_DESK','OPERATIONS','GUEST_RECOVERY')),title text NOT NULL CHECK(length(title) BETWEEN 5 AND 200),
 severity text NOT NULL CHECK(severity IN ('LOW','MEDIUM','HIGH','CRITICAL')),state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','ASSIGNED','IN_PROGRESS','RESOLVED','VERIFIED','CANCELLED')),
 assigned_to uuid REFERENCES business_accounts(id),room_id uuid,sla_due_at timestamptz NOT NULL,linked_record_type text CHECK(linked_record_type IS NULL OR linked_record_type IN ('HOUSEKEEPING_TASK','WORK_ORDER','SERVICE_RECOVERY_CASE','SUPPORT_CASE')),linked_record_id uuid,
 evidence_reference text NOT NULL DEFAULT '' CHECK(length(evidence_reference)<=300),resolution text NOT NULL DEFAULT '' CHECK(length(resolution)<=2000),follow_up_at timestamptz,resolved_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK((linked_record_type IS NULL)=(linked_record_id IS NULL)),CHECK(state NOT IN ('RESOLVED','VERIFIED') OR resolved_at IS NOT NULL AND length(resolution)>=5),
 FOREIGN KEY(organisation_id,property_id,issue_id) REFERENCES quality_issues(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,review_id) REFERENCES review_refs(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,room_id) REFERENCES inventory_nodes(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX review_action_tasks_one_open ON review_action_tasks(issue_id,team) WHERE issue_id IS NOT NULL AND state IN ('OPEN','ASSIGNED','IN_PROGRESS');
CREATE TRIGGER review_action_tasks_guard BEFORE UPDATE OR DELETE ON review_action_tasks FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('OPEN>ASSIGNED,OPEN>IN_PROGRESS,OPEN>CANCELLED,ASSIGNED>ASSIGNED,ASSIGNED>IN_PROGRESS,ASSIGNED>CANCELLED,IN_PROGRESS>RESOLVED,IN_PROGRESS>ASSIGNED,RESOLVED>VERIFIED,RESOLVED>IN_PROGRESS');

CREATE TABLE review_disputes (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,review_id uuid NOT NULL,
 reason_code text NOT NULL CHECK(reason_code IN ('FAKE_REVIEW','ABUSIVE','WRONG_BOOKING','PROVIDER_MODERATION','POLICY_VIOLATION')),route text NOT NULL CHECK(route IN ('PROVIDER','ADMIN')),
 statement text NOT NULL CHECK(length(statement) BETWEEN 10 AND 2000),evidence_refs jsonb NOT NULL CHECK(jsonb_typeof(evidence_refs)='array' AND jsonb_array_length(evidence_refs) BETWEEN 1 AND 20),
 review_version bigint NOT NULL,state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','SUBMITTED','UNDER_REVIEW','UPHELD','REJECTED','APPEALED','WITHDRAWN')),
 external_reference text CHECK(external_reference IS NULL OR length(external_reference) BETWEEN 3 AND 300),decision text NOT NULL DEFAULT '' CHECK(length(decision)<=2000),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(state NOT IN ('UNDER_REVIEW','UPHELD','REJECTED','APPEALED') OR external_reference IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id,review_id) REFERENCES review_refs(organisation_id,property_id,id),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX review_disputes_one_live ON review_disputes(review_id) WHERE state IN ('DRAFT','SUBMITTED','UNDER_REVIEW','APPEALED');
CREATE TRIGGER review_disputes_guard BEFORE UPDATE OR DELETE ON review_disputes FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>SUBMITTED,DRAFT>WITHDRAWN,SUBMITTED>UNDER_REVIEW,SUBMITTED>WITHDRAWN,UNDER_REVIEW>UPHELD,UNDER_REVIEW>REJECTED,UNDER_REVIEW>WITHDRAWN,REJECTED>APPEALED,APPEALED>UPHELD,APPEALED>REJECTED,APPEALED>WITHDRAWN');

-- Original review evidence is immutable while held; only provider status,
-- response state, legal hold and version may change on the live row.
-- +goose StatementBegin
CREATE FUNCTION business_review_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.source_version<OLD.source_version THEN RAISE EXCEPTION 'Review source versions never regress' USING ERRCODE='23514'; END IF;
 IF NEW.source_version=OLD.source_version AND (NEW.content_digest,NEW.rating_value,NEW.rating_scale,NEW.reviewed_at,NEW.source,NEW.external_review_id,NEW.reservation_id,NEW.verification_status) IS DISTINCT FROM (OLD.content_digest,OLD.rating_value,OLD.rating_scale,OLD.reviewed_at,OLD.source,OLD.external_review_id,OLD.reservation_id,OLD.verification_status) THEN
  RAISE EXCEPTION 'Original review evidence is immutable; a new source version is required' USING ERRCODE='23514';
 END IF;
 IF NEW.source_version>OLD.source_version AND NOT EXISTS(SELECT 1 FROM review_versions v WHERE v.review_id=NEW.id AND v.source_version=NEW.source_version) THEN
  RAISE EXCEPTION 'Every review source version requires immutable history' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER review_refs_evidence BEFORE UPDATE ON review_refs FOR EACH ROW EXECUTE FUNCTION business_review_evidence_guard();

CREATE TABLE reputation_metrics (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,period_from date NOT NULL,period_to date NOT NULL,
 metrics jsonb NOT NULL CHECK(jsonb_typeof(metrics)='object'),input_digest text NOT NULL CHECK(input_digest~'^[a-f0-9]{64}$'),created_by uuid REFERENCES business_accounts(id),
 created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(period_to>=period_from),CHECK(created_by IS NOT NULL OR created_by_service<>''),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id)
);
CREATE TRIGGER reputation_metrics_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reputation_metrics FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE review_sync_cursors (
 organisation_id uuid NOT NULL,connection_id uuid NOT NULL,cursor text NOT NULL DEFAULT '',lease_owner text,lease_expires_at timestamptz,lease_generation bigint NOT NULL DEFAULT 0,
 next_poll_at timestamptz NOT NULL DEFAULT now(),last_error text NOT NULL DEFAULT '',updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organisation_id,connection_id),FOREIGN KEY(organisation_id,connection_id) REFERENCES messaging_connections(organisation_id,id)
);

-- +goose Down
SELECT 1;
