-- Section 6 bounded context 21 — property manager delegation (§13.21).
-- The owner remains the authority: an assignment is property-specific,
-- capability-specific (versioned, append-only permission sets), time-limited
-- and revocable. Revocation never deletes historical activity. Manager
-- profiles and workspaces live in property-manager-service; only typed
-- references (manager_domain + manager_ref_id) are stored here.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE manager_proposals (
    id                     uuid PRIMARY KEY,
    property_id            uuid        NOT NULL REFERENCES properties (id),
    manager_domain         text        NOT NULL CHECK (manager_domain IN ('property-manager','business-operations')),
    manager_ref_id         uuid        NOT NULL,
    manager_display_name   text        NOT NULL CHECK (char_length(manager_display_name) BETWEEN 1 AND 160),
    role_type              text        NOT NULL CHECK (role_type IN ('FULL_MANAGEMENT','LETTING_ONLY','MAINTENANCE_ONLY','SHORTLET_OPERATIONS','INSPECTION_ONLY')),
    capabilities           text[]      NOT NULL,
    fee_type               text        NOT NULL CHECK (fee_type IN ('PERCENT_OF_RENT','FIXED_MONTHLY','PER_TASK','NONE')),
    fee_bps                integer     CHECK (fee_bps BETWEEN 0 AND 10000),
    fee_amount_minor       bigint      CHECK (fee_amount_minor >= 0),
    fee_currency           char(3)     CHECK (fee_currency ~ '^[A-Z]{3}$'),
    proposed_by            text        NOT NULL CHECK (proposed_by IN ('OWNER','MANAGER')),
    status                 text        NOT NULL CHECK (status IN ('SUBMITTED','ACCEPTED','DECLINED','WITHDRAWN','EXPIRED')),
    message                text        CHECK (char_length(message) <= 2000),
    expires_at             timestamptz NOT NULL,
    version                bigint      NOT NULL DEFAULT 1,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL
);
CREATE INDEX manager_proposals_property_idx ON manager_proposals (property_id, created_at DESC);

CREATE TABLE manager_assignments (
    id                          uuid PRIMARY KEY,
    property_id                 uuid        NOT NULL REFERENCES properties (id),
    proposal_id                 uuid        REFERENCES manager_proposals (id),
    manager_domain              text        NOT NULL CHECK (manager_domain IN ('property-manager','business-operations')),
    manager_ref_id              uuid        NOT NULL,
    manager_display_name        text        NOT NULL,
    role_type                   text        NOT NULL CHECK (role_type IN ('FULL_MANAGEMENT','LETTING_ONLY','MAINTENANCE_ONLY','SHORTLET_OPERATIONS','INSPECTION_ONLY')),
    status                      text        NOT NULL CHECK (status IN ('PROPOSED','ACCEPTED','ACTIVE','SUSPENDED','EXPIRING','REVOKED','EXPIRED','TERMINATED')),
    starts_at                   timestamptz NOT NULL,
    ends_at                     timestamptz NOT NULL,
    current_permission_version  integer     NOT NULL DEFAULT 1,
    report_schedule             text        NOT NULL DEFAULT 'MONTHLY' CHECK (report_schedule IN ('WEEKLY','MONTHLY','QUARTERLY')),
    created_by_account          uuid        NOT NULL REFERENCES owner_accounts (account_id),
    revoked_at                  timestamptz,
    revoke_reason               text,
    version                     bigint      NOT NULL DEFAULT 1,
    created_at                  timestamptz NOT NULL,
    updated_at                  timestamptz NOT NULL,
    CONSTRAINT manager_assignments_window_chk CHECK (ends_at > starts_at)
);
CREATE UNIQUE INDEX manager_assignments_live_uniq ON manager_assignments (property_id, manager_domain, manager_ref_id)
    WHERE status IN ('PROPOSED','ACCEPTED','ACTIVE','SUSPENDED','EXPIRING');
CREATE INDEX manager_assignments_manager_idx ON manager_assignments (manager_domain, manager_ref_id) WHERE status IN ('ACTIVE','EXPIRING');
CREATE INDEX manager_assignments_due_idx ON manager_assignments (ends_at) WHERE status IN ('ACTIVE','EXPIRING','ACCEPTED');

CREATE TABLE manager_permissions (
    id                     uuid PRIMARY KEY,
    assignment_id          uuid        NOT NULL REFERENCES manager_assignments (id),
    version_no             integer     NOT NULL CHECK (version_no > 0),
    capabilities           text[]      NOT NULL,
    spend_limit_minor      bigint      NOT NULL DEFAULT 0 CHECK (spend_limit_minor >= 0),
    emergency_limit_minor  bigint      NOT NULL DEFAULT 0 CHECK (emergency_limit_minor >= 0),
    currency               char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    emergency_rule         text        NOT NULL DEFAULT 'NOTIFY_OWNER' CHECK (emergency_rule IN ('NOTIFY_OWNER','OWNER_APPROVAL','ACT_WITHIN_LIMIT')),
    created_by_account     uuid        NOT NULL REFERENCES owner_accounts (account_id),
    created_at             timestamptz NOT NULL,
    CONSTRAINT manager_permissions_version_uniq UNIQUE (assignment_id, version_no),
    CONSTRAINT manager_permissions_no_payout CHECK (NOT ('payout.manage' = ANY (capabilities)))
);
CREATE TRIGGER manager_permissions_append_only BEFORE UPDATE OR DELETE ON manager_permissions
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE manager_activity_refs (
    id              uuid PRIMARY KEY,
    assignment_id   uuid        NOT NULL REFERENCES manager_assignments (id),
    property_id     uuid        NOT NULL REFERENCES properties (id),
    activity_type   text        NOT NULL CHECK (activity_type ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    ref_service     text        NOT NULL,
    ref_type        text,
    ref_id          uuid,
    summary         text        NOT NULL CHECK (char_length(summary) <= 1000),
    actor_domain    text,
    actor_id        uuid,
    occurred_at     timestamptz NOT NULL
);
CREATE INDEX manager_activity_refs_assignment_idx ON manager_activity_refs (assignment_id, occurred_at DESC, id DESC);
CREATE TRIGGER manager_activity_refs_append_only BEFORE UPDATE OR DELETE ON manager_activity_refs
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS manager_activity_refs;
DROP TABLE IF EXISTS manager_permissions;
DROP TABLE IF EXISTS manager_assignments;
DROP TABLE IF EXISTS manager_proposals;
-- +goose StatementEnd
