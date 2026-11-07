-- +goose Up
-- Section 11 authorized guest merge and durable CRM notifications.
-- A merge never rewrites immutable stay, consent, loyalty, recovery or identity
-- evidence. The source profile is marked MERGED, open pre-arrival bookings are
-- re-pointed with a recorded reference, and everything else resolves through
-- this lineage. Notifications are derived from authoritative transitions and
-- delivered through the existing signed Notification Service client; only the
-- service's acceptance is recorded, never delivery.
CREATE TABLE guest_merges (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,source_guest_id uuid NOT NULL,target_guest_id uuid NOT NULL,
 match_basis text NOT NULL CHECK(match_basis IN ('SHARED_SOURCE_IDENTITY','VERIFIED_OWNERSHIP')),ownership_evidence_reference text NOT NULL CHECK(length(ownership_evidence_reference) BETWEEN 5 AND 300),
 risk text NOT NULL CHECK(risk IN ('STANDARD','HIGH')),state text NOT NULL DEFAULT 'PREVIEWED' CHECK(state IN ('PREVIEWED','MERGED','REVERSED','CANCELLED')),
 preview jsonb NOT NULL CHECK(jsonb_typeof(preview)='object' AND octet_length(preview::text)<=65536),preview_digest text NOT NULL CHECK(preview_digest~'^[a-f0-9]{64}$'),
 source_version bigint NOT NULL CHECK(source_version>0),target_version bigint NOT NULL CHECK(target_version>0),preview_expires_at timestamptz NOT NULL,
 approval_id uuid,merged_by uuid REFERENCES business_accounts(id),merged_at timestamptz,
 reversal_reason text,reversal_approval_id uuid,reversed_by uuid REFERENCES business_accounts(id),reversed_at timestamptz,
 reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,source_guest_id) REFERENCES guest_profiles(organisation_id,id),FOREIGN KEY(organisation_id,target_guest_id) REFERENCES guest_profiles(organisation_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),FOREIGN KEY(organisation_id,reversal_approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK(source_guest_id<>target_guest_id),CHECK(state NOT IN ('MERGED','REVERSED') OR (merged_at IS NOT NULL AND merged_by IS NOT NULL)),CHECK(risk<>'HIGH' OR state NOT IN ('MERGED','REVERSED') OR approval_id IS NOT NULL),
 CHECK(state<>'REVERSED' OR (reversed_at IS NOT NULL AND reversed_by IS NOT NULL AND reversal_approval_id IS NOT NULL AND reversal_reason IS NOT NULL)),
 UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX guest_merge_active_source ON guest_merges(organisation_id,source_guest_id) WHERE state='MERGED';
CREATE INDEX guest_merge_family ON guest_merges(organisation_id,target_guest_id,state,id);
CREATE INDEX guest_merge_source ON guest_merges(organisation_id,source_guest_id,created_at DESC,id DESC);
-- +goose StatementBegin
CREATE FUNCTION business_guest_merge_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Guest merge lineage is retained' USING ERRCODE='23514';END IF;
 IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.source_guest_id,NEW.target_guest_id,NEW.match_basis,NEW.ownership_evidence_reference,NEW.risk,NEW.preview,NEW.preview_digest,NEW.source_version,NEW.target_version,NEW.preview_expires_at,NEW.reason,NEW.created_by,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.source_guest_id,OLD.target_guest_id,OLD.match_basis,OLD.ownership_evidence_reference,OLD.risk,OLD.preview,OLD.preview_digest,OLD.source_version,OLD.target_version,OLD.preview_expires_at,OLD.reason,OLD.created_by,OLD.created_at) OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'Merge preview terms are immutable and versions monotonic' USING ERRCODE='23514';END IF;
 IF NOT ((OLD.state='PREVIEWED' AND NEW.state IN ('MERGED','CANCELLED')) OR (OLD.state='MERGED' AND NEW.state='REVERSED')) THEN RAISE EXCEPTION 'Invalid guest merge transition' USING ERRCODE='23514';END IF;
 IF OLD.state='MERGED' AND (NEW.approval_id,NEW.merged_by,NEW.merged_at) IS DISTINCT FROM (OLD.approval_id,OLD.merged_by,OLD.merged_at) THEN RAISE EXCEPTION 'Executed merge evidence is immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER guest_merge_guard BEFORE UPDATE OR DELETE ON guest_merges FOR EACH ROW EXECUTE FUNCTION business_guest_merge_guard();
