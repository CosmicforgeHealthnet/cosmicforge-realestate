-- +goose Up
-- Property-manager-owned mandates: the manager's acceptance and use of a
-- Business management assignment. Business remains the authority for the
-- delegation (scope, capabilities, expiry, revocation); every action is
-- re-validated live by Business. Business ids are references only.

CREATE TABLE mandates (
    id                      uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    business_assignment_id  uuid        NOT NULL UNIQUE,
    organisation_id         uuid        NOT NULL,
    property_id             uuid        NOT NULL,
    manager_account_id      uuid        NOT NULL REFERENCES domain_accounts(id),
    kind                    text        NOT NULL,
    title                   text        NOT NULL CHECK (length(title) BETWEEN 3 AND 200),
    capabilities            text[]      NOT NULL DEFAULT '{}',
    state                   text        NOT NULL CHECK (state IN ('OFFERED','ACTIVE','DECLINED','SUSPENDED','REVOKED','EXPIRED')),
    business_state          text        NOT NULL,
    business_version        bigint      NOT NULL,
    ends_at                 timestamptz,
    accepted_at             timestamptz,
    version                 bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mandates_manager ON mandates (manager_account_id, state, created_at DESC);
CREATE TRIGGER mandates_guard BEFORE UPDATE OR DELETE ON mandates FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('OFFERED>ACTIVE,OFFERED>DECLINED,OFFERED>REVOKED,OFFERED>EXPIRED,ACTIVE>SUSPENDED,SUSPENDED>ACTIVE,ACTIVE>REVOKED,SUSPENDED>REVOKED,ACTIVE>EXPIRED,SUSPENDED>EXPIRED');

CREATE TABLE mandate_actions (
    id                   uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    mandate_id           uuid        NOT NULL REFERENCES mandates(id),
    action               text        NOT NULL CHECK (action IN ('MAINTENANCE_REQUEST')),
    request_key          text        NOT NULL,
    business_record_id   uuid        NOT NULL,
    capability           text        NOT NULL,
    created_by           uuid        NOT NULL REFERENCES domain_accounts(id),
    created_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (mandate_id, request_key)
);
-- +goose StatementBegin
CREATE FUNCTION mandate_actions_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION 'mandate_actions is append-only' USING ERRCODE='23514';
END;
$fn$;
-- +goose StatementEnd
CREATE TRIGGER mandate_actions_immutable BEFORE UPDATE OR DELETE ON mandate_actions FOR EACH ROW EXECUTE FUNCTION mandate_actions_append_only();

-- +goose Down
SELECT 1;
