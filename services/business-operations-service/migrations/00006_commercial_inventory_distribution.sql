-- +goose Up
-- Commercial objects reference the existing canonical physical graph. No
-- reservation, payment ledger, external credential authority or media store.
CREATE UNIQUE INDEX commercial_media_link_tenant_id ON inventory_media_links(organisation_id,id);
CREATE TABLE compliance_holds (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 3 AND 2000),source_reference text NOT NULL,source_service text NOT NULL DEFAULT 'business-operations-service',
 effective_at timestamptz NOT NULL,expires_at timestamptz,revoked_at timestamptz,
 version bigint NOT NULL DEFAULT 1,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),CHECK(expires_at IS NULL OR expires_at>effective_at),UNIQUE(organisation_id,id)
);
CREATE INDEX compliance_holds_property ON compliance_holds(organisation_id,property_id,effective_at);
CREATE TABLE tax_profiles (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,policy_key uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=32768),
 source_reference text NOT NULL CHECK(length(source_reference) BETWEEN 1 AND 500),approval_id uuid,effective_at timestamptz NOT NULL,expires_at timestamptz,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK(expires_at IS NULL OR expires_at>effective_at),UNIQUE(organisation_id,id),UNIQUE(organisation_id,policy_key,version)
);
CREATE TABLE currency_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 precision_digits integer NOT NULL CHECK(precision_digits BETWEEN 0 AND 4),rounding text NOT NULL CHECK(rounding='HALF_AWAY_FROM_ZERO'),version bigint NOT NULL DEFAULT 1,
 source_reference text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,property_id,currency),UNIQUE(organisation_id,id)
);
CREATE TABLE inventory_pools (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,node_id uuid NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),kind text NOT NULL CHECK(kind IN ('UNIT','ROOM','ROOM_TYPE','FACILITY')),
 capacity integer NOT NULL CHECK(capacity BETWEEN 1 AND 100000),state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','SUSPENDED','RETIRED')),
 version bigint NOT NULL DEFAULT 1,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE inventory_pool_members (
 organisation_id uuid NOT NULL,pool_id uuid NOT NULL,node_id uuid NOT NULL,capacity integer NOT NULL CHECK(capacity>0),node_version bigint NOT NULL,
 PRIMARY KEY(pool_id,node_id),UNIQUE(organisation_id,node_id),FOREIGN KEY(organisation_id,pool_id) REFERENCES inventory_pools(organisation_id,id),FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id)
);
ALTER TABLE inventory_pools ADD COLUMN source_node_version bigint NOT NULL DEFAULT 1 CHECK(source_node_version>0);
CREATE TABLE rate_plans (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 kind text NOT NULL,state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','SUSPENDED','ARCHIVED')),version bigint NOT NULL DEFAULT 1,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE rate_plan_versions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,rate_plan_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),amount_minor bigint NOT NULL CHECK(amount_minor>=0),
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=65536),
 effective_from date NOT NULL,effective_to date NOT NULL,approval_id uuid,reason text NOT NULL,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK(effective_to>effective_from),UNIQUE(organisation_id,id),UNIQUE(rate_plan_id,version),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX rate_versions_effective ON rate_plan_versions(organisation_id,rate_plan_id,effective_from,effective_to,version DESC);
CREATE TABLE rate_relations (
 organisation_id uuid NOT NULL,child_version_id uuid PRIMARY KEY,parent_version_id uuid NOT NULL,
 adjustments jsonb NOT NULL CHECK(jsonb_typeof(adjustments)='array'),
 FOREIGN KEY(organisation_id,child_version_id) REFERENCES rate_plan_versions(organisation_id,id),FOREIGN KEY(organisation_id,parent_version_id) REFERENCES rate_plan_versions(organisation_id,id),CHECK(child_version_id<>parent_version_id)
);
CREATE TABLE rate_rules (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,rate_version_id uuid NOT NULL,priority integer NOT NULL,
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),FOREIGN KEY(organisation_id,rate_version_id) REFERENCES rate_plan_versions(organisation_id,id),UNIQUE(rate_version_id,priority)
);
CREATE TABLE sellable_products (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,node_id uuid NOT NULL,pool_id uuid NOT NULL,rate_plan_id uuid,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','SUSPENDED','RETIRED')),
 min_guests integer NOT NULL DEFAULT 1 CHECK(min_guests>0),max_guests integer NOT NULL CHECK(max_guests>=min_guests AND max_guests<=100000),
 policy_code text NOT NULL,policy_version bigint NOT NULL CHECK(policy_version>0),channels text[] NOT NULL DEFAULT '{}',version bigint NOT NULL DEFAULT 1,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id),
 FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX sellable_products_property ON sellable_products(organisation_id,property_id,state,id);
