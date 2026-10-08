-- notification_db schema. Owned exclusively by notification-service.
-- System-to-user notifications only; human-to-human messaging is CF Meet.
-- Recipients are domain-aware references (recipient_domain, recipient_id);
-- this service never reads another domain's user tables.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE notification_templates (
    template_id          uuid PRIMARY KEY,
    template_key         text        NOT NULL CHECK (template_key ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    version              integer     NOT NULL CHECK (version > 0),
    locale               text        NOT NULL CHECK (locale ~ '^[a-z]{2}(-[A-Z]{2})?$'),
    category             text        NOT NULL CHECK (category IN ('SECURITY','ACCOUNT','TRANSACTIONAL','ACTIVITY')),
    channels             text[]      NOT NULL CHECK (cardinality(channels) > 0 AND channels <@ ARRAY['PUSH','EMAIL','SMS']),
    required_variables   text[]      NOT NULL DEFAULT '{}',
    push_title           text,
    push_body            text,
    email_subject        text,
    email_text           text,
    email_html           text,
    sms_text             text,
    ttl_seconds          integer     NOT NULL DEFAULT 86400 CHECK (ttl_seconds BETWEEN 60 AND 604800),
    status               text        NOT NULL CHECK (status IN ('ACTIVE','RETIRED')),
    created_by_service   text        NOT NULL,
    created_by_actor_id  uuid,
    created_at           timestamptz NOT NULL,
    CONSTRAINT notification_templates_version_uniq UNIQUE (template_key, locale, version),
    CONSTRAINT notification_templates_push CHECK (NOT ('PUSH' = ANY(channels)) OR (push_title IS NOT NULL AND push_body IS NOT NULL)),
    CONSTRAINT notification_templates_email CHECK (NOT ('EMAIL' = ANY(channels)) OR (email_subject IS NOT NULL AND email_text IS NOT NULL)),
    CONSTRAINT notification_templates_sms CHECK (NOT ('SMS' = ANY(channels)) OR sms_text IS NOT NULL)
);

CREATE TABLE notification_requests (
    notification_id      uuid PRIMARY KEY,
    recipient_domain     text        NOT NULL CHECK (recipient_domain IN ('marketplace','individual-owner','business-operations','property-manager','artisan','professional-services','admin')),
    recipient_id         uuid        NOT NULL,
    template_id          uuid        NOT NULL REFERENCES notification_templates (template_id),
    template_key         text        NOT NULL,
    template_version     integer     NOT NULL,
    locale               text        NOT NULL,
    category             text        NOT NULL,
    critical             boolean     NOT NULL,
    requested_by_service text        NOT NULL,
    requested_by_actor_domain text,
    requested_by_actor_id uuid,
    caller_reference     text CHECK (caller_reference IS NULL OR char_length(caller_reference) <= 128),
    suppressed           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    expires_at           timestamptz NOT NULL,
    created_at           timestamptz NOT NULL
);
CREATE INDEX notification_requests_recipient_idx ON notification_requests (recipient_domain, recipient_id, created_at DESC);

-- One delivery per channel destination (per push endpoint for PUSH).
CREATE TABLE notification_deliveries (
    delivery_id          uuid PRIMARY KEY,
    notification_id      uuid        NOT NULL REFERENCES notification_requests (notification_id),
    channel              text        NOT NULL CHECK (channel IN ('PUSH','EMAIL','SMS')),
    provider             text        NOT NULL,
    push_endpoint_id     uuid,
    destination          text        NOT NULL,     -- email address, E.164 phone or push endpoint reference
    status               text        NOT NULL CHECK (status IN ('QUEUED','DISPATCHING','SENT','RETRY_SCHEDULED','FAILED_FINAL')),
    attempts             integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts         integer     NOT NULL CHECK (max_attempts > 0),
    next_attempt_at      timestamptz NOT NULL,
    lease_owner          text,
    lease_expires_at     timestamptz,
    provider_message_id  text,
    last_error_code      text,
    -- Provider acceptance (SENT) is not user delivery/read; receipts are separate.
    receipt_status       text        NOT NULL DEFAULT 'NONE' CHECK (receipt_status IN ('NONE','DELIVERED','READ','BOUNCED')),
    receipt_at           timestamptz,
    subject              text,
    body                 text        NOT NULL,
    html_body            text,
    data                 jsonb,
    sent_at              timestamptz,
    failed_at            timestamptz,
    expires_at           timestamptz NOT NULL,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL,
    CONSTRAINT notification_deliveries_push_endpoint CHECK (channel <> 'PUSH' OR push_endpoint_id IS NOT NULL)
);
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (next_attempt_at) WHERE status IN ('QUEUED','RETRY_SCHEDULED','DISPATCHING');
CREATE INDEX notification_deliveries_request_idx ON notification_deliveries (notification_id);

