-- Section 6 contexts 25–27: owner finance reports and market intelligence
-- (§13.25), fraud/privacy/safety/disputes (§13.26) and privacy rights
-- (§13.27).
--
-- Disputes are owner-side cases linked to the admin operations workflow;
-- evidence is append-only. Consents are append-only decisions (the latest
-- per purpose is current). Report artifacts are PDFs stored in
-- media-document-service; only their media IDs and key figures live here.
-- Market insights are advisory and always carry model, version, input
-- provenance and confidence.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE domain_disputes (
    id                     uuid PRIMARY KEY,
    account_id             uuid        NOT NULL REFERENCES owner_accounts (account_id),
    property_id            uuid        REFERENCES properties (id),
    target_type            text        NOT NULL CHECK (target_type IN ('property','ownership_record','listing','manager_assignment','tenancy','rent_obligation',
                               'deposit_obligation','agreement','sale_transaction','shortlet_booking','damage_claim','maintenance_request',
                               'professional_appointment','payout_account','account')),
    target_id              uuid        NOT NULL,
    category               text        NOT NULL CHECK (category IN ('UNAUTHORIZED_MANAGER','FORGED_DOCUMENT','FALSE_OWNERSHIP','DUPLICATE_PROPERTY',
                               'PAYMENT_DIVERSION','ABUSE','PRIVACY','SERVICE_QUALITY','BILLING','SAFETY','OTHER')),
    status                 text        NOT NULL CHECK (status IN ('OPEN','UNDER_REVIEW','AWAITING_INFORMATION','RESOLVED','REJECTED','WITHDRAWN')),
    summary                text        NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 4000),
    desired_outcome        text        CHECK (char_length(desired_outcome) <= 1000),
    urgent                 boolean     NOT NULL DEFAULT false,
    admin_case_ref         text        CHECK (char_length(admin_case_ref) <= 128),
    information_request    text        CHECK (char_length(information_request) <= 2000),
    resolution_code        text        CHECK (resolution_code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    resolution_note        text        CHECK (char_length(resolution_note) <= 2000),
    resolved_at            timestamptz,
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL
);
CREATE INDEX domain_disputes_account_idx ON domain_disputes (account_id, created_at DESC);
CREATE INDEX domain_disputes_status_idx ON domain_disputes (status, created_at) WHERE status IN ('OPEN','UNDER_REVIEW','AWAITING_INFORMATION');
CREATE UNIQUE INDEX domain_disputes_open_uniq ON domain_disputes (account_id, target_type, target_id, category)
    WHERE status IN ('OPEN','UNDER_REVIEW','AWAITING_INFORMATION');

CREATE TABLE dispute_evidence (
    id                     uuid PRIMARY KEY,
    dispute_id             uuid        NOT NULL REFERENCES domain_disputes (id),
    media_id               uuid,
    note                   text        CHECK (char_length(note) <= 2000),
    submitted_by           text        NOT NULL CHECK (submitted_by IN ('OWNER','ADMIN')),
    created_at             timestamptz NOT NULL,
    CONSTRAINT dispute_evidence_content_chk CHECK (media_id IS NOT NULL OR note IS NOT NULL)
);
CREATE INDEX dispute_evidence_dispute_idx ON dispute_evidence (dispute_id, created_at);
CREATE TRIGGER dispute_evidence_append_only BEFORE UPDATE OR DELETE ON dispute_evidence
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- Privacy consents: purpose/version/expiry decisions, append-only.
CREATE TABLE consents (
    id                     uuid PRIMARY KEY,
    account_id             uuid        NOT NULL REFERENCES owner_accounts (account_id),
    purpose                text        NOT NULL CHECK (purpose IN ('MARKETING_EMAIL','MARKETING_SMS','MARKETING_PUSH','PRODUCT_ANALYTICS','AI_INSIGHTS',
                               'PARTNER_FINANCING','PARTNER_INSURANCE','PUBLIC_OWNER_PROFILE')),
    decision               text        NOT NULL CHECK (decision IN ('GRANTED','WITHDRAWN')),
    policy_version         text        NOT NULL CHECK (char_length(policy_version) BETWEEN 1 AND 40),
    expires_at             timestamptz,
    source                 text        NOT NULL CHECK (source IN ('OWNER','ASSISTED_SUPPORT')),
    actor_id               uuid,
    created_at             timestamptz NOT NULL
);
CREATE INDEX consents_account_idx ON consents (account_id, purpose, created_at DESC);
CREATE TRIGGER consents_append_only BEFORE UPDATE OR DELETE ON consents
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE report_snapshots (
    id                     uuid PRIMARY KEY,
    account_id             uuid        NOT NULL REFERENCES owner_accounts (account_id),
    report_type            text        NOT NULL CHECK (report_type IN ('PORTFOLIO','FINANCIAL','OCCUPANCY','SHORTLET','MAINTENANCE','PRIVACY_EXPORT')),
    period_start           date,
    period_end             date,
    property_id            uuid        REFERENCES properties (id),
    status                 text        NOT NULL CHECK (status IN ('QUEUED','READY','FAILED')),
    summary                jsonb,
    media_id               uuid,
    failure_reason         text        CHECK (char_length(failure_reason) <= 500),
    attempts               integer     NOT NULL DEFAULT 0,
    data_subject_request_id uuid,
    created_at             timestamptz NOT NULL,
    completed_at           timestamptz,
    CONSTRAINT report_snapshots_ready_chk CHECK (status <> 'READY' OR (media_id IS NOT NULL AND summary IS NOT NULL))
);
CREATE INDEX report_snapshots_account_idx ON report_snapshots (account_id, created_at DESC);
CREATE INDEX report_snapshots_queue_idx ON report_snapshots (created_at) WHERE status = 'QUEUED';