CREATE TABLE inventory_calendar (
 organisation_id uuid NOT NULL,pool_id uuid NOT NULL,day date NOT NULL,capacity integer NOT NULL CHECK(capacity>=0),blocked integer NOT NULL DEFAULT 0 CHECK(blocked>=0),
 committed integer NOT NULL DEFAULT 0 CHECK(committed>=0),version bigint NOT NULL DEFAULT 1,reason text NOT NULL DEFAULT '',updated_by uuid NOT NULL REFERENCES business_accounts(id),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(pool_id,day),FOREIGN KEY(organisation_id,pool_id) REFERENCES inventory_pools(organisation_id,id),CHECK(blocked+committed<=capacity),UNIQUE(organisation_id,pool_id,day)
);
CREATE TABLE inventory_allotments (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,pool_id uuid NOT NULL,channel text NOT NULL,
 date_from date NOT NULL,date_to date NOT NULL,quantity integer NOT NULL CHECK(quantity>0),release_at timestamptz NOT NULL,state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RELEASED','EXPIRED')),
 version bigint NOT NULL DEFAULT 1,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),CHECK(date_to>date_from),UNIQUE(organisation_id,id)
);
CREATE TABLE inventory_allotment_days (
 organisation_id uuid NOT NULL,allotment_id uuid NOT NULL,pool_id uuid NOT NULL,day date NOT NULL,channel text NOT NULL,quantity integer NOT NULL CHECK(quantity>0),released_at timestamptz,
 PRIMARY KEY(allotment_id,day),FOREIGN KEY(organisation_id,allotment_id) REFERENCES inventory_allotments(organisation_id,id),FOREIGN KEY(organisation_id,pool_id,day) REFERENCES inventory_calendar(organisation_id,pool_id,day)
);
CREATE UNIQUE INDEX active_allotment_day ON inventory_allotment_days(pool_id,day,channel) WHERE released_at IS NULL;
CREATE TABLE rate_restrictions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,pool_id uuid,product_id uuid,rate_plan_id uuid,channel text NOT NULL DEFAULT '',
 date_from date NOT NULL,date_to date NOT NULL,definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=16384),
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','REVOKED')),version bigint NOT NULL DEFAULT 1,reason text NOT NULL,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),CHECK(date_to>date_from),UNIQUE(organisation_id,id)
);
CREATE INDEX restriction_evaluation ON rate_restrictions(organisation_id,property_id,state,date_from,date_to);
CREATE TABLE promotions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,code text NOT NULL,definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','SUSPENDED','ARCHIVED')),version bigint NOT NULL DEFAULT 1,approval_id uuid,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,property_id,code),UNIQUE(organisation_id,id)
);
CREATE TABLE add_ons (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,name text NOT NULL,definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','SUSPENDED','ARCHIVED')),version bigint NOT NULL DEFAULT 1,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE packages (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,product_id uuid NOT NULL,name text NOT NULL,definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','SUSPENDED','ARCHIVED')),version bigint NOT NULL DEFAULT 1,approval_id uuid,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE TABLE package_components (
 organisation_id uuid NOT NULL,package_id uuid NOT NULL,add_on_id uuid NOT NULL,quantity integer NOT NULL CHECK(quantity BETWEEN 1 AND 100),
 PRIMARY KEY(package_id,add_on_id),FOREIGN KEY(organisation_id,package_id) REFERENCES packages(organisation_id,id),FOREIGN KEY(organisation_id,add_on_id) REFERENCES add_ons(organisation_id,id)
);
CREATE TABLE price_quotes (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,product_id uuid NOT NULL,rate_version_id uuid NOT NULL,
 date_from date NOT NULL,date_to date NOT NULL,amount_minor bigint NOT NULL CHECK(amount_minor>=0),currency text NOT NULL,expires_at timestamptz NOT NULL,
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=262144),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_version_id) REFERENCES rate_plan_versions(organisation_id,property_id,id),CHECK(date_to>date_from),UNIQUE(organisation_id,id)
);
CREATE TABLE inventory_holds (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,product_id uuid NOT NULL,pool_id uuid NOT NULL,quote_id uuid NOT NULL,
 date_from date NOT NULL,date_to date NOT NULL,quantity integer NOT NULL CHECK(quantity>0),channel text NOT NULL DEFAULT '',
 state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','CANCELLED','EXPIRED','HANDED_OFF')),expires_at timestamptz NOT NULL,version bigint NOT NULL DEFAULT 1,
 handoff_reference text,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),FOREIGN KEY(organisation_id,quote_id) REFERENCES price_quotes(organisation_id,id),
 CHECK(date_to>date_from),UNIQUE(organisation_id,id)
);
CREATE INDEX inventory_holds_expiry ON inventory_holds(state,expires_at,id);
CREATE TABLE inventory_hold_days (
 organisation_id uuid NOT NULL,hold_id uuid NOT NULL,pool_id uuid NOT NULL,day date NOT NULL,quantity integer NOT NULL CHECK(quantity>0),allotment_id uuid,allotted_quantity integer NOT NULL DEFAULT 0 CHECK(allotted_quantity>=0 AND allotted_quantity<=quantity),
 PRIMARY KEY(hold_id,day),FOREIGN KEY(organisation_id,hold_id) REFERENCES inventory_holds(organisation_id,id),FOREIGN KEY(organisation_id,pool_id,day) REFERENCES inventory_calendar(organisation_id,pool_id,day),FOREIGN KEY(organisation_id,allotment_id) REFERENCES inventory_allotments(organisation_id,id),CHECK(allotted_quantity=0 OR allotment_id IS NOT NULL)
);
CREATE INDEX hold_days_availability ON inventory_hold_days(organisation_id,pool_id,day,hold_id);
CREATE TABLE bulk_rate_commands (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,state text NOT NULL DEFAULT 'PREVIEW' CHECK(state IN ('PREVIEW','QUEUED','RUNNING','APPLIED','ROLLED_BACK','EXPIRED','QUARANTINED')),
 command jsonb NOT NULL,snapshot jsonb NOT NULL,execution_session_id uuid REFERENCES business_sessions(id),execution_action text,available_at timestamptz NOT NULL DEFAULT now(),last_error text,expires_at timestamptz NOT NULL,version bigint NOT NULL DEFAULT 1,approval_id uuid,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE TABLE listing_templates (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL REFERENCES organisations(id),name text NOT NULL,definition jsonb NOT NULL,version bigint NOT NULL DEFAULT 1,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(organisation_id,id)
);
CREATE TABLE bulk_rate_scope_links (
 organisation_id uuid NOT NULL,command_id uuid NOT NULL,property_id uuid NOT NULL,
 PRIMARY KEY(command_id,property_id),FOREIGN KEY(organisation_id,command_id) REFERENCES bulk_rate_commands(organisation_id,id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id)
);
CREATE TRIGGER bulk_rate_scope_links_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON bulk_rate_scope_links FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE listings (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,product_id uuid NOT NULL,template_id uuid,
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','REVIEW','APPROVED','PUBLISHED','PAUSED','WITHDRAWN','ARCHIVED')),version bigint NOT NULL DEFAULT 1,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),FOREIGN KEY(organisation_id,template_id) REFERENCES listing_templates(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE listing_versions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,listing_id uuid NOT NULL,version bigint NOT NULL,content jsonb NOT NULL CHECK(jsonb_typeof(content)='object' AND octet_length(content::text)<=65536),property_version bigint NOT NULL,
 reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,listing_id) REFERENCES listings(organisation_id,id),UNIQUE(listing_id,version),UNIQUE(organisation_id,id)
);
CREATE TABLE listing_media_links (
 organisation_id uuid NOT NULL,listing_version_id uuid NOT NULL,media_link_id uuid NOT NULL,PRIMARY KEY(listing_version_id,media_link_id),
 FOREIGN KEY(organisation_id,listing_version_id) REFERENCES listing_versions(organisation_id,id),FOREIGN KEY(organisation_id,media_link_id) REFERENCES inventory_media_links(organisation_id,id)
);
CREATE TABLE content_quality_scores (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,listing_version_id uuid NOT NULL,score integer NOT NULL CHECK(score BETWEEN 0 AND 100),issues jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,listing_version_id) REFERENCES listing_versions(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE TABLE listing_approvals (
 organisation_id uuid NOT NULL,listing_version_id uuid NOT NULL,approval_id uuid NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(listing_version_id,approval_id),FOREIGN KEY(organisation_id,listing_version_id) REFERENCES listing_versions(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id)
);
CREATE TABLE channel_providers (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),code text NOT NULL UNIQUE,name text NOT NULL,kind text NOT NULL CHECK(kind IN ('FIRST_PARTY','OTA','PARTNER')),
 status text NOT NULL CHECK(status IN ('AVAILABLE_UNCONFIGURED','CERTIFICATION_PENDING','CERTIFIED','SUSPENDED','DISABLED')),mechanisms text[] NOT NULL,
 contract_version text NOT NULL,rate_limit_per_minute integer NOT NULL CHECK(rate_limit_per_minute>0),regions text[] NOT NULL DEFAULT '{}',currencies text[] NOT NULL DEFAULT '{}',object_types text[] NOT NULL,
 certification_reference text,version bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE channel_capabilities (
 provider_id uuid NOT NULL REFERENCES channel_providers(id),capability text NOT NULL,status text NOT NULL CHECK(status IN ('UNSUPPORTED','AVAILABLE_UNCONFIGURED','CONFIGURATION_PENDING','CERTIFICATION_PENDING','CERTIFIED','DEGRADED','SUSPENDED','DISABLED')),
 evidence_reference text,version bigint NOT NULL DEFAULT 1,PRIMARY KEY(provider_id,capability),CHECK(status<>'CERTIFIED' OR length(evidence_reference)>0)
);
CREATE TABLE channel_connections (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,provider_id uuid NOT NULL REFERENCES channel_providers(id),provider_account_id text NOT NULL,
 state text NOT NULL DEFAULT 'DISCONNECTED' CHECK(state IN ('DISCONNECTED','AUTHORIZING','CONNECTED','DEGRADED','SUSPENDED')),
 credential_reference text NOT NULL DEFAULT '',credential_key_id text,encrypted_credentials bytea,credential_expires_at timestamptz,
 validation_reference text,validated_at timestamptz,version bigint NOT NULL DEFAULT 1,
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,provider_id,provider_account_id,property_id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE channel_property_mappings (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,connection_id uuid NOT NULL,property_id uuid NOT NULL,external_id text NOT NULL CHECK(length(external_id) BETWEEN 1 AND 200),state text NOT NULL CHECK(state IN ('PREVIEW','CONFIRMED','REJECTED')),provenance text NOT NULL,version bigint NOT NULL DEFAULT 1,
 validated_at timestamptz,last_synced_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),UNIQUE(connection_id,external_id),UNIQUE(connection_id,property_id),UNIQUE(organisation_id,id)
);
CREATE TABLE channel_product_mappings (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,product_id uuid NOT NULL,external_id text NOT NULL CHECK(length(external_id) BETWEEN 1 AND 200),state text NOT NULL CHECK(state IN ('PREVIEW','CONFIRMED','REJECTED')),provenance text NOT NULL,version bigint NOT NULL DEFAULT 1,
 validated_at timestamptz,last_synced_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),UNIQUE(connection_id,external_id),UNIQUE(connection_id,product_id),UNIQUE(organisation_id,id)
);
CREATE TABLE channel_rate_mappings (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,rate_plan_id uuid NOT NULL,external_id text NOT NULL CHECK(length(external_id) BETWEEN 1 AND 200),state text NOT NULL CHECK(state IN ('PREVIEW','CONFIRMED','REJECTED')),provenance text NOT NULL,version bigint NOT NULL DEFAULT 1,
 validated_at timestamptz,last_synced_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),UNIQUE(connection_id,external_id),UNIQUE(connection_id,rate_plan_id),UNIQUE(organisation_id,id)
);
CREATE TABLE channel_field_authority (
 organisation_id uuid NOT NULL,connection_id uuid NOT NULL,field text NOT NULL CHECK(field NOT IN ('capacity','organisation_id','property_id','pool_id','committed','holds')),
 mode text NOT NULL CHECK(mode IN ('INTERNAL','CHANNEL_OVERRIDE','MANUAL_OVERRIDE')),value jsonb,reason text NOT NULL,source_reference text NOT NULL,expires_at timestamptz,last_synced_at timestamptz,
 version bigint NOT NULL DEFAULT 1,edited_by uuid NOT NULL REFERENCES business_accounts(id),updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(connection_id,field),FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id)
);
CREATE TABLE channel_mapping_previews (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,connection_version bigint NOT NULL,
 entries jsonb NOT NULL CHECK(jsonb_typeof(entries)='array' AND octet_length(entries::text)<=65536),proofs jsonb NOT NULL CHECK(jsonb_typeof(proofs)='array'),digest text NOT NULL,
 created_by uuid NOT NULL REFERENCES business_accounts(id),expires_at timestamptz NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),UNIQUE(organisation_id,id)
);
CREATE TRIGGER channel_mapping_preview_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON channel_mapping_previews FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE listing_publication (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,listing_id uuid NOT NULL,listing_version_id uuid NOT NULL,connection_id uuid NOT NULL,
 desired_state text NOT NULL CHECK(desired_state IN ('PUBLISHED','PAUSED','WITHDRAWN')),delivery_state text NOT NULL DEFAULT 'PENDING' CHECK(delivery_state IN ('PENDING','CONFIRMED','UNKNOWN','CONFLICT','QUARANTINED')),
 canonical_version bigint NOT NULL,acknowledged_version bigint,readback_version bigint,version bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,listing_id) REFERENCES listings(organisation_id,id),FOREIGN KEY(organisation_id,listing_version_id) REFERENCES listing_versions(organisation_id,id),FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id),UNIQUE(listing_id,connection_id),UNIQUE(organisation_id,id)
);
CREATE TABLE channel_content_overrides (
 organisation_id uuid NOT NULL,connection_id uuid NOT NULL,listing_id uuid NOT NULL,content jsonb NOT NULL,reason text NOT NULL,expires_at timestamptz,version bigint NOT NULL DEFAULT 1,edited_by uuid NOT NULL REFERENCES business_accounts(id),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(connection_id,listing_id),FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id),FOREIGN KEY(organisation_id,listing_id) REFERENCES listings(organisation_id,id)
);
CREATE TABLE channel_sync_jobs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,publication_id uuid,kind text NOT NULL CHECK(kind IN ('CONTENT','ARI','WITHDRAW','PAUSE','RECONCILE')),
 canonical_version bigint NOT NULL,payload jsonb NOT NULL CHECK(octet_length(payload::text)<=524288),payload_hash text NOT NULL,idempotency_key text NOT NULL,
 state text NOT NULL DEFAULT 'QUEUED' CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','RETRY','QUARANTINED','FAILED_FINAL')),
 actor_id uuid NOT NULL REFERENCES business_accounts(id),session_id uuid REFERENCES business_sessions(id) ON DELETE SET NULL,
 attempts integer NOT NULL DEFAULT 0,available_at timestamptz NOT NULL DEFAULT now(),lease_owner text,lease_expires_at timestamptz,lease_generation bigint NOT NULL DEFAULT 0,
 provider_request_id text,acknowledged_version bigint,readback_version bigint,last_error text NOT NULL DEFAULT '',last_success_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),FOREIGN KEY(organisation_id,publication_id) REFERENCES listing_publication(organisation_id,id),UNIQUE(connection_id,idempotency_key),UNIQUE(organisation_id,id)
);
CREATE INDEX channel_sync_work ON channel_sync_jobs(state,available_at,lease_expires_at,id);
CREATE INDEX channel_sync_connection ON channel_sync_jobs(organisation_id,connection_id,created_at,id);
CREATE TABLE channel_provider_dispatch (
 provider_id uuid PRIMARY KEY REFERENCES channel_providers(id),next_at timestamptz NOT NULL,lease_owner text,lease_expires_at timestamptz
);
ALTER TABLE channel_sync_jobs ADD COLUMN correlation_context jsonb NOT NULL DEFAULT '{}';
ALTER TABLE channel_sync_jobs ADD COLUMN provider_command_key text NOT NULL DEFAULT '';
CREATE TABLE channel_sync_cursors (
 organisation_id uuid NOT NULL,connection_id uuid PRIMARY KEY,poll_cursor text NOT NULL DEFAULT '',last_canonical_version bigint NOT NULL DEFAULT 0,last_published_version bigint NOT NULL DEFAULT 0,last_acknowledged_version bigint NOT NULL DEFAULT 0,readback_version bigint NOT NULL DEFAULT 0,
 last_success_at timestamptz,last_failure_at timestamptz,last_poll_at timestamptz,version bigint NOT NULL DEFAULT 1,FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id)
);
CREATE TABLE channel_sync_failures (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,connection_id uuid NOT NULL,job_id uuid,classification text NOT NULL,evidence jsonb NOT NULL,attempt integer NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id),FOREIGN KEY(organisation_id,job_id) REFERENCES channel_sync_jobs(organisation_id,id)
);
CREATE TABLE channel_conflicts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,job_id uuid,classification text NOT NULL,severity text NOT NULL CHECK(severity IN ('LOW','MEDIUM','HIGH','CRITICAL')),
 source_object text NOT NULL,canonical_version bigint,provider_version text,evidence jsonb NOT NULL,state text NOT NULL DEFAULT 'OPEN' CHECK(state IN ('OPEN','RETRY','RESOLVED','REJECTED')),required_action text NOT NULL,
 version bigint NOT NULL DEFAULT 1,first_seen_at timestamptz NOT NULL DEFAULT now(),last_seen_at timestamptz NOT NULL DEFAULT now(),resolved_at timestamptz,
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),FOREIGN KEY(organisation_id,job_id) REFERENCES channel_sync_jobs(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE INDEX channel_conflicts_open ON channel_conflicts(organisation_id,property_id,state,last_seen_at,id);
CREATE TABLE channel_conflict_decisions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,conflict_id uuid NOT NULL,action text NOT NULL,reason text NOT NULL,actor_id uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,conflict_id) REFERENCES channel_conflicts(organisation_id,id)
);
CREATE TABLE channel_event_receipts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,provider_event_id text NOT NULL,external_object_id text NOT NULL,external_version text NOT NULL,
 payload_hash text NOT NULL,normalized jsonb NOT NULL CHECK(octet_length(normalized::text)<=65536),state text NOT NULL CHECK(state IN ('RECEIVED','DUPLICATE','QUARANTINED','ACCEPTED')),reason text NOT NULL DEFAULT '',received_at timestamptz NOT NULL DEFAULT now(),processed_at timestamptz,
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),UNIQUE(connection_id,provider_event_id),UNIQUE(connection_id,external_object_id,external_version),UNIQUE(organisation_id,id)
);
ALTER TABLE channel_event_receipts ADD COLUMN duplicate_count bigint NOT NULL DEFAULT 0;
CREATE TABLE channel_event_delivery_keys (
 organisation_id uuid NOT NULL,connection_id uuid NOT NULL,provider_event_id text NOT NULL CHECK(length(provider_event_id) BETWEEN 1 AND 200),receipt_id uuid NOT NULL,payload_hash text NOT NULL,
 PRIMARY KEY(connection_id,provider_event_id),FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id),FOREIGN KEY(organisation_id,receipt_id) REFERENCES channel_event_receipts(organisation_id,id)
);
CREATE TRIGGER channel_delivery_keys_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON channel_event_delivery_keys FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE channel_reservation_refs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,connection_id uuid NOT NULL,receipt_id uuid NOT NULL,external_reservation_id text NOT NULL,external_version text NOT NULL,
 hold_id uuid,canonical_reservation_id uuid,acceptance_reference text,state text NOT NULL CHECK(state IN ('DEPENDENCY_BLOCKED','QUARANTINED','CANONICALLY_ACCEPTED')),
 created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id),FOREIGN KEY(organisation_id,receipt_id) REFERENCES channel_event_receipts(organisation_id,id),FOREIGN KEY(organisation_id,hold_id) REFERENCES inventory_holds(organisation_id,id),UNIQUE(connection_id,external_reservation_id,external_version),CHECK(state<>'CANONICALLY_ACCEPTED' OR canonical_reservation_id IS NOT NULL AND acceptance_reference IS NOT NULL)
);
CREATE TABLE channel_commission_rules (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,connection_id uuid NOT NULL,currency text NOT NULL,commission_bps integer NOT NULL CHECK(commission_bps BETWEEN 0 AND 10000),fixed_minor bigint NOT NULL DEFAULT 0 CHECK(fixed_minor>=0),
 effective_at timestamptz NOT NULL,expires_at timestamptz,source_reference text NOT NULL,version bigint NOT NULL DEFAULT 1,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id),UNIQUE(organisation_id,id),CHECK(expires_at IS NULL OR expires_at>effective_at)
);
CREATE TABLE channel_financial_inputs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,connection_id uuid NOT NULL,product_id uuid,rate_plan_id uuid,promotion_id uuid,
 component text NOT NULL CHECK(component IN ('GROSS_REVENUE','COMMISSION','PAYMENT_FEE','PROMOTION_COST','OPERATIONAL_COST')),amount_minor bigint NOT NULL CHECK(amount_minor>=0),currency text NOT NULL,
 period_from date NOT NULL,period_to date NOT NULL,source_service text NOT NULL,source_reference text NOT NULL,observed_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,reconciled boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,connection_id) REFERENCES channel_connections(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,product_id) REFERENCES sellable_products(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,rate_plan_id) REFERENCES rate_plans(organisation_id,property_id,id),FOREIGN KEY(organisation_id,promotion_id) REFERENCES promotions(organisation_id,id),CHECK(period_to>period_from),UNIQUE(connection_id,component,source_service,source_reference)
);
CREATE TABLE channel_metrics_snapshots (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,connection_id uuid NOT NULL,snapshot jsonb NOT NULL,observed_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,connection_id) REFERENCES channel_connections(organisation_id,id)
);
ALTER TABLE channel_financial_inputs ADD COLUMN source_payload_hash text NOT NULL;
ALTER TABLE channel_content_overrides ADD COLUMN last_synced_at timestamptz;
CREATE TABLE commercial_history (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,entity_type text NOT NULL,entity_id uuid NOT NULL,operation text NOT NULL,
 version bigint NOT NULL,actor_id uuid REFERENCES business_accounts(id),correlation_id text NOT NULL DEFAULT '',snapshot jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id)
);
ALTER TABLE commercial_history ADD COLUMN sequence bigint GENERATED ALWAYS AS IDENTITY;
CREATE INDEX commercial_history_projection ON commercial_history(organisation_id,property_id,sequence);
ALTER TABLE channel_sync_cursors ADD COLUMN last_history_sequence bigint NOT NULL DEFAULT 0;
ALTER TABLE channel_sync_cursors ADD COLUMN last_maintenance_at timestamptz;
CREATE INDEX commercial_history_entity ON commercial_history(organisation_id,entity_type,entity_id,created_at,id);
ALTER TABLE sellable_products ADD CONSTRAINT product_pool_identity UNIQUE(organisation_id,id,pool_id);
ALTER TABLE price_quotes ADD CONSTRAINT quote_product_identity UNIQUE(organisation_id,property_id,product_id,id);
ALTER TABLE inventory_holds ADD CONSTRAINT hold_pool_identity UNIQUE(organisation_id,id,pool_id);
ALTER TABLE inventory_holds ADD CONSTRAINT hold_product_pool FOREIGN KEY(organisation_id,product_id,pool_id) REFERENCES sellable_products(organisation_id,id,pool_id);
ALTER TABLE inventory_holds ADD CONSTRAINT hold_quote_product FOREIGN KEY(organisation_id,property_id,product_id,quote_id) REFERENCES price_quotes(organisation_id,property_id,product_id,id);
ALTER TABLE inventory_allotments ADD CONSTRAINT allotment_pool_identity UNIQUE(organisation_id,id,pool_id);
ALTER TABLE inventory_hold_days ADD CONSTRAINT hold_day_pool_identity FOREIGN KEY(organisation_id,hold_id,pool_id) REFERENCES inventory_holds(organisation_id,id,pool_id);
ALTER TABLE inventory_allotment_days ADD CONSTRAINT allotment_day_pool_identity FOREIGN KEY(organisation_id,allotment_id,pool_id) REFERENCES inventory_allotments(organisation_id,id,pool_id);
CREATE TRIGGER commercial_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON commercial_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER rate_versions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rate_plan_versions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER rate_relations_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rate_relations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER rate_rules_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rate_rules FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER price_quotes_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON price_quotes FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER tax_profiles_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON tax_profiles FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER listing_versions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON listing_versions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER listing_approvals_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON listing_approvals FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER listing_media_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON listing_media_links FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER channel_failures_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON channel_sync_failures FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER channel_decisions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON channel_conflict_decisions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER channel_commission_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON channel_commission_rules FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE UNIQUE INDEX channel_commission_versions ON channel_commission_rules(organisation_id,connection_id,currency,version);
CREATE TRIGGER channel_financial_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON channel_financial_inputs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER channel_metrics_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON channel_metrics_snapshots FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_commercial_version_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM 1 FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 THEN RAISE EXCEPTION 'Initial commercial version must be one' USING ERRCODE='23514'; END IF;
 ELSE
  IF TG_TABLE_NAME IN ('channel_field_authority','channel_content_overrides','channel_property_mappings','channel_product_mappings','channel_rate_mappings') AND NEW.version=OLD.version AND to_jsonb(NEW)-'last_synced_at' IS NOT DISTINCT FROM to_jsonb(OLD)-'last_synced_at' THEN RETURN NEW; END IF;
  IF NEW.version<>OLD.version+1 OR (to_jsonb(NEW)->'id',to_jsonb(NEW)->'organisation_id',to_jsonb(NEW)->'property_id',to_jsonb(NEW)->'created_at',to_jsonb(NEW)->'created_by') IS DISTINCT FROM (to_jsonb(OLD)->'id',to_jsonb(OLD)->'organisation_id',to_jsonb(OLD)->'property_id',to_jsonb(OLD)->'created_at',to_jsonb(OLD)->'created_by') THEN RAISE EXCEPTION 'Immutable commercial identity or stale version' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER currency_policies_version_guard BEFORE INSERT OR UPDATE ON currency_policies FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER compliance_holds_version_guard BEFORE INSERT OR UPDATE ON compliance_holds FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER inventory_pools_version_guard BEFORE INSERT OR UPDATE ON inventory_pools FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER rate_plans_version_guard BEFORE INSERT OR UPDATE ON rate_plans FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER sellable_products_version_guard BEFORE INSERT OR UPDATE ON sellable_products FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER inventory_calendar_version_guard BEFORE INSERT OR UPDATE ON inventory_calendar FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER inventory_allotments_version_guard BEFORE INSERT OR UPDATE ON inventory_allotments FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER rate_restrictions_version_guard BEFORE INSERT OR UPDATE ON rate_restrictions FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER promotions_version_guard BEFORE INSERT OR UPDATE ON promotions FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER add_ons_version_guard BEFORE INSERT OR UPDATE ON add_ons FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER packages_version_guard BEFORE INSERT OR UPDATE ON packages FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER inventory_holds_version_guard BEFORE INSERT OR UPDATE ON inventory_holds FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER bulk_rate_commands_version_guard BEFORE INSERT OR UPDATE ON bulk_rate_commands FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER listing_templates_version_guard BEFORE INSERT OR UPDATE ON listing_templates FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER listings_version_guard BEFORE INSERT OR UPDATE ON listings FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER channel_connections_version_guard BEFORE INSERT OR UPDATE ON channel_connections FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER channel_property_mappings_version_guard BEFORE INSERT OR UPDATE ON channel_property_mappings FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER channel_product_mappings_version_guard BEFORE INSERT OR UPDATE ON channel_product_mappings FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER channel_rate_mappings_version_guard BEFORE INSERT OR UPDATE ON channel_rate_mappings FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER channel_field_authority_version_guard BEFORE INSERT OR UPDATE ON channel_field_authority FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER listing_publication_version_guard BEFORE INSERT OR UPDATE ON listing_publication FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER channel_content_overrides_version_guard BEFORE INSERT OR UPDATE ON channel_content_overrides FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER channel_sync_cursors_version_guard BEFORE INSERT OR UPDATE ON channel_sync_cursors FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER channel_conflicts_version_guard BEFORE INSERT OR UPDATE ON channel_conflicts FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
