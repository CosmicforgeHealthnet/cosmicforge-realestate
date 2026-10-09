-- Section 6 context 20: personally operated short-let (§13.20, §14.4).
--
-- Date inventory lives in availability_calendar (00005). Bookings cannot
-- overlap: the calendar rows are claimed with conditional upserts inside the
-- booking transaction, and an exclusion constraint on the stay range is the
-- database-level guarantee. A booking's price snapshot is immutable;
-- payments, refunds and damage settlements are payment-service truth.

-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE shortlet_settings (
    listing_id                 uuid PRIMARY KEY REFERENCES listings (id),
    property_id                uuid        NOT NULL REFERENCES properties (id),
    max_guests                 integer     NOT NULL CHECK (max_guests BETWEEN 1 AND 100),
    min_nights                 integer     NOT NULL CHECK (min_nights BETWEEN 1 AND 365),
    max_nights                 integer     NOT NULL CHECK (max_nights BETWEEN 1 AND 365),
    check_in_time              text        NOT NULL CHECK (check_in_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    check_out_time             text        NOT NULL CHECK (check_out_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    instant_book               boolean     NOT NULL DEFAULT false,
    guest_verification_required boolean    NOT NULL DEFAULT true,
    payment_collection         text        NOT NULL CHECK (payment_collection IN ('PLATFORM','OFF_PLATFORM')),
    cancellation_policy        text        NOT NULL CHECK (cancellation_policy IN ('FLEXIBLE','MODERATE','STRICT','NON_REFUNDABLE')),
    house_rules                text[]      NOT NULL DEFAULT '{}',
    add_ons                    jsonb       NOT NULL DEFAULT '[]',
    turnover_buffer_days       integer     NOT NULL DEFAULT 0 CHECK (turnover_buffer_days BETWEEN 0 AND 3),
    damage_claim_window_hours  integer     NOT NULL DEFAULT 48 CHECK (damage_claim_window_hours BETWEEN 0 AND 720),
    external_calendar_ref      text        CHECK (char_length(external_calendar_ref) <= 500),
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT shortlet_settings_nights_chk CHECK (max_nights >= min_nights)
);

CREATE TABLE shortlet_rates (
    id                         uuid PRIMARY KEY,
    listing_id                 uuid        NOT NULL REFERENCES listings (id),
    start_date                 date        NOT NULL,
    end_date                   date        NOT NULL,
    nightly_rate_minor         bigint      NOT NULL CHECK (nightly_rate_minor > 0),
    min_nights                 integer     CHECK (min_nights BETWEEN 1 AND 365),
    label                      text        CHECK (char_length(label) <= 80),
    superseded_at              timestamptz,
    created_at                 timestamptz NOT NULL,
    CONSTRAINT shortlet_rates_range_chk CHECK (end_date >= start_date)
);
CREATE INDEX shortlet_rates_listing_idx ON shortlet_rates (listing_id, start_date) WHERE superseded_at IS NULL;

CREATE TABLE shortlet_bookings (
    id                         uuid PRIMARY KEY,
    listing_id                 uuid        NOT NULL REFERENCES listings (id),
    property_id                uuid        NOT NULL REFERENCES properties (id),
    customer_ref_id            uuid        NOT NULL,
    status                     text        NOT NULL CHECK (status IN ('INQUIRY','HOLD','PENDING_APPROVAL','PENDING_PAYMENT','CONFIRMED','CHECKED_IN',
                                   'CHECKED_OUT','COMPLETED','CANCELLED','NO_SHOW','DISPUTED')),
    check_in                   date        NOT NULL,
    check_out                  date        NOT NULL,
    guests                     integer     NOT NULL CHECK (guests BETWEEN 1 AND 100),
    pricing_version_id         uuid        NOT NULL REFERENCES pricing_versions (id),
    cost_snapshot              jsonb       NOT NULL,
    total_minor                bigint      NOT NULL CHECK (total_minor >= 0),
    currency                   char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    payment_collection         text        NOT NULL CHECK (payment_collection IN ('PLATFORM','OFF_PLATFORM')),
    cancellation_policy        text        NOT NULL,
    hold_expires_at            timestamptz,
    payment_intent_ref         text        CHECK (char_length(payment_intent_ref) <= 200),
    paid_minor                 bigint      NOT NULL DEFAULT 0,
    last_payment_status        text,
    cancelled_by               text        CHECK (cancelled_by IN ('GUEST','OWNER','SYSTEM')),
    cancel_reason              text        CHECK (cancel_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    refund_due_minor           bigint      NOT NULL DEFAULT 0 CHECK (refund_due_minor >= 0),
    refund_request_ref         text        CHECK (char_length(refund_request_ref) <= 200),
    refund_status              text,
    refunded_minor             bigint      NOT NULL DEFAULT 0,
    access_grant_ref           uuid,
    conversation_ref           text        CHECK (char_length(conversation_ref) <= 128),
    external_ref               text        CHECK (char_length(external_ref) <= 128),
    checked_in_at              timestamptz,
    checked_out_at             timestamptz,
    last_checked_at            timestamptz,
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT shortlet_bookings_stay_chk CHECK (check_out > check_in),
    CONSTRAINT shortlet_bookings_no_overlap EXCLUDE USING gist (listing_id WITH =, daterange(check_in, check_out) WITH &&)
        WHERE (status IN ('HOLD','PENDING_APPROVAL','PENDING_PAYMENT','CONFIRMED','CHECKED_IN'))
);
CREATE INDEX shortlet_bookings_listing_idx ON shortlet_bookings (listing_id, check_in);
CREATE INDEX shortlet_bookings_customer_idx ON shortlet_bookings (customer_ref_id, check_in DESC);
CREATE INDEX shortlet_bookings_hold_idx ON shortlet_bookings (hold_expires_at) WHERE status IN ('HOLD','PENDING_APPROVAL','PENDING_PAYMENT');
CREATE UNIQUE INDEX shortlet_bookings_intent_uniq ON shortlet_bookings (payment_intent_ref) WHERE payment_intent_ref IS NOT NULL;
CREATE UNIQUE INDEX shortlet_bookings_external_uniq ON shortlet_bookings (listing_id, external_ref) WHERE external_ref IS NOT NULL;

CREATE TABLE shortlet_guests (
    id                         uuid PRIMARY KEY,
    booking_id                 uuid        NOT NULL REFERENCES shortlet_bookings (id),
    is_primary                 boolean     NOT NULL DEFAULT false,
    customer_ref_id            uuid,
    display_name               text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 120),
    verification_ref           text        CHECK (char_length(verification_ref) <= 128),
    verification_status        text        NOT NULL DEFAULT 'NOT_PROVIDED' CHECK (verification_status IN ('NOT_PROVIDED','PENDING','VERIFIED','REJECTED','EXPIRED')),
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL
);
CREATE INDEX shortlet_guests_booking_idx ON shortlet_guests (booking_id);
CREATE UNIQUE INDEX shortlet_guests_primary_uniq ON shortlet_guests (booking_id) WHERE is_primary;

CREATE TABLE turnover_jobs (
    id                         uuid PRIMARY KEY,
    listing_id                 uuid        NOT NULL REFERENCES listings (id),
    property_id                uuid        NOT NULL REFERENCES properties (id),
    booking_id                 uuid        REFERENCES shortlet_bookings (id),
    job_type                   text        NOT NULL CHECK (job_type IN ('CLEANING','INSPECTION','RESTOCK','MAINTENANCE_CHECK')),
    status                     text        NOT NULL CHECK (status IN ('SCHEDULED','IN_PROGRESS','COMPLETED','ISSUE_REPORTED','CANCELLED')),
    scheduled_for              timestamptz NOT NULL,
    assignee_type              text        NOT NULL CHECK (assignee_type IN ('OWNER','MANAGER','ARTISAN')),
    assignee_ref_id            uuid,
    checklist                  jsonb       NOT NULL DEFAULT '[]',
    notes                      text        CHECK (char_length(notes) <= 2000),
    evidence_media_ids         uuid[]      NOT NULL DEFAULT '{}',
    completed_at               timestamptz,
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT turnover_jobs_media_chk CHECK (cardinality(evidence_media_ids) <= 20)
);
CREATE INDEX turnover_jobs_listing_idx ON turnover_jobs (listing_id, scheduled_for);
CREATE UNIQUE INDEX turnover_jobs_booking_uniq ON turnover_jobs (booking_id, job_type) WHERE booking_id IS NOT NULL AND status <> 'CANCELLED';

CREATE TABLE damage_claims (
    id                         uuid PRIMARY KEY,
    booking_id                 uuid        NOT NULL REFERENCES shortlet_bookings (id),
    property_id                uuid        NOT NULL REFERENCES properties (id),
    status                     text        NOT NULL CHECK (status IN ('OPEN','GUEST_ACCEPTED','DISPUTED','RESOLVED','WITHDRAWN','SETTLED')),
    items                      jsonb       NOT NULL,
    claimed_minor              bigint      NOT NULL CHECK (claimed_minor > 0),
    currency                   char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    respond_by                 timestamptz NOT NULL,
    guest_response_note        text        CHECK (char_length(guest_response_note) <= 2000),
    guest_evidence_media_ids   uuid[]      NOT NULL DEFAULT '{}',
    awarded_minor              bigint      CHECK (awarded_minor >= 0),
    settlement                 text        CHECK (settlement IN ('PLATFORM','OFF_PLATFORM')),
    payment_intent_ref         text        CHECK (char_length(payment_intent_ref) <= 200),
    paid_minor                 bigint      NOT NULL DEFAULT 0,
    last_payment_status        text,
    settled_at                 timestamptz,
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT damage_claims_award_chk CHECK (awarded_minor IS NULL OR awarded_minor <= claimed_minor)
);
CREATE UNIQUE INDEX damage_claims_open_uniq ON damage_claims (booking_id) WHERE status IN ('OPEN','GUEST_ACCEPTED','DISPUTED','RESOLVED');
CREATE UNIQUE INDEX damage_claims_intent_uniq ON damage_claims (payment_intent_ref) WHERE payment_intent_ref IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS damage_claims;
DROP TABLE IF EXISTS turnover_jobs;
DROP TABLE IF EXISTS shortlet_guests;
DROP TABLE IF EXISTS shortlet_bookings;
DROP TABLE IF EXISTS shortlet_rates;
DROP TABLE IF EXISTS shortlet_settings;
-- +goose StatementEnd