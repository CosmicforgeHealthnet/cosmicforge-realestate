-- +goose Up
-- Section 11 privacy lifecycle and source-backed guest classification. Only
-- Business-owned CRM data is anonymised here; other services receive
-- coordination references, never direct deletion of their canonical data.
CREATE TABLE privacy_requests (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('EXPORT','ACCESS','CORRECTION','ERASURE','RESTRICTION')),state text NOT NULL DEFAULT 'RECEIVED' CHECK(state IN ('RECEIVED','APPROVED','COMPLETED','REJECTED','BLOCKED_BY_LEGAL_HOLD','CANCELLED')),
 requester_kind text NOT NULL CHECK(requester_kind IN ('GUEST','AUTHORISED_REPRESENTATIVE')),verification_reference text NOT NULL CHECK(length(verification_reference) BETWEEN 5 AND 300),correction jsonb CHECK(correction IS NULL OR jsonb_typeof(correction)='object'),
 due_at timestamptz NOT NULL,approval_id uuid,result jsonb CHECK(result IS NULL OR jsonb_typeof(result)='object'),result_digest text CHECK(result_digest IS NULL OR result_digest~'^[a-f0-9]{64}$'),coordination jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(jsonb_typeof(coordination)='array'),
 reason text NOT NULL,decided_by uuid REFERENCES business_accounts(id),completed_at timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK((kind='CORRECTION')=(correction IS NOT NULL)),CHECK(kind<>'ERASURE' OR state NOT IN ('APPROVED','COMPLETED') OR approval_id IS NOT NULL),CHECK(state<>'COMPLETED' OR completed_at IS NOT NULL),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX privacy_request_queue ON privacy_requests(organisation_id,property_id,state,due_at,id);
CREATE TABLE legal_holds (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,scope text NOT NULL CHECK(scope IN ('GUEST_PROFILE','ALL_GUEST_DATA')),authority_reference text NOT NULL CHECK(length(authority_reference) BETWEEN 5 AND 300),
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RELEASED')),reason text NOT NULL,release_approval_id uuid,released_by uuid REFERENCES business_accounts(id),released_at timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),FOREIGN KEY(organisation_id,release_approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK((state='RELEASED')=(released_at IS NOT NULL AND release_approval_id IS NOT NULL AND released_by IS NOT NULL)),UNIQUE(organisation_id,id)
);
CREATE INDEX legal_hold_active ON legal_holds(organisation_id,guest_id,state);
CREATE TABLE data_retention_jobs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,privacy_request_id uuid,action text NOT NULL CHECK(action IN ('ANONYMISE_GUEST')),
 state text NOT NULL DEFAULT 'QUEUED' CHECK(state IN ('QUEUED','RUNNING','BLOCKED','COMPLETED','FAILED')),lease_owner text,lease_generation bigint NOT NULL DEFAULT 0,lease_expires_at timestamptz,attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),available_at timestamptz NOT NULL DEFAULT now(),last_error text,result jsonb CHECK(result IS NULL OR jsonb_typeof(result)='object'),
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),FOREIGN KEY(organisation_id,property_id,privacy_request_id) REFERENCES privacy_requests(organisation_id,property_id,id),UNIQUE(privacy_request_id),UNIQUE(organisation_id,id)
);
CREATE INDEX data_retention_work ON data_retention_jobs(state,available_at,id);
CREATE TABLE vip_classifications (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 tier text NOT NULL CHECK(tier IN ('NONE','VIP','VVIP')),basis text NOT NULL CHECK(basis IN ('OPERATOR_REVIEWED','LOYALTY_TIER','CORPORATE_CONTRACT','REPEAT_STAYS')),evidence_reference text NOT NULL CHECK(length(evidence_reference) BETWEEN 5 AND 300),expires_at timestamptz,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),UNIQUE(organisation_id,guest_id,property_id,version),UNIQUE(organisation_id,id)
);
-- Segments use a closed list of source-backed criteria. There is no free-form
-- attribute rule, so protected personal characteristics cannot be targeted.
CREATE TABLE guest_segments (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,segment_key uuid NOT NULL,version bigint NOT NULL CHECK(version>0),name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 kind text NOT NULL CHECK(kind IN ('STAY_FREQUENCY','CORPORATE','LOYALTY_TIER','VIP','MARKETING_ELIGIBLE','SOURCE_CHANNEL')),definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=4096),state text NOT NULL CHECK(state IN ('ACTIVE','RETIRED')),reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(segment_key,version),UNIQUE(organisation_id,id)
);
CREATE TABLE guest_segment_memberships (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,segment_id uuid NOT NULL,evaluation_id uuid NOT NULL,guest_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('MEMBER','EXITED')),source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,segment_id) REFERENCES guest_segments(organisation_id,id),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),UNIQUE(evaluation_id,guest_id),UNIQUE(organisation_id,id)
);
CREATE INDEX guest_segment_current ON guest_segment_memberships(organisation_id,segment_id,guest_id,created_at DESC,id DESC);
CREATE TRIGGER vip_classification_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON vip_classifications FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER guest_segment_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON guest_segments FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER guest_segment_membership_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON guest_segment_memberships FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER privacy_request_no_delete BEFORE DELETE OR TRUNCATE ON privacy_requests FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER legal_hold_no_delete BEFORE DELETE OR TRUNCATE ON legal_holds FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER data_retention_no_delete BEFORE DELETE OR TRUNCATE ON data_retention_jobs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
-- A legal hold makes destructive anonymisation impossible, even for a direct
-- statement that bypasses the application workflow.
CREATE FUNCTION business_guest_legal_hold_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.state='ANONYMISED' AND OLD.state IS DISTINCT FROM 'ANONYMISED' AND EXISTS(SELECT 1 FROM legal_holds h WHERE h.organisation_id=NEW.organisation_id AND h.guest_id=NEW.id AND h.state='ACTIVE') THEN RAISE EXCEPTION 'An active legal hold prevents destructive guest anonymisation' USING ERRCODE='23514';END IF;
 IF OLD.state='ANONYMISED' AND NEW.state IS DISTINCT FROM 'ANONYMISED' THEN RAISE EXCEPTION 'Anonymisation cannot be reversed' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER guest_legal_hold_guard BEFORE UPDATE ON guest_profiles FOR EACH ROW EXECUTE FUNCTION business_guest_legal_hold_guard();
-- +goose StatementBegin
CREATE FUNCTION business_legal_hold_terms_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.guest_id,NEW.property_id,NEW.scope,NEW.authority_reference,NEW.created_by,NEW.created_at) IS DISTINCT FROM (OLD.guest_id,OLD.property_id,OLD.scope,OLD.authority_reference,OLD.created_by,OLD.created_at) OR OLD.state='RELEASED' THEN RAISE EXCEPTION 'Legal hold terms and releases are immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER legal_hold_terms_guard BEFORE UPDATE ON legal_holds FOR EACH ROW EXECUTE FUNCTION business_legal_hold_terms_guard();
-- +goose Down
SELECT 1;
