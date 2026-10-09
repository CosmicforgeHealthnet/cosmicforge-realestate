-- Section 6 contexts 13 (agreements & move-in), 14 (tenancy, rent and
-- deposit obligations), 15 (renewal, notices, move-out) and 16 (utilities
-- and service charges) — §13.13–§13.16.
--
-- Legal content is controlled configuration (agreement/notice templates
-- managed by operators, versioned per jurisdiction), never hard-coded.
-- Agreement versions, condition evidence, meter readings, renewal terms and
-- service-charge items are append-only: corrections and disputes add rows.
-- Rent and deposit rows are business obligations; their money state changes
-- only from payment-service outcomes (events or read-back), never locally.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE agreement_templates (
    id                    uuid PRIMARY KEY,
    code                  text        NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9_]{2,63}$'),
    version_no            integer     NOT NULL CHECK (version_no > 0),
    country               char(2)     NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    agreement_kind        text        NOT NULL CHECK (agreement_kind IN ('RESIDENTIAL_LEASE','COMMERCIAL_LEASE','LAND_LEASE','RENEWAL_ADDENDUM')),
    title                 text        NOT NULL CHECK (char_length(title) BETWEEN 3 AND 200),
    document_media_id     uuid        NOT NULL,
    clauses               jsonb       NOT NULL DEFAULT '[]',
    status                text        NOT NULL CHECK (status IN ('ACTIVE','RETIRED')),
    policy_version        text        NOT NULL,
    created_by_actor_domain text      NOT NULL,
    created_by_actor_id   uuid        NOT NULL,
    created_at            timestamptz NOT NULL,
    retired_at            timestamptz,
    CONSTRAINT agreement_templates_uniq UNIQUE (code, version_no)
);
CREATE INDEX agreement_templates_lookup_idx ON agreement_templates (country, agreement_kind) WHERE status = 'ACTIVE';

CREATE TABLE notice_templates (
    id                    uuid PRIMARY KEY,
    code                  text        NOT NULL CHECK (code ~ '^[A-Z][A-Z0-9_]{2,63}$'),
    version_no            integer     NOT NULL CHECK (version_no > 0),
    country               char(2)     NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
    notice_type           text        NOT NULL CHECK (notice_type IN ('RENT_REMINDER','ARREARS','RENT_REVIEW','RENEWAL','TERMINATION','ENTRY','BREACH','GENERAL')),
    title                 text        NOT NULL CHECK (char_length(title) BETWEEN 3 AND 200),
    min_notice_days       integer     NOT NULL CHECK (min_notice_days BETWEEN 0 AND 730),
    document_media_id     uuid,
    status                text        NOT NULL CHECK (status IN ('ACTIVE','RETIRED')),
    policy_version        text        NOT NULL,
    created_by_actor_domain text      NOT NULL,
    created_by_actor_id   uuid        NOT NULL,
    created_at            timestamptz NOT NULL,
    retired_at            timestamptz,
    CONSTRAINT notice_templates_uniq UNIQUE (code, version_no)
);
CREATE INDEX notice_templates_lookup_idx ON notice_templates (country, notice_type) WHERE status = 'ACTIVE';

-- Templates are immutable except for retirement.
CREATE FUNCTION template_retire_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME USING ERRCODE = 'check_violation';
    END IF;
    IF OLD.status <> 'ACTIVE' OR NEW.status <> 'RETIRED' OR NEW.retired_at IS NULL
       OR (to_jsonb(NEW) - 'status' - 'retired_at') <> (to_jsonb(OLD) - 'status' - 'retired_at') THEN
        RAISE EXCEPTION '% rows can only be retired', TG_TABLE_NAME USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER agreement_templates_guard BEFORE UPDATE OR DELETE ON agreement_templates
    FOR EACH ROW EXECUTE FUNCTION template_retire_only();
CREATE TRIGGER notice_templates_guard BEFORE UPDATE OR DELETE ON notice_templates
    FOR EACH ROW EXECUTE FUNCTION template_retire_only();

