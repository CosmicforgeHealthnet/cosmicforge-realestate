-- +goose Up
-- Section 14 marketing, direct commerce and conversion. Consent stays in the
-- Section 11 consents authority; campaign delivery uses the existing durable
-- guest notification pipeline (Notification Service acceptance only).

-- Campaign messages reuse the guest notification jobs/templates authority.
ALTER TABLE guest_notification_jobs DROP CONSTRAINT guest_notification_jobs_source_kind_check;
ALTER TABLE guest_notification_jobs ADD CONSTRAINT guest_notification_jobs_source_kind_check CHECK(source_kind IN ('WAITLIST_OFFER','RESERVATION_VERSION','RESERVATION','STAY','SERVICE_RECOVERY_ACTION','RECOVERY_COMPENSATION','LOYALTY_ACCOUNT','LOYALTY_TRANSACTION','CAMPAIGN_MEMBER'));
ALTER TABLE guest_notification_jobs DROP CONSTRAINT guest_notification_jobs_kind_check;
ALTER TABLE guest_notification_jobs ADD CONSTRAINT guest_notification_jobs_kind_check CHECK(kind IN ('WAITLIST_OFFER','WAITLIST_EXPIRED','RESERVATION_CHANGE','ARRIVAL_NOTICE','STAY_NOTICE','SERVICE_RECOVERY_UPDATE','COMPENSATION_STATUS','LOYALTY_ENROLLED','LOYALTY_REWARD','CORPORATE_BOOKING_UPDATE','CAMPAIGN_MESSAGE'));
ALTER TABLE guest_notification_jobs ADD CONSTRAINT guest_notification_campaign_marketing CHECK(kind<>'CAMPAIGN_MESSAGE' OR purpose='MARKETING' AND 'MARKETING'=ANY(consent_purposes));
ALTER TABLE guest_notification_templates DROP CONSTRAINT guest_notification_templates_kind_check;
ALTER TABLE guest_notification_templates ADD CONSTRAINT guest_notification_templates_kind_check CHECK(kind IN ('WAITLIST_OFFER','WAITLIST_EXPIRED','RESERVATION_CHANGE','ARRIVAL_NOTICE','STAY_NOTICE','SERVICE_RECOVERY_UPDATE','COMPENSATION_STATUS','LOYALTY_ENROLLED','LOYALTY_REWARD','CORPORATE_BOOKING_UPDATE','CAMPAIGN_MESSAGE'));

CREATE TABLE lead_sources (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,code text NOT NULL CHECK(code~'^[A-Z0-9][A-Z0-9_-]{1,59}$'),name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 channel text NOT NULL CHECK(channel IN ('MARKETPLACE','WEBSITE','OTA','REFERRAL','WALK_IN','PHONE','SOCIAL','PARTNER','EVENT','CF_MEET')),attribution_window_days integer NOT NULL DEFAULT 30 CHECK(attribution_window_days BETWEEN 1 AND 365),
 response_sla_seconds integer NOT NULL DEFAULT 7200 CHECK(response_sla_seconds BETWEEN 300 AND 604800),state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RETIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,property_id,code),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TRIGGER lead_sources_guard BEFORE UPDATE OR DELETE ON lead_sources FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('ACTIVE>RETIRED');

