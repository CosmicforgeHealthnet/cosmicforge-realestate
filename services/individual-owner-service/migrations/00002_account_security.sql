-- Section 6 bounded context 01 — account and security (Section 5).
-- Domain-owned authentication persistence: there is no central identity
-- service; shared/go supplies primitives only (AUTHENTICATION_MODEL.md).
--
-- Secrets at rest: passwords are Argon2id PHC strings; refresh tokens,
-- one-time codes, MFA login tokens, device keys and recovery codes are stored
-- only as hashes; TOTP seeds are AES-256-GCM ciphertext. No raw biometric,
-- KYC document or provider secret is stored here.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE owner_accounts (
    account_id            uuid PRIMARY KEY,
    status                text        NOT NULL CHECK (status IN
                              ('PENDING_VERIFICATION','ACTIVE','RESTRICTED','SUSPENDED','CLOSURE_PENDING','CLOSED')),
    email                 text        NOT NULL CHECK (email = lower(email) AND char_length(email) BETWEEN 3 AND 254),
    email_verified_at     timestamptz,
    phone_e164            text        CHECK (phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
    phone_verified_at     timestamptz,
    display_name          text        NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 120),
    locale                text        NOT NULL DEFAULT 'en' CHECK (locale ~ '^[a-z]{2}(-[A-Z]{2})?$'),
    password_hash         text        NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    password_changed_at   timestamptz NOT NULL,
    failed_login_count    integer     NOT NULL DEFAULT 0 CHECK (failed_login_count >= 0),
    locked_until          timestamptz,
    mfa_enabled           boolean     NOT NULL DEFAULT false,
    status_reason         text,
    status_changed_at     timestamptz NOT NULL,
    closure_requested_at  timestamptz,
    closure_effective_at  timestamptz,
    closed_at             timestamptz,
    version               bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT owner_accounts_phone_verified_chk CHECK (phone_verified_at IS NULL OR phone_e164 IS NOT NULL),
    CONSTRAINT owner_accounts_closure_chk CHECK (status <> 'CLOSURE_PENDING' OR (closure_requested_at IS NOT NULL AND closure_effective_at IS NOT NULL)),
    CONSTRAINT owner_accounts_closed_chk CHECK ((status = 'CLOSED') = (closed_at IS NOT NULL))
);
-- A login identifier may belong to at most one open account. Closed accounts
-- release their identifiers (cross-domain accounts are never linked by email).
CREATE UNIQUE INDEX owner_accounts_email_uniq ON owner_accounts (email) WHERE status <> 'CLOSED';
CREATE UNIQUE INDEX owner_accounts_verified_phone_uniq ON owner_accounts (phone_e164)
    WHERE phone_verified_at IS NOT NULL AND status <> 'CLOSED';
CREATE INDEX owner_accounts_closure_due_idx ON owner_accounts (closure_effective_at) WHERE status = 'CLOSURE_PENDING';

