-- verification_db schema. Owned exclusively by verification-service.
-- No foreign keys to any other service's database; cross-service entities are
-- referenced by (domain, UUIDv7) only.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE verification_cases (
    verification_id             uuid PRIMARY KEY,
    verification_type           text        NOT NULL CHECK (verification_type IN ('IDENTITY','BUSINESS','PROPERTY_AUTHORITY','PROFESSIONAL_CREDENTIAL')),
    subject_type                text        NOT NULL CHECK (subject_type IN ('USER','BUSINESS','PROPERTY','PROFESSIONAL')),
    subject_domain              text        NOT NULL CHECK (subject_domain IN ('marketplace','individual-owner','business-operations','property-manager','artisan','professional-services','admin')),
    subject_id                  uuid        NOT NULL,
    status                      text        NOT NULL CHECK (status IN ('DRAFT','SUBMITTED','PROCESSING','MANUAL_REVIEW','VERIFIED','REJECTED','EXPIRED')),
    processing_substatus        text        CHECK (processing_substatus IN ('AWAITING_PROVIDER','PROVIDER_SUBMITTED','UNKNOWN_PROVIDER_RESULT')),
    provider                    text        NOT NULL,
    requested_by_service        text        NOT NULL,
    requested_by_actor_domain   text,
    requested_by_actor_id       uuid,
    organization_id             uuid,
    supersedes_verification_id  uuid REFERENCES verification_cases (verification_id),
    caller_reference            text,
    -- Non-sensitive provider options (string map). Never personal data.
    provider_metadata           jsonb       NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(provider_metadata) = 'object'),
    reason_code                 text,
    submitted_at                timestamptz,
    decided_at                  timestamptz,
    expires_at                  timestamptz,
    expired_at                  timestamptz,
    created_at                  timestamptz NOT NULL,
    updated_at                  timestamptz NOT NULL,
    CONSTRAINT verification_cases_substatus_only_processing CHECK (processing_substatus IS NULL OR status = 'PROCESSING'),
    CONSTRAINT verification_cases_actor_pair CHECK ((requested_by_actor_domain IS NULL) = (requested_by_actor_id IS NULL)),
    CONSTRAINT verification_cases_verified_has_expiry CHECK (status <> 'VERIFIED' OR expires_at IS NOT NULL),
    CONSTRAINT verification_cases_caller_ref_len CHECK (caller_reference IS NULL OR char_length(caller_reference) <= 128)
);
-- At most one open case per subject and type: duplicates are rejected by the database.
CREATE UNIQUE INDEX verification_cases_open_uniq ON verification_cases (verification_type, subject_type, subject_domain, subject_id)
    WHERE status IN ('DRAFT','SUBMITTED','PROCESSING','MANUAL_REVIEW');
CREATE INDEX verification_cases_subject_idx ON verification_cases (subject_domain, subject_type, subject_id, created_at DESC, verification_id DESC);
CREATE INDEX verification_cases_expiry_idx ON verification_cases (expires_at) WHERE status = 'VERIFIED';
CREATE INDEX verification_cases_status_idx ON verification_cases (status, updated_at);

