-- Section 6 contexts 07 (listings), 08 (pricing) and 09 (availability &
-- freshness) — §13.7–§13.9.
--
-- One listing aggregate per property/unit/transaction type with
-- category-specific details validated by the service; every publication
-- creates an immutable listing version; pricing versions are immutable
-- snapshots (an accepted version can never change); availability follows
-- the §7 state set; freshness checks drive reminders and system pauses.
-- Money is integer minor units + ISO currency.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE listings (
    id                     uuid PRIMARY KEY,
    property_id            uuid        NOT NULL REFERENCES properties (id),
    unit_id                uuid        REFERENCES property_units (id),
    transaction_type       text        NOT NULL CHECK (transaction_type IN ('RENT_LONG_TERM','SALE','SHORT_LET','COMMERCIAL_LEASE','LAND_LEASE')),
    status                 text        NOT NULL CHECK (status IN ('DRAFT','READY_FOR_REVIEW','PUBLISHED','PAUSED','WITHDRAWN','ARCHIVED')),
    title                  text        NOT NULL CHECK (char_length(title) BETWEEN 5 AND 160),
    description            text        CHECK (char_length(description) <= 10000),
    details                jsonb       NOT NULL DEFAULT '{}'::jsonb,
    copy_of_listing_id     uuid        REFERENCES listings (id),
    current_version_no     integer     NOT NULL DEFAULT 0,
    published_version_no   integer,
    first_published_at     timestamptz,
    published_at           timestamptz,
    pause_source           text        CHECK (pause_source IN ('OWNER','SYSTEM','ADMIN')),
    pause_reason           text        CHECK (char_length(pause_reason) <= 200),
    quality_score          integer     CHECK (quality_score BETWEEN 0 AND 100),
    created_by_actor_domain text       NOT NULL,
    created_by_actor_id    uuid        NOT NULL,
    version                bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL
);
-- §6.1: one active published listing per property/unit + transaction type.
CREATE UNIQUE INDEX listings_one_published_uniq ON listings
    (property_id, coalesce(unit_id, '00000000-0000-0000-0000-000000000000'::uuid), transaction_type) WHERE status = 'PUBLISHED';
CREATE INDEX listings_property_idx ON listings (property_id, created_at DESC, id DESC);
CREATE INDEX listings_status_idx ON listings (status, updated_at);

CREATE TABLE listing_versions (
    id             uuid PRIMARY KEY,
    listing_id     uuid        NOT NULL REFERENCES listings (id),
    version_no     integer     NOT NULL CHECK (version_no > 0),
    snapshot       jsonb       NOT NULL,
    pricing_version_id uuid,
    reason         text        NOT NULL CHECK (reason IN ('PUBLISH','EDIT','PRICE_CHANGE','AVAILABILITY','RESUME','MEDIA')),
    actor_domain   text,
    actor_id       uuid,
    created_at     timestamptz NOT NULL,
    CONSTRAINT listing_versions_uniq UNIQUE (listing_id, version_no)
);
CREATE TRIGGER listing_versions_append_only BEFORE UPDATE OR DELETE ON listing_versions
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE listing_media_links (
    listing_id     uuid        NOT NULL REFERENCES listings (id),
    media_id       uuid        NOT NULL,
    position       integer     NOT NULL DEFAULT 0 CHECK (position BETWEEN 0 AND 1000),
    is_cover       boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL,
    PRIMARY KEY (listing_id, media_id)
);
CREATE UNIQUE INDEX listing_media_links_cover_uniq ON listing_media_links (listing_id) WHERE is_cover;
CREATE INDEX listing_media_links_media_idx ON listing_media_links (media_id);