CREATE TABLE delivery_attempts (
    attempt_id           uuid PRIMARY KEY,
    delivery_id          uuid        NOT NULL REFERENCES notification_deliveries (delivery_id),
    attempt_no           integer     NOT NULL CHECK (attempt_no > 0),
    provider             text        NOT NULL,
    outcome              text        NOT NULL CHECK (outcome IN ('ACCEPTED','TRANSIENT_ERROR','PERMANENT_ERROR','INVALID_DESTINATION','UNKNOWN')),
    error_code           text,
    provider_message_id  text,
    duration_ms          integer     NOT NULL CHECK (duration_ms >= 0),
    created_at           timestamptz NOT NULL,
    CONSTRAINT delivery_attempts_no_uniq UNIQUE (delivery_id, attempt_no)
);

-- Normalized provider response metadata (no message content, no credentials).
CREATE TABLE provider_responses (
    response_id          uuid PRIMARY KEY,
    attempt_id           uuid        NOT NULL REFERENCES delivery_attempts (attempt_id),
    provider             text        NOT NULL,
    http_status          integer,
    provider_code        text,
    retry_after_seconds  integer,
    created_at           timestamptz NOT NULL
);

CREATE TABLE push_endpoints (
    endpoint_id          uuid PRIMARY KEY,
    recipient_domain     text        NOT NULL,
    recipient_id         uuid        NOT NULL,
    provider             text        NOT NULL CHECK (provider IN ('FCM')),
    platform             text        NOT NULL CHECK (platform IN ('ANDROID','IOS','WEB')),
    token                text        NOT NULL UNIQUE CHECK (char_length(token) BETWEEN 16 AND 4096),
    device_id            text,
    app_version          text,
    status               text        NOT NULL CHECK (status IN ('ACTIVE','INVALID','REVOKED')),
    invalid_reason       text,
    last_seen_at         timestamptz NOT NULL,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL
);
CREATE INDEX push_endpoints_recipient_idx ON push_endpoints (recipient_domain, recipient_id) WHERE status = 'ACTIVE';

CREATE TABLE notification_preferences (
    recipient_domain     text        NOT NULL,
    recipient_id         uuid        NOT NULL,
    category             text        NOT NULL CHECK (category IN ('ACCOUNT','TRANSACTIONAL','ACTIVITY')),
    channel              text        NOT NULL CHECK (channel IN ('PUSH','EMAIL','SMS')),
    enabled              boolean     NOT NULL,
    updated_at           timestamptz NOT NULL,
    PRIMARY KEY (recipient_domain, recipient_id, category, channel)
);

CREATE TABLE recipient_settings (
    recipient_domain     text        NOT NULL,
    recipient_id         uuid        NOT NULL,
    quiet_start          time,
    quiet_end            time,
    timezone             text,
    updated_at           timestamptz NOT NULL,
    PRIMARY KEY (recipient_domain, recipient_id),
    CONSTRAINT recipient_settings_quiet CHECK ((quiet_start IS NULL) = (quiet_end IS NULL) AND (quiet_start IS NULL) = (timezone IS NULL))
);

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
DROP TABLE IF EXISTS recipient_settings;
DROP TABLE IF EXISTS notification_preferences;
DROP TABLE IF EXISTS push_endpoints;
DROP TABLE IF EXISTS provider_responses;
DROP TABLE IF EXISTS delivery_attempts;
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_requests;
DROP TABLE IF EXISTS notification_templates;
-- +goose StatementEnd