CREATE TABLE verification_evidence_refs (
    evidence_id           uuid PRIMARY KEY,
    verification_id       uuid        NOT NULL REFERENCES verification_cases (verification_id),
    media_id              uuid        NOT NULL,           -- media-document-service reference (no cross-DB FK)
    evidence_type         text        NOT NULL CHECK (evidence_type ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    media_checksum_sha256 text,
    attached_by_service   text        NOT NULL,
    created_at            timestamptz NOT NULL,
    CONSTRAINT verification_evidence_unique UNIQUE (verification_id, media_id)
);

-- One processing attempt against a provider; also the leased work item for the dispatcher.
CREATE TABLE verification_attempts (
    attempt_id          uuid PRIMARY KEY,
    verification_id     uuid        NOT NULL REFERENCES verification_cases (verification_id),
    attempt_no          integer     NOT NULL CHECK (attempt_no > 0),
    provider            text        NOT NULL,
    client_reference    text        NOT NULL UNIQUE,      -- idempotency reference sent to the provider
    provider_reference  text,
    state               text        NOT NULL CHECK (state IN ('QUEUED','SUBMITTING','SUBMITTED','UNKNOWN','COMPLETED','FAILED')),
    tries               integer     NOT NULL DEFAULT 0 CHECK (tries >= 0),
    next_attempt_at     timestamptz NOT NULL,
    lease_owner         text,
    lease_expires_at    timestamptz,
    last_error_code     text,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    completed_at        timestamptz,
    CONSTRAINT verification_attempts_no_uniq UNIQUE (verification_id, attempt_no)
);
CREATE INDEX verification_attempts_due_idx ON verification_attempts (next_attempt_at) WHERE state IN ('QUEUED','SUBMITTING','SUBMITTED','UNKNOWN');
CREATE INDEX verification_attempts_provider_ref_idx ON verification_attempts (provider, provider_reference) WHERE provider_reference IS NOT NULL;

-- Immutable log of outbound provider calls (no personal data, no raw payloads).
CREATE TABLE provider_requests (
    provider_request_id  uuid PRIMARY KEY,
    attempt_id           uuid        NOT NULL REFERENCES verification_attempts (attempt_id),
    verification_id      uuid        NOT NULL REFERENCES verification_cases (verification_id),
    provider             text        NOT NULL,
    operation            text        NOT NULL CHECK (operation IN ('SUBMIT','READ_STATUS','FETCH_RESULT')),
    outcome              text        NOT NULL CHECK (outcome IN ('ACCEPTED','RESULT','PENDING','NOT_FOUND','TIMEOUT','ERROR_RETRYABLE','ERROR_PERMANENT')),
    error_code           text,
    duration_ms          integer     NOT NULL CHECK (duration_ms >= 0),
    created_at           timestamptz NOT NULL
);
CREATE INDEX provider_requests_attempt_idx ON provider_requests (attempt_id, created_at);

-- Provider outcomes from callbacks, read-backs or synchronous responses. The
-- (provider, provider_event_id) key makes duplicate callbacks harmless.
CREATE TABLE provider_results (
    provider_result_id   uuid PRIMARY KEY,
    verification_id      uuid        NOT NULL REFERENCES verification_cases (verification_id),
    attempt_id           uuid        NOT NULL REFERENCES verification_attempts (attempt_id),
    provider             text        NOT NULL,
    provider_event_id    text        NOT NULL,
    outcome              text        NOT NULL CHECK (outcome IN ('VERIFIED','REJECTED','INCONCLUSIVE','PENDING','RESULT_AVAILABLE')),
    reason_code          text,
    received_via         text        NOT NULL CHECK (received_via IN ('CALLBACK','READ_BACK','SYNC')),
    payload_sha256       text,
    applied              boolean     NOT NULL DEFAULT false,
    received_at          timestamptz NOT NULL,
    CONSTRAINT provider_results_event_uniq UNIQUE (provider, provider_event_id)
);

CREATE TABLE manual_reviews (
    review_id                uuid PRIMARY KEY,
    verification_id          uuid        NOT NULL REFERENCES verification_cases (verification_id),
    status                   text        NOT NULL CHECK (status IN ('OPEN','ASSIGNED','DECIDED')),
    reason                   text        NOT NULL CHECK (reason IN ('PROVIDER_INCONCLUSIVE','PROVIDER_FAILURE','PROVIDER_REQUESTED')),
    assigned_admin_id        uuid,
    assigned_at              timestamptz,
    decided_at               timestamptz,
    created_at               timestamptz NOT NULL,
    updated_at               timestamptz NOT NULL,
    CONSTRAINT manual_reviews_assignment CHECK (status = 'OPEN' OR assigned_admin_id IS NOT NULL OR status = 'DECIDED')
);
CREATE UNIQUE INDEX manual_reviews_one_active ON manual_reviews (verification_id) WHERE status <> 'DECIDED';
CREATE INDEX manual_reviews_queue_idx ON manual_reviews (status, created_at, review_id);

-- Final decisions. One terminal decision per case.
CREATE TABLE verification_decisions (
    decision_id              uuid PRIMARY KEY,
    verification_id          uuid        NOT NULL UNIQUE REFERENCES verification_cases (verification_id),
    decision                 text        NOT NULL CHECK (decision IN ('VERIFIED','REJECTED')),
    decision_source          text        NOT NULL CHECK (decision_source IN ('PROVIDER','MANUAL_REVIEW')),
    reason_code              text,
    notes                    text CHECK (notes IS NULL OR char_length(notes) <= 1000),
    provider                 text,
    provider_reference       text,
    decided_by_service       text        NOT NULL,
    decided_by_actor_domain  text,
    decided_by_actor_id      uuid,
    expires_at               timestamptz,
    created_at               timestamptz NOT NULL
);

-- Transition history (every state change).
CREATE TABLE verification_history (
    history_id         uuid PRIMARY KEY,
    verification_id    uuid        NOT NULL REFERENCES verification_cases (verification_id),
    from_status        text,
    to_status          text        NOT NULL,
    substatus          text,
    decision_source    text,
    reason_code        text,
    calling_service    text,
    actor_domain       text,
    actor_id           uuid,
    provider           text,
    provider_reference text,
    request_id         text,
    trace_id           text,
    occurred_at        timestamptz NOT NULL
);
CREATE INDEX verification_history_case_idx ON verification_history (verification_id, occurred_at);

CREATE TABLE idempotency_records (
    id               uuid PRIMARY KEY,
    scope            text        NOT NULL,
    caller_type      text        NOT NULL,
    caller_id        text        NOT NULL,
    idempotency_key  text        NOT NULL,
    request_hash     text        NOT NULL,
    status           text        NOT NULL CHECK (status IN ('IN_PROGRESS','COMPLETED','FAILED_RETRYABLE','FAILED_FINAL')),
    response_code    integer,
    response_body    bytea,
    resource_type    text,
    resource_id      text,
    attempts         integer     NOT NULL DEFAULT 1 CHECK (attempts > 0),
    locked_until     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    CONSTRAINT idempotency_records_key_uniq UNIQUE (scope, caller_type, caller_id, idempotency_key),
    CONSTRAINT idempotency_records_key_len CHECK (char_length(idempotency_key) BETWEEN 1 AND 255)
);
CREATE INDEX idempotency_records_expires_idx ON idempotency_records (expires_at);

CREATE TABLE outbox_events (
    event_id          uuid PRIMARY KEY,
    event_type        text        NOT NULL,
    producer          text        NOT NULL,
    aggregate_type    text        NOT NULL,
    aggregate_id      uuid        NOT NULL,
    subject           text        NOT NULL,
    trace_id          text,
    actor_domain      text,
    actor_id          uuid,
    metadata          jsonb,
    payload           jsonb       NOT NULL,
    occurred_at       timestamptz NOT NULL,
    attempts          integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at   timestamptz NOT NULL DEFAULT now(),
    lease_owner       text,
    lease_expires_at  timestamptz,
    published_at      timestamptz,
    dead_at           timestamptz,
    last_error        text,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outbox_events_pending_idx ON outbox_events (next_attempt_at, occurred_at)
    WHERE published_at IS NULL AND dead_at IS NULL;
CREATE INDEX outbox_events_aggregate_idx ON outbox_events (aggregate_type, aggregate_id, occurred_at);

CREATE TABLE audit_events (
    audit_id         uuid PRIMARY KEY,
    occurred_at      timestamptz NOT NULL,
    service          text        NOT NULL,
    action           text        NOT NULL,
    actor_type       text        NOT NULL CHECK (actor_type IN ('USER','SERVICE','SYSTEM','PROVIDER')),
    actor_domain     text,
    actor_id         uuid,
    calling_service  text,
    target_type      text        NOT NULL,
    target_id        uuid        NOT NULL,
    request_id       text,
    trace_id         text,
    result           text        NOT NULL CHECK (result IN ('SUCCESS','DENIED','FAILURE')),
    changes          jsonb,
    metadata         jsonb
);
CREATE INDEX audit_events_target_idx ON audit_events (target_type, target_id, occurred_at);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_domain, actor_id, occurred_at) WHERE actor_id IS NOT NULL;

CREATE FUNCTION audit_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$fn$;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS audit_events;
DROP FUNCTION IF EXISTS audit_events_append_only();
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS verification_history;
DROP TABLE IF EXISTS verification_decisions;
DROP TABLE IF EXISTS manual_reviews;
DROP TABLE IF EXISTS provider_results;
DROP TABLE IF EXISTS provider_requests;
DROP TABLE IF EXISTS verification_attempts;
DROP TABLE IF EXISTS verification_evidence_refs;
DROP TABLE IF EXISTS verification_cases;
-- +goose StatementEnd