CREATE TABLE data_subject_requests (
    id                     uuid PRIMARY KEY,
    account_id             uuid        NOT NULL REFERENCES owner_accounts (account_id),
    request_type           text        NOT NULL CHECK (request_type IN ('ACCESS','EXPORT','CORRECTION','DELETION','OBJECTION','RESTRICTION')),
    status                 text        NOT NULL CHECK (status IN ('SUBMITTED','VERIFIED_REQUESTER','PROCESSING','PARTIALLY_FULFILLED','FULFILLED',
                               'DENIED_WITH_REASON','CLOSED')),
    details                text        CHECK (char_length(details) <= 4000),
    verified_at            timestamptz,
    due_at                 timestamptz NOT NULL,
    report_id              uuid        REFERENCES report_snapshots (id),
    decision_reason        text        CHECK (char_length(decision_reason) <= 2000),
    retained_categories    text[]      NOT NULL DEFAULT '{}',
    decided_at             timestamptz,
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL
);
CREATE INDEX data_subject_requests_account_idx ON data_subject_requests (account_id, created_at DESC);
CREATE INDEX data_subject_requests_open_idx ON data_subject_requests (status, due_at) WHERE status NOT IN ('FULFILLED','DENIED_WITH_REASON','CLOSED');
CREATE UNIQUE INDEX data_subject_requests_open_uniq ON data_subject_requests (account_id, request_type)
    WHERE status IN ('SUBMITTED','VERIFIED_REQUESTER','PROCESSING');
ALTER TABLE report_snapshots ADD CONSTRAINT report_snapshots_dsr_fk FOREIGN KEY (data_subject_request_id) REFERENCES data_subject_requests (id);

CREATE TABLE market_insights (
    id                     uuid PRIMARY KEY,
    account_id             uuid        NOT NULL REFERENCES owner_accounts (account_id),
    property_id            uuid        NOT NULL REFERENCES properties (id),
    listing_id             uuid        REFERENCES listings (id),
    insight_type           text        NOT NULL CHECK (insight_type IN ('PRICE_RECOMMENDATION','MARKET_TREND','VACANCY_RISK')),
    model_name             text        NOT NULL,
    model_version          text        NOT NULL,
    inputs                 jsonb       NOT NULL,
    result                 jsonb       NOT NULL,
    confidence             numeric(4,3) NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    advisory               boolean     NOT NULL DEFAULT true CHECK (advisory),
    generated_at           timestamptz NOT NULL,
    expires_at             timestamptz NOT NULL
);
CREATE INDEX market_insights_lookup_idx ON market_insights (property_id, insight_type, generated_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS market_insights;
ALTER TABLE report_snapshots DROP CONSTRAINT IF EXISTS report_snapshots_dsr_fk;
DROP TABLE IF EXISTS data_subject_requests;
DROP TABLE IF EXISTS report_snapshots;
DROP TABLE IF EXISTS consents;
DROP TABLE IF EXISTS dispute_evidence;
DROP TABLE IF EXISTS domain_disputes;
-- +goose StatementEnd