-- Append-only publication log (marketplace and external channels).
CREATE TABLE listing_publication (
    id             uuid PRIMARY KEY,
    listing_id     uuid        NOT NULL REFERENCES listings (id),
    version_no     integer,
    action         text        NOT NULL CHECK (action IN ('PUBLISHED','UPDATED','PAUSED','RESUMED','WITHDRAWN','ARCHIVED','AVAILABILITY_CONFIRMED','REPORTED')),
    channel        text        NOT NULL DEFAULT 'MARKETPLACE' CHECK (channel ~ '^(MARKETPLACE|EXTERNAL:[a-z0-9_-]{1,40})$'),
    source         text        NOT NULL CHECK (source IN ('OWNER','SYSTEM','ADMIN','MANAGER','MARKETPLACE')),
    reason         text        CHECK (char_length(reason) <= 200),
    occurred_at    timestamptz NOT NULL
);
CREATE INDEX listing_publication_listing_idx ON listing_publication (listing_id, occurred_at DESC);
CREATE TRIGGER listing_publication_append_only BEFORE UPDATE OR DELETE ON listing_publication
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- 08 Pricing -------------------------------------------------------------
CREATE TABLE price_schedules (
    listing_id                 uuid PRIMARY KEY REFERENCES listings (id),
    active_pricing_version_id  uuid,
    currency                   char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    version                    bigint      NOT NULL DEFAULT 1,
    updated_at                 timestamptz NOT NULL
);

CREATE TABLE pricing_versions (
    id                  uuid PRIMARY KEY,
    listing_id          uuid        NOT NULL REFERENCES listings (id),
    version_no          integer     NOT NULL CHECK (version_no > 0),
    currency            char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    base_amount_minor   bigint      NOT NULL CHECK (base_amount_minor > 0),
    base_period         text        NOT NULL CHECK (base_period IN ('MONTH','YEAR','NIGHT','TOTAL')),
    payment_cycle       text        NOT NULL CHECK (payment_cycle IN ('MONTHLY','QUARTERLY','BIANNUAL','ANNUAL','PER_STAY','ONE_TIME')),
    deposit_amount_minor bigint     NOT NULL DEFAULT 0 CHECK (deposit_amount_minor >= 0),
    negotiable          boolean     NOT NULL DEFAULT false,
    policy_version      text        NOT NULL,
    created_by_actor_domain text    NOT NULL,
    created_by_actor_id uuid        NOT NULL,
    accepted_at         timestamptz,
    created_at          timestamptz NOT NULL,
    CONSTRAINT pricing_versions_uniq UNIQUE (listing_id, version_no)
);
ALTER TABLE price_schedules ADD CONSTRAINT price_schedules_active_fk FOREIGN KEY (active_pricing_version_id) REFERENCES pricing_versions (id);
-- Pricing versions are immutable; the only permitted change is recording
-- the first acceptance (after which the version is frozen for good).
CREATE FUNCTION pricing_versions_guard() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'pricing versions are immutable';
    END IF;
    IF OLD.accepted_at IS NOT NULL OR NEW.accepted_at IS NULL
       OR (to_jsonb(NEW) - 'accepted_at') <> (to_jsonb(OLD) - 'accepted_at') THEN
        RAISE EXCEPTION 'pricing versions are immutable';
    END IF;
    RETURN NEW;
END;
$fn$;
CREATE TRIGGER pricing_versions_immutable BEFORE UPDATE OR DELETE ON pricing_versions
    FOR EACH ROW EXECUTE FUNCTION pricing_versions_guard();