CREATE TABLE segments (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,guest_segment_key uuid NOT NULL,name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 purpose text NOT NULL DEFAULT 'MARKETING' CHECK(purpose IN ('MARKETING')),state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','RETIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TRIGGER segments_guard BEFORE UPDATE OR DELETE ON segments FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('ACTIVE>RETIRED');

CREATE TABLE campaigns (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 segment_id uuid NOT NULL,template_id uuid NOT NULL,channel_category text NOT NULL CHECK(channel_category IN ('EMAIL','SMS','PUSH','MULTI')),purpose text NOT NULL DEFAULT 'MARKETING' CHECK(purpose='MARKETING'),
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','APPROVED','SCHEDULED','RUNNING','PAUSED','COMPLETED','CANCELLED')),scheduled_at timestamptz,
 budget_minor bigint NOT NULL DEFAULT 0 CHECK(budget_minor BETWEEN 0 AND 1000000000000),currency text CHECK(currency IS NULL OR currency~'^[A-Z]{3}$'),
 audience_size integer,approval_id uuid,sponsor_membership_id uuid,sponsor_role_id uuid,launched_at timestamptz,completed_at timestamptz,campaign_version bigint NOT NULL DEFAULT 1 CHECK(campaign_version>0),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(budget_minor=0 OR currency IS NOT NULL),CHECK(state NOT IN ('SCHEDULED','RUNNING','PAUSED','COMPLETED') OR sponsor_membership_id IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id,segment_id) REFERENCES segments(organisation_id,property_id,id),FOREIGN KEY(organisation_id,template_id) REFERENCES guest_notification_templates(organisation_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TRIGGER campaigns_guard BEFORE UPDATE OR DELETE ON campaigns FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>SCHEDULED,DRAFT>RUNNING,DRAFT>CANCELLED,SCHEDULED>RUNNING,SCHEDULED>CANCELLED,RUNNING>PAUSED,RUNNING>COMPLETED,RUNNING>CANCELLED,PAUSED>RUNNING,PAUSED>CANCELLED');
CREATE TABLE campaign_versions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,campaign_id uuid NOT NULL,campaign_version bigint NOT NULL,snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,campaign_id) REFERENCES campaigns(organisation_id,id),UNIQUE(campaign_id,campaign_version)
);
CREATE TRIGGER campaign_versions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON campaign_versions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE campaign_members (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,campaign_id uuid NOT NULL,guest_id uuid NOT NULL,
 eligibility text NOT NULL CHECK(eligibility IN ('ELIGIBLE','SUPPRESSED_NO_CONSENT','SUPPRESSED_LIST','WITHDRAWN')),notification_job_id uuid,consent_version bigint,
 evaluated_at timestamptz NOT NULL DEFAULT now(),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,campaign_id) REFERENCES campaigns(organisation_id,property_id,id),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),
 FOREIGN KEY(notification_job_id) REFERENCES guest_notification_jobs(id),UNIQUE(campaign_id,guest_id),UNIQUE(organisation_id,id)
);