-- +goose StatementBegin
CREATE FUNCTION business_pool_integrity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE org uuid;pool uuid;p inventory_pools;bad boolean;total integer;
BEGIN
 org=coalesce(NEW.organisation_id,OLD.organisation_id);
 IF TG_TABLE_NAME='inventory_pools' THEN pool=coalesce(NEW.id,OLD.id);ELSE pool=coalesce(NEW.pool_id,OLD.pool_id);END IF;
 SELECT * INTO STRICT p FROM inventory_pools WHERE organisation_id=org AND id=pool;
 SELECT count(*)>0 INTO bad FROM inventory_pool_members m JOIN inventory_nodes n ON n.id=m.node_id AND n.organisation_id=m.organisation_id WHERE m.organisation_id=org AND m.pool_id=pool AND (business_graph_property(org,n.id) IS DISTINCT FROM p.property_id OR (p.kind IN ('UNIT','ROOM') AND (n.kind<>p.kind OR n.id<>p.node_id OR m.capacity<>1)) OR (p.kind='ROOM_TYPE' AND (n.kind<>'ROOM' OR n.room_type_id IS DISTINCT FROM p.node_id OR m.capacity<>1)) OR (p.kind='FACILITY' AND (n.kind<>'FACILITY' OR n.id<>p.node_id OR m.capacity>coalesce((n.attributes->>'capacity')::integer,0))));
 SELECT coalesce(sum(capacity),0) INTO total FROM inventory_pool_members WHERE organisation_id=org AND pool_id=pool;
 IF bad OR p.state<>'RETIRED' AND p.capacity<>total OR EXISTS(SELECT 1 FROM inventory_calendar WHERE organisation_id=org AND pool_id=pool AND capacity>p.capacity) THEN RAISE EXCEPTION 'Pool capacity/physical membership invariant rejected' USING ERRCODE='23514';END IF;
 RETURN NULL;
