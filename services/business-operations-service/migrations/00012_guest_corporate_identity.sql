-- +goose Up
CREATE TABLE guest_profiles (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RESTRICTED','ARCHIVED','ANONYMISED','MERGED')),registration_reference text NOT NULL CHECK(length(registration_reference) BETWEEN 5 AND 300),encrypted_profile bytea NOT NULL,profile_key_id text NOT NULL,merged_into uuid,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,merged_into) REFERENCES guest_profiles(organisation_id,id),CHECK(merged_into IS DISTINCT FROM id),CHECK((state='MERGED')=(merged_into IS NOT NULL))
);
CREATE INDEX guest_profile_scope ON guest_profiles(organisation_id,property_id,state,created_at DESC,id DESC);
CREATE TABLE guest_property_scopes (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,guest_id uuid NOT NULL,property_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED')),approval_id uuid,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id) REFERENCES guest_profiles(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,guest_id,property_id),UNIQUE(organisation_id,id)
);
CREATE INDEX guest_property_visibility ON guest_property_scopes(organisation_id,property_id,state,guest_id);
CREATE TABLE customer_accounts_projection (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,source_kind text NOT NULL CHECK(source_kind IN ('MARKETPLACE','OTA')),source_service text NOT NULL,source_reference_hash text NOT NULL CHECK(source_reference_hash~'^[a-f0-9]{64}$'),source_version bigint NOT NULL CHECK(source_version>0),assertion_digest text NOT NULL CHECK(assertion_digest~'^[a-f0-9]{64}$'),evidence_reference text NOT NULL,expires_at timestamptz NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','RESTRICTED','REVOKED')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(connection_id,source_kind,source_reference_hash,source_version)
);
CREATE INDEX customer_projection_current ON customer_accounts_projection(organisation_id,connection_id,source_kind,source_reference_hash,source_version DESC);
CREATE TRIGGER customer_projection_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON customer_accounts_projection FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE guest_identity_links (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,guest_id uuid NOT NULL,property_id uuid NOT NULL,source_kind text NOT NULL CHECK(source_kind IN ('DIRECT','WALK_IN','CORPORATE','LEGACY','MARKETPLACE','OTA')),source_authority text NOT NULL,source_reference_hash text NOT NULL CHECK(source_reference_hash~'^[a-f0-9]{64}$'),connection_id uuid,projection_id uuid,proof_kind text NOT NULL CHECK(proof_kind IN ('LOCAL_REGISTRATION','APPROVED_SOURCE_LINK')),approval_id uuid,evidence_reference text NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED')),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id) REFERENCES guest_profiles(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,property_id,projection_id) REFERENCES customer_accounts_projection(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE NULLS NOT DISTINCT(organisation_id,source_kind,source_authority,connection_id,source_reference_hash),UNIQUE(organisation_id,id),CHECK((source_kind IN ('MARKETPLACE','OTA'))=(projection_id IS NOT NULL AND connection_id IS NOT NULL)),CHECK(proof_kind<>'APPROVED_SOURCE_LINK' OR approval_id IS NOT NULL)
);
CREATE INDEX guest_identity_active ON guest_identity_links(organisation_id,guest_id,property_id,state);
CREATE TABLE guest_preferences (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,encrypted_preferences bytea NOT NULL,preference_key_id text NOT NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>0),updated_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),UNIQUE(organisation_id,guest_id,property_id),UNIQUE(organisation_id,id)
);
CREATE TABLE consents (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,purpose text NOT NULL CHECK(purpose IN ('OPERATIONAL_CONTACT','MARKETING','PREFERENCES','ACCESSIBILITY','LOYALTY')),version bigint NOT NULL CHECK(version>0),state text NOT NULL CHECK(state IN ('GRANTED','WITHDRAWN')),source text NOT NULL CHECK(source IN ('STAFF_RECORDED','SIGNED_SOURCE')),evidence_reference text NOT NULL CHECK(length(evidence_reference) BETWEEN 5 AND 300),captured_at timestamptz NOT NULL,expires_at timestamptz,created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service IN ('business-operations-service','marketplace-service')),CHECK(expires_at IS NULL OR expires_at>captured_at),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),UNIQUE(guest_id,property_id,purpose,version),UNIQUE(organisation_id,id)
);
CREATE INDEX guest_consent_current ON consents(organisation_id,guest_id,property_id,purpose,version DESC);
CREATE TRIGGER consents_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON consents FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE guest_identity_history (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,entity_type text NOT NULL,entity_id uuid NOT NULL,version bigint NOT NULL,operation text NOT NULL,actor_id uuid REFERENCES business_accounts(id),actor_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),context jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(actor_id IS NOT NULL OR actor_service IN ('business-operations-service','marketplace-service')),FOREIGN KEY(organisation_id,guest_id) REFERENCES guest_profiles(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(entity_type,entity_id,version)
);
CREATE TRIGGER guest_identity_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON guest_identity_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE corporate_customer_profiles (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,legal_name text NOT NULL CHECK(length(legal_name) BETWEEN 1 AND 200),registration_reference text NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','SUSPENDED','ARCHIVED')),encrypted_contacts bytea NOT NULL,contact_key_id text NOT NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(organisation_id,registration_reference)
);
CREATE INDEX corporate_customer_scope ON corporate_customer_profiles(organisation_id,property_id,state,created_at DESC,id DESC);
CREATE TABLE corporate_property_scopes (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,corporate_id uuid NOT NULL,property_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED')),approval_id uuid,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,corporate_id) REFERENCES corporate_customer_profiles(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,corporate_id,property_id),UNIQUE(organisation_id,id)
);
CREATE INDEX corporate_property_visibility ON corporate_property_scopes(organisation_id,property_id,state,corporate_id);
CREATE TABLE corporate_customer_contracts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,corporate_id uuid NOT NULL,contract_key uuid NOT NULL,version bigint NOT NULL CHECK(version>0),state text NOT NULL CHECK(state IN ('ACTIVE','SUSPENDED','EXPIRED')),definition jsonb NOT NULL,effective_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,approval_id uuid NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(expires_at>effective_at),FOREIGN KEY(organisation_id,corporate_id,property_id) REFERENCES corporate_property_scopes(organisation_id,corporate_id,property_id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(contract_key,version)
);
CREATE INDEX corporate_contract_current ON corporate_customer_contracts(organisation_id,corporate_id,contract_key,version DESC);
CREATE TRIGGER corporate_contract_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON corporate_customer_contracts FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE guest_company_associations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_id uuid NOT NULL,corporate_id uuid NOT NULL,contract_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED')),effective_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,policy jsonb NOT NULL,approval_id uuid NOT NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(expires_at>effective_at),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),FOREIGN KEY(organisation_id,corporate_id,property_id) REFERENCES corporate_property_scopes(organisation_id,corporate_id,property_id),FOREIGN KEY(organisation_id,property_id,contract_id) REFERENCES corporate_customer_contracts(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(contract_id,guest_id)
);
CREATE INDEX corporate_traveler_current ON guest_company_associations(organisation_id,property_id,corporate_id,guest_id,state);
ALTER TABLE corporate_customer_contracts ADD CONSTRAINT corporate_contract_customer_identity UNIQUE(organisation_id,property_id,corporate_id,id);
ALTER TABLE guest_company_associations ADD CONSTRAINT corporate_traveler_contract_customer FOREIGN KEY(organisation_id,property_id,corporate_id,contract_id) REFERENCES corporate_customer_contracts(organisation_id,property_id,corporate_id,id);
ALTER TABLE reservation_guests ADD CONSTRAINT reservation_guest_owned_identity FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id) NOT VALID;
ALTER TABLE reservation_guests VALIDATE CONSTRAINT reservation_guest_owned_identity;
ALTER TABLE reservations ADD CONSTRAINT reservation_corporate_customer FOREIGN KEY(organisation_id,corporate_id,property_id) REFERENCES corporate_property_scopes(organisation_id,corporate_id,property_id) NOT VALID;
ALTER TABLE reservations VALIDATE CONSTRAINT reservation_corporate_customer;
ALTER TABLE reservations ADD CONSTRAINT reservation_corporate_contract FOREIGN KEY(organisation_id,property_id,corporate_id,corporate_contract_id) REFERENCES corporate_customer_contracts(organisation_id,property_id,corporate_id,id) NOT VALID;
ALTER TABLE reservations VALIDATE CONSTRAINT reservation_corporate_contract;

