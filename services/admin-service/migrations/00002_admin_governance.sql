-- +goose Up
-- Admin-owned platform governance: review cases (disputes, moderation,
-- financial remedy approvals, high-risk reviews, appeals) decided under
-- maker-checker with TOTP step-up, and account restrictions enforced through
-- the owning domain services. Business, Marketplace and partner ids are
-- references only; their records stay authoritative in their own services.

CREATE TABLE admin_mfa (
    account_id     uuid PRIMARY KEY REFERENCES domain_accounts(id),
    key_id         text        NOT NULL,
    sealed_secret  bytea       NOT NULL,
    confirmed_at   timestamptz,
    last_step      bigint      NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE review_cases (
    id                  uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    kind                text        NOT NULL CHECK (kind IN ('DISPUTE','MODERATION','FINANCIAL_REMEDY','HIGH_RISK','APPEAL')),
    source_service      text        NOT NULL CHECK (length(source_service) BETWEEN 3 AND 80),
    source_reference    text        NOT NULL CHECK (length(source_reference) BETWEEN 3 AND 200),
    organisation_id     uuid,
    business_record_id  uuid,
    business_version    bigint,
    parent_case_id      uuid        REFERENCES review_cases(id),
    subject_service     text,
    subject_account_id  uuid,
    title               text        NOT NULL CHECK (length(title) BETWEEN 3 AND 200),
    summary             text        NOT NULL DEFAULT '' CHECK (length(summary) <= 4000),
    amount_minor        bigint      CHECK (amount_minor IS NULL OR amount_minor > 0),
    currency            text        CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
    state               text        NOT NULL CHECK (state IN ('OPEN','IN_REVIEW','PROPOSED','DECIDED','WITHDRAWN')),
    assignee_id         uuid        REFERENCES domain_accounts(id),
    proposed_by         uuid        REFERENCES domain_accounts(id),
    proposed_outcome    text,
    proposed_rationale  text        CHECK (proposed_rationale IS NULL OR length(proposed_rationale) <= 4000),
    decided_by          uuid        REFERENCES domain_accounts(id),
    outcome             text,
    rationale           text        CHECK (rationale IS NULL OR length(rationale) <= 4000),
    decided_at          timestamptz,
    version             bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_service, source_reference, kind),
    -- Maker-checker: a decision needs a proposal by a different account.
    CHECK (state <> 'DECIDED' OR (decided_by IS NOT NULL AND proposed_by IS NOT NULL AND decided_by <> proposed_by AND outcome IS NOT NULL AND decided_at IS NOT NULL)),
    CHECK (state <> 'PROPOSED' OR (proposed_by IS NOT NULL AND proposed_outcome IS NOT NULL)),
    CHECK (kind <> 'FINANCIAL_REMEDY' OR (amount_minor IS NOT NULL AND currency IS NOT NULL)),
    CHECK (kind <> 'DISPUTE' OR (organisation_id IS NOT NULL AND business_record_id IS NOT NULL)),
    CHECK ((kind = 'APPEAL') = (parent_case_id IS NOT NULL))
);
CREATE UNIQUE INDEX review_cases_one_appeal ON review_cases (parent_case_id) WHERE kind = 'APPEAL';
CREATE INDEX review_cases_queue ON review_cases (state, kind, created_at);
CREATE TRIGGER review_cases_guard BEFORE UPDATE OR DELETE ON review_cases FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('OPEN>IN_REVIEW,IN_REVIEW>PROPOSED,PROPOSED>IN_REVIEW,PROPOSED>DECIDED,OPEN>WITHDRAWN,IN_REVIEW>WITHDRAWN,PROPOSED>WITHDRAWN');

CREATE TABLE case_notes (
    id             uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    case_id        uuid        NOT NULL REFERENCES review_cases(id),
    note           text        NOT NULL CHECK (length(note) BETWEEN 3 AND 4000),
    evidence_refs  uuid[]      NOT NULL DEFAULT '{}' CHECK (cardinality(evidence_refs) <= 20),
    created_by     uuid        NOT NULL REFERENCES domain_accounts(id),
    created_at     timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementBegin
CREATE FUNCTION case_notes_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION 'case_notes is append-only' USING ERRCODE='23514';
END;
$fn$;
-- +goose StatementEnd
CREATE TRIGGER case_notes_immutable BEFORE UPDATE OR DELETE ON case_notes FOR EACH ROW EXECUTE FUNCTION case_notes_append_only();

CREATE TABLE restrictions (
    id                  uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    case_id             uuid        NOT NULL REFERENCES review_cases(id),
    subject_service     text        NOT NULL CHECK (subject_service IN ('artisan-service','property-manager-service','professional-services-service')),
    subject_account_id  uuid        NOT NULL,
    reason              text        NOT NULL CHECK (length(reason) BETWEEN 5 AND 1000),
    state               text        NOT NULL CHECK (state IN ('PROPOSED','ACTIVE','REJECTED','LIFTED')),
    proposed_by         uuid        NOT NULL REFERENCES domain_accounts(id),
    approved_by         uuid        REFERENCES domain_accounts(id),
    lifted_by           uuid        REFERENCES domain_accounts(id),
    lift_reason         text,
    version             bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (approved_by IS NULL OR approved_by <> proposed_by),
    CHECK (state <> 'ACTIVE' OR approved_by IS NOT NULL)
);
CREATE UNIQUE INDEX restrictions_one_live ON restrictions (subject_service, subject_account_id) WHERE state IN ('PROPOSED','ACTIVE');
CREATE TRIGGER restrictions_guard BEFORE UPDATE OR DELETE ON restrictions FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('PROPOSED>ACTIVE,PROPOSED>REJECTED,ACTIVE>LIFTED');

-- +goose Down
SELECT 1;