END; $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER pool_member_integrity AFTER INSERT OR UPDATE OR DELETE ON inventory_pool_members DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_pool_integrity();
CREATE CONSTRAINT TRIGGER pool_capacity_integrity AFTER INSERT OR UPDATE ON inventory_pools DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_pool_integrity();
-- +goose StatementBegin
CREATE FUNCTION business_stock_integrity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE org uuid;pool uuid;
BEGIN
 org=coalesce(NEW.organisation_id,OLD.organisation_id);pool=coalesce(NEW.pool_id,OLD.pool_id);
 IF EXISTS(SELECT 1 FROM inventory_calendar c JOIN inventory_pools p ON p.id=c.pool_id AND p.organisation_id=c.organisation_id WHERE c.organisation_id=org AND c.pool_id=pool AND (c.capacity>p.capacity OR c.blocked+c.committed+
  (SELECT coalesce(sum(d.quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND h.state='ACTIVE' AND h.expires_at>now())+
  (SELECT coalesce(sum(d.quantity),0) FROM inventory_allotment_days d JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>now())-
  (SELECT coalesce(sum(d.allotted_quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND h.state='ACTIVE' AND h.expires_at>now() AND a.state='ACTIVE' AND a.release_at>now())>c.capacity)) THEN RAISE EXCEPTION 'Canonical stock cannot be oversold or manufactured by an allotment' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END; $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER inventory_calendar_stock_integrity AFTER INSERT OR UPDATE OR DELETE ON inventory_calendar DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stock_integrity();
CREATE CONSTRAINT TRIGGER inventory_hold_days_stock_integrity AFTER INSERT OR UPDATE OR DELETE ON inventory_hold_days DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stock_integrity();
CREATE CONSTRAINT TRIGGER inventory_holds_stock_integrity AFTER INSERT OR UPDATE OR DELETE ON inventory_holds DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stock_integrity();
CREATE CONSTRAINT TRIGGER inventory_allotment_days_stock_integrity AFTER INSERT OR UPDATE OR DELETE ON inventory_allotment_days DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stock_integrity();
CREATE CONSTRAINT TRIGGER inventory_allotments_stock_integrity AFTER INSERT OR UPDATE OR DELETE ON inventory_allotments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stock_integrity();
INSERT INTO permissions(code,description) VALUES ('products:read','Commercial products:read'),('products:write','Commercial products:write'),('pools:read','Commercial pools:read'),('pools:write','Commercial pools:write'),('calendar:read','Commercial calendar:read'),('calendar:write','Commercial calendar:write'),('holds:read','Commercial holds:read'),('holds:write','Commercial holds:write'),('allotments:read','Commercial allotments:read'),('allotments:write','Commercial allotments:write'),('rates:read','Commercial rates:read'),('rates:write','Commercial rates:write'),('rates:approve','Commercial rates:approve'),('restrictions:read','Commercial restrictions:read'),('restrictions:write','Commercial restrictions:write'),('promotions:read','Commercial promotions:read'),('promotions:write','Commercial promotions:write'),('pricing:read','Commercial pricing:read'),('pricing:write','Commercial pricing:write'),('pricing:approve','Commercial pricing:approve'),('content:read','Commercial content:read'),('content:write','Commercial content:write'),('publication:write','Commercial publication:write'),('publication:approve','Commercial publication:approve'),('channels:read','Commercial channels:read'),('channels:write','Commercial channels:write'),('channels:credentials','Commercial channels:credentials'),('channels:mappings','Commercial channels:mappings'),('channels:override','Commercial channels:override'),('channels:resolve','Commercial channels:resolve'),('channels:report','Commercial channels:report'),('channels:approve','Commercial channels:approve'),('tax:read','Commercial tax:read'),('tax:write','Commercial tax:write') ON CONFLICT DO NOTHING;
INSERT INTO channel_providers(id,code,name,kind,status,mechanisms,contract_version,rate_limit_per_minute,object_types) VALUES
('019a5fb0-0000-7000-8000-000000000101','COSMICFORGE_MARKETPLACE','CosmicForge Marketplace','FIRST_PARTY','AVAILABLE_UNCONFIGURED',ARRAY['SIGNED_HTTP','NATS'],'business-supply.v1',120,ARRAY['PROPERTY','PRODUCT','RATE','LISTING']),
('019a5fb0-0000-7000-8000-000000000102','BOOKING_COM','Booking.com','OTA','CERTIFICATION_PENDING',ARRAY['OPTIONAL_PARTNER_ADAPTER'],'adapter-contract.v1',30,ARRAY['PROPERTY','PRODUCT','RATE']),
('019a5fb0-0000-7000-8000-000000000103','AIRBNB','Airbnb','OTA','CERTIFICATION_PENDING',ARRAY['OPTIONAL_PARTNER_ADAPTER'],'adapter-contract.v1',30,ARRAY['PROPERTY','PRODUCT','RATE']);
INSERT INTO channel_capabilities(provider_id,capability,status)
SELECT p.id,c,CASE WHEN c IN ('PROPERTY_MAPPING','PRODUCT_MAPPING','RATE_MAPPING','CONTENT','ARI','READBACK','WITHDRAW','PAUSE') THEN CASE WHEN p.kind='FIRST_PARTY' THEN 'AVAILABLE_UNCONFIGURED' ELSE 'CERTIFICATION_PENDING' END ELSE 'UNSUPPORTED' END
FROM channel_providers p CROSS JOIN unnest(ARRAY['PROPERTY_MAPPING','PRODUCT_MAPPING','RATE_MAPPING','CONTENT','ARI','READBACK','WITHDRAW','PAUSE','RESERVATION_INGEST','RESERVATION_CHANGE','MESSAGING','REVIEWS','PROMOTIONS','PAYMENT_METADATA','REPORTING','POLLING','WEBHOOK']) c;
-- +goose StatementBegin
CREATE FUNCTION business_graph_uuidv7_seed() RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE t text;r text;
BEGIN t=lpad(to_hex(floor(extract(epoch FROM clock_timestamp())*1000)::bigint),12,'0');r=replace(gen_random_uuid()::text,'-','');RETURN (substring(t,1,8)||'-'||substring(t,9,4)||'-7'||substring(r,1,3)||'-8'||substring(r,4,3)||'-'||substring(r,7,12))::uuid;END;$$;
-- +goose StatementEnd
-- +goose StatementBegin
DO $$ DECLARE item record;next_role uuid;next_version bigint;permission_code text;
BEGIN
 FOR item IN SELECT m.id member_id,m.organisation_id,m.account_id,r.* FROM staff_memberships m JOIN organisations o ON o.id=m.organisation_id JOIN role_templates r ON r.id=m.role_id AND r.organisation_id=m.organisation_id WHERE m.kind='OWNER' AND m.status='ACTIVE' AND m.scope_kind='COMPANY' AND m.scope_id=m.organisation_id AND o.representative_account_id=m.account_id AND r.active AND r.name='Owner' AND m.effective_at<=now() AND (m.expires_at IS NULL OR m.expires_at>now()) AND EXISTS(SELECT 1 FROM role_permissions rp WHERE rp.role_id=r.id AND rp.permission='roles:write') AND NOT EXISTS(SELECT 1 FROM staff_scope_grants denied WHERE denied.membership_id=m.id AND denied.capability='roles:write' AND denied.deny AND denied.revoked_at IS NULL AND denied.effective_at<=now() AND (denied.expires_at IS NULL OR denied.expires_at>now())) AND EXISTS(SELECT 1 FROM staff_scope_grants g WHERE g.membership_id=m.id AND g.capability='roles:write' AND g.scope_kind='COMPANY' AND g.scope_id=m.organisation_id AND g.inherit AND NOT g.deny AND g.revoked_at IS NULL AND g.expires_at IS NULL AND g.effective_at<=now())
 LOOP
 PERFORM 1 FROM organisations WHERE id=item.organisation_id FOR UPDATE;
 next_role=business_graph_uuidv7_seed();SELECT max(version)+1 INTO next_version FROM role_templates WHERE organisation_id=item.organisation_id AND role_key=item.role_key;
 INSERT INTO role_templates(id,organisation_id,role_key,name,version,active,senior_emergency_eligible) VALUES(next_role,item.organisation_id,item.role_key,item.name,next_version,true,item.senior_emergency_eligible);
 INSERT INTO role_permissions(role_id,permission) SELECT next_role,permission FROM role_permissions WHERE role_id=item.id;
 FOR permission_code IN SELECT unnest(ARRAY['products:read','products:write','pools:read','pools:write','calendar:read','calendar:write','holds:read','holds:write','allotments:read','allotments:write','rates:read','rates:write','rates:approve','restrictions:read','restrictions:write','promotions:read','promotions:write','pricing:read','pricing:write','pricing:approve','content:read','content:write','publication:write','publication:approve','channels:read','channels:write','channels:credentials','channels:mappings','channels:override','channels:resolve','channels:report','channels:approve','tax:read','tax:write']::text[]) LOOP
 INSERT INTO role_permissions(role_id,permission) VALUES(next_role,permission_code) ON CONFLICT DO NOTHING;
 INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES(business_graph_uuidv7_seed(),item.organisation_id,item.member_id,permission_code,'COMPANY',item.organisation_id,true,now());
 END LOOP;
 UPDATE staff_memberships SET role_id=next_role,version=version+1,updated_at=now() WHERE id=item.member_id;
 INSERT INTO audit_events(id,organisation_id,action,target_type,target_id,change_context) VALUES(business_graph_uuidv7_seed(),item.organisation_id,'SECTIONS567_OWNER_CAPABILITY_MIGRATION','staff_membership',item.member_id,jsonb_build_object('from_role',item.id,'to_role',next_role,'version',next_version));
 END LOOP;
END;$$;
-- +goose StatementEnd
DROP FUNCTION business_graph_uuidv7_seed();
ALTER TABLE add_ons ADD COLUMN approval_id uuid;
ALTER TABLE add_ons ADD CONSTRAINT add_on_approval_ref FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id);
-- +goose Down
-- Forward-only: never discard accepted quotes, provider receipts or audit history.
SELECT 1;