CREATE TABLE leads (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,source_id uuid NOT NULL,campaign_id uuid,listing_id uuid,guest_id uuid,
 channel text NOT NULL,external_reference text NOT NULL CHECK(length(external_reference) BETWEEN 1 AND 200),dedupe_key text NOT NULL CHECK(dedupe_key~'^[a-f0-9]{64}$'),
 contact_reference text NOT NULL DEFAULT '' CHECK(length(contact_reference)<=300),interest text NOT NULL CHECK(interest IN ('STAY','PURCHASE','RENTAL','LEASE','EVENT','OTHER')),
 state text NOT NULL DEFAULT 'NEW' CHECK(state IN ('NEW','ASSIGNED','CONTACTED','QUALIFIED','NURTURING','CONVERTED','LOST','DISQUALIFIED','MERGED')),
 assigned_to uuid REFERENCES business_accounts(id),first_response_due_at timestamptz NOT NULL,first_responded_at timestamptz,follow_up_at timestamptz,follow_up_notified_at timestamptz,sla_breached boolean NOT NULL DEFAULT false,
 converted_reservation_id uuid,merged_into uuid,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid REFERENCES business_accounts(id),
 created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(created_by IS NOT NULL OR created_by_service<>''),CHECK((state='CONVERTED')=(converted_reservation_id IS NOT NULL)),CHECK((state='MERGED')=(merged_into IS NOT NULL)),
 FOREIGN KEY(organisation_id,property_id,source_id) REFERENCES lead_sources(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,campaign_id) REFERENCES campaigns(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,listing_id) REFERENCES listings(organisation_id,id),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),
 FOREIGN KEY(organisation_id,property_id,converted_reservation_id) REFERENCES reservations(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE UNIQUE INDEX leads_dedupe_live ON leads(organisation_id,property_id,dedupe_key) WHERE state<>'MERGED';
CREATE INDEX leads_queue ON leads(organisation_id,property_id,state,first_response_due_at,id);
CREATE TRIGGER leads_guard BEFORE UPDATE OR DELETE ON leads FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('NEW>ASSIGNED,NEW>CONTACTED,NEW>DISQUALIFIED,NEW>MERGED,NEW>CONVERTED,NEW>LOST,ASSIGNED>ASSIGNED,ASSIGNED>CONTACTED,ASSIGNED>QUALIFIED,ASSIGNED>LOST,ASSIGNED>DISQUALIFIED,ASSIGNED>MERGED,ASSIGNED>CONVERTED,CONTACTED>QUALIFIED,CONTACTED>NURTURING,CONTACTED>LOST,CONTACTED>CONVERTED,CONTACTED>MERGED,CONTACTED>ASSIGNED,QUALIFIED>NURTURING,QUALIFIED>CONVERTED,QUALIFIED>LOST,QUALIFIED>ASSIGNED,NURTURING>QUALIFIED,NURTURING>CONVERTED,NURTURING>LOST,NURTURING>ASSIGNED,LOST>ASSIGNED');
CREATE TABLE lead_assignments (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,lead_id uuid NOT NULL,lead_version bigint NOT NULL,previous_assignee uuid,assignee uuid NOT NULL REFERENCES business_accounts(id),
 reason text NOT NULL,actor_id uuid REFERENCES business_accounts(id),actor_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(actor_id IS NOT NULL OR actor_service<>''),FOREIGN KEY(organisation_id,property_id,lead_id) REFERENCES leads(organisation_id,property_id,id),UNIQUE(lead_id,lead_version)
);
CREATE TRIGGER lead_assignments_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON lead_assignments FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE lead_activities (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,lead_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('NOTE','CALL','MESSAGE','MEETING','VIEWING','STATUS_CHANGE','FOLLOW_UP_SCHEDULED','FOLLOW_UP_DUE','CONVERSION','MERGE')),
 summary text NOT NULL CHECK(length(summary) BETWEEN 1 AND 2000),context_reference text NOT NULL DEFAULT '' CHECK(length(context_reference)<=300),occurred_at timestamptz NOT NULL,
 actor_id uuid REFERENCES business_accounts(id),actor_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(actor_id IS NOT NULL OR actor_service<>''),FOREIGN KEY(organisation_id,property_id,lead_id) REFERENCES leads(organisation_id,property_id,id)
);
CREATE INDEX lead_activities_lead ON lead_activities(organisation_id,lead_id,occurred_at,id);
CREATE TRIGGER lead_activities_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON lead_activities FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- Attribution and funnel facts. Impressions/views come only from the owning
-- Marketplace/channel; canonical stages are projected from canonical records.
CREATE TABLE attribution_touchpoints (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,lead_id uuid,guest_id uuid,reservation_id uuid,campaign_id uuid,source_id uuid,
 channel text NOT NULL,touch_kind text NOT NULL CHECK(touch_kind IN ('CAMPAIGN_SEND','LEAD_CAPTURE','LEAD_CONVERSION','MARKETPLACE_REFERRAL')),source_reference text NOT NULL CHECK(length(source_reference) BETWEEN 3 AND 300),
 observed_at timestamptz NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,source_reference)
);
CREATE TRIGGER attribution_touchpoints_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON attribution_touchpoints FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE funnel_observations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,listing_id uuid,
 stage text NOT NULL CHECK(stage IN ('IMPRESSION','LISTING_VIEW','INQUIRY','HOLD','RESERVATION','CHECK_IN','COMPLETED_STAY','REVIEW','REPEAT')),
 source text NOT NULL CHECK(source IN ('MARKETPLACE','CHANNEL','CANONICAL')),source_reference text NOT NULL CHECK(length(source_reference) BETWEEN 3 AND 300),
 channel text NOT NULL DEFAULT '',campaign_id uuid,lead_id uuid,reservation_id uuid,quantity integer NOT NULL DEFAULT 1 CHECK(quantity BETWEEN 1 AND 10000000),observed_at timestamptz NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(stage NOT IN ('IMPRESSION','LISTING_VIEW') OR source<>'CANONICAL'),CHECK(stage IN ('IMPRESSION','LISTING_VIEW','INQUIRY') OR source='CANONICAL'),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,source,source_reference)
);
CREATE INDEX funnel_observations_window ON funnel_observations(organisation_id,property_id,stage,observed_at);
CREATE TRIGGER funnel_observations_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON funnel_observations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

CREATE TABLE brand_profiles (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL UNIQUE REFERENCES organisations(id),display_name text NOT NULL CHECK(length(display_name) BETWEEN 1 AND 120),
 tagline text NOT NULL DEFAULT '' CHECK(length(tagline)<=200),description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),logo_media_id uuid,cover_media_id uuid,
 primary_color text NOT NULL DEFAULT '' CHECK(primary_color='' OR primary_color~'^#[0-9A-Fa-f]{6}$'),state text NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER brand_profiles_guard BEFORE UPDATE OR DELETE ON brand_profiles FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('');

