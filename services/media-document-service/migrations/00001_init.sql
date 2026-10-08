-- media_document_db schema. Owned exclusively by media-document-service.
-- Raw bytes live in object storage; this database holds metadata, access and
-- lifecycle state. Owners in other domains are referenced by (domain, type, UUIDv7).

-- +goose Up
-- +goose StatementBegin
CREATE TABLE retention_rules (
    media_class                     text PRIMARY KEY CHECK (media_class IN ('IMAGE','VIDEO','DOCUMENT','VERIFICATION_DOCUMENT')),
    min_retention_days              integer     NOT NULL CHECK (min_retention_days >= 0),
    auto_delete_rejected_after_days integer     NOT NULL CHECK (auto_delete_rejected_after_days >= 0),
    updated_at                      timestamptz NOT NULL DEFAULT now()
);
-- Defaults: verification documents are kept for 5 years after acceptance
-- (AML/KYC record keeping); rejected uploads are purged after 7 days.
INSERT INTO retention_rules (media_class, min_retention_days, auto_delete_rejected_after_days) VALUES
    ('IMAGE', 0, 7), ('VIDEO', 0, 7), ('DOCUMENT', 0, 7), ('VERIFICATION_DOCUMENT', 1825, 7);

CREATE TABLE media_objects (
    media_id                uuid PRIMARY KEY,
    owner_domain            text        NOT NULL CHECK (owner_domain IN ('marketplace','individual-owner','business-operations','property-manager','artisan','professional-services','admin')),
    owner_type              text        NOT NULL CHECK (owner_type ~ '^[A-Z][A-Z0-9_]{1,39}$'),
    owner_id                uuid        NOT NULL,
    created_by_service      text        NOT NULL,
    created_by_actor_domain text,
    created_by_actor_id     uuid,
    media_class             text        NOT NULL REFERENCES retention_rules (media_class),
    filename                text        NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
    mime_type               text        NOT NULL,
    detected_mime_type      text,
    size_bytes              bigint      NOT NULL CHECK (size_bytes > 0),
    checksum_sha256         text        NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
    storage_key             text        NOT NULL UNIQUE,
    access_classification   text        NOT NULL CHECK (access_classification IN ('PUBLIC','PRIVATE','DOMAIN_RESTRICTED','ADMIN_RESTRICTED')),
    scan_status             text        NOT NULL CHECK (scan_status IN ('NOT_SCANNED','PENDING','CLEAN','INFECTED','FAILED')),
    status                  text        NOT NULL CHECK (status IN ('INITIATED','UPLOAD_PENDING','UPLOADED','SCANNING','READY','REJECTED','DELETE_PENDING','DELETED')),
    rejection_reason        text,
    retention_until         timestamptz,
    scan_attempts           integer     NOT NULL DEFAULT 0 CHECK (scan_attempts >= 0),
    next_work_at            timestamptz,
    lease_owner             text,
    lease_expires_at        timestamptz,
    uploaded_at             timestamptz,
    ready_at                timestamptz,
    rejected_at             timestamptz,
    deleted_at              timestamptz,
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL,
    CONSTRAINT media_ready_is_clean CHECK (status <> 'READY' OR scan_status = 'CLEAN'),
    CONSTRAINT media_actor_pair CHECK ((created_by_actor_domain IS NULL) = (created_by_actor_id IS NULL))
);
CREATE INDEX media_objects_owner_idx ON media_objects (owner_domain, owner_type, owner_id, created_at DESC);
CREATE INDEX media_objects_work_idx ON media_objects (next_work_at) WHERE status IN ('UPLOADED','SCANNING','DELETE_PENDING');
CREATE INDEX media_objects_rejected_idx ON media_objects (rejected_at) WHERE status = 'REJECTED';