-- +goose StatementBegin
CREATE FUNCTION business_crm_version_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Identity/corporate lifecycle history is retained' USING ERRCODE='23514';END IF;
 IF (NEW.id,NEW.organisation_id,NEW.property_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.property_id,OLD.created_at) OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'Immutable CRM scope and monotonic version required' USING ERRCODE='23514';END IF;
 IF (to_jsonb(NEW)->'guest_id',to_jsonb(NEW)->'corporate_id',to_jsonb(NEW)->'contract_id') IS DISTINCT FROM (to_jsonb(OLD)->'guest_id',to_jsonb(OLD)->'corporate_id',to_jsonb(OLD)->'contract_id') THEN RAISE EXCEPTION 'CRM relationships are immutable; append a new approved association' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER guest_profile_version BEFORE UPDATE OR DELETE ON guest_profiles FOR EACH ROW EXECUTE FUNCTION business_crm_version_guard();
CREATE TRIGGER guest_scope_version BEFORE UPDATE OR DELETE ON guest_property_scopes FOR EACH ROW EXECUTE FUNCTION business_crm_version_guard();
CREATE TRIGGER guest_link_version BEFORE UPDATE OR DELETE ON guest_identity_links FOR EACH ROW EXECUTE FUNCTION business_crm_version_guard();
CREATE TRIGGER guest_preference_version BEFORE UPDATE OR DELETE ON guest_preferences FOR EACH ROW EXECUTE FUNCTION business_crm_version_guard();
CREATE TRIGGER corporate_profile_version BEFORE UPDATE OR DELETE ON corporate_customer_profiles FOR EACH ROW EXECUTE FUNCTION business_crm_version_guard();
CREATE TRIGGER corporate_scope_version BEFORE UPDATE OR DELETE ON corporate_property_scopes FOR EACH ROW EXECUTE FUNCTION business_crm_version_guard();
CREATE TRIGGER corporate_association_version BEFORE UPDATE OR DELETE ON guest_company_associations FOR EACH ROW EXECUTE FUNCTION business_crm_version_guard();

