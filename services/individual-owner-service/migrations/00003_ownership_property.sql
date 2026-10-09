-- Section 6 bounded contexts 02 (profile & contacts), 03 (banking, payouts, tax),
-- 04 (ownership & authority), 05 (properties, units, parcels, assets),
-- 06 (property verification linkage) plus engineering additions:
-- lifecycle_transitions (immutable transition history for every Section 7
-- lifecycle), property_imports (asynchronous bulk import, §13.5),
-- dashboard_preferences (§13.1) and owner_credentials (OIDC credential
-- metadata, §6 account & security; unused until an OAuth adapter exists).
--
-- Rules (§6.1): one ownership root per property; duplicate claims become
-- conflict cases; consents keep history; cross-service references are typed
-- IDs only (no foreign keys outside individual_owner_db); no raw KYC
-- documents, bank account numbers or provider secrets are stored.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$fn$;

CREATE TABLE lifecycle_transitions (
    id                uuid PRIMARY KEY,
    aggregate_type    text        NOT NULL CHECK (aggregate_type ~ '^[a-z][a-z0-9_]*$'),
    aggregate_id      uuid        NOT NULL,
    property_id       uuid,
    from_status       text        NOT NULL,
    to_status         text        NOT NULL,
    command           text        NOT NULL,
    reason            text        CHECK (char_length(reason) <= 500),
    actor_type        text        NOT NULL CHECK (actor_type IN ('USER','SERVICE','SYSTEM')),
    actor_domain      text,
    actor_id          uuid,
    calling_service   text,
    aggregate_version bigint      NOT NULL,
    metadata          jsonb,
    occurred_at       timestamptz NOT NULL
);
CREATE INDEX lifecycle_transitions_aggregate_idx ON lifecycle_transitions (aggregate_type, aggregate_id, occurred_at);
CREATE INDEX lifecycle_transitions_property_idx ON lifecycle_transitions (property_id, occurred_at) WHERE property_id IS NOT NULL;
CREATE TRIGGER lifecycle_transitions_append_only BEFORE UPDATE OR DELETE ON lifecycle_transitions
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE owner_credentials (
    id              uuid PRIMARY KEY,
    account_id      uuid        NOT NULL REFERENCES owner_accounts (account_id),
    provider        text        NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,39}$'),
    provider_subject text       NOT NULL CHECK (char_length(provider_subject) BETWEEN 1 AND 255),
    email_at_link   text,
    linked_at       timestamptz NOT NULL,
    revoked_at      timestamptz,
    CONSTRAINT owner_credentials_subject_uniq UNIQUE (provider, provider_subject)
);

-- 02 Profile and contacts -------------------------------------------------
CREATE TABLE owner_profiles (
    account_id          uuid PRIMARY KEY REFERENCES owner_accounts (account_id),
    preferred_name      text        CHECK (char_length(preferred_name) <= 120),
    bio                 text        CHECK (char_length(bio) <= 2000),
    avatar_media_id     uuid,
    public_profile      boolean     NOT NULL DEFAULT false,
    languages           text[]      NOT NULL DEFAULT '{}',
    timezone            text        NOT NULL DEFAULT 'Africa/Lagos',
    accessibility_needs text[]      NOT NULL DEFAULT '{}',
    preferred_channel   text        NOT NULL DEFAULT 'PUSH' CHECK (preferred_channel IN ('PUSH','EMAIL','SMS','IN_APP')),
    marketing_opt_in    boolean     NOT NULL DEFAULT false,
    version             bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL
);

