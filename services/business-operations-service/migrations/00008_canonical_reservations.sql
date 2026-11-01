-- +goose Up
-- Canonical reservation authority. Existing graph, prices, holds and stock remain authoritative.
-- A service command carries an explicit authenticated service identity rather
-- than impersonating a human business owner. Historical human identities remain.
ALTER TABLE inventory_holds ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE inventory_holds ADD COLUMN created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE inventory_holds ADD CONSTRAINT inventory_hold_actor_identity CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service'));
ALTER TABLE price_quotes ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE price_quotes ADD COLUMN created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE price_quotes ADD CONSTRAINT price_quote_actor_identity CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service'));
ALTER TABLE inventory_calendar ALTER COLUMN updated_by DROP NOT NULL;
ALTER TABLE inventory_calendar ADD COLUMN updated_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'');
ALTER TABLE inventory_calendar ADD CONSTRAINT calendar_actor_identity CHECK(updated_by IS NOT NULL OR updated_by_service IN ('marketplace-service','business-operations-service'));
CREATE TABLE reservation_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=32768),approval_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','SUSPENDED')),effective_at timestamptz NOT NULL,expires_at timestamptz,
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id)
);
CREATE TRIGGER reservation_policies_version_guard BEFORE INSERT OR UPDATE ON reservation_policies FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_policies_scope ON reservation_policies(organisation_id,property_id,created_at,id);
CREATE UNIQUE INDEX reservation_policy_current ON reservation_policies(organisation_id,property_id) WHERE state='ACTIVE';
CREATE TABLE reservation_groups (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),source_reference text NOT NULL DEFAULT '',billing_reference text NOT NULL DEFAULT '',state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','CANCELLED','COMPLETED')),
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TRIGGER reservation_groups_version_guard BEFORE INSERT OR UPDATE ON reservation_groups FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_groups_scope ON reservation_groups(organisation_id,property_id,created_at,id);
CREATE TABLE reservations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 source text NOT NULL CHECK(source IN ('DIRECT','MARKETPLACE','OTA','WALK_IN','CORPORATE','GROUP','LEGACY','INTERNAL')),channel text NOT NULL DEFAULT '',source_reference text NOT NULL DEFAULT '',
 state text NOT NULL CHECK(state IN ('INQUIRY','HOLD','PENDING_PAYMENT','PENDING_APPROVAL','CONFIRMED','CHECKED_IN','CHECKED_OUT','COMPLETED','CANCELLED','NO_SHOW','REJECTED','EXPIRED','DISPUTED')),
 prior_state text,allocation_state text NOT NULL DEFAULT 'ACTIVE' CHECK(allocation_state IN ('ACTIVE','SPLIT_SOURCE','MERGED_SOURCE')),
 group_id uuid,parent_reservation_id uuid,policy_id uuid NOT NULL,policy_snapshot jsonb NOT NULL,
 payment_responsibility text NOT NULL CHECK(payment_responsibility IN ('PLATFORM_COLLECT','PAY_AT_PROPERTY','CORPORATE_CREDIT','CHANNEL_COLLECT')),
 financial_state text NOT NULL DEFAULT 'UNVERIFIED' CHECK(financial_state IN ('UNVERIFIED','PENDING','UNKNOWN','FAILED','EXPIRED','VERIFIED','APPROVED_GUARANTEE','DISPUTED')),
 amount_minor bigint NOT NULL CHECK(amount_minor>=0),currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),date_from date NOT NULL,date_to date NOT NULL CHECK(date_to>date_from),
 hold_expires_at timestamptz,confirmed_at timestamptz,cancelled_at timestamptz,completed_at timestamptz,risk_status text NOT NULL DEFAULT 'UNREVIEWED' CHECK(risk_status IN ('UNREVIEWED','CLEAR','REVIEW','RESTRICTED')),
 corporate_id uuid,corporate_contract_id uuid,reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES reservation_policies(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,group_id) REFERENCES reservation_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,parent_reservation_id) REFERENCES reservations(organisation_id,property_id,id)
);
CREATE TRIGGER reservations_version_guard BEFORE INSERT OR UPDATE ON reservations FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservations_scope ON reservations(organisation_id,property_id,created_at,id);
CREATE INDEX reservation_command_center ON reservations(organisation_id,property_id,state,date_from,date_to,id);
CREATE INDEX reservation_expiry ON reservations(state,hold_expires_at,id);
CREATE TABLE reservation_financial_groups (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,parent_group_id uuid,amount_minor bigint NOT NULL CHECK(amount_minor>=0),required_amount_minor bigint NOT NULL CHECK(required_amount_minor>=0),currency text NOT NULL,policy_reference text NOT NULL,agreement_version bigint NOT NULL,state text NOT NULL DEFAULT 'PENDING' CHECK(state IN ('PENDING','UNKNOWN','VERIFIED','APPROVED_GUARANTEE','FAILED','EXPIRED','DISPUTED')),responsibility text NOT NULL,guarantee_approval_id uuid,
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,parent_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,guarantee_approval_id) REFERENCES approval_requests(organisation_id,id)
);
CREATE TRIGGER reservation_financial_groups_version_guard BEFORE INSERT OR UPDATE ON reservation_financial_groups FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_financial_groups_scope ON reservation_financial_groups(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_financial_credits (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 source_group_id uuid NOT NULL,target_group_id uuid NOT NULL,amount_minor bigint NOT NULL CHECK(amount_minor>0),currency text NOT NULL,approval_id uuid NOT NULL,source_reservation_version bigint NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,source_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,target_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),CHECK(source_group_id<>target_group_id),UNIQUE(source_group_id,target_group_id)
);
CREATE TRIGGER reservation_financial_credits_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_financial_credits FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_financial_credits_target ON reservation_financial_credits(organisation_id,target_group_id);
-- +goose StatementBegin
CREATE FUNCTION business_reservation_credit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM reservation_financial_groups f WHERE f.organisation_id=NEW.organisation_id AND (f.id=NEW.source_group_id AND f.amount_minor<(SELECT coalesce(sum(c.amount_minor),0) FROM reservation_financial_credits c WHERE c.source_group_id=f.id) OR f.id=NEW.target_group_id AND f.amount_minor<f.required_amount_minor+(SELECT coalesce(sum(c.amount_minor),0) FROM reservation_financial_credits c WHERE c.target_group_id=f.id) OR f.id IN (NEW.source_group_id,NEW.target_group_id) AND f.currency<>NEW.currency)) THEN RAISE EXCEPTION 'Financial credit exceeds its bound obligation or currency' USING ERRCODE='23514'; END IF;
 IF EXISTS(WITH RECURSIVE ancestors(id,path,cycle) AS (SELECT NEW.source_group_id,ARRAY[NEW.source_group_id],false UNION ALL SELECT c.source_group_id,a.path||c.source_group_id,c.source_group_id=ANY(a.path) FROM ancestors a JOIN reservation_financial_credits c ON c.target_group_id=a.id WHERE NOT a.cycle AND cardinality(a.path)<=21) SELECT 1 FROM ancestors WHERE id=NEW.target_group_id OR cycle OR cardinality(path)>20) THEN RAISE EXCEPTION 'Financial credit lineage cannot cycle or exceed its review depth' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END;$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER reservation_financial_credit_integrity AFTER INSERT ON reservation_financial_credits DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_reservation_credit_guard();