CREATE TRIGGER guest_merge_retention BEFORE TRUNCATE ON guest_merges FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE guest_merge_references (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,merge_id uuid NOT NULL,
 entity_type text NOT NULL CHECK(entity_type IN ('reservation_guest','guest_property_scope','consent','loyalty_account','stay_record','stay_history_projection','identity_link','service_recovery_case','vip_classification','segment_membership','legal_hold','privacy_request','guest_profile')),
 entity_id uuid NOT NULL,from_guest_id uuid NOT NULL,to_guest_id uuid NOT NULL,
 operation text NOT NULL CHECK(operation IN ('REASSIGNED','RESTORED','SCOPE_ADDED','SCOPE_REVOKED','CONSENT_RESTRICTED','RESOLVED_VIA_LINEAGE','LOYALTY_ASSOCIATED','PROFILE_MERGED','PROFILE_RESTORED')),
 details jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(details)='object'),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,merge_id) REFERENCES guest_merges(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(merge_id,entity_type,entity_id,operation)
);
CREATE INDEX guest_merge_reference_lookup ON guest_merge_references(organisation_id,merge_id,entity_type,operation);
CREATE TRIGGER guest_merge_references_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON guest_merge_references FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
-- A MERGED profile must be explained by an executed lineage record, and a
-- restored profile by its reversal. Direct statements cannot fabricate either.
CREATE FUNCTION business_guest_merge_lineage_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.state='MERGED' AND OLD.state IS DISTINCT FROM 'MERGED' AND NOT EXISTS(SELECT 1 FROM guest_merges m WHERE m.organisation_id=NEW.organisation_id AND m.source_guest_id=NEW.id AND m.target_guest_id=NEW.merged_into AND m.state='MERGED') THEN RAISE EXCEPTION 'A merged guest requires executed merge lineage' USING ERRCODE='23514';END IF;
 IF OLD.state='MERGED' AND NEW.state IS DISTINCT FROM 'MERGED' AND NOT EXISTS(SELECT 1 FROM guest_merges m WHERE m.organisation_id=NEW.organisation_id AND m.source_guest_id=NEW.id AND m.target_guest_id=OLD.merged_into AND m.state='REVERSED') THEN RAISE EXCEPTION 'A merged guest is restored only by an approved reversal' USING ERRCODE='23514';END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER guest_merge_lineage AFTER UPDATE ON guest_profiles DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_guest_merge_lineage_guard();
-- +goose StatementBegin
-- Canonical guest resolution follows only executed merges, bounded and tenant-local.
CREATE FUNCTION business_guest_canonical(org uuid,guest uuid) RETURNS uuid LANGUAGE sql STABLE AS $$
 WITH RECURSIVE chain(id,depth) AS (SELECT guest,0 UNION ALL SELECT g.merged_into,c.depth+1 FROM chain c JOIN guest_profiles g ON g.organisation_id=org AND g.id=c.id WHERE g.state='MERGED' AND g.merged_into IS NOT NULL AND c.depth<32)
 SELECT id FROM chain ORDER BY depth DESC LIMIT 1