-- 13 Agreements -------------------------------------------------------------
CREATE TABLE agreements (
    id                    uuid PRIMARY KEY,
    property_id           uuid        NOT NULL REFERENCES properties (id),
    unit_id               uuid        REFERENCES property_units (id),
    listing_id            uuid        REFERENCES listings (id),
    offer_id              uuid        REFERENCES rental_offers (id),
    parent_agreement_id   uuid        REFERENCES agreements (id),
    renewal_case_id       uuid,
    agreement_kind        text        NOT NULL CHECK (agreement_kind IN ('RESIDENTIAL_LEASE','COMMERCIAL_LEASE','LAND_LEASE','RENEWAL_ADDENDUM')),
    source                text        NOT NULL CHECK (source IN ('RENTAL_OFFER','DIRECT','RENEWAL')),
    status                text        NOT NULL CHECK (status IN ('DRAFT','REVIEW','SIGNING','FULLY_SIGNED','EFFECTIVE','CANCELLED')),
    customer_ref_id       uuid,
    template_id           uuid        REFERENCES agreement_templates (id),
    pricing_version_id    uuid        REFERENCES pricing_versions (id),
    current_version_no    integer     NOT NULL DEFAULT 0,
    signing_method        text        CHECK (signing_method IN ('ESIGN','OFFLINE')),
    esign_envelope_ref    text        CHECK (char_length(esign_envelope_ref) <= 200),
    signing_version_no    integer,
    signing_started_at    timestamptz,
    fully_signed_at       timestamptz,
    effective_at          timestamptz,
    cancelled_at          timestamptz,
    cancel_reason         text        CHECK (cancel_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT agreements_source_chk CHECK ((source = 'RENTAL_OFFER') = (offer_id IS NOT NULL)),
    CONSTRAINT agreements_renewal_chk CHECK ((source = 'RENEWAL') = (parent_agreement_id IS NOT NULL AND agreement_kind = 'RENEWAL_ADDENDUM')),
    CONSTRAINT agreements_signing_chk CHECK (status IN ('DRAFT','REVIEW','CANCELLED') OR (signing_method IS NOT NULL AND signing_version_no IS NOT NULL))
);
CREATE INDEX agreements_property_idx ON agreements (property_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX agreements_offer_uniq ON agreements (offer_id) WHERE offer_id IS NOT NULL AND status <> 'CANCELLED';
CREATE INDEX agreements_signing_idx ON agreements (updated_at) WHERE status = 'SIGNING' AND signing_method = 'ESIGN';

-- Versions are immutable; a document or term change is a new version and
-- is impossible once signature invitations went out (use case + status).
CREATE TABLE agreement_versions (
    id                    uuid PRIMARY KEY,
    agreement_id          uuid        NOT NULL REFERENCES agreements (id),
    version_no            integer     NOT NULL CHECK (version_no > 0),
    terms                 jsonb       NOT NULL,
    document_media_id     uuid,
    template_id           uuid        REFERENCES agreement_templates (id),
    template_version_no   integer,
    change_note           text        CHECK (char_length(change_note) <= 1000),
    actor_domain          text,
    actor_id              uuid,
    created_at            timestamptz NOT NULL,
    CONSTRAINT agreement_versions_uniq UNIQUE (agreement_id, version_no)
);
CREATE TRIGGER agreement_versions_append_only BEFORE UPDATE OR DELETE ON agreement_versions
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE agreement_participants (
    id                    uuid PRIMARY KEY,
    agreement_id          uuid        NOT NULL REFERENCES agreements (id),
    role                  text        NOT NULL CHECK (role IN ('OWNER','CO_OWNER','REPRESENTATIVE','TENANT','CO_TENANT','GUARANTOR','WITNESS')),
    party_domain          text        NOT NULL CHECK (party_domain IN ('individual-owner','marketplace','external')),
    party_ref_id          uuid,
    display_name          text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 120),
    signing_email         text        CHECK (char_length(signing_email) <= 254),
    sign_required         boolean     NOT NULL DEFAULT true,
    removed_at            timestamptz,
    created_at            timestamptz NOT NULL,
    CONSTRAINT agreement_participants_ref_chk CHECK ((party_domain = 'external') = (party_ref_id IS NULL))
);
CREATE INDEX agreement_participants_agreement_idx ON agreement_participants (agreement_id) WHERE removed_at IS NULL;
CREATE INDEX agreement_participants_party_idx ON agreement_participants (party_domain, party_ref_id) WHERE removed_at IS NULL;

CREATE TABLE signature_requests (
    id                    uuid PRIMARY KEY,
    agreement_id          uuid        NOT NULL REFERENCES agreements (id),
    agreement_version_no  integer     NOT NULL,
    participant_id        uuid        NOT NULL REFERENCES agreement_participants (id),
    method                text        NOT NULL CHECK (method IN ('ESIGN','OFFLINE')),
    status                text        NOT NULL CHECK (status IN ('PENDING','SENT','VIEWED','AWAITING_CONFIRMATION','SIGNED','DECLINED','EXPIRED','VOIDED')),
    provider_signer_ref   text        CHECK (char_length(provider_signer_ref) <= 200),
    evidence_media_id     uuid,
    certificate_media_id  uuid,
    attested_by           text        CHECK (attested_by IN ('PROVIDER','SIGNER','OWNER')),
    signed_at             timestamptz,
    declined_reason       text        CHECK (char_length(declined_reason) <= 500),
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT signature_requests_uniq UNIQUE (agreement_id, agreement_version_no, participant_id),
    CONSTRAINT signature_requests_signed_chk CHECK (status <> 'SIGNED' OR (signed_at IS NOT NULL AND attested_by IS NOT NULL))
);

-- 14 Tenancy ----------------------------------------------------------------
CREATE TABLE tenancies (
    id                         uuid PRIMARY KEY,
    property_id                uuid        NOT NULL REFERENCES properties (id),
    unit_id                    uuid        REFERENCES property_units (id),
    listing_id                 uuid        REFERENCES listings (id),
    agreement_id               uuid        NOT NULL UNIQUE REFERENCES agreements (id),
    customer_ref_id            uuid,
    tenant_display_name        text        NOT NULL CHECK (char_length(tenant_display_name) BETWEEN 1 AND 120),
    status                     text        NOT NULL CHECK (status IN ('PENDING_MOVE_IN','ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING','MOVE_OUT_PENDING','ENDED')),
    start_date                 date        NOT NULL,
    end_date                   date,
    rent_minor                 bigint      NOT NULL CHECK (rent_minor >= 0),
    currency                   char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    frequency                  text        NOT NULL CHECK (frequency IN ('MONTHLY','QUARTERLY','BIANNUAL','ANNUAL','ONE_TIME')),
    recurring_fees_minor       bigint      NOT NULL DEFAULT 0 CHECK (recurring_fees_minor >= 0),
    escalation_after_months    integer     CHECK (escalation_after_months BETWEEN 1 AND 600),
    escalation_bps             integer     CHECK (escalation_bps BETWEEN -10000 AND 10000),
    due_day                    integer     NOT NULL CHECK (due_day BETWEEN 1 AND 28),
    rent_collection            text        NOT NULL CHECK (rent_collection IN ('PLATFORM','OFF_PLATFORM')),
    deposit_handling           text        NOT NULL CHECK (deposit_handling IN ('PLATFORM','OFF_PLATFORM','NONE')),
    late_fee_grace_days        integer     NOT NULL DEFAULT 0 CHECK (late_fee_grace_days BETWEEN 0 AND 60),
    late_fee_minor             bigint      NOT NULL DEFAULT 0 CHECK (late_fee_minor >= 0),
    dispute_flag               boolean     NOT NULL DEFAULT false,
    schedule_anchor            date,
    schedule_materialized_to   date,
    move_in_at                 timestamptz,
    termination_effective_date date,
    ended_at                   timestamptz,
    end_reason                 text        CHECK (end_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    renewal_reminder_sent_at   timestamptz,
    version                    bigint      NOT NULL DEFAULT 1,
    created_at                 timestamptz NOT NULL,
    updated_at                 timestamptz NOT NULL,
    CONSTRAINT tenancies_dates_chk CHECK (end_date IS NULL OR end_date > start_date)
);
CREATE INDEX tenancies_property_idx ON tenancies (property_id, created_at DESC, id DESC);
CREATE INDEX tenancies_customer_idx ON tenancies (customer_ref_id) WHERE customer_ref_id IS NOT NULL;
-- One occupying tenancy per space (whole property when unit_id is NULL).
CREATE UNIQUE INDEX tenancies_occupying_uniq ON tenancies (property_id, coalesce(unit_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE status IN ('ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING');
CREATE INDEX tenancies_schedule_idx ON tenancies (schedule_materialized_to) WHERE status IN ('ACTIVE','RENEWAL_PENDING','TERMINATION_PENDING','PENDING_MOVE_IN') AND rent_collection = 'PLATFORM';

ALTER TABLE agreements ADD COLUMN tenancy_id uuid REFERENCES tenancies (id);

CREATE TABLE rent_obligations (
    id                    uuid PRIMARY KEY,
    tenancy_id            uuid        NOT NULL REFERENCES tenancies (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    kind                  text        NOT NULL CHECK (kind IN ('RENT','LATE_FEE')),
    period_start          date        NOT NULL,
    period_end            date        NOT NULL,
    due_date              date        NOT NULL,
    amount_minor          bigint      NOT NULL CHECK (amount_minor > 0),
    currency              char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status                text        NOT NULL CHECK (status IN ('SCHEDULED','DUE','PARTIAL','PAID','OVERDUE','DISPUTED','WAIVED','REFUND_PENDING','REFUNDED')),
    paid_minor            bigint      NOT NULL DEFAULT 0 CHECK (paid_minor >= 0),
    refunded_minor        bigint      NOT NULL DEFAULT 0 CHECK (refunded_minor >= 0),
    payment_intent_ref    text        CHECK (char_length(payment_intent_ref) <= 200),
    last_payment_status   text,
    status_reason         text        CHECK (status_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    reminder_sent_at      timestamptz,
    last_checked_at       timestamptz,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT rent_obligations_period_uniq UNIQUE (tenancy_id, kind, period_start),
    CONSTRAINT rent_obligations_period_chk CHECK (period_end >= period_start)
);
CREATE INDEX rent_obligations_tenancy_idx ON rent_obligations (tenancy_id, due_date);
CREATE INDEX rent_obligations_due_idx ON rent_obligations (due_date) WHERE status IN ('SCHEDULED','DUE','PARTIAL');
CREATE UNIQUE INDEX rent_obligations_intent_uniq ON rent_obligations (payment_intent_ref) WHERE payment_intent_ref IS NOT NULL;

CREATE TABLE rent_obligation_status_history (
    id                    uuid PRIMARY KEY,
    obligation_id         uuid        NOT NULL REFERENCES rent_obligations (id),
    from_status           text        NOT NULL,
    to_status             text        NOT NULL,
    source                text        NOT NULL CHECK (source IN ('SCHEDULE','PAYMENT_EVENT','PAYMENT_READBACK','OWNER','TENANT','SYSTEM')),
    payment_ref           text,
    source_event_id       uuid,
    paid_minor            bigint,
    reason                text,
    occurred_at           timestamptz NOT NULL
);
CREATE INDEX rent_obligation_history_idx ON rent_obligation_status_history (obligation_id, occurred_at);
CREATE TRIGGER rent_obligation_status_history_append_only BEFORE UPDATE OR DELETE ON rent_obligation_status_history
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE deposit_obligations (
    id                    uuid PRIMARY KEY,
    tenancy_id            uuid        NOT NULL UNIQUE REFERENCES tenancies (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    amount_minor          bigint      NOT NULL CHECK (amount_minor > 0),
    currency              char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status                text        NOT NULL CHECK (status IN ('REQUIRED','FUNDED','HELD','RELEASE_PENDING','DEDUCTION_PROPOSED','PARTIAL_RELEASED','RELEASED','DISPUTED')),
    payment_intent_ref    text        CHECK (char_length(payment_intent_ref) <= 200),
    last_payment_status   text,
    funded_minor          bigint      NOT NULL DEFAULT 0 CHECK (funded_minor >= 0),
    release_due_days      integer     NOT NULL CHECK (release_due_days BETWEEN 1 AND 180),
    deductions            jsonb       NOT NULL DEFAULT '[]',
    deduction_total_minor bigint      NOT NULL DEFAULT 0 CHECK (deduction_total_minor >= 0),
    proposed_at           timestamptz,
    response_due_at       timestamptz,
    tenant_response       text        CHECK (tenant_response IN ('ACCEPTED','DISPUTED')),
    disputed_minor        bigint      NOT NULL DEFAULT 0 CHECK (disputed_minor >= 0),
    release_request_ref   text        CHECK (char_length(release_request_ref) <= 200),
    release_requested_at  timestamptz,
    released_minor        bigint      NOT NULL DEFAULT 0 CHECK (released_minor >= 0),
    resolution_owner_minor bigint     CHECK (resolution_owner_minor >= 0),
    resolved_at           timestamptz,
    last_checked_at       timestamptz,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT deposit_deductions_chk CHECK (deduction_total_minor <= amount_minor AND disputed_minor <= deduction_total_minor)
);
CREATE UNIQUE INDEX deposit_obligations_intent_uniq ON deposit_obligations (payment_intent_ref) WHERE payment_intent_ref IS NOT NULL;

CREATE TABLE move_in_checklists (
    id                    uuid PRIMARY KEY,
    tenancy_id            uuid        NOT NULL REFERENCES tenancies (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    phase                 text        NOT NULL CHECK (phase IN ('MOVE_IN','MOVE_OUT')),
    status                text        NOT NULL CHECK (status IN ('OPEN','SUBMITTED','ACKNOWLEDGED','DISPUTED')),
    submitted_at          timestamptz,
    owner_ack_at          timestamptz,
    tenant_ack_at         timestamptz,
    tenant_ack_evidence_media_id uuid,
    locked_at             timestamptz,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT move_in_checklists_phase_uniq UNIQUE (tenancy_id, phase),
    CONSTRAINT move_in_checklists_lock_chk CHECK (locked_at IS NULL OR (owner_ack_at IS NOT NULL AND tenant_ack_at IS NOT NULL))
);

CREATE TABLE condition_items (
    id                    uuid PRIMARY KEY,
    checklist_id          uuid        NOT NULL REFERENCES move_in_checklists (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    room                  text        NOT NULL CHECK (char_length(room) BETWEEN 1 AND 80),
    item                  text        NOT NULL CHECK (char_length(item) BETWEEN 1 AND 120),
    condition             text        NOT NULL CHECK (condition IN ('EXCELLENT','GOOD','FAIR','POOR','DAMAGED','MISSING','NOT_PRESENT')),
    notes                 text        CHECK (char_length(notes) <= 1000),
    media_ids             uuid[]      NOT NULL DEFAULT '{}',
    supersedes_id         uuid        REFERENCES condition_items (id),
    addendum              boolean     NOT NULL DEFAULT false,
    recorded_by_party     text        NOT NULL CHECK (recorded_by_party IN ('OWNER','TENANT')),
    actor_domain          text,
    actor_id              uuid,
    recorded_at           timestamptz NOT NULL,
    CONSTRAINT condition_items_media_chk CHECK (cardinality(media_ids) <= 10)
);
CREATE INDEX condition_items_checklist_idx ON condition_items (checklist_id, recorded_at);
CREATE TRIGGER condition_items_append_only BEFORE UPDATE OR DELETE ON condition_items
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Keys, cards and codes handed over. Codes themselves are never stored.
CREATE TABLE key_access_inventory (
    id                    uuid PRIMARY KEY,
    tenancy_id            uuid        NOT NULL REFERENCES tenancies (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    item_type             text        NOT NULL CHECK (item_type IN ('KEY','CARD','FOB','REMOTE','LOCKBOX','SMART_ACCESS','OTHER')),
    label                 text        NOT NULL CHECK (char_length(label) BETWEEN 1 AND 120),
    quantity              integer     NOT NULL CHECK (quantity BETWEEN 1 AND 100),
    access_grant_ref      uuid,
    issued_at             timestamptz NOT NULL,
    returned_at           timestamptz,
    returned_quantity     integer     CHECK (returned_quantity BETWEEN 0 AND 100),
    notes                 text        CHECK (char_length(notes) <= 500),
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL
);
CREATE INDEX key_access_inventory_tenancy_idx ON key_access_inventory (tenancy_id);

-- 15 Renewal, notices, move-out ---------------------------------------------
CREATE TABLE renewal_cases (
    id                    uuid PRIMARY KEY,
    tenancy_id            uuid        NOT NULL REFERENCES tenancies (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    status                text        NOT NULL CHECK (status IN ('PROPOSED','COUNTERED','ACCEPTED','DECLINED','WITHDRAWN','EXPIRED','COMPLETED')),
    current_round         integer     NOT NULL DEFAULT 1,
    addendum_agreement_id uuid        REFERENCES agreements (id),
    respond_by            timestamptz NOT NULL,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL
);
CREATE UNIQUE INDEX renewal_cases_open_uniq ON renewal_cases (tenancy_id) WHERE status IN ('PROPOSED','COUNTERED','ACCEPTED');
CREATE INDEX renewal_cases_deadline_idx ON renewal_cases (respond_by) WHERE status IN ('PROPOSED','COUNTERED');

ALTER TABLE agreements ADD CONSTRAINT agreements_renewal_case_fk FOREIGN KEY (renewal_case_id) REFERENCES renewal_cases (id);

CREATE TABLE renewal_terms (
    id                    uuid PRIMARY KEY,
    case_id               uuid        NOT NULL REFERENCES renewal_cases (id),
    round_no              integer     NOT NULL CHECK (round_no > 0),
    proposed_by           text        NOT NULL CHECK (proposed_by IN ('OWNER','TENANT')),
    rent_minor            bigint      NOT NULL CHECK (rent_minor >= 0),
    currency              char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    term_months           integer     NOT NULL CHECK (term_months BETWEEN 1 AND 600),
    start_date            date        NOT NULL,
    change_explanation    text        CHECK (char_length(change_explanation) <= 2000),
    market_context        jsonb,
    actor_domain          text,
    actor_id              uuid,
    created_at            timestamptz NOT NULL,
    CONSTRAINT renewal_terms_uniq UNIQUE (case_id, round_no)
);
CREATE TRIGGER renewal_terms_append_only BEFORE UPDATE OR DELETE ON renewal_terms
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE notices (
    id                    uuid PRIMARY KEY,
    tenancy_id            uuid        NOT NULL REFERENCES tenancies (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    issued_by             text        NOT NULL CHECK (issued_by IN ('OWNER','TENANT')),
    notice_type           text        NOT NULL CHECK (notice_type IN ('RENT_REMINDER','ARREARS','RENT_REVIEW','RENEWAL','TERMINATION','ENTRY','BREACH','GENERAL')),
    template_id           uuid        REFERENCES notice_templates (id),
    status                text        NOT NULL CHECK (status IN ('DRAFT','ISSUED','DELIVERED','ACKNOWLEDGED','DELIVERY_FAILED','DISPUTED')),
    summary               text        NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 2000),
    document_media_id     uuid,
    requested_effective_date date,
    effective_date        date,
    delivery_channel      text        NOT NULL CHECK (delivery_channel IN ('PLATFORM','EMAIL','POST','HAND_DELIVERY','COURIER')),
    issued_at             timestamptz,
    delivered_at          timestamptz,
    delivery_evidence_media_id uuid,
    delivery_ref          text        CHECK (char_length(delivery_ref) <= 200),
    acknowledged_at       timestamptz,
    disputed_at           timestamptz,
    dispute_reason        text        CHECK (char_length(dispute_reason) <= 1000),
    failure_reason        text        CHECK (char_length(failure_reason) <= 500),
    policy_version        text,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT notices_issued_chk CHECK (status = 'DRAFT' OR (issued_at IS NOT NULL AND effective_date IS NOT NULL))
);
CREATE INDEX notices_tenancy_idx ON notices (tenancy_id, created_at DESC);

CREATE TABLE move_out_cases (
    id                    uuid PRIMARY KEY,
    tenancy_id            uuid        NOT NULL UNIQUE REFERENCES tenancies (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    status                text        NOT NULL CHECK (status IN ('OPEN','INSPECTION_SCHEDULED','INSPECTED','SETTLEMENT_PENDING','COMPLETED')),
    initiated_by          text        NOT NULL CHECK (initiated_by IN ('OWNER','TENANT')),
    notice_id             uuid        REFERENCES notices (id),
    move_out_date         date        NOT NULL,
    inspection_at         timestamptz,
    inspected_at          timestamptz,
    keys_returned_at      timestamptz,
    owner_confirmed_at    timestamptz,
    return_to_market      boolean,
    completed_at          timestamptz,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL
);

-- 16 Utilities & service charges ------------------------------------------
CREATE TABLE utility_accounts (
    id                    uuid PRIMARY KEY,
    property_id           uuid        NOT NULL REFERENCES properties (id),
    unit_id               uuid        REFERENCES property_units (id),
    utility_type          text        NOT NULL CHECK (utility_type IN ('ELECTRICITY','WATER','GAS','INTERNET','WASTE','SECURITY','DIESEL_GENERATOR','OTHER')),
    provider_name         text        NOT NULL CHECK (char_length(provider_name) BETWEEN 1 AND 120),
    account_ref_last4     text        CHECK (account_ref_last4 ~ '^[A-Za-z0-9]{1,4}$'),
    meter_number          text        CHECK (char_length(meter_number) <= 64),
    meter_type            text        NOT NULL CHECK (meter_type IN ('PREPAID','POSTPAID','SHARED','NONE')),
    responsibility        text        NOT NULL CHECK (responsibility IN ('OWNER','TENANT','SHARED')),
    allocation_rule_version integer   NOT NULL DEFAULT 0,
    status                text        NOT NULL CHECK (status IN ('ACTIVE','CLOSED')),
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL
);
CREATE INDEX utility_accounts_property_idx ON utility_accounts (property_id);

CREATE TABLE utility_allocation_rules (
    id                    uuid PRIMARY KEY,
    utility_account_id    uuid        NOT NULL REFERENCES utility_accounts (id),
    version_no            integer     NOT NULL CHECK (version_no > 0),
    method                text        NOT NULL CHECK (method IN ('EQUAL','PERCENTAGE','FIXED','SUB_METER')),
    shares                jsonb       NOT NULL,
    actor_domain          text,
    actor_id              uuid,
    created_at            timestamptz NOT NULL,
    CONSTRAINT utility_allocation_rules_uniq UNIQUE (utility_account_id, version_no)
);
CREATE TRIGGER utility_allocation_rules_append_only BEFORE UPDATE OR DELETE ON utility_allocation_rules
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE meter_readings (
    id                    uuid PRIMARY KEY,
    utility_account_id    uuid        NOT NULL REFERENCES utility_accounts (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    tenancy_id            uuid        REFERENCES tenancies (id),
    reading_type          text        NOT NULL CHECK (reading_type IN ('MOVE_IN','PERIODIC','MOVE_OUT','CORRECTION')),
    value                 numeric(16,3) NOT NULL CHECK (value >= 0),
    unit                  text        NOT NULL CHECK (unit IN ('KWH','M3','LITRE','UNIT','OTHER')),
    read_at               timestamptz NOT NULL,
    media_id              uuid,
    corrects_id           uuid        REFERENCES meter_readings (id),
    recorded_by_party     text        NOT NULL CHECK (recorded_by_party IN ('OWNER','TENANT','MANAGER','PROVIDER')),
    actor_domain          text,
    actor_id              uuid,
    created_at            timestamptz NOT NULL,
    CONSTRAINT meter_readings_correction_chk CHECK ((reading_type = 'CORRECTION') = (corrects_id IS NOT NULL))
);
CREATE INDEX meter_readings_account_idx ON meter_readings (utility_account_id, read_at DESC);
CREATE TRIGGER meter_readings_append_only BEFORE UPDATE OR DELETE ON meter_readings
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE utility_bills (
    id                    uuid PRIMARY KEY,
    utility_account_id    uuid        NOT NULL REFERENCES utility_accounts (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    period_start          date        NOT NULL,
    period_end            date        NOT NULL,
    amount_minor          bigint      NOT NULL CHECK (amount_minor >= 0),
    currency              char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    bill_media_id         uuid,
    status                text        NOT NULL CHECK (status IN ('RECORDED','ALLOCATED','DISPUTED','RESOLVED')),
    allocation            jsonb,
    allocation_rule_version integer,
    original_allocation   jsonb,
    dispute_reason        text        CHECK (char_length(dispute_reason) <= 1000),
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT utility_bills_period_chk CHECK (period_end >= period_start),
    CONSTRAINT utility_bills_uniq UNIQUE (utility_account_id, period_start, period_end)
);

CREATE TABLE service_charge_budgets (
    id                    uuid PRIMARY KEY,
    property_id           uuid        NOT NULL REFERENCES properties (id),
    period_start          date        NOT NULL,
    period_end            date        NOT NULL,
    currency              char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status                text        NOT NULL CHECK (status IN ('DRAFT','APPROVED','CLOSED')),
    categories            jsonb       NOT NULL,
    total_expected_minor  bigint      NOT NULL CHECK (total_expected_minor >= 0),
    approved_at           timestamptz,
    closed_at             timestamptz,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT service_charge_budgets_period_chk CHECK (period_end > period_start),
    CONSTRAINT service_charge_budgets_uniq UNIQUE (property_id, period_start)
);

CREATE TABLE service_charge_items (
    id                    uuid PRIMARY KEY,
    budget_id             uuid        NOT NULL REFERENCES service_charge_budgets (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    category              text        NOT NULL CHECK (category ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    item_type             text        NOT NULL CHECK (item_type IN ('ACTUAL','BILLED','PAID','ADJUSTMENT')),
    amount_minor          bigint      NOT NULL,
    description           text        NOT NULL CHECK (char_length(description) BETWEEN 1 AND 500),
    evidence_media_ids    uuid[]      NOT NULL DEFAULT '{}',
    adjusts_id            uuid        REFERENCES service_charge_items (id),
    incurred_on           date        NOT NULL,
    actor_domain          text,
    actor_id              uuid,
    created_at            timestamptz NOT NULL,
    CONSTRAINT service_charge_items_amount_chk CHECK (item_type = 'ADJUSTMENT' OR amount_minor >= 0),
    CONSTRAINT service_charge_items_adjust_chk CHECK ((item_type = 'ADJUSTMENT') = (adjusts_id IS NOT NULL)),
    CONSTRAINT service_charge_items_media_chk CHECK (cardinality(evidence_media_ids) <= 10)
);
CREATE INDEX service_charge_items_budget_idx ON service_charge_items (budget_id, created_at);
CREATE TRIGGER service_charge_items_append_only BEFORE UPDATE OR DELETE ON service_charge_items
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS service_charge_items;
DROP TABLE IF EXISTS service_charge_budgets;
DROP TABLE IF EXISTS utility_bills;
DROP TABLE IF EXISTS meter_readings;
DROP TABLE IF EXISTS utility_allocation_rules;
DROP TABLE IF EXISTS utility_accounts;
DROP TABLE IF EXISTS move_out_cases;
DROP TABLE IF EXISTS notices;
DROP TABLE IF EXISTS renewal_terms;
ALTER TABLE agreements DROP CONSTRAINT IF EXISTS agreements_renewal_case_fk;
DROP TABLE IF EXISTS renewal_cases;
DROP TABLE IF EXISTS key_access_inventory;
DROP TABLE IF EXISTS condition_items;
DROP TABLE IF EXISTS move_in_checklists;
DROP TABLE IF EXISTS deposit_obligations;
DROP TABLE IF EXISTS rent_obligation_status_history;
DROP TABLE IF EXISTS rent_obligations;
ALTER TABLE agreements DROP COLUMN IF EXISTS tenancy_id;
DROP TABLE IF EXISTS tenancies;
DROP TABLE IF EXISTS signature_requests;
DROP TABLE IF EXISTS agreement_participants;
DROP TABLE IF EXISTS agreement_versions;
DROP TABLE IF EXISTS agreements;
DROP TABLE IF EXISTS notice_templates;
DROP TABLE IF EXISTS agreement_templates;
DROP FUNCTION IF EXISTS template_retire_only();
-- +goose StatementEnd