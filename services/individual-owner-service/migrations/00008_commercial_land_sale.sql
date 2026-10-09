-- Section 6 contexts 17 (commercial), 18 (land) and 19 (sale transactions)
-- — §13.17–§13.19.
--
-- Commercial and land terms are append-only versions (the latest is
-- current). LOI rounds and sale offers are immutable once made (a counter is
-- a new row). The sale aggregate is independent of the listing and survives
-- its withdrawal. Data-room access is purpose/time limited and audited; buyer
-- proof of funds and corporate verification stay as external references.
-- Completion requires the configured closing checklist; money milestones are
-- satisfied by payment-service truth (or, off-platform, by evidence plus the
-- buyer's confirmation) and registration/perfection only with evidence.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE commercial_terms (
    id                         uuid PRIMARY KEY,
    property_id                uuid        NOT NULL REFERENCES properties (id),
    unit_id                    uuid        REFERENCES property_units (id),
    version_no                 integer     NOT NULL CHECK (version_no > 0),
    permitted_uses             text[]      NOT NULL,
    zoning_class               text        CHECK (char_length(zoning_class) <= 64),
    terms                      jsonb       NOT NULL,
    actor_domain               text,
    actor_id                   uuid,
    created_at                 timestamptz NOT NULL
);
CREATE UNIQUE INDEX commercial_terms_version_uniq ON commercial_terms (property_id, coalesce(unit_id, '00000000-0000-0000-0000-000000000000'::uuid), version_no);
CREATE TRIGGER commercial_terms_append_only BEFORE UPDATE OR DELETE ON commercial_terms
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE land_terms (
    id                         uuid PRIMARY KEY,
    property_id                uuid        NOT NULL REFERENCES properties (id),
    parcel_id                  uuid        REFERENCES land_parcels (id),
    version_no                 integer     NOT NULL CHECK (version_no > 0),
    permitted_uses             text[]      NOT NULL,
    terms                      jsonb       NOT NULL,
    evidence_media_ids         uuid[]      NOT NULL DEFAULT '{}',
    actor_domain               text,
    actor_id                   uuid,
    created_at                 timestamptz NOT NULL,
    CONSTRAINT land_terms_media_chk CHECK (cardinality(evidence_media_ids) <= 20)
);
CREATE UNIQUE INDEX land_terms_version_uniq ON land_terms (property_id, coalesce(parcel_id, '00000000-0000-0000-0000-000000000000'::uuid), version_no);
CREATE TRIGGER land_terms_append_only BEFORE UPDATE OR DELETE ON land_terms
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE commercial_lois (
    id                         uuid PRIMARY KEY,
    property_id                uuid        NOT NULL REFERENCES properties (id),
    unit_id                    uuid        REFERENCES property_units (id),
    listing_id                 uuid        NOT NULL REFERENCES listings (id),
    applicant_ref_id           uuid        NOT NULL,
    business_name              text        NOT NULL CHECK (char_length(business_name) BETWEEN 1 AND 200),
    business_verification_ref  text        CHECK (char_length(business_verification_ref) <= 128),
    status                     text        NOT NULL CHECK (status IN ('SUBMITTED','COUNTERED','ACCEPTED','DECLINED','WITHDRAWN','EXPIRED','CONVERTED')),
    current_round              integer     NOT NULL DEFAULT 1,
    respond_by                 timestamptz NOT NULL,
    agreement_id               uuid        REFERENCES agreements (id),
    decline_reason             text        CHECK (decline_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL
);
CREATE INDEX commercial_lois_property_idx ON commercial_lois (property_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX commercial_lois_open_uniq ON commercial_lois (listing_id, applicant_ref_id) WHERE status IN ('SUBMITTED','COUNTERED','ACCEPTED');
CREATE INDEX commercial_lois_deadline_idx ON commercial_lois (respond_by) WHERE status IN ('SUBMITTED','COUNTERED');

CREATE TABLE commercial_loi_rounds (
    id                         uuid PRIMARY KEY,
    loi_id                     uuid        NOT NULL REFERENCES commercial_lois (id),
    round_no                   integer     NOT NULL CHECK (round_no > 0),
    proposed_by                text        NOT NULL CHECK (proposed_by IN ('OWNER','APPLICANT')),
    terms                      jsonb       NOT NULL,
    note                       text        CHECK (char_length(note) <= 2000),
    created_at                 timestamptz NOT NULL,
    CONSTRAINT commercial_loi_rounds_uniq UNIQUE (loi_id, round_no)
);
CREATE TRIGGER commercial_loi_rounds_append_only BEFORE UPDATE OR DELETE ON commercial_loi_rounds
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Commercial leases may start with rent-free (fit-out) months.
ALTER TABLE tenancies ADD COLUMN rent_free_months integer NOT NULL DEFAULT 0 CHECK (rent_free_months BETWEEN 0 AND 120);

-- Commercial leases come from accepted LOIs.
ALTER TABLE agreements ADD COLUMN loi_id uuid REFERENCES commercial_lois (id);
ALTER TABLE agreements DROP CONSTRAINT agreements_source_check;
ALTER TABLE agreements ADD CONSTRAINT agreements_source_check CHECK (source IN ('RENTAL_OFFER','DIRECT','RENEWAL','COMMERCIAL_LOI'));
ALTER TABLE agreements ADD CONSTRAINT agreements_loi_chk CHECK ((source = 'COMMERCIAL_LOI') = (loi_id IS NOT NULL));
CREATE UNIQUE INDEX agreements_loi_uniq ON agreements (loi_id) WHERE loi_id IS NOT NULL AND status <> 'CANCELLED';

-- 19 Sale transactions -----------------------------------------------------
CREATE TABLE sale_transactions (
    id                         uuid PRIMARY KEY,
    property_id                uuid        NOT NULL REFERENCES properties (id),
    unit_id                    uuid        REFERENCES property_units (id),
    listing_id                 uuid        REFERENCES listings (id),
    sale_kind                  text        NOT NULL CHECK (sale_kind IN ('PROPERTY','COMMERCIAL','LAND')),
    status                     text        NOT NULL CHECK (status IN ('INQUIRY','OFFERING','OFFER_ACCEPTED','DUE_DILIGENCE','CONTRACTING',
                                   'CONDITIONS_PENDING','CLOSING','COMPLETED','CANCELLED','FAILED','DISPUTED')),
    currency                   char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    asking_price_minor         bigint      CHECK (asking_price_minor > 0),
    accepted_offer_id          uuid,
    buyer_ref_id               uuid,
    agreed_price_minor         bigint      CHECK (agreed_price_minor > 0),
    target_closing_date        date,
    status_before_dispute      text,
    end_reason                 text        CHECK (end_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    completed_at               timestamptz,
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT sale_transactions_accepted_chk CHECK (status IN ('INQUIRY','OFFERING','CANCELLED','FAILED')
        OR status = 'DISPUTED' OR (accepted_offer_id IS NOT NULL AND buyer_ref_id IS NOT NULL AND agreed_price_minor IS NOT NULL))
);
CREATE INDEX sale_transactions_property_idx ON sale_transactions (property_id, created_at DESC, id DESC);
-- One live sale per property/unit (the aggregate outlives listing changes).
CREATE UNIQUE INDEX sale_transactions_live_uniq ON sale_transactions (property_id, coalesce(unit_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE status NOT IN ('COMPLETED','CANCELLED','FAILED');

CREATE TABLE sale_offers (
    id                         uuid PRIMARY KEY,
    sale_id                    uuid        NOT NULL REFERENCES sale_transactions (id),
    buyer_ref_id               uuid        NOT NULL,
    buyer_display_name         text        NOT NULL CHECK (char_length(buyer_display_name) BETWEEN 1 AND 120),
    parent_offer_id            uuid        REFERENCES sale_offers (id),
    made_by                    text        NOT NULL CHECK (made_by IN ('BUYER','OWNER')),
    amount_minor               bigint      NOT NULL CHECK (amount_minor > 0),
    currency                   char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    financing                  text        NOT NULL CHECK (financing IN ('CASH','MORTGAGE','INSTALLMENT','OTHER')),
    conditions                 text[]      NOT NULL DEFAULT '{}',
    proof_of_funds_ref         text        CHECK (char_length(proof_of_funds_ref) <= 128),
    status                     text        NOT NULL CHECK (status IN ('SUBMITTED','COUNTERED','ACCEPTED','DECLINED','WITHDRAWN','EXPIRED','SUPERSEDED')),
    expires_at                 timestamptz NOT NULL,
    decided_at                 timestamptz,
    decline_reason             text        CHECK (decline_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL
);
CREATE INDEX sale_offers_sale_idx ON sale_offers (sale_id, created_at);
CREATE UNIQUE INDEX sale_offers_accepted_uniq ON sale_offers (sale_id) WHERE status = 'ACCEPTED';
CREATE INDEX sale_offers_expiry_idx ON sale_offers (expires_at) WHERE status = 'SUBMITTED';
ALTER TABLE sale_transactions ADD CONSTRAINT sale_transactions_offer_fk FOREIGN KEY (accepted_offer_id) REFERENCES sale_offers (id);

CREATE TABLE data_rooms (
    id                         uuid PRIMARY KEY,
    sale_id                    uuid        NOT NULL UNIQUE REFERENCES sale_transactions (id),
    property_id                uuid        NOT NULL REFERENCES properties (id),
    status                     text        NOT NULL CHECK (status IN ('OPEN','CLOSED')),
    closed_at                  timestamptz,
    created_at                 timestamptz NOT NULL
);

CREATE TABLE data_room_documents (
    id                         uuid PRIMARY KEY,
    data_room_id               uuid        NOT NULL REFERENCES data_rooms (id),
    media_id                   uuid        NOT NULL,
    category                   text        NOT NULL CHECK (category IN ('TITLE','SURVEY','PLANNING','VALUATION','INSPECTION','TAX','LEGAL','FINANCIAL','OTHER')),
    title                      text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    removed_at                 timestamptz,
    created_at                 timestamptz NOT NULL,
    CONSTRAINT data_room_documents_uniq UNIQUE (data_room_id, media_id)
);

CREATE TABLE data_room_grants (
    id                         uuid PRIMARY KEY,
    data_room_id               uuid        NOT NULL REFERENCES data_rooms (id),
    grantee_domain             text        NOT NULL CHECK (grantee_domain IN ('marketplace','professional-services')),
    grantee_ref_id             uuid        NOT NULL,
    grantee_label              text        NOT NULL CHECK (char_length(grantee_label) BETWEEN 1 AND 120),
    purpose                    text        NOT NULL CHECK (purpose ~ '^[a-z][a-z0-9_.:-]{2,99}$'),
    categories                 text[]      NOT NULL DEFAULT '{}',
    expires_at                 timestamptz NOT NULL,
    revoked_at                 timestamptz,
    access_count               integer     NOT NULL DEFAULT 0,
    last_accessed_at           timestamptz,
    created_at                 timestamptz NOT NULL
);
CREATE INDEX data_room_grants_grantee_idx ON data_room_grants (grantee_domain, grantee_ref_id) WHERE revoked_at IS NULL;

CREATE TABLE data_room_access_log (
    id                         uuid PRIMARY KEY,
    grant_id                   uuid        NOT NULL REFERENCES data_room_grants (id),
    document_id                uuid        NOT NULL REFERENCES data_room_documents (id),
    accessed_at                timestamptz NOT NULL
);
CREATE INDEX data_room_access_log_grant_idx ON data_room_access_log (grant_id, accessed_at);
CREATE TRIGGER data_room_access_log_append_only BEFORE UPDATE OR DELETE ON data_room_access_log
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE due_diligence_items (
    id                         uuid PRIMARY KEY,
    sale_id                    uuid        NOT NULL REFERENCES sale_transactions (id),
    category                   text        NOT NULL CHECK (category IN ('LEGAL','SURVEY','VALUATION','INSPECTION','TITLE_SEARCH','CHARTING','ENCUMBRANCE','PLANNING','OTHER')),
    title                      text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    raised_by                  text        NOT NULL CHECK (raised_by IN ('OWNER','BUYER','PROFESSIONAL')),
    status                     text        NOT NULL CHECK (status IN ('OPEN','IN_PROGRESS','ISSUE_FOUND','RESOLVED','WAIVED')),
    blocking                   boolean     NOT NULL DEFAULT false,
    finding                    text        CHECK (char_length(finding) <= 4000),
    evidence_media_ids         uuid[]      NOT NULL DEFAULT '{}',
    professional_appointment_ref uuid,
    resolution_note            text        CHECK (char_length(resolution_note) <= 2000),
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT due_diligence_media_chk CHECK (cardinality(evidence_media_ids) <= 20)
);
CREATE INDEX due_diligence_items_sale_idx ON due_diligence_items (sale_id, created_at);

CREATE TABLE closing_milestones (
    id                         uuid PRIMARY KEY,
    sale_id                    uuid        NOT NULL REFERENCES sale_transactions (id),
    code                       text        NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    title                      text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    kind                       text        NOT NULL CHECK (kind IN ('PAYMENT','DOCUMENT','CONSENT','HANDOVER','PERFECTION','OTHER')),
    required_for_completion    boolean     NOT NULL,
    sequence                   integer     NOT NULL,
    settlement                 text        CHECK (settlement IN ('PLATFORM','OFF_PLATFORM')),
    amount_minor               bigint      CHECK (amount_minor > 0),
    payment_intent_ref         text        CHECK (char_length(payment_intent_ref) <= 200),
    paid_minor                 bigint      NOT NULL DEFAULT 0,
    last_payment_status        text,
    status                     text        NOT NULL CHECK (status IN ('PENDING','AWAITING_CONFIRMATION','SATISFIED','WAIVED','FAILED')),
    evidence_media_ids         uuid[]      NOT NULL DEFAULT '{}',
    attested_by                text        CHECK (attested_by IN ('PAYMENT_SERVICE','OWNER','OWNER_AND_BUYER','PROFESSIONAL','PROVIDER')),
    buyer_confirmed_at         timestamptz,
    satisfied_at               timestamptz,
    waived_reason              text        CHECK (waived_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    due_date                   date,
    last_checked_at            timestamptz,
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT closing_milestones_uniq UNIQUE (sale_id, code),
    CONSTRAINT closing_milestones_payment_chk CHECK ((kind = 'PAYMENT') = (settlement IS NOT NULL AND amount_minor IS NOT NULL)),
    CONSTRAINT closing_milestones_evidence_chk CHECK (status <> 'SATISFIED' OR kind = 'PAYMENT' AND settlement = 'PLATFORM' OR cardinality(evidence_media_ids) > 0),
    CONSTRAINT closing_milestones_waive_chk CHECK (status <> 'WAIVED' OR (kind <> 'PAYMENT' AND waived_reason IS NOT NULL)),
    CONSTRAINT closing_milestones_media_chk CHECK (cardinality(evidence_media_ids) <= 20)
);
CREATE UNIQUE INDEX closing_milestones_intent_uniq ON closing_milestones (payment_intent_ref) WHERE payment_intent_ref IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS closing_milestones;
DROP TABLE IF EXISTS due_diligence_items;
DROP TABLE IF EXISTS data_room_access_log;
DROP TABLE IF EXISTS data_room_grants;
DROP TABLE IF EXISTS data_room_documents;
DROP TABLE IF EXISTS data_rooms;
ALTER TABLE sale_transactions DROP CONSTRAINT IF EXISTS sale_transactions_offer_fk;
DROP TABLE IF EXISTS sale_offers;
DROP TABLE IF EXISTS sale_transactions;
DROP INDEX IF EXISTS agreements_loi_uniq;
ALTER TABLE agreements DROP CONSTRAINT IF EXISTS agreements_loi_chk;
ALTER TABLE agreements DROP CONSTRAINT IF EXISTS agreements_source_check;
ALTER TABLE agreements ADD CONSTRAINT agreements_source_check CHECK (source IN ('RENTAL_OFFER','DIRECT','RENEWAL'));
ALTER TABLE agreements DROP COLUMN IF EXISTS loi_id;
ALTER TABLE tenancies DROP COLUMN IF EXISTS rent_free_months;
DROP TABLE IF EXISTS commercial_loi_rounds;
DROP TABLE IF EXISTS commercial_lois;
DROP TABLE IF EXISTS land_terms;
DROP TABLE IF EXISTS commercial_terms;
-- +goose StatementEnd