-- Every lifecycle transition is preserved (Section 7 lifecycle 1/21).
CREATE TABLE account_status_history (
    history_id            uuid PRIMARY KEY,
    account_id            uuid        NOT NULL REFERENCES owner_accounts (account_id),
    from_status           text,
    to_status             text        NOT NULL,
    reason_code           text        NOT NULL CHECK (reason_code ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    actor_type            text        NOT NULL CHECK (actor_type IN ('USER','SERVICE','SYSTEM')),
    actor_domain          text,
    actor_id              uuid,
    calling_service       text,
    resolution_reference  text        CHECK (char_length(resolution_reference) <= 128),
    note                  text        CHECK (char_length(note) <= 1000),
    account_version       bigint      NOT NULL,
    occurred_at           timestamptz NOT NULL
);
CREATE INDEX account_status_history_account_idx ON account_status_history (account_id, occurred_at);

CREATE TABLE auth_devices (
    device_id        uuid PRIMARY KEY,
    account_id       uuid        NOT NULL REFERENCES owner_accounts (account_id),
    device_key_hash  text        NOT NULL CHECK (char_length(device_key_hash) = 64),
    name             text        CHECK (char_length(name) <= 120),
    platform         text        NOT NULL CHECK (platform IN ('IOS','ANDROID','WEB','OTHER')),
    user_agent       text        CHECK (char_length(user_agent) <= 512),
    last_ip          inet,
    first_seen_at    timestamptz NOT NULL,
    last_seen_at     timestamptz NOT NULL,
    trusted_at       timestamptz,
    revoked_at       timestamptz,
    CONSTRAINT auth_devices_key_uniq UNIQUE (account_id, device_key_hash)
);

-- Refresh-session token families (shared/go/sessions schema_reference.sql).
CREATE TABLE auth_sessions (
    id                 uuid PRIMARY KEY,
    account_id         uuid        NOT NULL REFERENCES owner_accounts (account_id),
    created_at         timestamptz NOT NULL,
    expires_at         timestamptz NOT NULL,
    last_refreshed_at  timestamptz NOT NULL,
    revoked_at         timestamptz,
    revoke_reason      text,
    device_id          uuid        REFERENCES auth_devices (device_id),
    device_name        text,
    device_platform    text,
    user_agent         text,
    ip_address         inet,
    auth_method        text        NOT NULL CHECK (auth_method IN ('PASSWORD','PHONE_OTP','OAUTH')),
    mfa_method         text        CHECK (mfa_method IN ('TOTP','RECOVERY_CODE','SMS','EMAIL')),
    authenticated_at   timestamptz NOT NULL,
    step_up_at         timestamptz,
    step_up_method     text        CHECK (step_up_method IN ('PASSWORD','TOTP','RECOVERY_CODE','SMS','EMAIL')),
    CONSTRAINT auth_sessions_expiry_chk CHECK (expires_at > created_at)
);
CREATE INDEX auth_sessions_account_idx ON auth_sessions (account_id) WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_device_idx ON auth_sessions (device_id) WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_expires_idx ON auth_sessions (expires_at);

CREATE TABLE auth_refresh_tokens (
    id           uuid PRIMARY KEY,
    session_id   uuid        NOT NULL REFERENCES auth_sessions (id) ON DELETE CASCADE,
    token_hash   text        NOT NULL UNIQUE CHECK (char_length(token_hash) = 64),
    issued_at    timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    consumed_at  timestamptz,
    replaced_by  uuid
);
CREATE INDEX auth_refresh_tokens_session_idx ON auth_refresh_tokens (session_id);

-- One-time secrets: verification/reset/OTP codes and MFA login tokens.
-- secret_hash is an HMAC-SHA256 keyed with a server-side pepper so a database
-- leak does not permit offline guessing of 6-digit codes.
CREATE TABLE auth_challenges (
    challenge_id     uuid PRIMARY KEY,
    account_id       uuid        NOT NULL REFERENCES owner_accounts (account_id),
    session_id       uuid        REFERENCES auth_sessions (id) ON DELETE CASCADE,
    parent_id        uuid        REFERENCES auth_challenges (challenge_id) ON DELETE CASCADE,
    purpose          text        NOT NULL CHECK (purpose IN ('EMAIL_VERIFICATION','PHONE_VERIFICATION','EMAIL_CHANGE',
                         'PHONE_CHANGE','PASSWORD_RESET','LOGIN_OTP','LOGIN_MFA','MFA_CODE','STEP_UP_CODE')),
    channel          text        NOT NULL CHECK (channel IN ('EMAIL','SMS','NONE')),
    destination      text        CHECK (char_length(destination) <= 254),
    secret_hash      text        NOT NULL CHECK (char_length(secret_hash) = 64),
    attempts         integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts     integer     NOT NULL CHECK (max_attempts BETWEEN 1 AND 10),
    delivery_status  text        NOT NULL CHECK (delivery_status IN ('NOT_REQUIRED','PENDING','ACCEPTED','FAILED','UNKNOWN')),
    notification_id  uuid,
    context          jsonb,
    created_at       timestamptz NOT NULL,
    expires_at       timestamptz NOT NULL,
    consumed_at      timestamptz,
    invalidated_at   timestamptz,
    CONSTRAINT auth_challenges_expiry_chk CHECK (expires_at > created_at),
    CONSTRAINT auth_challenges_attempts_chk CHECK (attempts <= max_attempts)
);
CREATE INDEX auth_challenges_account_purpose_idx ON auth_challenges (account_id, purpose, created_at);
CREATE INDEX auth_challenges_expires_idx ON auth_challenges (expires_at);

CREATE TABLE mfa_totp_credentials (
    credential_id      uuid PRIMARY KEY,
    account_id         uuid        NOT NULL REFERENCES owner_accounts (account_id),
    status             text        NOT NULL CHECK (status IN ('PENDING','ACTIVE','DISABLED')),
    secret_ciphertext  bytea       NOT NULL,
    key_id             text        NOT NULL,
    last_used_step     bigint,
    created_at         timestamptz NOT NULL,
    confirmed_at       timestamptz,
    disabled_at        timestamptz,
    CONSTRAINT mfa_totp_active_chk CHECK (status <> 'ACTIVE' OR confirmed_at IS NOT NULL)
);
CREATE UNIQUE INDEX mfa_totp_one_active_uniq ON mfa_totp_credentials (account_id) WHERE status = 'ACTIVE';
CREATE UNIQUE INDEX mfa_totp_one_pending_uniq ON mfa_totp_credentials (account_id) WHERE status = 'PENDING';

CREATE TABLE mfa_recovery_codes (
    code_id         uuid PRIMARY KEY,
    account_id      uuid        NOT NULL REFERENCES owner_accounts (account_id),
    batch_id        uuid        NOT NULL,
    code_hash       text        NOT NULL CHECK (char_length(code_hash) = 64),
    created_at      timestamptz NOT NULL,
    used_at         timestamptz,
    invalidated_at  timestamptz,
    CONSTRAINT mfa_recovery_codes_hash_uniq UNIQUE (account_id, code_hash)
);
CREATE INDEX mfa_recovery_codes_usable_idx ON mfa_recovery_codes (account_id) WHERE used_at IS NULL AND invalidated_at IS NULL;

-- Owner-visible security history (operator audit lives in audit_events).
CREATE TABLE account_security_events (
    event_id     uuid PRIMARY KEY,
    account_id   uuid        NOT NULL REFERENCES owner_accounts (account_id),
    event_type   text        NOT NULL CHECK (event_type ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    session_id   uuid,
    device_id    uuid,
    ip_address   inet,
    user_agent   text        CHECK (char_length(user_agent) <= 512),
    metadata     jsonb,
    occurred_at  timestamptz NOT NULL
);
CREATE INDEX account_security_events_account_idx ON account_security_events (account_id, occurred_at DESC, event_id DESC);

-- Durable requests to notification-service for non-secret notices (security
-- alerts). Destinations are cleared once the request is accepted.
CREATE TABLE notification_dispatches (
    dispatch_id        uuid PRIMARY KEY,
    account_id         uuid        NOT NULL REFERENCES owner_accounts (account_id),
    template_key       text        NOT NULL CHECK (template_key ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    channels           text[]      NOT NULL,
    variables          jsonb       NOT NULL,
    destination_email  text,
    destination_phone  text,
    status             text        NOT NULL CHECK (status IN ('PENDING','ACCEPTED','DEAD')),
    attempts           integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at    timestamptz NOT NULL,
    lease_owner        text,
    lease_expires_at   timestamptz,
    notification_id    uuid,
    last_error         text,
    created_at         timestamptz NOT NULL,
    completed_at       timestamptz
);
CREATE INDEX notification_dispatches_due_idx ON notification_dispatches (next_attempt_at) WHERE status = 'PENDING';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS notification_dispatches;
DROP TABLE IF EXISTS account_security_events;
DROP TABLE IF EXISTS mfa_recovery_codes;
DROP TABLE IF EXISTS mfa_totp_credentials;
DROP TABLE IF EXISTS auth_challenges;
DROP TABLE IF EXISTS auth_refresh_tokens;
DROP TABLE IF EXISTS auth_sessions;
DROP TABLE IF EXISTS auth_devices;
DROP TABLE IF EXISTS account_status_history;
DROP TABLE IF EXISTS owner_accounts;
-- +goose StatementEnd
