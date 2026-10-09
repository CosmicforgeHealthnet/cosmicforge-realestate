-- Section 6 contexts 10 (leads), 11 (viewings) and 12 (applications &
-- tenant selection) — §13.10–§13.12.
--
-- Customers belong to the marketplace domain: only typed references
-- (customer_ref_id) and the minimum display data are stored. Human message
-- content lives in CF Meet (only a conversation reference is kept). Evidence
-- is referenced by media-service ID through purpose- and time-limited grants.
-- Qualification and screening use configured, lawful criteria only.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE inquiries (
    id                  uuid PRIMARY KEY,
    property_id         uuid        NOT NULL REFERENCES properties (id),
    listing_id          uuid        NOT NULL REFERENCES listings (id),
    customer_ref_id     uuid        NOT NULL,
    customer_display_name text      NOT NULL CHECK (char_length(customer_display_name) BETWEEN 1 AND 120),
    channel             text        NOT NULL CHECK (channel IN ('MARKETPLACE','CF_MEET','PHONE','WALK_IN','REFERRAL','EXTERNAL')),
    status              text        NOT NULL CHECK (status IN ('NEW','ACKNOWLEDGED','QUALIFIED','VIEWING','APPLICATION','OFFER','BOOKING','CLOSED_WON','CLOSED_LOST')),
    priority            text        NOT NULL DEFAULT 'NORMAL' CHECK (priority IN ('LOW','NORMAL','HIGH')),
    conversation_ref    text        CHECK (char_length(conversation_ref) <= 128),
    intended_move_in    date,
    occupants           integer     CHECK (occupants BETWEEN 1 AND 100),
    term_months         integer     CHECK (term_months BETWEEN 1 AND 600),
    budget_within_range boolean,
    has_pets            boolean,
    follow_up_at        timestamptz,
    close_reason        text        CHECK (close_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    external_ref        text        CHECK (char_length(external_ref) <= 128),
    acknowledged_at     timestamptz,
    version             bigint      NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL
);
CREATE INDEX inquiries_property_idx ON inquiries (property_id, created_at DESC, id DESC);
CREATE INDEX inquiries_listing_idx ON inquiries (listing_id, status);
CREATE UNIQUE INDEX inquiries_external_uniq ON inquiries (listing_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX inquiries_follow_up_idx ON inquiries (follow_up_at) WHERE follow_up_at IS NOT NULL AND status NOT IN ('CLOSED_WON','CLOSED_LOST');

CREATE TABLE lead_activities (
    id            uuid PRIMARY KEY,
    inquiry_id    uuid        NOT NULL REFERENCES inquiries (id),
    property_id   uuid        NOT NULL REFERENCES properties (id),
    activity_type text        NOT NULL CHECK (activity_type IN ('NOTE','CALL_LOGGED','STATUS_CHANGE','REMINDER','QUALIFICATION','LINKED')),
    note          text        CHECK (char_length(note) <= 2000),
    ref_type      text,
    ref_id        uuid,
    actor_domain  text,
    actor_id      uuid,
    occurred_at   timestamptz NOT NULL
);
CREATE INDEX lead_activities_inquiry_idx ON lead_activities (inquiry_id, occurred_at DESC, id DESC);
CREATE TRIGGER lead_activities_append_only BEFORE UPDATE OR DELETE ON lead_activities
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- 11 Viewings ---------------------------------------------------------------
CREATE TABLE viewing_slots (
    id            uuid PRIMARY KEY,
    property_id   uuid        NOT NULL REFERENCES properties (id),
    listing_id    uuid        NOT NULL REFERENCES listings (id),
    starts_at     timestamptz NOT NULL,
    ends_at       timestamptz NOT NULL,
    mode          text        NOT NULL CHECK (mode IN ('PHYSICAL','VIRTUAL_LIVE','OPEN_HOUSE')),
    capacity      integer     NOT NULL CHECK (capacity BETWEEN 1 AND 200),
    booked        integer     NOT NULL DEFAULT 0 CHECK (booked >= 0),
    host_type     text        NOT NULL CHECK (host_type IN ('OWNER','REPRESENTATIVE','MANAGER')),
    host_ref_id   uuid,
    status        text        NOT NULL CHECK (status IN ('OPEN','CANCELLED')),
    notes         text        CHECK (char_length(notes) <= 1000),
    version       bigint      NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,
    CONSTRAINT viewing_slots_window_chk CHECK (ends_at > starts_at),
    CONSTRAINT viewing_slots_capacity_chk CHECK (booked <= capacity)
);
CREATE INDEX viewing_slots_listing_idx ON viewing_slots (listing_id, starts_at);

CREATE TABLE viewing_requests (
    id                     uuid PRIMARY KEY,
    property_id            uuid        NOT NULL REFERENCES properties (id),
    listing_id             uuid        NOT NULL REFERENCES listings (id),
    slot_id                uuid        REFERENCES viewing_slots (id),
    inquiry_id             uuid        REFERENCES inquiries (id),
    customer_ref_id        uuid        NOT NULL,
    customer_display_name  text        NOT NULL CHECK (char_length(customer_display_name) BETWEEN 1 AND 120),
    mode                   text        NOT NULL CHECK (mode IN ('PHYSICAL','VIRTUAL_LIVE','OPEN_HOUSE')),
    status                 text        NOT NULL CHECK (status IN ('REQUESTED','CONFIRMED','CHECKED_IN','COMPLETED','RESCHEDULED','CANCELLED','NO_SHOW')),
    starts_at              timestamptz NOT NULL,
    ends_at                timestamptz NOT NULL,
    proposed_starts_at     timestamptz,
    proposed_ends_at       timestamptz,
    host_type              text        NOT NULL DEFAULT 'OWNER' CHECK (host_type IN ('OWNER','REPRESENTATIVE','MANAGER')),
    host_ref_id            uuid,
    occupied               boolean     NOT NULL DEFAULT false,
    occupant_consent_at    timestamptz,
    occupant_consent_ref   text        CHECK (char_length(occupant_consent_ref) <= 128),
    access_code_sealed     bytea,
    access_code_key_id     text,
    access_code_expires_at timestamptz,
    conversation_ref       text        CHECK (char_length(conversation_ref) <= 128),
    owner_notes            text        CHECK (char_length(owner_notes) <= 2000),
    cancel_reason          text        CHECK (cancel_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    cancelled_by           text        CHECK (cancelled_by IN ('OWNER','CUSTOMER','SYSTEM')),
    reminder_sent_at       timestamptz,
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    CONSTRAINT viewing_requests_window_chk CHECK (ends_at > starts_at)
);
CREATE INDEX viewing_requests_property_idx ON viewing_requests (property_id, starts_at DESC);
CREATE INDEX viewing_requests_customer_idx ON viewing_requests (customer_ref_id, starts_at DESC);
CREATE INDEX viewing_requests_reminder_idx ON viewing_requests (starts_at) WHERE status = 'CONFIRMED' AND reminder_sent_at IS NULL;

CREATE TABLE viewing_attendance (
    id             uuid PRIMARY KEY,
    viewing_id     uuid        NOT NULL REFERENCES viewing_requests (id),
    party          text        NOT NULL CHECK (party IN ('CUSTOMER','HOST')),
    outcome        text        NOT NULL CHECK (outcome IN ('CHECKED_IN','NO_SHOW','COMPLETED')),
    rating         integer     CHECK (rating BETWEEN 1 AND 5),
    feedback       text        CHECK (char_length(feedback) <= 2000),
    safety_flag    boolean     NOT NULL DEFAULT false,
    actor_domain   text,
    actor_id       uuid,
    occurred_at    timestamptz NOT NULL
);
CREATE INDEX viewing_attendance_viewing_idx ON viewing_attendance (viewing_id, occurred_at);
CREATE TRIGGER viewing_attendance_append_only BEFORE UPDATE OR DELETE ON viewing_attendance
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- 12 Applications -----------------------------------------------------------
CREATE TABLE screening_policies (
    id           uuid PRIMARY KEY,
    property_id  uuid        NOT NULL REFERENCES properties (id),
    version_no   integer     NOT NULL CHECK (version_no > 0),
    criteria     jsonb       NOT NULL,
    policy_version text      NOT NULL,
    created_by_actor_domain text NOT NULL,
    created_by_actor_id uuid NOT NULL,
    created_at   timestamptz NOT NULL,
    CONSTRAINT screening_policies_uniq UNIQUE (property_id, version_no)
);
CREATE TRIGGER screening_policies_append_only BEFORE UPDATE OR DELETE ON screening_policies
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE rental_applications (
    id                    uuid PRIMARY KEY,
    property_id           uuid        NOT NULL REFERENCES properties (id),
    listing_id            uuid        NOT NULL REFERENCES listings (id),
    unit_id               uuid        REFERENCES property_units (id),
    inquiry_id            uuid        REFERENCES inquiries (id),
    customer_ref_id       uuid        NOT NULL,
    status                text        NOT NULL CHECK (status IN ('DRAFT','RECEIVED','SUBMITTED','UNDER_REVIEW','INFO_REQUESTED','APPROVED','CONDITIONAL',
                                          'DECLINED','WAITLISTED','WITHDRAWN','EXPIRED','APPEAL','HUMAN_REVIEW')),
    desired_move_in       date,
    term_months           integer     CHECK (term_months BETWEEN 1 AND 600),
    occupants             integer     CHECK (occupants BETWEEN 1 AND 100),
    has_pets              boolean,
    screening_policy_id   uuid        REFERENCES screening_policies (id),
    submitted_at          timestamptz,
    respond_by            timestamptz,
    expires_at            timestamptz,
    applicant_consent_at  timestamptz NOT NULL,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL
);
CREATE INDEX rental_applications_property_idx ON rental_applications (property_id, created_at DESC, id DESC);
CREATE INDEX rental_applications_listing_idx ON rental_applications (listing_id, status);
CREATE UNIQUE INDEX rental_applications_open_uniq ON rental_applications (listing_id, customer_ref_id)
    WHERE status IN ('DRAFT','RECEIVED','SUBMITTED','UNDER_REVIEW','INFO_REQUESTED','APPROVED','CONDITIONAL','WAITLISTED','APPEAL','HUMAN_REVIEW');
CREATE INDEX rental_applications_expiry_idx ON rental_applications (expires_at) WHERE status IN ('SUBMITTED','RECEIVED','INFO_REQUESTED','UNDER_REVIEW','WAITLISTED');

CREATE TABLE application_participants (
    id                     uuid PRIMARY KEY,
    application_id         uuid        NOT NULL REFERENCES rental_applications (id),
    role                   text        NOT NULL CHECK (role IN ('PRIMARY','CO_APPLICANT','GUARANTOR','OCCUPANT')),
    customer_ref_id        uuid,
    display_name           text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 120),
    relationship           text        CHECK (relationship IN ('SELF','SPOUSE_PARTNER','FAMILY','FLATMATE','EMPLOYER','GUARANTOR','OTHER')),
    employment_status      text        CHECK (employment_status IN ('EMPLOYED','SELF_EMPLOYED','STUDENT','RETIRED','UNEMPLOYED','OTHER')),
    declared_monthly_income_minor bigint CHECK (declared_monthly_income_minor >= 0),
    income_currency        char(3)     CHECK (income_currency ~ '^[A-Z]{3}$'),
    identity_verification_id uuid,
    created_at             timestamptz NOT NULL
);
CREATE INDEX application_participants_app_idx ON application_participants (application_id);

CREATE TABLE application_evidence_grants (
    id                uuid PRIMARY KEY,
    application_id    uuid        NOT NULL REFERENCES rental_applications (id),
    participant_id    uuid        REFERENCES application_participants (id),
    evidence_type     text        NOT NULL CHECK (evidence_type IN ('PROOF_OF_INCOME','EMPLOYMENT_LETTER','BANK_STATEMENT','ID_DOCUMENT',
                                      'REFERENCE_LETTER','GUARANTOR_FORM','PREVIOUS_LANDLORD_REFERENCE','OTHER_ALTERNATIVE_EVIDENCE')),
    purpose           text        NOT NULL CHECK (purpose ~ '^[a-z][a-z0-9_.:-]{2,99}$'),
    status            text        NOT NULL CHECK (status IN ('REQUESTED','GRANTED','EXPIRED','REVOKED')),
    media_id          uuid,
    request_note      text        CHECK (char_length(request_note) <= 1000),
    due_at            timestamptz,
    granted_at        timestamptz,
    expires_at        timestamptz,
    revoked_at        timestamptz,
    access_count      integer     NOT NULL DEFAULT 0,
    last_accessed_at  timestamptz,
    created_at        timestamptz NOT NULL,
    CONSTRAINT application_evidence_media_chk CHECK (status <> 'GRANTED' OR (media_id IS NOT NULL AND expires_at IS NOT NULL))
);
CREATE INDEX application_evidence_app_idx ON application_evidence_grants (application_id);
CREATE INDEX application_evidence_expiry_idx ON application_evidence_grants (expires_at) WHERE status = 'GRANTED';

CREATE TABLE screening_results (
    id                uuid PRIMARY KEY,
    application_id    uuid        NOT NULL REFERENCES rental_applications (id),
    policy_id         uuid        NOT NULL REFERENCES screening_policies (id),
    criterion         text        NOT NULL,
    outcome           text        NOT NULL CHECK (outcome IN ('MET','NOT_MET','UNKNOWN','WAIVED')),
    source            text        NOT NULL CHECK (source IN ('APPLICANT_DECLARED','EVIDENCE_REVIEWED','OWNER_ASSESSED','AUTOMATED_FLAG')),
    note              text        CHECK (char_length(note) <= 500),
    actor_domain      text,
    actor_id          uuid,
    recorded_at       timestamptz NOT NULL
);
CREATE INDEX screening_results_app_idx ON screening_results (application_id, recorded_at);
CREATE TRIGGER screening_results_append_only BEFORE UPDATE OR DELETE ON screening_results
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Human decisions only (an automated flag is never the final decision).
CREATE TABLE application_decisions (
    id                  uuid PRIMARY KEY,
    application_id      uuid        NOT NULL REFERENCES rental_applications (id),
    decision            text        NOT NULL CHECK (decision IN ('APPROVED','CONDITIONAL','DECLINED','WAITLISTED')),
    reason_codes        text[]      NOT NULL DEFAULT '{}',
    conditions          text[]      NOT NULL DEFAULT '{}',
    note                text        CHECK (char_length(note) <= 2000),
    screening_policy_id uuid        REFERENCES screening_policies (id),
    policy_version      text        NOT NULL,
    human_review        boolean     NOT NULL DEFAULT false,
    actor_domain        text        NOT NULL,
    actor_id            uuid        NOT NULL,
    decided_at          timestamptz NOT NULL,
    CONSTRAINT application_decisions_reason_chk CHECK (decision <> 'DECLINED' OR cardinality(reason_codes) > 0)
);
CREATE INDEX application_decisions_app_idx ON application_decisions (application_id, decided_at);
CREATE TRIGGER application_decisions_append_only BEFORE UPDATE OR DELETE ON application_decisions
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE rental_offers (
    id                  uuid PRIMARY KEY,
    application_id      uuid        NOT NULL REFERENCES rental_applications (id),
    property_id         uuid        NOT NULL REFERENCES properties (id),
    listing_id          uuid        NOT NULL REFERENCES listings (id),
    unit_id             uuid        REFERENCES property_units (id),
    customer_ref_id     uuid        NOT NULL,
    status              text        NOT NULL CHECK (status IN ('DRAFT','SENT','ACCEPTED','DECLINED','EXPIRED','WITHDRAWN')),
    pricing_version_id  uuid        NOT NULL REFERENCES pricing_versions (id),
    cost_snapshot       jsonb       NOT NULL,
    start_date          date        NOT NULL,
    term_months         integer     NOT NULL CHECK (term_months BETWEEN 1 AND 600),
    conditions          text[]      NOT NULL DEFAULT '{}',
    expires_at          timestamptz NOT NULL,
    sent_at             timestamptz,
    responded_at        timestamptz,
    withdraw_reason     text,
    version             bigint      NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL
);
CREATE INDEX rental_offers_application_idx ON rental_offers (application_id);
-- At most one live offer per application. Competing acceptances on one
-- listing are serialized by the availability compare-and-swap.
CREATE UNIQUE INDEX rental_offers_live_uniq ON rental_offers (application_id) WHERE status IN ('DRAFT','SENT');
CREATE INDEX rental_offers_expiry_idx ON rental_offers (expires_at) WHERE status = 'SENT';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS rental_offers;
DROP TABLE IF EXISTS application_decisions;
DROP TABLE IF EXISTS screening_results;
DROP TABLE IF EXISTS application_evidence_grants;
DROP TABLE IF EXISTS application_participants;
DROP TABLE IF EXISTS rental_applications;
DROP TABLE IF EXISTS screening_policies;
DROP TABLE IF EXISTS viewing_attendance;
DROP TABLE IF EXISTS viewing_requests;
DROP TABLE IF EXISTS viewing_slots;
DROP TABLE IF EXISTS lead_activities;
DROP TABLE IF EXISTS inquiries;
-- +goose StatementEnd