-- +goose StatementBegin
CREATE FUNCTION business_reservation_financial_terms_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.reservation_id,NEW.parent_group_id,NEW.amount_minor,NEW.required_amount_minor,NEW.currency,NEW.policy_reference,NEW.agreement_version,NEW.responsibility,NEW.guarantee_approval_id) IS DISTINCT FROM (OLD.reservation_id,OLD.parent_group_id,OLD.amount_minor,OLD.required_amount_minor,OLD.currency,OLD.policy_reference,OLD.agreement_version,OLD.responsibility,OLD.guarantee_approval_id) THEN RAISE EXCEPTION 'Accepted financial terms are immutable; append an obligation and bound credit lineage' USING ERRCODE='23514'; END IF; RETURN NEW;END;$$;
-- +goose StatementEnd
CREATE TRIGGER reservation_financial_terms_immutable BEFORE UPDATE ON reservation_financial_groups FOR EACH ROW EXECUTE FUNCTION business_reservation_financial_terms_guard();
CREATE TABLE reservation_rooms (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,financial_group_id uuid NOT NULL,product_id uuid NOT NULL,pool_id uuid NOT NULL,quote_id uuid NOT NULL,hold_id uuid,
 date_from date NOT NULL,date_to date NOT NULL CHECK(date_to>date_from),quantity integer NOT NULL CHECK(quantity BETWEEN 1 AND 1000),adults integer NOT NULL CHECK(adults>0),children integer NOT NULL CHECK(children>=0),
 amount_minor bigint NOT NULL CHECK(amount_minor>=0),currency text NOT NULL,accommodation_minor bigint CHECK(accommodation_minor>=0),snapshot jsonb NOT NULL CHECK(octet_length(snapshot::text)<=262144),accepted_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','CANCELLED','SUPERSEDED','COMPLETED')),previous_room_id uuid,overbooked_quantity integer NOT NULL DEFAULT 0 CHECK(overbooked_quantity>=0 AND overbooked_quantity<=quantity),
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,financial_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),FOREIGN KEY(organisation_id,quote_id) REFERENCES price_quotes(organisation_id,id),FOREIGN KEY(organisation_id,hold_id) REFERENCES inventory_holds(organisation_id,id),FOREIGN KEY(organisation_id,previous_room_id) REFERENCES reservation_rooms(organisation_id,id)
);
CREATE TRIGGER reservation_rooms_version_guard BEFORE INSERT OR UPDATE ON reservation_rooms FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_rooms_scope ON reservation_rooms(organisation_id,property_id,created_at,id);
CREATE INDEX reservation_rooms_parent ON reservation_rooms(organisation_id,reservation_id,state,id);
CREATE TABLE reservation_versions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,reservation_version bigint NOT NULL,action text NOT NULL,prior_state text,snapshot jsonb NOT NULL,reason text NOT NULL,correlation_id text NOT NULL DEFAULT '',
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),UNIQUE(reservation_id,reservation_version)
);
CREATE TRIGGER reservation_versions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_versions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_versions_scope ON reservation_versions(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_price_snapshots (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,room_id uuid NOT NULL,quote_id uuid NOT NULL,snapshot jsonb NOT NULL,accepted_at timestamptz NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,room_id) REFERENCES reservation_rooms(organisation_id,id),FOREIGN KEY(organisation_id,quote_id) REFERENCES price_quotes(organisation_id,id),UNIQUE(room_id)
);
CREATE TRIGGER reservation_price_snapshots_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_price_snapshots FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_price_snapshots_scope ON reservation_price_snapshots(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_guests (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,room_id uuid,guest_id uuid,source_kind text NOT NULL CHECK(source_kind IN ('DIRECT','MARKETPLACE','OTA','CORPORATE','WALK_IN','LEGACY')),source_reference text NOT NULL CHECK(length(source_reference) BETWEEN 1 AND 300),is_primary boolean NOT NULL DEFAULT false,registration_reference text NOT NULL DEFAULT '',document_references uuid[] NOT NULL DEFAULT '{}',
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,room_id) REFERENCES reservation_rooms(organisation_id,id),UNIQUE(reservation_id,source_kind,source_reference)
);
CREATE TRIGGER reservation_guests_version_guard BEFORE INSERT OR UPDATE ON reservation_guests FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_guests_scope ON reservation_guests(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_source_refs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 connection_id uuid NOT NULL,reservation_id uuid NOT NULL,source_service text NOT NULL,provider text NOT NULL,external_reservation_id text NOT NULL,external_version text NOT NULL,payload_hash text NOT NULL,receipt_id uuid,acceptance_reference text NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,receipt_id) REFERENCES channel_event_receipts(organisation_id,id),FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id),UNIQUE(organisation_id,connection_id,provider,external_reservation_id,external_version)
);
CREATE TRIGGER reservation_source_refs_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_source_refs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_source_refs_scope ON reservation_source_refs(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_holds (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 room_id uuid NOT NULL,hold_id uuid NOT NULL,hold_version bigint NOT NULL,source_reference text NOT NULL DEFAULT '',
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,room_id) REFERENCES reservation_rooms(organisation_id,id),FOREIGN KEY(organisation_id,hold_id) REFERENCES inventory_holds(organisation_id,id),UNIQUE(hold_id)
);
CREATE TRIGGER reservation_holds_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_holds FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_holds_scope ON reservation_holds(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_inventory_days (
 organisation_id uuid NOT NULL,property_id uuid NOT NULL,room_id uuid NOT NULL,pool_id uuid NOT NULL,day date NOT NULL,quantity integer NOT NULL CHECK(quantity>0),
 allotment_id uuid,allotted_quantity integer NOT NULL DEFAULT 0 CHECK(allotted_quantity>=0 AND allotted_quantity<=quantity),released_at timestamptz,
 PRIMARY KEY(room_id,day),FOREIGN KEY(organisation_id,property_id,room_id) REFERENCES reservation_rooms(organisation_id,property_id,id),FOREIGN KEY(organisation_id,pool_id,day) REFERENCES inventory_calendar(organisation_id,pool_id,day),FOREIGN KEY(organisation_id,allotment_id) REFERENCES inventory_allotments(organisation_id,id),CHECK(allotted_quantity=0 OR allotment_id IS NOT NULL)
);
CREATE INDEX reservation_inventory_active ON reservation_inventory_days(organisation_id,pool_id,day) WHERE released_at IS NULL;
CREATE TABLE reservation_modifications (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,prior_version bigint NOT NULL,new_version bigint NOT NULL,action text NOT NULL,change_context jsonb NOT NULL,reason text NOT NULL,approval_id uuid,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(reservation_id,new_version)
);
CREATE TRIGGER reservation_modifications_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_modifications FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_modifications_scope ON reservation_modifications(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_cancellation_obligations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,reservation_id uuid NOT NULL,reservation_version bigint NOT NULL,
 kind text NOT NULL CHECK(kind IN ('CANCELLED','PARTIAL_CANCELLED','NO_SHOW','REJECTED','EXPIRED')),currency text NOT NULL,accepted_amount_minor bigint NOT NULL CHECK(accepted_amount_minor>=0),penalty_minor bigint NOT NULL CHECK(penalty_minor>=0),platform_collected_minor bigint CHECK(platform_collected_minor>=0),refundable_minor bigint CHECK(refundable_minor>=0),financial_state text NOT NULL CHECK(financial_state IN ('KNOWN_PLATFORM_REFERENCE','READBACK_REQUIRED','EXTERNAL_COLLECTION_UNKNOWN')),policy_snapshot jsonb NOT NULL,room_effects jsonb NOT NULL,reason text NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(reservation_id,reservation_version) REFERENCES reservation_versions(reservation_id,reservation_version) DEFERRABLE INITIALLY DEFERRED,UNIQUE(reservation_id,reservation_version)
);
CREATE TRIGGER reservation_cancellation_obligations_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_cancellation_obligations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_cancellation_obligations_scope ON reservation_cancellation_obligations(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_lineage (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 source_reservation_id uuid NOT NULL,target_reservation_id uuid NOT NULL,room_ids uuid[] NOT NULL,action text NOT NULL CHECK(action IN ('SPLIT','MERGED')),source_version bigint NOT NULL,target_version bigint NOT NULL,approval_id uuid NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,source_reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,target_reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),CHECK(source_reservation_id<>target_reservation_id)
);
CREATE TRIGGER reservation_lineage_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_lineage FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_lineage_scope ON reservation_lineage(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_financial_requests (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,financial_group_id uuid NOT NULL,session_id uuid REFERENCES business_sessions(id),kind text NOT NULL CHECK(kind IN ('COLLECTION','REFUND','SETTLEMENT','READBACK')),state text NOT NULL DEFAULT 'QUEUED' CHECK(state IN ('QUEUED','RUNNING','RETRY','PENDING','SUCCEEDED','DEPENDENCY_BLOCKED','QUARANTINED')),request jsonb NOT NULL,request_hash text NOT NULL,idempotency_key text NOT NULL,provider_reference text NOT NULL DEFAULT '',lease_owner text,lease_generation bigint NOT NULL DEFAULT 0,lease_expires_at timestamptz,attempts integer NOT NULL DEFAULT 0,available_at timestamptz NOT NULL DEFAULT now(),last_error text,
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,financial_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),UNIQUE(organisation_id,idempotency_key)
);
CREATE TRIGGER reservation_financial_requests_version_guard BEFORE INSERT OR UPDATE ON reservation_financial_requests FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_financial_requests_scope ON reservation_financial_requests(organisation_id,property_id,created_at,id);
CREATE INDEX reservation_financial_work ON reservation_financial_requests(state,available_at,id);
CREATE TABLE reservation_payment_bindings (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 financial_group_id uuid NOT NULL,intent_id uuid NOT NULL,locked_quote_id uuid NOT NULL,payer_wallet_id uuid NOT NULL,source_service text NOT NULL,request_hash text NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,financial_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),UNIQUE(financial_group_id),UNIQUE(intent_id),UNIQUE(locked_quote_id)
);
CREATE TRIGGER reservation_payment_bindings_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_payment_bindings FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_payment_bindings_scope ON reservation_payment_bindings(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_payment_projections (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 financial_group_id uuid NOT NULL,intent_id uuid NOT NULL,provider_state text NOT NULL,settlement_state text NOT NULL,reversed_minor bigint NOT NULL DEFAULT 0 CHECK(reversed_minor>=0),funding_confirmed boolean NOT NULL DEFAULT false,remedy_version integer NOT NULL DEFAULT 0 CHECK(remedy_version>=0),remedy_review_required boolean NOT NULL DEFAULT true,amount_minor bigint NOT NULL CHECK(amount_minor>=0),currency text NOT NULL,source_reference text NOT NULL,payload_hash text NOT NULL,observed_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,source_service text NOT NULL CHECK(source_service='payment-service'),
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,financial_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),UNIQUE(financial_group_id,intent_id,source_reference)
);
CREATE TRIGGER reservation_payment_projections_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_payment_projections FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_payment_projections_scope ON reservation_payment_projections(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_risk_projection (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,source_service text NOT NULL,source_reference text NOT NULL,status text NOT NULL CHECK(status IN ('CLEAR','REVIEW','RESTRICTED')),reason_code text NOT NULL,review_reference text NOT NULL DEFAULT '',appeal_reference text NOT NULL DEFAULT '',expires_at timestamptz NOT NULL,approval_id uuid,
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(reservation_id,source_service)
);
CREATE TRIGGER reservation_risk_projection_version_guard BEFORE INSERT OR UPDATE ON reservation_risk_projection FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_risk_projection_scope ON reservation_risk_projection(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_conflicts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid,connection_id uuid,receipt_id uuid,source_reference text NOT NULL,classification text NOT NULL,state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','RETRY','RESOLVED','REJECTED')),evidence jsonb NOT NULL,reason text NOT NULL,
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,receipt_id),FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),FOREIGN KEY(organisation_id,receipt_id) REFERENCES channel_event_receipts(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id)
);
CREATE TRIGGER reservation_conflicts_version_guard BEFORE INSERT OR UPDATE ON reservation_conflicts FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_conflicts_scope ON reservation_conflicts(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_conflict_decisions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 conflict_id uuid NOT NULL,action text NOT NULL,reason text NOT NULL,approval_id uuid NOT NULL,evidence_references text[] NOT NULL DEFAULT '{}',
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,conflict_id) REFERENCES reservation_conflicts(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id)
);
CREATE TRIGGER reservation_conflict_decisions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_conflict_decisions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_conflict_decisions_scope ON reservation_conflict_decisions(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_overbooking_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 definition jsonb NOT NULL,state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','SUSPENDED')),approval_id uuid NOT NULL,
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id)
);
CREATE TRIGGER reservation_overbooking_policies_version_guard BEFORE INSERT OR UPDATE ON reservation_overbooking_policies FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX reservation_overbooking_policies_scope ON reservation_overbooking_policies(organisation_id,property_id,created_at,id);
CREATE TABLE reservation_overbooking_permits (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,policy_id uuid NOT NULL,quantity integer NOT NULL CHECK(quantity>0),approval_id uuid NOT NULL,reason text NOT NULL,recovery_reference text NOT NULL,expires_at timestamptz NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,policy_id) REFERENCES reservation_overbooking_policies(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id)
);
CREATE TRIGGER reservation_overbooking_permits_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_overbooking_permits FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE INDEX reservation_overbooking_permits_scope ON reservation_overbooking_permits(organisation_id,property_id,created_at,id);
CREATE TABLE waitlist_entries (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 product_id uuid NOT NULL,date_from date NOT NULL,date_to date NOT NULL CHECK(date_to>date_from),adults integer NOT NULL CHECK(adults>0),children integer NOT NULL CHECK(children>=0),quantity integer NOT NULL CHECK(quantity>0),priority integer NOT NULL DEFAULT 0 CHECK(priority BETWEEN 0 AND 100),guest_reference text NOT NULL,state text NOT NULL DEFAULT 'WAITING' CHECK(state IN ('WAITING','OFFERED','ACCEPTED','REJECTED','CANCELLED','EXPIRED')),expires_at timestamptz NOT NULL,notification_account_id uuid,
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),FOREIGN KEY(notification_account_id) REFERENCES business_accounts(id)
);
CREATE TRIGGER waitlist_entries_version_guard BEFORE INSERT OR UPDATE ON waitlist_entries FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX waitlist_entries_scope ON waitlist_entries(organisation_id,property_id,created_at,id);
CREATE INDEX waitlist_pending ON waitlist_entries(organisation_id,property_id,state,priority DESC,created_at,id);
CREATE TABLE waitlist_offers (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL, property_id uuid NOT NULL,
 entry_id uuid NOT NULL,hold_id uuid NOT NULL,quote_id uuid NOT NULL,state text NOT NULL DEFAULT 'OFFERED' CHECK(state IN ('OFFERED','ACCEPTED','REJECTED','EXPIRED')),expires_at timestamptz NOT NULL,reservation_id uuid,
 version bigint NOT NULL DEFAULT 1,updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),'') CHECK(created_by IS NOT NULL OR created_by_service IN ('marketplace-service','business-operations-service')),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),FOREIGN KEY(organisation_id,entry_id) REFERENCES waitlist_entries(organisation_id,id),FOREIGN KEY(organisation_id,hold_id) REFERENCES inventory_holds(organisation_id,id),FOREIGN KEY(organisation_id,quote_id) REFERENCES price_quotes(organisation_id,id),FOREIGN KEY(organisation_id,reservation_id) REFERENCES reservations(organisation_id,id),UNIQUE(hold_id)
);
CREATE TRIGGER waitlist_offers_version_guard BEFORE INSERT OR UPDATE ON waitlist_offers FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE INDEX waitlist_offers_scope ON waitlist_offers(organisation_id,property_id,created_at,id);
ALTER TABLE channel_reservation_refs ADD CONSTRAINT channel_canonical_reservation FOREIGN KEY(organisation_id,canonical_reservation_id) REFERENCES reservations(organisation_id,id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION business_stock_integrity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE org uuid;pool uuid;
BEGIN
 org=coalesce(NEW.organisation_id,OLD.organisation_id);pool=coalesce(NEW.pool_id,OLD.pool_id);
 IF EXISTS(SELECT 1 FROM inventory_calendar c JOIN inventory_pools p ON p.id=c.pool_id AND p.organisation_id=c.organisation_id WHERE c.organisation_id=org AND c.pool_id=pool AND (c.committed<(SELECT coalesce(sum(d.quantity),0) FROM reservation_inventory_days d WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND d.released_at IS NULL) OR c.capacity>p.capacity OR c.blocked+c.committed+
  (SELECT coalesce(sum(d.quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND h.state='ACTIVE' AND h.expires_at>now())+
  (SELECT coalesce(sum(d.quantity),0) FROM inventory_allotment_days d JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>now())-
  (SELECT coalesce(sum(d.allotted_quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND h.state='ACTIVE' AND h.expires_at>now() AND a.state='ACTIVE' AND a.release_at>now())-
  (SELECT coalesce(sum(d.allotted_quantity),0) FROM reservation_inventory_days d JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>now())>c.capacity)) THEN RAISE EXCEPTION 'Canonical stock cannot be oversold or manufactured by an allotment' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM inventory_allotment_days a WHERE a.organisation_id=org AND a.pool_id=pool AND a.quantity <
 (SELECT coalesce(sum(d.allotted_quantity),0) FROM reservation_inventory_days d WHERE d.organisation_id=org AND d.allotment_id=a.allotment_id AND d.day=a.day AND d.released_at IS NULL)+
 (SELECT coalesce(sum(d.allotted_quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.allotment_id=a.allotment_id AND d.day=a.day AND h.state='ACTIVE' AND h.expires_at>now())) THEN RAISE EXCEPTION 'Held and committed channel allotment provenance exceeds its original quota' USING ERRCODE='23514';END IF;
 RETURN NULL;
END; $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER reservation_days_stock_integrity AFTER INSERT OR UPDATE OR DELETE ON reservation_inventory_days DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stock_integrity();
-- +goose StatementBegin
CREATE FUNCTION business_reservation_days_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM 1 FROM organisations WHERE id=coalesce(NEW.organisation_id,OLD.organisation_id) FOR UPDATE;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Reservation inventory history is retained'; END IF;
 IF TG_OP='UPDATE' AND (to_jsonb(NEW)-'released_at' IS DISTINCT FROM to_jsonb(OLD)-'released_at' OR OLD.released_at IS NOT NULL OR NEW.released_at IS NULL) THEN RAISE EXCEPTION 'Only one-way reservation inventory release is permitted' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER reservation_days_guard BEFORE INSERT OR UPDATE OR DELETE ON reservation_inventory_days FOR EACH ROW EXECUTE FUNCTION business_reservation_days_guard();
-- +goose StatementBegin
CREATE FUNCTION business_reservation_room_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.product_id,NEW.pool_id,NEW.quote_id,NEW.hold_id,NEW.date_from,NEW.date_to,NEW.quantity,NEW.adults,NEW.children,NEW.amount_minor,NEW.currency,NEW.snapshot,NEW.accepted_at,NEW.financial_group_id) IS DISTINCT FROM (OLD.product_id,OLD.pool_id,OLD.quote_id,OLD.hold_id,OLD.date_from,OLD.date_to,OLD.quantity,OLD.adults,OLD.children,OLD.amount_minor,OLD.currency,OLD.snapshot,OLD.accepted_at,OLD.financial_group_id) THEN RAISE EXCEPTION 'Accepted room price and allocation history is immutable; append a modification' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER reservation_room_price_guard BEFORE UPDATE ON reservation_rooms FOR EACH ROW EXECUTE FUNCTION business_reservation_room_guard();
-- A reservation mutation cannot commit without its matching immutable version,
-- security audit and durable event in the same transaction.
-- +goose StatementBegin
CREATE FUNCTION business_reservation_transition_commit_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM reservation_versions v WHERE v.organisation_id=NEW.organisation_id AND v.reservation_id=NEW.id AND v.reservation_version=NEW.version AND v.snapshot->'reservation'->>'state'=NEW.state AND v.snapshot->'reservation'->>'financial_state'=NEW.financial_state AND (v.snapshot->'reservation'->>'amount_minor')::bigint=NEW.amount_minor AND v.snapshot->'reservation'->>'allocation_state'=NEW.allocation_state)
 OR NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.organisation_id=NEW.organisation_id AND a.target_type='reservation' AND a.target_id=NEW.id AND (a.change_context->>'version')::bigint=NEW.version)
 OR NOT EXISTS(SELECT 1 FROM outbox_events e WHERE e.producer='business-operations-service' AND e.aggregate_type='reservation' AND e.aggregate_id=NEW.id AND e.event_type LIKE 'reservation.%.v1' AND (e.payload->>'version')::bigint=NEW.version)
 THEN RAISE EXCEPTION 'Reservation transition must commit its matching immutable history, audit and outbox event' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END;$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER reservation_transition_commit_integrity AFTER INSERT OR UPDATE ON reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_reservation_transition_commit_guard();
INSERT INTO permissions(code,description) VALUES ('reservations:read','Operational reservations:read'),('reservations:write','Operational reservations:write'),('reservations:approve','Operational reservations:approve'),('reservations:confirm','Operational reservations:confirm'),('reservations:cancel','Operational reservations:cancel'),('reservations:modify','Operational reservations:modify'),('reservations:overbook','Operational reservations:overbook'),('reservations:resolve','Operational reservations:resolve'),('reservation_risk:read','Operational reservation_risk:read'),('reservation_risk:write','Operational reservation_risk:write'),('rms:read','Operational rms:read'),('rms:write','Operational rms:write'),('rms:approve','Operational rms:approve'),('rms:autopilot','Operational rms:autopilot'),('pms:read','Operational pms:read'),('pms:write','Operational pms:write'),('pms:approve','Operational pms:approve'),('room_status:read','Operational room_status:read'),('room_status:write','Operational room_status:write'),('room_status:inspect','Operational room_status:inspect'),('folios:read','Operational folios:read'),('folios:write','Operational folios:write'),('folios:approve','Operational folios:approve'),('cashier:read','Operational cashier:read'),('cashier:write','Operational cashier:write'),('cashier:approve','Operational cashier:approve'),('night_audit:read','Operational night_audit:read'),('night_audit:write','Operational night_audit:write'),('night_audit:approve','Operational night_audit:approve'),('guests:read','Operational guests:read'),('guests:write','Operational guests:write'),('guest_private:read','Operational guest_private:read'),('guest_private:write','Operational guest_private:write'),('guest_privacy:write','Operational guest_privacy:write'),('corporate:read','Operational corporate:read'),('corporate:write','Operational corporate:write'),('loyalty:read','Operational loyalty:read'),('loyalty:write','Operational loyalty:write'),('loyalty:approve','Operational loyalty:approve'),('recovery:read','Operational recovery:read'),('recovery:write','Operational recovery:write'),('recovery:approve','Operational recovery:approve') ON CONFLICT DO NOTHING;
-- +goose StatementBegin
CREATE FUNCTION business_graph_uuidv7_seed() RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE t text;r text;
BEGIN t=lpad(to_hex(floor(extract(epoch FROM clock_timestamp())*1000)::bigint),12,'0');r=replace(gen_random_uuid()::text,'-','');RETURN (substring(t,1,8)||'-'||substring(t,9,4)||'-7'||substring(r,1,3)||'-8'||substring(r,4,3)||'-'||substring(r,7,12))::uuid;END;$$;
-- +goose StatementEnd
-- +goose StatementBegin
DO $$ DECLARE item record;next_role uuid;next_version bigint;permission_code text;
BEGIN
 FOR item IN SELECT m.id member_id,m.organisation_id,m.account_id,r.* FROM staff_memberships m JOIN organisations o ON o.id=m.organisation_id JOIN role_templates r ON r.id=m.role_id AND r.organisation_id=m.organisation_id JOIN business_accounts actor_account ON actor_account.id=m.account_id WHERE actor_account.status='ACTIVE' AND o.state IN ('DRAFT','KYB_PENDING','ACTIVE') AND m.kind='OWNER' AND m.status='ACTIVE' AND m.scope_kind='COMPANY' AND m.scope_id=m.organisation_id AND o.representative_account_id=m.account_id AND r.active AND r.name='Owner' AND m.effective_at<=now() AND (m.expires_at IS NULL OR m.expires_at>now()) AND EXISTS(SELECT 1 FROM role_permissions rp WHERE rp.role_id=r.id AND rp.permission='roles:write') AND NOT EXISTS(SELECT 1 FROM staff_scope_grants denied WHERE denied.membership_id=m.id AND denied.capability='roles:write' AND denied.deny AND denied.revoked_at IS NULL AND denied.effective_at<=now() AND (denied.expires_at IS NULL OR denied.expires_at>now())) AND EXISTS(SELECT 1 FROM staff_scope_grants g WHERE g.membership_id=m.id AND g.capability='roles:write' AND g.scope_kind='COMPANY' AND g.scope_id=m.organisation_id AND g.inherit AND NOT g.deny AND g.revoked_at IS NULL AND g.expires_at IS NULL AND g.effective_at<=now())
 LOOP
 PERFORM 1 FROM organisations WHERE id=item.organisation_id FOR UPDATE;
 next_role=business_graph_uuidv7_seed();SELECT max(version)+1 INTO next_version FROM role_templates WHERE organisation_id=item.organisation_id AND role_key=item.role_key;
 INSERT INTO role_templates(id,organisation_id,role_key,name,version,active,senior_emergency_eligible) VALUES(next_role,item.organisation_id,item.role_key,item.name,next_version,true,item.senior_emergency_eligible);
 INSERT INTO role_permissions(role_id,permission) SELECT next_role,permission FROM role_permissions WHERE role_id=item.id;
 FOR permission_code IN SELECT unnest(ARRAY['reservations:read','reservations:write','reservations:approve','reservations:confirm','reservations:cancel','reservations:modify','reservations:overbook','reservations:resolve','reservation_risk:read','reservation_risk:write','rms:read','rms:write','rms:approve','rms:autopilot','pms:read','pms:write','pms:approve','room_status:read','room_status:write','room_status:inspect','folios:read','folios:write','folios:approve','cashier:read','cashier:write','cashier:approve','night_audit:read','night_audit:write','night_audit:approve','guests:read','guests:write','guest_private:read','guest_private:write','guest_privacy:write','corporate:read','corporate:write','loyalty:read','loyalty:write','loyalty:approve','recovery:read','recovery:write','recovery:approve']::text[]) LOOP
 INSERT INTO role_permissions(role_id,permission) VALUES(next_role,permission_code) ON CONFLICT DO NOTHING;
 INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES(business_graph_uuidv7_seed(),item.organisation_id,item.member_id,permission_code,'COMPANY',item.organisation_id,true,now());
 END LOOP;
 UPDATE staff_memberships SET role_id=next_role,version=version+1,updated_at=now() WHERE id=item.member_id;
 INSERT INTO audit_events(id,organisation_id,action,target_type,target_id,change_context) VALUES(business_graph_uuidv7_seed(),item.organisation_id,'SECTIONS811_OWNER_CAPABILITY_MIGRATION','staff_membership',item.member_id,jsonb_build_object('from_role',item.id,'to_role',next_role,'version',next_version));
 END LOOP;
END;$$;
-- +goose StatementEnd
DROP FUNCTION business_graph_uuidv7_seed();
-- +goose Down
-- Forward only; preserve reservation/source/financial/audit lineage.
SELECT 1;