CREATE TABLE storefront_collections (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 listing_ids uuid[] NOT NULL CHECK(cardinality(listing_ids) BETWEEN 1 AND 100),package_ids uuid[] NOT NULL DEFAULT '{}' CHECK(cardinality(package_ids)<=50),promotion_ids uuid[] NOT NULL DEFAULT '{}' CHECK(cardinality(promotion_ids)<=50),
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','PUBLISHED','PAUSED','WITHDRAWN')),publication_version bigint NOT NULL DEFAULT 0 CHECK(publication_version>=0),approval_id uuid,
 marketplace_state text NOT NULL DEFAULT 'NOT_PROJECTED' CHECK(marketplace_state IN ('NOT_PROJECTED','PROJECTION_PUBLISHED','ACKNOWLEDGED','READBACK_UNAVAILABLE','READBACK_MISMATCH')),marketplace_checked_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id)
);
CREATE TRIGGER storefront_collections_guard BEFORE UPDATE OR DELETE ON storefront_collections FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>PUBLISHED,PUBLISHED>PAUSED,PUBLISHED>WITHDRAWN,PAUSED>PUBLISHED,PAUSED>WITHDRAWN,WITHDRAWN>PUBLISHED');
CREATE TABLE storefront_publications (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,collection_id uuid NOT NULL,publication_version bigint NOT NULL,action text NOT NULL CHECK(action IN ('PUBLISH','PAUSE','WITHDRAW')),
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),actor_id uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,collection_id) REFERENCES storefront_collections(organisation_id,id),UNIQUE(collection_id,publication_version)
);
CREATE TRIGGER storefront_publications_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON storefront_publications FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- Upsells reference canonical Section 7 add-ons; the price is frozen from the
-- canonical add-on at acceptance, never independently configured here.
CREATE TABLE upsell_offers (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,add_on_id uuid NOT NULL,
 category text NOT NULL CHECK(category IN ('BREAKFAST','AIRPORT_TRANSFER','SPA','EVENT','EARLY_CHECK_IN','LATE_CHECKOUT','CLEANING','OTHER')),
 min_nights integer NOT NULL DEFAULT 1 CHECK(min_nights BETWEEN 1 AND 365),sources text[] NOT NULL DEFAULT '{}' CHECK(cardinality(sources)<=10),daily_capacity integer CHECK(daily_capacity IS NULL OR daily_capacity BETWEEN 1 AND 10000),
 valid_from date NOT NULL,valid_to date NOT NULL,state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','PAUSED','RETIRED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(valid_to>=valid_from),FOREIGN KEY(organisation_id,property_id,add_on_id) REFERENCES add_ons(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TRIGGER upsell_offers_guard BEFORE UPDATE OR DELETE ON upsell_offers FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('DRAFT>ACTIVE,DRAFT>RETIRED,ACTIVE>PAUSED,ACTIVE>RETIRED,PAUSED>ACTIVE,PAUSED>RETIRED');
CREATE TABLE upsell_acceptances (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,offer_id uuid NOT NULL,reservation_id uuid NOT NULL,service_date date NOT NULL,quantity integer NOT NULL CHECK(quantity BETWEEN 1 AND 100),
 add_on_version bigint NOT NULL,unit_amount_minor bigint NOT NULL CHECK(unit_amount_minor>=0),amount_minor bigint NOT NULL CHECK(amount_minor>=0),currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),
 state text NOT NULL DEFAULT 'ACCEPTED_PENDING_FOLIO' CHECK(state IN ('ACCEPTED_PENDING_FOLIO','FOLIO_POSTED','CANCELLED')),folio_item_id uuid,payment_authority text NOT NULL DEFAULT 'FOLIO_AND_PAYMENT_SERVICE' CHECK(payment_authority='FOLIO_AND_PAYMENT_SERVICE'),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(amount_minor=unit_amount_minor*quantity),CHECK((state='FOLIO_POSTED')=(folio_item_id IS NOT NULL)),
 FOREIGN KEY(organisation_id,property_id,offer_id) REFERENCES upsell_offers(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(folio_item_id)
);
CREATE TRIGGER upsell_acceptances_guard BEFORE UPDATE OR DELETE ON upsell_acceptances FOR EACH ROW EXECUTE FUNCTION business_ops_state_guard('ACCEPTED_PENDING_FOLIO>FOLIO_POSTED,ACCEPTED_PENDING_FOLIO>CANCELLED');

CREATE TABLE conversion_projection_cursors (name text PRIMARY KEY,last_run_at timestamptz,updated_at timestamptz NOT NULL DEFAULT now());

-- +goose Down
SELECT 1;
