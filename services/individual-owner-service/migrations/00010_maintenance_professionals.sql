-- Section 6 contexts 22–23: maintenance, artisans and preventive care
-- (§13.22) and professional services and due diligence (§13.23).
--
-- The owner service owns the property request, responsibility, approval and
-- property history; artisan-service and professional-services-service own
-- provider discovery and execution and report back through internal
-- contracts. Provider evidence is append-only (job events, findings) so a
-- challenged completion never overwrites what was submitted. Money moves
-- only through payment-service (PLATFORM) or is recorded as off-platform
-- evidence.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE preventive_schedules (
    id                     uuid PRIMARY KEY,
    property_id            uuid        NOT NULL REFERENCES properties (id),
    asset_id               uuid        REFERENCES property_assets (id),
    title                  text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    category               text        NOT NULL CHECK (category ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    interval_days          integer     NOT NULL CHECK (interval_days BETWEEN 1 AND 3650),
    lead_days              integer     NOT NULL DEFAULT 7 CHECK (lead_days BETWEEN 0 AND 90),
    next_due_on            date        NOT NULL,
    priority               text        NOT NULL DEFAULT 'NORMAL' CHECK (priority IN ('LOW','NORMAL','HIGH')),
    status                 text        NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','PAUSED','RETIRED')),
    last_generated_on      date,
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL
);
CREATE INDEX preventive_schedules_due_idx ON preventive_schedules (next_due_on) WHERE status = 'ACTIVE';

CREATE TABLE maintenance_requests (
    id                     uuid PRIMARY KEY,
    property_id            uuid        NOT NULL REFERENCES properties (id),
    unit_id                uuid        REFERENCES property_units (id),
    asset_id               uuid        REFERENCES property_assets (id),
    tenancy_id             uuid        REFERENCES tenancies (id),
    preventive_schedule_id uuid        REFERENCES preventive_schedules (id),
    due_on                 date,
    warranty_of_request_id uuid        REFERENCES maintenance_requests (id),
    title                  text        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    description            text        NOT NULL CHECK (char_length(description) BETWEEN 1 AND 4000),
    category               text        NOT NULL CHECK (category ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    priority               text        NOT NULL CHECK (priority IN ('LOW','NORMAL','HIGH','EMERGENCY')),
    responsibility         text        NOT NULL DEFAULT 'PENDING_REVIEW' CHECK (responsibility IN ('PENDING_REVIEW','OWNER','TENANT','SHARED','WARRANTY')),
    responsibility_note    text        CHECK (char_length(responsibility_note) <= 1000),
    sourcing               text        CHECK (sourcing IN ('PLATFORM','SELF_SOURCED')),
    reported_by_type       text        NOT NULL CHECK (reported_by_type IN ('OWNER','MANAGER','TENANT','SYSTEM')),
    reported_by_ref        uuid,
    status                 text        NOT NULL CHECK (status IN ('OPEN','TRIAGED','QUOTING','APPROVAL_PENDING','APPROVED','ASSIGNED','IN_PROGRESS',
                               'COMPLETION_REVIEW','COMPLETED','REWORK','CANCELLED','DISPUTED')),
    evidence_media_ids     uuid[]      NOT NULL DEFAULT '{}',
    budget_minor           bigint      CHECK (budget_minor > 0),
    currency               char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    cfmeet_context_id      text        CHECK (char_length(cfmeet_context_id) <= 128),
    cancel_reason          text        CHECK (cancel_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    CONSTRAINT maintenance_requests_media_chk CHECK (cardinality(evidence_media_ids) <= 20)
);
CREATE INDEX maintenance_requests_property_idx ON maintenance_requests (property_id, created_at DESC);
CREATE UNIQUE INDEX maintenance_requests_preventive_uniq ON maintenance_requests (preventive_schedule_id, due_on) WHERE preventive_schedule_id IS NOT NULL;

CREATE TABLE maintenance_quotes (
    id                     uuid PRIMARY KEY,
    request_id             uuid        NOT NULL REFERENCES maintenance_requests (id),
    source                 text        NOT NULL CHECK (source IN ('ARTISAN_SERVICE','OWNER_RECORDED')),
    provider_ref           uuid,
    provider_display_name  text        NOT NULL CHECK (char_length(provider_display_name) BETWEEN 1 AND 120),
    external_quote_ref     text        CHECK (char_length(external_quote_ref) <= 128),
    scope                  text        NOT NULL CHECK (char_length(scope) BETWEEN 1 AND 4000),
    amount_minor           bigint      NOT NULL CHECK (amount_minor > 0),
    currency               char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    earliest_start_at      timestamptz,
    valid_until            timestamptz,
    warranty_days          integer     NOT NULL DEFAULT 0 CHECK (warranty_days BETWEEN 0 AND 3650),
    evidence_media_ids     uuid[]      NOT NULL DEFAULT '{}',
    status                 text        NOT NULL CHECK (status IN ('SUBMITTED','SELECTED','REJECTED','WITHDRAWN','EXPIRED')),
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    CONSTRAINT maintenance_quotes_provider_chk CHECK (source = 'OWNER_RECORDED' OR (provider_ref IS NOT NULL AND external_quote_ref IS NOT NULL))
);
CREATE INDEX maintenance_quotes_request_idx ON maintenance_quotes (request_id);
CREATE UNIQUE INDEX maintenance_quotes_external_uniq ON maintenance_quotes (external_quote_ref) WHERE external_quote_ref IS NOT NULL;
CREATE UNIQUE INDEX maintenance_quotes_selected_uniq ON maintenance_quotes (request_id) WHERE status = 'SELECTED';

-- Approval decisions are append-only: who approved what amount, under which
-- authority (owner or a delegated manager within limits).
CREATE TABLE maintenance_approvals (
    id                     uuid PRIMARY KEY,
    request_id             uuid        NOT NULL REFERENCES maintenance_requests (id),
    quote_id               uuid        REFERENCES maintenance_quotes (id),
    decision               text        NOT NULL CHECK (decision IN ('APPROVED','REJECTED')),
    amount_minor           bigint      CHECK (amount_minor > 0),
    currency               char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    approved_via           text        NOT NULL CHECK (approved_via IN ('OWNER','CO_OWNER','MANAGER')),
    approver_ref           uuid        NOT NULL,
    assignment_id          uuid        REFERENCES manager_assignments (id),
    limit_minor            bigint,
    emergency              boolean     NOT NULL DEFAULT false,
    note                   text        CHECK (char_length(note) <= 1000),
    created_at             timestamptz NOT NULL
);
CREATE INDEX maintenance_approvals_request_idx ON maintenance_approvals (request_id, created_at);
CREATE TRIGGER maintenance_approvals_append_only BEFORE UPDATE OR DELETE ON maintenance_approvals
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE maintenance_jobs (
    id                     uuid PRIMARY KEY,
    request_id             uuid        NOT NULL REFERENCES maintenance_requests (id),
    quote_id               uuid        NOT NULL REFERENCES maintenance_quotes (id),
    provider_ref           uuid,
    external_job_ref       text        CHECK (char_length(external_job_ref) <= 128),
    status                 text        NOT NULL CHECK (status IN ('ASSIGNED','IN_PROGRESS','COMPLETION_SUBMITTED','REWORK_REQUESTED','ACCEPTED','CANCELLED')),
    amount_minor           bigint      NOT NULL CHECK (amount_minor > 0),
    invoiced_minor         bigint,
    currency               char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    payment_collection     text        NOT NULL CHECK (payment_collection IN ('PLATFORM','OFF_PLATFORM')),
    payment_intent_ref     text        CHECK (char_length(payment_intent_ref) <= 200),
    paid_minor             bigint      NOT NULL DEFAULT 0,
    last_payment_status    text,
    last_checked_at        timestamptz,
    offline_payment_evidence uuid[]    NOT NULL DEFAULT '{}',
    provider_payment_ack   boolean     NOT NULL DEFAULT false,
    rework_count           integer     NOT NULL DEFAULT 0,
    accepted_at            timestamptz,
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL
);
CREATE UNIQUE INDEX maintenance_jobs_active_uniq ON maintenance_jobs (request_id) WHERE status <> 'CANCELLED';
CREATE UNIQUE INDEX maintenance_jobs_external_uniq ON maintenance_jobs (external_job_ref) WHERE external_job_ref IS NOT NULL;
CREATE UNIQUE INDEX maintenance_jobs_intent_uniq ON maintenance_jobs (payment_intent_ref) WHERE payment_intent_ref IS NOT NULL;

-- Attendance/progress/completion/invoice/rework records, append-only.
CREATE TABLE maintenance_job_events (
    id                     uuid PRIMARY KEY,
    job_id                 uuid        NOT NULL REFERENCES maintenance_jobs (id),
    event_type             text        NOT NULL CHECK (event_type IN ('ATTENDED','PROGRESS','COMPLETION_SUBMITTED','INVOICE','REWORK_REQUESTED',
                               'ACCEPTED','PAYMENT_ACKNOWLEDGED','CANCELLED')),
    source                 text        NOT NULL CHECK (source IN ('ARTISAN_SERVICE','OWNER','MANAGER','SYSTEM')),
    external_event_id      text        CHECK (char_length(external_event_id) <= 128),
    note                   text        CHECK (char_length(note) <= 2000),
    evidence_media_ids     uuid[]      NOT NULL DEFAULT '{}',
    amount_minor           bigint,
    occurred_at            timestamptz NOT NULL,
    recorded_at            timestamptz NOT NULL
);
CREATE INDEX maintenance_job_events_job_idx ON maintenance_job_events (job_id, occurred_at);
CREATE UNIQUE INDEX maintenance_job_events_external_uniq ON maintenance_job_events (job_id, external_event_id) WHERE external_event_id IS NOT NULL;
CREATE TRIGGER maintenance_job_events_append_only BEFORE UPDATE OR DELETE ON maintenance_job_events
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE maintenance_warranties (
    id                     uuid PRIMARY KEY,
    request_id             uuid        NOT NULL REFERENCES maintenance_requests (id),
    job_id                 uuid        NOT NULL REFERENCES maintenance_jobs (id),
    provider_ref           uuid,
    starts_on              date        NOT NULL,
    ends_on                date        NOT NULL,
    status                 text        NOT NULL CHECK (status IN ('ACTIVE','CLAIMED','EXPIRED')),
    claim_request_id       uuid        REFERENCES maintenance_requests (id),
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    CONSTRAINT maintenance_warranties_range_chk CHECK (ends_on >= starts_on)
);
CREATE UNIQUE INDEX maintenance_warranties_job_uniq ON maintenance_warranties (job_id);

CREATE TABLE professional_appointments (
    id                     uuid PRIMARY KEY,
    owner_account_id       uuid        NOT NULL REFERENCES owner_accounts (account_id),
    property_id            uuid        REFERENCES properties (id),
    sale_id                uuid        REFERENCES sale_transactions (id),
    purpose                text        NOT NULL CHECK (purpose IN ('VALUATION','SURVEY','LEGAL_DUE_DILIGENCE','TITLE_SEARCH','INSPECTION','TAX_ADVICE',
                               'CONVEYANCING','OTHER')),
    scope                  text        NOT NULL CHECK (char_length(scope) BETWEEN 1 AND 4000),
    professional_ref       uuid        NOT NULL,
    professional_label     text        NOT NULL CHECK (char_length(professional_label) BETWEEN 1 AND 120),
    external_ref           text        CHECK (char_length(external_ref) <= 128),
    status                 text        NOT NULL CHECK (status IN ('REQUESTED','QUOTING','APPOINTED','IN_PROGRESS','REPORT_SUBMITTED','ACCEPTED','DISPUTED','CANCELLED')),
    quote_amount_minor     bigint      CHECK (quote_amount_minor > 0),
    currency               char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    quote_valid_until      timestamptz,
    licence_ref            text        CHECK (char_length(licence_ref) <= 128),
    conflict_declared      boolean,
    conflict_note          text        CHECK (char_length(conflict_note) <= 1000),
    fee_collection         text        CHECK (fee_collection IN ('PLATFORM','OFF_PLATFORM')),
    payment_intent_ref     text        CHECK (char_length(payment_intent_ref) <= 200),
    paid_minor             bigint      NOT NULL DEFAULT 0,
    last_payment_status    text,
    last_checked_at        timestamptz,
    data_room_grant_id     uuid        REFERENCES data_room_grants (id),
    report_media_id        uuid,
    report_signed          boolean     NOT NULL DEFAULT false,
    report_submitted_at    timestamptz,
    due_by                 date,
    rating                 integer     CHECK (rating BETWEEN 1 AND 5),
    feedback               text        CHECK (char_length(feedback) <= 2000),
    cancel_reason          text        CHECK (cancel_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    cfmeet_context_id      text        CHECK (char_length(cfmeet_context_id) <= 128),
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    CONSTRAINT professional_appointments_subject_chk CHECK (property_id IS NOT NULL OR sale_id IS NOT NULL)
);
CREATE INDEX professional_appointments_owner_idx ON professional_appointments (owner_account_id, created_at DESC);
CREATE UNIQUE INDEX professional_appointments_external_uniq ON professional_appointments (external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX professional_appointments_intent_uniq ON professional_appointments (payment_intent_ref) WHERE payment_intent_ref IS NOT NULL;

-- Report findings (signed report references) and how the owner used them.
CREATE TABLE professional_findings (
    id                     uuid PRIMARY KEY,
    appointment_id         uuid        NOT NULL REFERENCES professional_appointments (id),
    category               text        NOT NULL CHECK (category ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    severity               text        NOT NULL CHECK (severity IN ('INFO','LOW','MEDIUM','HIGH','BLOCKING')),
    summary                text        NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 2000),
    media_id               uuid,
    external_finding_ref   text        CHECK (char_length(external_finding_ref) <= 128),
    used_in_type           text        CHECK (used_in_type IN ('due_diligence_item','maintenance_request')),
    used_in_id             uuid,
    created_at             timestamptz NOT NULL
);
CREATE INDEX professional_findings_appointment_idx ON professional_findings (appointment_id);
CREATE UNIQUE INDEX professional_findings_external_uniq ON professional_findings (appointment_id, external_finding_ref) WHERE external_finding_ref IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS professional_findings;
DROP TABLE IF EXISTS professional_appointments;
DROP TABLE IF EXISTS maintenance_warranties;
DROP TABLE IF EXISTS maintenance_job_events;
DROP TABLE IF EXISTS maintenance_jobs;
DROP TABLE IF EXISTS maintenance_approvals;
DROP TABLE IF EXISTS maintenance_quotes;
DROP TABLE IF EXISTS maintenance_requests;
DROP TABLE IF EXISTS preventive_schedules;
-- +goose StatementEnd