$$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Set-based notification planning needs UUIDv7 keys inside one statement.
CREATE FUNCTION business_crm_uuidv7() RETURNS uuid LANGUAGE plpgsql VOLATILE AS $$
DECLARE t text;r text;
BEGIN t=lpad(to_hex(floor(extract(epoch FROM clock_timestamp())*1000)::bigint),12,'0');r=replace(gen_random_uuid()::text,'-','');RETURN (substring(t,1,8)||'-'||substring(t,9,4)||'-7'||substring(r,1,3)||'-'||substring('89ab',(get_byte(decode(substring(r,4,2),'hex'),0)%4)+1,1)||substring(r,6,3)||'-'||substring(r,9,12))::uuid;END;$$;
-- +goose StatementEnd

CREATE TABLE guest_notification_templates (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('WAITLIST_OFFER','WAITLIST_EXPIRED','RESERVATION_CHANGE','ARRIVAL_NOTICE','STAY_NOTICE','SERVICE_RECOVERY_UPDATE','COMPENSATION_STATUS','LOYALTY_ENROLLED','LOYALTY_REWARD','CORPORATE_BOOKING_UPDATE')),
 version bigint NOT NULL CHECK(version>0),provider_template_key text NOT NULL CHECK(provider_template_key~'^[a-z0-9][a-z0-9._-]{2,99}$'),purpose text NOT NULL CHECK(purpose IN ('OPERATIONAL','MARKETING')),
 channels text[] NOT NULL CHECK(cardinality(channels) BETWEEN 1 AND 3 AND channels<@ARRAY['EMAIL','SMS','PUSH']::text[]),state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RETIRED')),
 approval_id uuid NOT NULL,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),retired_by uuid REFERENCES business_accounts(id),retired_at timestamptz,
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK((state='RETIRED')=(retired_at IS NOT NULL)),UNIQUE(organisation_id,property_id,kind,version),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX guest_notification_template_current ON guest_notification_templates(organisation_id,property_id,kind) WHERE state='ACTIVE';
-- +goose StatementBegin
CREATE FUNCTION business_notification_template_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Approved templates are retained' USING ERRCODE='23514';END IF;
 IF (to_jsonb(NEW)-'state'-'retired_by'-'retired_at') IS DISTINCT FROM (to_jsonb(OLD)-'state'-'retired_by'-'retired_at') OR NOT (OLD.state='ACTIVE' AND NEW.state='RETIRED') THEN RAISE EXCEPTION 'Approved template content is immutable; publish a new version' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER guest_notification_template_guard BEFORE UPDATE OR DELETE ON guest_notification_templates FOR EACH ROW EXECUTE FUNCTION business_notification_template_guard();

CREATE TABLE guest_notification_suppressions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,
 scope text NOT NULL CHECK(scope IN ('ALL','MARKETING','KIND')),kind text,state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','LIFTED')),
 evidence_reference text NOT NULL CHECK(length(evidence_reference) BETWEEN 5 AND 300),reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),
 lift_reason text,lifted_by uuid REFERENCES business_accounts(id),lifted_at timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),
 CHECK((scope='KIND')=(kind IS NOT NULL)),CHECK((state='LIFTED')=(lifted_at IS NOT NULL AND lifted_by IS NOT NULL AND lift_reason IS NOT NULL)),UNIQUE(organisation_id,id)
);
CREATE INDEX guest_notification_suppression_active ON guest_notification_suppressions(organisation_id,guest_id,property_id,state);
-- +goose StatementBegin
CREATE FUNCTION business_notification_suppression_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Suppression history is retained' USING ERRCODE='23514';END IF;
 IF (to_jsonb(NEW)-'state'-'lift_reason'-'lifted_by'-'lifted_at'-'version') IS DISTINCT FROM (to_jsonb(OLD)-'state'-'lift_reason'-'lifted_by'-'lifted_at'-'version') OR NEW.version<>OLD.version+1 OR NOT (OLD.state='ACTIVE' AND NEW.state='LIFTED') THEN RAISE EXCEPTION 'Suppressions are lifted once and otherwise immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER guest_notification_suppression_guard BEFORE UPDATE OR DELETE ON guest_notification_suppressions FOR EACH ROW EXECUTE FUNCTION business_notification_suppression_guard();

