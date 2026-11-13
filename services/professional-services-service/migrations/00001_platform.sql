-- +goose Up
-- Standard domain-service platform schema (shared/go/domainkit). Each service
-- applies this to ITS OWN database; no table is shared across services.

CREATE TABLE domain_accounts (
    id             uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    email          text        NOT NULL UNIQUE CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
    password_hash  text        NOT NULL,
    display_name   text        NOT NULL CHECK (length(display_name) BETWEEN 2 AND 120),
    roles          text[]      NOT NULL DEFAULT '{}',
    status         text        NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','SUSPENDED')),
    version        bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth_sessions (
    id                 uuid PRIMARY KEY,
    account_id         uuid        NOT NULL REFERENCES domain_accounts(id),
    created_at         timestamptz NOT NULL,
    expires_at         timestamptz NOT NULL,
    last_refreshed_at  timestamptz NOT NULL,
    revoked_at         timestamptz,
    revoke_reason      text,
    user_agent         text
);
CREATE INDEX auth_sessions_account_idx ON auth_sessions (account_id) WHERE revoked_at IS NULL;

CREATE TABLE auth_refresh_tokens (
    id           uuid PRIMARY KEY,
    session_id   uuid        NOT NULL REFERENCES auth_sessions(id),
    token_hash   text        NOT NULL UNIQUE,
    issued_at    timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    consumed_at  timestamptz,
    replaced_by  uuid
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

CREATE TABLE processed_events (
    consumer      text        NOT NULL,
    event_id      uuid        NOT NULL,
    event_type    text        NOT NULL,
    processed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

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

-- +goose StatementBegin
CREATE FUNCTION audit_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$fn$;
-- +goose StatementEnd
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();

-- +goose StatementBegin
-- Governed records: state changes must follow the declared edges and the
-- version must advance by one; rows are never deleted.
CREATE FUNCTION domain_state_guard() RETURNS trigger LANGUAGE plpgsql AS $fn$
DECLARE edges text := ','||TG_ARGV[0]||',';
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION '% records are retained', TG_TABLE_NAME USING ERRCODE='23514'; END IF;
    IF NEW.id<>OLD.id OR NEW.created_at<>OLD.created_at THEN
        RAISE EXCEPTION '% identity is immutable', TG_TABLE_NAME USING ERRCODE='23514';
    END IF;
    IF NEW.version<>OLD.version+1 THEN RAISE EXCEPTION '% version must advance by one', TG_TABLE_NAME USING ERRCODE='23514'; END IF;
    IF NEW.state<>OLD.state AND position(','||OLD.state||'>'||NEW.state||',' IN edges)=0 THEN
        RAISE EXCEPTION 'Invalid % transition % -> %', TG_TABLE_NAME, OLD.state, NEW.state USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$fn$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