CREATE TABLE owner_contacts (
    id             uuid PRIMARY KEY,
    account_id     uuid        NOT NULL REFERENCES owner_accounts (account_id),
    contact_type   text        NOT NULL CHECK (contact_type IN ('EMAIL','PHONE','EMERGENCY','TRUSTED')),
    label          text        CHECK (char_length(label) <= 60),
    name           text        CHECK (char_length(name) <= 120),
    relationship   text        CHECK (char_length(relationship) <= 60),
    email          text        CHECK (email IS NULL OR email = lower(email)),
    phone_e164     text        CHECK (phone_e164 IS NULL OR phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
    verified_at    timestamptz,
    removed_at     timestamptz,
    created_at     timestamptz NOT NULL,
    CONSTRAINT owner_contacts_value_chk CHECK (email IS NOT NULL OR phone_e164 IS NOT NULL)
);
CREATE INDEX owner_contacts_account_idx ON owner_contacts (account_id) WHERE removed_at IS NULL;

-- 03 Banking, payouts and tax ---------------------------------------------
CREATE TABLE payout_accounts (
    id                         uuid PRIMARY KEY,
    account_id                 uuid        NOT NULL REFERENCES owner_accounts (account_id),
    status                     text        NOT NULL CHECK (status IN
                                   ('PENDING_VERIFICATION','COOLING_OFF','ACTIVE','REJECTED','REMOVED')),
    bank_code                  text        NOT NULL CHECK (bank_code ~ '^[A-Z0-9]{2,20}$'),
    bank_name                  text        NOT NULL CHECK (char_length(bank_name) <= 120),
    account_name               text        NOT NULL CHECK (char_length(account_name) <= 160),
    account_number_last4       text        NOT NULL CHECK (account_number_last4 ~ '^[0-9]{4}$'),
    account_fingerprint        text        NOT NULL CHECK (char_length(account_fingerprint) = 64),
    currency                   char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    payment_beneficiary_ref    text,
    name_match                 text        CHECK (name_match IN ('MATCH','PARTIAL','MISMATCH','UNKNOWN')),
    verified_at                timestamptz,
    cooling_off_until          timestamptz,
    activated_at               timestamptz,
    removed_at                 timestamptz,
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL
);
CREATE UNIQUE INDEX payout_accounts_fingerprint_uniq ON payout_accounts (account_id, account_fingerprint) WHERE status <> 'REMOVED';
CREATE INDEX payout_accounts_cooling_idx ON payout_accounts (cooling_off_until) WHERE status = 'COOLING_OFF';

CREATE TABLE payout_profiles (
    account_id                  uuid PRIMARY KEY REFERENCES owner_accounts (account_id),
    default_payout_account_id   uuid REFERENCES payout_accounts (id),
    rent_payout_account_id      uuid REFERENCES payout_accounts (id),
    sale_payout_account_id      uuid REFERENCES payout_accounts (id),
    shortlet_payout_account_id  uuid REFERENCES payout_accounts (id),
    version                     bigint      NOT NULL DEFAULT 1,
    updated_at                  timestamptz NOT NULL
);

CREATE TABLE payout_change_requests (
    id                 uuid PRIMARY KEY,
    account_id         uuid        NOT NULL REFERENCES owner_accounts (account_id),
    payout_account_id  uuid        NOT NULL REFERENCES payout_accounts (id),
    change_type        text        NOT NULL CHECK (change_type IN ('ADD','MAKE_DEFAULT','REMOVE')),
    status             text        NOT NULL CHECK (status IN ('PENDING_COOLING_OFF','APPLIED','CANCELLED','REJECTED')),
    step_up_method     text        NOT NULL,
    requested_session  uuid,
    cooling_off_until  timestamptz NOT NULL,
    applied_at         timestamptz,
    created_at         timestamptz NOT NULL
);
CREATE INDEX payout_change_requests_due_idx ON payout_change_requests (cooling_off_until) WHERE status = 'PENDING_COOLING_OFF';

CREATE TABLE tax_profiles (
    account_id             uuid PRIMARY KEY REFERENCES owner_accounts (account_id),
    tax_id_type            text        CHECK (tax_id_type ~ '^[A-Z][A-Z0-9_]{1,31}$'),
    tax_id_last4           text        CHECK (tax_id_last4 ~ '^[A-Za-z0-9]{1,4}$'),
    tax_id_fingerprint     text        CHECK (char_length(tax_id_fingerprint) = 64),
    residency_country      char(2)     CHECK (residency_country ~ '^[A-Z]{2}$'),
    reporting_preferences  text[]      NOT NULL DEFAULT '{}',
    policy_version         text,
    version                bigint      NOT NULL DEFAULT 1,
    updated_at             timestamptz NOT NULL
);

-- 05 Properties -----------------------------------------------------------
CREATE TABLE properties (
    id                   uuid PRIMARY KEY,
    owner_account_id     uuid        NOT NULL REFERENCES owner_accounts (account_id),
    status               text        NOT NULL CHECK (status IN ('DRAFT','ACTIVE','RESTRICTED','TRANSFER_PENDING','ARCHIVED')),
    category             text        NOT NULL CHECK (category IN ('RESIDENTIAL','COMMERCIAL','LAND','MIXED_USE')),
    property_type        text        NOT NULL CHECK (property_type ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    title                text        NOT NULL CHECK (char_length(title) BETWEEN 3 AND 160),
    description          text        CHECK (char_length(description) <= 10000),
    timezone             text        NOT NULL,
    bedrooms             integer     CHECK (bedrooms BETWEEN 0 AND 500),
    bathrooms            integer     CHECK (bathrooms BETWEEN 0 AND 500),
    floor_area_sqm       integer     CHECK (floor_area_sqm BETWEEN 1 AND 100000000),
    year_built           integer     CHECK (year_built BETWEEN 1600 AND 2200),
    transaction_ready    boolean     NOT NULL DEFAULT false,
    restriction_reason   text,
    external_ref         text        CHECK (char_length(external_ref) <= 128),
    import_id            uuid,
    version              bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL
);
CREATE INDEX properties_owner_idx ON properties (owner_account_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX properties_external_ref_uniq ON properties (owner_account_id, external_ref) WHERE external_ref IS NOT NULL;

CREATE TABLE property_addresses (
    property_id      uuid PRIMARY KEY REFERENCES properties (id),
    line1            text        NOT NULL CHECK (char_length(line1) BETWEEN 1 AND 200),
    line2            text        CHECK (char_length(line2) <= 200),
    city             text        NOT NULL CHECK (char_length(city) BETWEEN 1 AND 100),
    state_region     text        NOT NULL CHECK (char_length(state_region) BETWEEN 1 AND 100),
    postal_code      text        CHECK (char_length(postal_code) <= 20),
    country_code     char(2)     NOT NULL CHECK (country_code ~ '^[A-Z]{2}$'),
    latitude         double precision CHECK (latitude BETWEEN -90 AND 90),
    longitude        double precision CHECK (longitude BETWEEN -180 AND 180),
    visibility       text        NOT NULL DEFAULT 'APPROXIMATE' CHECK (visibility IN ('EXACT','APPROXIMATE','HIDDEN')),
    area_label       text        CHECK (char_length(area_label) <= 120),
    fingerprint      text        NOT NULL CHECK (char_length(fingerprint) = 64),
    geocode_source   text        NOT NULL DEFAULT 'OWNER' CHECK (geocode_source IN ('OWNER','PROVIDER')),
    geocoded_at      timestamptz,
    updated_at       timestamptz NOT NULL,
    CONSTRAINT property_addresses_coords_chk CHECK ((latitude IS NULL) = (longitude IS NULL))
);
CREATE INDEX property_addresses_fingerprint_idx ON property_addresses (fingerprint);

CREATE TABLE property_units (
    id               uuid PRIMARY KEY,
    property_id      uuid        NOT NULL REFERENCES properties (id),
    parent_unit_id   uuid        REFERENCES property_units (id),
    unit_type        text        NOT NULL CHECK (unit_type IN ('BUILDING','BLOCK','FLOOR','UNIT','ROOM','SHOP','OFFICE','WAREHOUSE','OTHER')),
    label            text        NOT NULL CHECK (char_length(label) BETWEEN 1 AND 80),
    floor_number     integer,
    bedrooms         integer     CHECK (bedrooms BETWEEN 0 AND 100),
    bathrooms        integer     CHECK (bathrooms BETWEEN 0 AND 100),
    floor_area_sqm   integer     CHECK (floor_area_sqm > 0),
    status           text        NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','ARCHIVED')),
    version          bigint      NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);
CREATE UNIQUE INDEX property_units_label_uniq ON property_units (property_id, coalesce(parent_unit_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(label)) WHERE status = 'ACTIVE';

CREATE TABLE land_parcels (
    id               uuid PRIMARY KEY,
    property_id      uuid        NOT NULL REFERENCES properties (id),
    parcel_ref       text        NOT NULL CHECK (char_length(parcel_ref) BETWEEN 1 AND 120),
    title_type       text        CHECK (title_type ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    area_sqm         integer     CHECK (area_sqm > 0),
    zoning           text        CHECK (char_length(zoning) <= 120),
    access_notes     text        CHECK (char_length(access_notes) <= 2000),
    boundary_geojson jsonb,
    status           text        NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','ARCHIVED')),
    version          bigint      NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);
CREATE UNIQUE INDEX land_parcels_ref_uniq ON land_parcels (property_id, lower(parcel_ref)) WHERE status = 'ACTIVE';

CREATE TABLE property_features (
    property_id   uuid        NOT NULL REFERENCES properties (id),
    feature_key   text        NOT NULL CHECK (feature_key ~ '^[a-z][a-z0-9_]{1,59}$'),
    category      text        NOT NULL CHECK (category IN ('AMENITY','UTILITY','ACCESSIBILITY','FACILITY','CONDITION','SECURITY')),
    value         text        CHECK (char_length(value) <= 200),
    updated_at    timestamptz NOT NULL,
    PRIMARY KEY (property_id, feature_key)
);

CREATE TABLE property_media_links (
    id             uuid PRIMARY KEY,
    property_id    uuid        NOT NULL REFERENCES properties (id),
    media_id       uuid        NOT NULL,
    purpose        text        NOT NULL CHECK (purpose IN ('PHOTO','FLOOR_PLAN','VIDEO','VIRTUAL_TOUR','INSPECTION','DOCUMENT')),
    media_status   text        NOT NULL CHECK (media_status IN ('PENDING','READY','REJECTED','DELETED')),
    position       integer     NOT NULL DEFAULT 0,
    caption        text        CHECK (char_length(caption) <= 200),
    geotag_lat     double precision,
    geotag_lon     double precision,
    captured_at    timestamptz,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    CONSTRAINT property_media_links_uniq UNIQUE (property_id, media_id, purpose)
);
CREATE INDEX property_media_links_media_idx ON property_media_links (media_id);

CREATE TABLE property_assets (
    id                     uuid PRIMARY KEY,
    property_id            uuid        NOT NULL REFERENCES properties (id),
    unit_id                uuid        REFERENCES property_units (id),
    asset_type             text        NOT NULL CHECK (asset_type ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    name                   text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    make                   text        CHECK (char_length(make) <= 80),
    model                  text        CHECK (char_length(model) <= 80),
    serial_number          text        CHECK (char_length(serial_number) <= 80),
    installed_on           date,
    warranty_until         date,
    service_interval_days  integer     CHECK (service_interval_days BETWEEN 1 AND 3650),
    last_serviced_on       date,
    status                 text        NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','RETIRED')),
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL
);
CREATE INDEX property_assets_property_idx ON property_assets (property_id) WHERE status = 'ACTIVE';

CREATE TABLE property_history (
    id            uuid PRIMARY KEY,
    property_id   uuid        NOT NULL REFERENCES properties (id),
    event_type    text        NOT NULL CHECK (event_type ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    summary       text        NOT NULL CHECK (char_length(summary) <= 500),
    ref_type      text,
    ref_id        uuid,
    actor_domain  text,
    actor_id      uuid,
    occurred_at   timestamptz NOT NULL
);
CREATE INDEX property_history_property_idx ON property_history (property_id, occurred_at DESC, id DESC);
CREATE TRIGGER property_history_append_only BEFORE UPDATE OR DELETE ON property_history
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE property_imports (
    id             uuid PRIMARY KEY,
    account_id     uuid        NOT NULL REFERENCES owner_accounts (account_id),
    status         text        NOT NULL CHECK (status IN ('RECEIVED','PROCESSING','COMPLETED','COMPLETED_WITH_ERRORS','FAILED')),
    rows           jsonb       NOT NULL,
    total_rows     integer     NOT NULL CHECK (total_rows BETWEEN 1 AND 500),
    created_count  integer     NOT NULL DEFAULT 0,
    matched_count  integer     NOT NULL DEFAULT 0,
    error_count    integer     NOT NULL DEFAULT 0,
    errors         jsonb,
    lease_owner    text,
    lease_expires_at timestamptz,
    attempts       integer     NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL,
    completed_at   timestamptz
);
CREATE INDEX property_imports_due_idx ON property_imports (created_at) WHERE status IN ('RECEIVED','PROCESSING');

-- 04 Ownership and authority ---------------------------------------------
CREATE TABLE ownership_records (
    id                     uuid PRIMARY KEY,
    property_id            uuid        NOT NULL REFERENCES properties (id),
    claimant_account_id    uuid        NOT NULL REFERENCES owner_accounts (account_id),
    claim_type             text        NOT NULL CHECK (claim_type IN ('SOLE','JOINT','FAMILY','INHERITED','PROBATE','POWER_OF_ATTORNEY','SUBLETTING_AUTHORITY','LEASEHOLD')),
    status                 text        NOT NULL CHECK (status IN ('DRAFT','EVIDENCE_PENDING','UNDER_VERIFICATION','VERIFIED','REJECTED','CONFLICT',
                                           'REVERIFICATION_REQUIRED','TRANSFER_PENDING','TRANSFERRED')),
    is_primary             boolean     NOT NULL DEFAULT false,
    conflict_with_record_id uuid       REFERENCES ownership_records (id),
    verification_id        uuid,
    verified_at            timestamptz,
    expires_at             timestamptz,
    reason_code            text,
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL
);
-- One authoritative ownership root per persistent property record.
CREATE UNIQUE INDEX ownership_records_primary_uniq ON ownership_records (property_id) WHERE is_primary;
CREATE INDEX ownership_records_property_idx ON ownership_records (property_id, created_at);
CREATE INDEX ownership_records_expiry_idx ON ownership_records (expires_at) WHERE status = 'VERIFIED' AND expires_at IS NOT NULL;

CREATE TABLE ownership_parties (
    id                   uuid PRIMARY KEY,
    ownership_record_id  uuid        NOT NULL REFERENCES ownership_records (id),
    property_id          uuid        NOT NULL REFERENCES properties (id),
    party_role           text        NOT NULL CHECK (party_role IN ('OWNER','CO_OWNER','ESTATE_EXECUTOR','AUTHORIZED_REPRESENTATIVE','BENEFICIAL_OWNER')),
    account_id           uuid        REFERENCES owner_accounts (account_id),
    invite_email         text        CHECK (invite_email = lower(invite_email) AND char_length(invite_email) <= 254),
    display_name         text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 160),
    share_bps            integer     CHECK (share_bps BETWEEN 0 AND 10000),
    capabilities         text[]      NOT NULL DEFAULT '{}',
    evidence_media_id    uuid,
    status               text        NOT NULL CHECK (status IN ('INVITED','ACTIVE','REVOKED','EXPIRED')),
    valid_from           timestamptz,
    valid_until          timestamptz,
    revoked_at           timestamptz,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL,
    CONSTRAINT ownership_parties_window_chk CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),
    -- Representatives (attorney / mandate holders) are always time-limited.
    CONSTRAINT ownership_parties_rep_chk CHECK (party_role <> 'AUTHORIZED_REPRESENTATIVE' OR valid_until IS NOT NULL),
    CONSTRAINT ownership_parties_link_chk CHECK (account_id IS NOT NULL OR invite_email IS NOT NULL OR party_role = 'BENEFICIAL_OWNER'),
    -- Owner-only authorities are never delegable to a party.
    CONSTRAINT ownership_parties_caps_chk CHECK (NOT (capabilities && ARRAY['payout.manage','ownership.transfer','sale.manage','manager.assign','property.archive']::text[]))
);
CREATE INDEX ownership_parties_invite_idx ON ownership_parties (invite_email) WHERE status = 'INVITED';
CREATE INDEX ownership_parties_property_idx ON ownership_parties (property_id) WHERE status = 'ACTIVE';
CREATE INDEX ownership_parties_account_idx ON ownership_parties (account_id) WHERE status = 'ACTIVE';
CREATE UNIQUE INDEX ownership_parties_account_uniq ON ownership_parties (ownership_record_id, account_id, party_role) WHERE account_id IS NOT NULL AND status IN ('INVITED','ACTIVE');

CREATE TABLE ownership_consents (
    id                    uuid PRIMARY KEY,
    property_id           uuid        NOT NULL REFERENCES properties (id),
    party_id              uuid        NOT NULL REFERENCES ownership_parties (id),
    action_type           text        NOT NULL CHECK (action_type IN ('LISTING_PUBLISH','SALE','MANAGER_ASSIGNMENT','OWNERSHIP_TRANSFER','PRICE_CHANGE','LEASE')),
    target_type           text        NOT NULL CHECK (target_type ~ '^[a-z][a-z0-9_]*$'),
    target_id             uuid        NOT NULL,
    requested_by_account  uuid        NOT NULL REFERENCES owner_accounts (account_id),
    decision              text        NOT NULL CHECK (decision IN ('PENDING','APPROVED','REJECTED','WITHDRAWN','EXPIRED')),
    decision_note         text        CHECK (char_length(decision_note) <= 1000),
    decided_at            timestamptz,
    expires_at            timestamptz NOT NULL,
    created_at            timestamptz NOT NULL
);
CREATE UNIQUE INDEX ownership_consents_open_uniq ON ownership_consents (party_id, action_type, target_id) WHERE decision = 'PENDING';
CREATE INDEX ownership_consents_target_idx ON ownership_consents (property_id, action_type, target_id);
-- Decided consents are immutable (history is preserved, never rewritten).
CREATE FUNCTION ownership_consents_guard() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    IF TG_OP = 'DELETE' OR OLD.decision <> 'PENDING' THEN
        RAISE EXCEPTION 'decided consents are immutable';
    END IF;
    RETURN NEW;
END;
$fn$;
CREATE TRIGGER ownership_consents_immutable BEFORE UPDATE OR DELETE ON ownership_consents
    FOR EACH ROW EXECUTE FUNCTION ownership_consents_guard();

CREATE TABLE authority_documents (
    id                   uuid PRIMARY KEY,
    ownership_record_id  uuid        NOT NULL REFERENCES ownership_records (id),
    property_id          uuid        NOT NULL REFERENCES properties (id),
    media_id             uuid        NOT NULL,
    document_type        text        NOT NULL CHECK (document_type ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    media_status         text        NOT NULL CHECK (media_status IN ('PENDING','READY','REJECTED','DELETED')),
    verification_evidence_id uuid,
    uploaded_by_account  uuid        NOT NULL REFERENCES owner_accounts (account_id),
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL,
    CONSTRAINT authority_documents_uniq UNIQUE (ownership_record_id, media_id)
);
CREATE INDEX authority_documents_media_idx ON authority_documents (media_id);

CREATE TABLE ownership_change_cases (
    id                    uuid PRIMARY KEY,
    property_id           uuid        NOT NULL REFERENCES properties (id),
    case_type             text        NOT NULL CHECK (case_type IN ('TRANSFER','SUCCESSION','INCAPACITY','CORRECTION')),
    from_record_id        uuid        NOT NULL REFERENCES ownership_records (id),
    to_account_id         uuid        REFERENCES owner_accounts (account_id),
    to_email              text        CHECK (to_email = lower(to_email) AND char_length(to_email) <= 254),
    to_record_id          uuid        REFERENCES ownership_records (id),
    status                text        NOT NULL CHECK (status IN ('OPEN','EVIDENCE_PENDING','UNDER_REVIEW','APPROVED','COMPLETED','REJECTED','CANCELLED')),
    requested_by_account  uuid        NOT NULL REFERENCES owner_accounts (account_id),
    reason                text        CHECK (char_length(reason) <= 2000),
    decision_note         text        CHECK (char_length(decision_note) <= 2000),
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT ownership_change_cases_recipient_chk CHECK (case_type = 'CORRECTION' OR to_account_id IS NOT NULL OR to_email IS NOT NULL)
);
CREATE UNIQUE INDEX ownership_change_cases_open_uniq ON ownership_change_cases (property_id)
    WHERE status IN ('OPEN','EVIDENCE_PENDING','UNDER_REVIEW','APPROVED');

-- 06 Verification linkage -------------------------------------------------
CREATE TABLE verification_links (
    id                   uuid PRIMARY KEY,
    verification_id      uuid        NOT NULL UNIQUE,
    verification_type    text        NOT NULL CHECK (verification_type IN ('IDENTITY','PROPERTY_AUTHORITY')),
    subject_type         text        NOT NULL CHECK (subject_type IN ('USER','PROPERTY')),
    subject_id           uuid        NOT NULL,
    account_id           uuid        NOT NULL REFERENCES owner_accounts (account_id),
    property_id          uuid        REFERENCES properties (id),
    ownership_record_id  uuid        REFERENCES ownership_records (id),
    status               text        NOT NULL CHECK (status IN ('DRAFT','SUBMITTED','PROCESSING','MANUAL_REVIEW','VERIFIED','REJECTED','EXPIRED')),
    reason_code          text,
    decided_at           timestamptz,
    expires_at           timestamptz,
    last_event_at        timestamptz,
    last_synced_at       timestamptz NOT NULL,
    created_at           timestamptz NOT NULL
);
CREATE INDEX verification_links_subject_idx ON verification_links (subject_type, subject_id, created_at DESC);
CREATE INDEX verification_links_expiry_idx ON verification_links (expires_at) WHERE status = 'VERIFIED' AND expires_at IS NOT NULL;
CREATE INDEX verification_links_open_idx ON verification_links (last_synced_at) WHERE status IN ('SUBMITTED','PROCESSING','MANUAL_REVIEW');

-- Authority view: which owner accounts may act on which property, and how.
CREATE VIEW property_access AS
    SELECT p.id AS property_id, p.owner_account_id AS account_id, 'OWNER'::text AS role,
           NULL::timestamptz AS valid_from, NULL::timestamptz AS valid_until, '{}'::text[] AS capabilities
      FROM properties p
    UNION ALL
    SELECT op.property_id, op.account_id, op.party_role, op.valid_from, op.valid_until, op.capabilities
      FROM ownership_parties op
     WHERE op.status = 'ACTIVE' AND op.account_id IS NOT NULL AND op.revoked_at IS NULL;

CREATE TABLE dashboard_preferences (
    account_id           uuid PRIMARY KEY REFERENCES owner_accounts (account_id),
    widgets              text[]      NOT NULL DEFAULT '{}',
    default_property_id  uuid        REFERENCES properties (id),
    updated_at           timestamptz NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS dashboard_preferences;
DROP VIEW IF EXISTS property_access;
DROP TABLE IF EXISTS verification_links;
DROP TABLE IF EXISTS ownership_change_cases;
DROP TABLE IF EXISTS authority_documents;
DROP TABLE IF EXISTS ownership_consents;
DROP FUNCTION IF EXISTS ownership_consents_guard();
DROP TABLE IF EXISTS ownership_parties;
DROP TABLE IF EXISTS ownership_records;
DROP TABLE IF EXISTS property_imports;
DROP TABLE IF EXISTS property_history;
DROP TABLE IF EXISTS property_assets;
DROP TABLE IF EXISTS property_media_links;
DROP TABLE IF EXISTS property_features;
DROP TABLE IF EXISTS land_parcels;
DROP TABLE IF EXISTS property_units;
DROP TABLE IF EXISTS property_addresses;
DROP TABLE IF EXISTS properties;
DROP TABLE IF EXISTS tax_profiles;
DROP TABLE IF EXISTS payout_change_requests;
DROP TABLE IF EXISTS payout_profiles;
DROP TABLE IF EXISTS payout_accounts;
DROP TABLE IF EXISTS owner_contacts;
DROP TABLE IF EXISTS owner_profiles;
DROP TABLE IF EXISTS owner_credentials;
DROP TABLE IF EXISTS lifecycle_transitions;
DROP FUNCTION IF EXISTS forbid_mutation();
-- +goose StatementEnd