-- Waitlist entries carry only an opaque source reference. A notification
-- recipient must be an explicitly bound, registered guest in the same property.
CREATE TABLE waitlist_guest_bindings (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,entry_id uuid NOT NULL,guest_id uuid NOT NULL,
 evidence_reference text NOT NULL CHECK(length(evidence_reference) BETWEEN 5 AND 300),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,entry_id) REFERENCES waitlist_entries(organisation_id,property_id,id),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),
 UNIQUE(organisation_id,entry_id),UNIQUE(organisation_id,id)
);
CREATE TRIGGER waitlist_guest_bindings_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON waitlist_guest_bindings FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE guest_notification_jobs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 recipient_kind text NOT NULL CHECK(recipient_kind IN ('GUEST','CORPORATE')),guest_id uuid,corporate_id uuid,resolved_guest_id uuid,
 source_kind text NOT NULL CHECK(source_kind IN ('WAITLIST_OFFER','RESERVATION_VERSION','RESERVATION','STAY','SERVICE_RECOVERY_ACTION','RECOVERY_COMPENSATION','LOYALTY_ACCOUNT','LOYALTY_TRANSACTION')),source_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('WAITLIST_OFFER','WAITLIST_EXPIRED','RESERVATION_CHANGE','ARRIVAL_NOTICE','STAY_NOTICE','SERVICE_RECOVERY_UPDATE','COMPENSATION_STATUS','LOYALTY_ENROLLED','LOYALTY_REWARD','CORPORATE_BOOKING_UPDATE')),
 purpose text NOT NULL CHECK(purpose IN ('OPERATIONAL','MARKETING')),consent_purposes text[] NOT NULL DEFAULT '{}',
 state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','RUNNING','RETRY','ACCEPTED','SUPPRESSED','CANCELLED','EXPIRED','FAILED','BLOCKED')),
 template_id uuid,template_version bigint,channel text CHECK(channel IS NULL OR channel IN ('EMAIL','SMS','PUSH')),variables jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(variables)='object' AND octet_length(variables::text)<=8192),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),max_attempts integer NOT NULL DEFAULT 8 CHECK(max_attempts BETWEEN 1 AND 20),available_at timestamptz NOT NULL DEFAULT now(),expires_at timestamptz NOT NULL,
 lease_owner text,lease_generation bigint NOT NULL DEFAULT 0,lease_expires_at timestamptz,
 provider_notification_id uuid,provider_suppressed jsonb,delivery_status text NOT NULL DEFAULT 'NOT_SENT' CHECK(delivery_status IN ('NOT_SENT','ACCEPTED_BY_NOTIFICATION_SERVICE','PROVIDER_SUPPRESSED')),
 outcome_reason text,last_error text,accepted_at timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,guest_id) REFERENCES guest_profiles(organisation_id,id),FOREIGN KEY(organisation_id,resolved_guest_id) REFERENCES guest_profiles(organisation_id,id),
 FOREIGN KEY(organisation_id,corporate_id) REFERENCES corporate_customer_profiles(organisation_id,id),FOREIGN KEY(organisation_id,template_id) REFERENCES guest_notification_templates(organisation_id,id),
 CHECK((recipient_kind='GUEST')=(guest_id IS NOT NULL AND corporate_id IS NULL)),CHECK((recipient_kind='CORPORATE')=(corporate_id IS NOT NULL AND guest_id IS NULL)),
 CHECK(state<>'ACCEPTED' OR (provider_notification_id IS NOT NULL AND accepted_at IS NOT NULL AND delivery_status<>'NOT_SENT')),CHECK(delivery_status='NOT_SENT' OR provider_notification_id IS NOT NULL),
 UNIQUE NULLS NOT DISTINCT(organisation_id,source_kind,source_id,kind,guest_id,corporate_id),UNIQUE(organisation_id,id)
);
CREATE INDEX guest_notification_work ON guest_notification_jobs(state,available_at,id) WHERE state IN ('PENDING','RETRY','RUNNING');
CREATE INDEX guest_notification_recipient ON guest_notification_jobs(organisation_id,property_id,guest_id,created_at DESC,id DESC);
CREATE INDEX guest_notification_source ON guest_notification_jobs(organisation_id,source_kind,source_id);
-- +goose StatementBegin
-- Terminal outcomes are final. Provider acceptance is recorded once and can
-- never be rewritten into delivery or removed by an operator action.
CREATE FUNCTION business_notification_job_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Notification job history is retained' USING ERRCODE='23514';END IF;
 IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.recipient_kind,NEW.guest_id,NEW.corporate_id,NEW.source_kind,NEW.source_id,NEW.kind,NEW.purpose,NEW.consent_purposes,NEW.expires_at,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.recipient_kind,OLD.guest_id,OLD.corporate_id,OLD.source_kind,OLD.source_id,OLD.kind,OLD.purpose,OLD.consent_purposes,OLD.expires_at,OLD.created_at) OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'Notification job identity is immutable and versions monotonic' USING ERRCODE='23514';END IF;
 IF OLD.state IN ('ACCEPTED','SUPPRESSED','CANCELLED','EXPIRED') AND NEW.state IS DISTINCT FROM OLD.state THEN RAISE EXCEPTION 'Terminal notification outcomes are final' USING ERRCODE='23514';END IF;
 IF OLD.provider_notification_id IS NOT NULL AND (NEW.provider_notification_id,NEW.delivery_status,NEW.accepted_at) IS DISTINCT FROM (OLD.provider_notification_id,OLD.delivery_status,OLD.accepted_at) THEN RAISE EXCEPTION 'Recorded provider acceptance is immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER guest_notification_job_guard BEFORE UPDATE OR DELETE ON guest_notification_jobs FOR EACH ROW EXECUTE FUNCTION business_notification_job_guard();
