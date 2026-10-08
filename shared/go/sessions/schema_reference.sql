-- Reference schema for a user-domain service implementing sessions.Store.
-- Copy into the DOMAIN service's own Goose migrations (never a shared DB).

CREATE TABLE auth_sessions (
    id                 uuid PRIMARY KEY,
    account_id         uuid        NOT NULL,           -- domain-local account
    created_at         timestamptz NOT NULL,
    expires_at         timestamptz NOT NULL,           -- absolute lifetime
    last_refreshed_at  timestamptz NOT NULL,
    revoked_at         timestamptz,
    revoke_reason      text,
    device_id          text,
    device_name        text,
    device_platform    text,
    user_agent         text,
    ip_address         inet
);
CREATE INDEX auth_sessions_account_idx ON auth_sessions (account_id) WHERE revoked_at IS NULL;

CREATE TABLE auth_refresh_tokens (
    id           uuid PRIMARY KEY,
    session_id   uuid        NOT NULL REFERENCES auth_sessions (id) ON DELETE CASCADE,
    token_hash   text        NOT NULL UNIQUE,          -- sha256 hex; raw token never stored
    issued_at    timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    consumed_at  timestamptz,
    replaced_by  uuid
);

-- Rotate (compare-and-set, inside one transaction with the INSERT of the new token):
--   UPDATE auth_refresh_tokens SET consumed_at = $2, replaced_by = $3
--    WHERE id = $1 AND consumed_at IS NULL;   -- 0 rows => ErrAlreadyConsumed