CREATE TABLE documents (
    media_id       uuid PRIMARY KEY REFERENCES media_objects (media_id),
    document_type  text        NOT NULL CHECK (document_type ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    title          text        CHECK (title IS NULL OR char_length(title) <= 200),
    created_at     timestamptz NOT NULL
);

CREATE TABLE upload_sessions (
    upload_id      uuid PRIMARY KEY,
    media_id       uuid        NOT NULL REFERENCES media_objects (media_id),
    status         text        NOT NULL CHECK (status IN ('OPEN','COMPLETED','EXPIRED')),
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL,
    completed_at   timestamptz
);
CREATE UNIQUE INDEX upload_sessions_one_open ON upload_sessions (media_id) WHERE status = 'OPEN';
CREATE INDEX upload_sessions_expiry_idx ON upload_sessions (expires_at) WHERE status = 'OPEN';

-- Physical objects in the bucket (one per media today; copies keep lineage).
CREATE TABLE storage_objects (
    storage_key     text PRIMARY KEY,
    media_id        uuid        NOT NULL REFERENCES media_objects (media_id),
    bucket          text        NOT NULL,
    size_bytes      bigint      NOT NULL CHECK (size_bytes >= 0),
    etag            text,
    created_at      timestamptz NOT NULL,
    deleted_at      timestamptz
);

-- Explicit read grants in addition to classification rules.
CREATE TABLE access_policies (
    policy_id            uuid PRIMARY KEY,
    media_id             uuid        NOT NULL REFERENCES media_objects (media_id),
    grantee_type         text        NOT NULL CHECK (grantee_type IN ('SERVICE','ACTOR')),
    grantee_service      text,
    grantee_domain       text,
    grantee_actor_id     uuid,
    permission           text        NOT NULL CHECK (permission IN ('READ')),
    granted_by_service   text        NOT NULL,
    granted_by_actor_domain text,
    granted_by_actor_id  uuid,
    created_at           timestamptz NOT NULL,
    revoked_at           timestamptz,
    CONSTRAINT access_policies_grantee CHECK (
        (grantee_type = 'SERVICE' AND grantee_service IS NOT NULL AND grantee_actor_id IS NULL) OR
        (grantee_type = 'ACTOR' AND grantee_domain IS NOT NULL AND grantee_actor_id IS NOT NULL))
);
CREATE UNIQUE INDEX access_policies_active_uniq ON access_policies (media_id, grantee_type, coalesce(grantee_service,''), coalesce(grantee_domain,''), coalesce(grantee_actor_id::text,''))
    WHERE revoked_at IS NULL;

CREATE TABLE scan_results (
    scan_id             uuid PRIMARY KEY,
    media_id            uuid        NOT NULL REFERENCES media_objects (media_id),
    scanner             text        NOT NULL,
    verdict             text        NOT NULL CHECK (verdict IN ('CLEAN','INFECTED','REJECTED','ERROR')),
    reason              text,
    signature           text,
    detected_mime_type  text,
    computed_sha256     text,
    computed_size       bigint,
    duration_ms         integer     NOT NULL CHECK (duration_ms >= 0),
    scanned_at          timestamptz NOT NULL
);
CREATE INDEX scan_results_media_idx ON scan_results (media_id, scanned_at);

-- Every issued download URL (who, why, until when).
CREATE TABLE download_grants (
    grant_id          uuid PRIMARY KEY,
    media_id          uuid        NOT NULL REFERENCES media_objects (media_id),
    grantee_service   text        NOT NULL,
    grantee_domain    text,
    grantee_actor_id  uuid,
    purpose           text,
    expires_at        timestamptz NOT NULL,
    created_at        timestamptz NOT NULL
);
CREATE INDEX download_grants_media_idx ON download_grants (media_id, created_at);

CREATE TABLE deletion_requests (
    request_id              uuid PRIMARY KEY,
    media_id                uuid        NOT NULL REFERENCES media_objects (media_id),
    status                  text        NOT NULL CHECK (status IN ('PENDING','COMPLETED')),
    reason                  text        NOT NULL,
    requested_by_service    text        NOT NULL,
    requested_by_actor_domain text,
    requested_by_actor_id   uuid,
    created_at              timestamptz NOT NULL,
    completed_at            timestamptz
);
CREATE UNIQUE INDEX deletion_requests_one_pending ON deletion_requests (media_id) WHERE status = 'PENDING';

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
DROP TABLE IF EXISTS deletion_requests;
DROP TABLE IF EXISTS download_grants;
DROP TABLE IF EXISTS scan_results;
DROP TABLE IF EXISTS access_policies;
DROP TABLE IF EXISTS storage_objects;
DROP TABLE IF EXISTS upload_sessions;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS media_objects;
DROP TABLE IF EXISTS retention_rules;
-- +goose StatementEnd