CREATE TABLE fee_definitions (
    id                  uuid PRIMARY KEY,
    pricing_version_id  uuid        NOT NULL REFERENCES pricing_versions (id),
    code                text        NOT NULL CHECK (code ~ '^[a-z][a-z0-9_]{1,39}$'),
    category            text        NOT NULL CHECK (category ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    label               text        NOT NULL CHECK (char_length(label) BETWEEN 1 AND 120),
    calculation         text        NOT NULL CHECK (calculation IN ('FIXED','PERCENT_OF_BASE')),
    amount_minor        bigint      CHECK (amount_minor >= 0),
    rate_bps            integer     CHECK (rate_bps BETWEEN 0 AND 10000),
    frequency           text        NOT NULL CHECK (frequency IN ('ONE_TIME','PER_CYCLE','PER_NIGHT','PER_STAY')),
    mandatory           boolean     NOT NULL,
    refundable          boolean     NOT NULL,
    payable_to          text        NOT NULL CHECK (payable_to IN ('OWNER','MANAGER','PLATFORM','THIRD_PARTY')),
    CONSTRAINT fee_definitions_uniq UNIQUE (pricing_version_id, code),
    CONSTRAINT fee_definitions_calc_chk CHECK ((calculation = 'FIXED' AND amount_minor IS NOT NULL AND rate_bps IS NULL)
                                             OR (calculation = 'PERCENT_OF_BASE' AND rate_bps IS NOT NULL AND amount_minor IS NULL))
);
CREATE TRIGGER fee_definitions_append_only BEFORE UPDATE OR DELETE ON fee_definitions
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE pricing_rules (
    id                  uuid PRIMARY KEY,
    pricing_version_id  uuid        NOT NULL REFERENCES pricing_versions (id),
    rule_type           text        NOT NULL CHECK (rule_type IN ('ESCALATION','DISCOUNT','SEASONAL','LONG_STAY','WEEKEND')),
    position            integer     NOT NULL,
    params              jsonb       NOT NULL,
    CONSTRAINT pricing_rules_uniq UNIQUE (pricing_version_id, position)
);
CREATE TRIGGER pricing_rules_append_only BEFORE UPDATE OR DELETE ON pricing_rules
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- 09 Availability and freshness -----------------------------------------
-- id = listing id (lifecycle engine convention: id, property_id, status, version).
CREATE TABLE availability_state (
    id               uuid PRIMARY KEY REFERENCES listings (id),
    property_id      uuid        NOT NULL REFERENCES properties (id),
    status           text        NOT NULL CHECK (status IN ('AVAILABLE','RESERVED','APPLICATION_UNDER_REVIEW','OFFER_ACCEPTED','OCCUPIED',
                                     'TEMP_UNAVAILABLE','OFF_MARKET','SOLD','WITHDRAWN')),
    available_from   date,
    available_until  date,
    note             text        CHECK (char_length(note) <= 500),
    last_confirmed_at timestamptz,
    next_confirmation_due timestamptz,
    version          bigint      NOT NULL DEFAULT 1,
    updated_at       timestamptz NOT NULL,
    CONSTRAINT availability_window_chk CHECK (available_until IS NULL OR available_from IS NULL OR available_until >= available_from)
);

CREATE TABLE availability_calendar (
    listing_id   uuid        NOT NULL REFERENCES listings (id),
    day          date        NOT NULL,
    status       text        NOT NULL CHECK (status IN ('AVAILABLE','BLOCKED','HOLD','BOOKED')),
    source       text        NOT NULL CHECK (source IN ('OWNER','BOOKING','EXTERNAL','SYSTEM')),
    ref_id       uuid,
    price_override_minor bigint CHECK (price_override_minor > 0),
    min_nights   integer     CHECK (min_nights BETWEEN 1 AND 365),
    updated_at   timestamptz NOT NULL,
    PRIMARY KEY (listing_id, day)
);
CREATE INDEX availability_calendar_ref_idx ON availability_calendar (ref_id) WHERE ref_id IS NOT NULL;

CREATE TABLE freshness_checks (
    id             uuid PRIMARY KEY,
    listing_id     uuid        NOT NULL REFERENCES listings (id),
    trigger        text        NOT NULL CHECK (trigger IN ('SCHEDULED','REPORT','MANUAL')),
    status         text        NOT NULL CHECK (status IN ('PENDING','REMINDED','CONFIRMED','MISSED','CANCELLED')),
    due_at         timestamptz NOT NULL,
    reminded_at    timestamptz,
    resolved_at    timestamptz,
    report_type    text        CHECK (report_type IN ('STALE','UNAVAILABLE','DUPLICATE','MISLEADING','PRICE_MISMATCH','FRAUD_SUSPECTED')),
    created_at     timestamptz NOT NULL
);
CREATE UNIQUE INDEX freshness_checks_open_uniq ON freshness_checks (listing_id) WHERE status IN ('PENDING','REMINDED');
CREATE INDEX freshness_checks_due_idx ON freshness_checks (due_at) WHERE status IN ('PENDING','REMINDED');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS freshness_checks;
DROP TABLE IF EXISTS availability_calendar;
DROP TABLE IF EXISTS availability_state;
DROP TABLE IF EXISTS pricing_rules;
DROP TABLE IF EXISTS fee_definitions;
ALTER TABLE IF EXISTS price_schedules DROP CONSTRAINT IF EXISTS price_schedules_active_fk;
DROP TABLE IF EXISTS pricing_versions;
DROP FUNCTION IF EXISTS pricing_versions_guard();
DROP TABLE IF EXISTS price_schedules;
DROP TABLE IF EXISTS listing_publication;
DROP TABLE IF EXISTS listing_media_links;
DROP TABLE IF EXISTS listing_versions;
DROP TABLE IF EXISTS listings;
-- +goose StatementEnd