CREATE TRIGGER guest_notification_job_retention BEFORE TRUNCATE ON guest_notification_jobs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE guest_notification_attempts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,job_id uuid NOT NULL,attempt integer NOT NULL CHECK(attempt>=0),
 outcome text NOT NULL CHECK(outcome IN ('ACCEPTED','RETRYABLE_ERROR','DEFINITE_ERROR','SUPPRESSED','CANCELLED','EXPIRED','BLOCKED','OPERATOR_RETRY','OPERATOR_CANCEL')),
 detail text NOT NULL CHECK(length(detail) BETWEEN 1 AND 300),provider_notification_id uuid,actor_id uuid REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,job_id) REFERENCES guest_notification_jobs(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE INDEX guest_notification_attempt_job ON guest_notification_attempts(organisation_id,job_id,created_at,id);
CREATE TRIGGER guest_notification_attempts_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON guest_notification_attempts FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- +goose Down
DROP TABLE IF EXISTS guest_notification_attempts;
DROP TABLE IF EXISTS guest_notification_jobs;
DROP FUNCTION IF EXISTS business_notification_job_guard();
DROP TABLE IF EXISTS waitlist_guest_bindings;
DROP TABLE IF EXISTS guest_notification_suppressions;
DROP FUNCTION IF EXISTS business_notification_suppression_guard();
DROP TABLE IF EXISTS guest_notification_templates;
DROP FUNCTION IF EXISTS business_notification_template_guard();
DROP FUNCTION IF EXISTS business_crm_uuidv7();
DROP TRIGGER IF EXISTS guest_merge_lineage ON guest_profiles;
DROP FUNCTION IF EXISTS business_guest_merge_lineage_guard();
DROP FUNCTION IF EXISTS business_guest_canonical(uuid,uuid);
DROP TABLE IF EXISTS guest_merge_references;
DROP TABLE IF EXISTS guest_merges;
DROP FUNCTION IF EXISTS business_guest_merge_guard();