-- +goose StatementBegin
CREATE FUNCTION business_guest_link_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='UPDATE' AND (NEW.guest_id,NEW.source_kind,NEW.source_authority,NEW.source_reference_hash,NEW.connection_id,NEW.projection_id,NEW.proof_kind,NEW.evidence_reference,NEW.approval_id) IS DISTINCT FROM (OLD.guest_id,OLD.source_kind,OLD.source_authority,OLD.source_reference_hash,OLD.connection_id,OLD.projection_id,OLD.proof_kind,OLD.evidence_reference,OLD.approval_id) THEN RAISE EXCEPTION 'Identity evidence is immutable; use explicit conflict review' USING ERRCODE='23514';END IF;
 IF NEW.projection_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM customer_accounts_projection p WHERE p.organisation_id=NEW.organisation_id AND p.property_id=NEW.property_id AND p.id=NEW.projection_id AND p.connection_id=NEW.connection_id AND p.source_kind=NEW.source_kind AND p.source_reference_hash=NEW.source_reference_hash AND p.source_service=NEW.source_authority) THEN RAISE EXCEPTION 'Exact authenticated source projection required' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER guest_link_evidence BEFORE INSERT OR UPDATE ON guest_identity_links FOR EACH ROW EXECUTE FUNCTION business_guest_link_guard();

-- +goose Down
-- Identity/consent/corporate evidence is retained; rollback is application-based.
SELECT 1;
