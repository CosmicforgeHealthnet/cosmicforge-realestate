-- Customer, tenant and guest notifications (§13.16 announcements, §13.24).
--
-- notification-service only lets a user-domain service address recipients
-- of its own domain, and the recipient's contacts, consent and channel
-- preferences belong to that domain. The owner domain therefore records
-- what it is entitled to send — sender authority, recipient relationship,
-- purpose and content — and hands each request to the recipient's domain
-- (marketplace-service) through a service contract; that domain applies
-- consent and preferred channels and sends through notification-service.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE owner_announcements (
    id                  uuid PRIMARY KEY,
    property_id         uuid        NOT NULL REFERENCES properties (id),
    sender_account_id   uuid        NOT NULL REFERENCES owner_accounts (account_id),
    sender_role         text        NOT NULL CHECK (sender_role IN ('OWNER','CO_OWNER','REPRESENTATIVE','MANAGER')),
    audience            text        NOT NULL CHECK (audience IN ('TENANTS','GUESTS')),
    purpose             text        NOT NULL CHECK (purpose IN ('SERVICE_ANNOUNCEMENT','UTILITY_INTERRUPTION','SERVICE_CHARGE_UPDATE','MAINTENANCE_ACCESS','SAFETY_NOTICE')),
    title               text        NOT NULL CHECK (char_length(title) BETWEEN 3 AND 120),
    message             text        NOT NULL CHECK (char_length(message) BETWEEN 3 AND 1000),
    recipients          integer     NOT NULL DEFAULT 0,
    skipped_no_account  integer     NOT NULL DEFAULT 0,
    created_at          timestamptz NOT NULL
);
CREATE INDEX owner_announcements_property_idx ON owner_announcements (property_id, created_at DESC);

CREATE TABLE customer_notifications (
    id                  uuid PRIMARY KEY,
    announcement_id     uuid        REFERENCES owner_announcements (id),
    property_id         uuid        NOT NULL REFERENCES properties (id),
    recipient_domain    text        NOT NULL CHECK (recipient_domain = 'marketplace'),
    recipient_ref       uuid        NOT NULL,
    relationship_type   text        NOT NULL CHECK (relationship_type IN ('tenancy','shortlet_booking')),
    relationship_id     uuid        NOT NULL,
    purpose             text        NOT NULL,
    variables           jsonb       NOT NULL,
    status              text        NOT NULL CHECK (status IN ('PENDING','ACCEPTED','SUPPRESSED','BLOCKED','FAILED')),
    status_reason       text        CHECK (status_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    channels            text[]      NOT NULL DEFAULT '{}',
    external_ref        text        CHECK (char_length(external_ref) <= 128),
    attempts            integer     NOT NULL DEFAULT 0,
    next_attempt_at     timestamptz NOT NULL,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    CONSTRAINT customer_notifications_once UNIQUE (announcement_id, recipient_ref, relationship_id)
);
CREATE INDEX customer_notifications_due_idx ON customer_notifications (next_attempt_at) WHERE status IN ('PENDING','BLOCKED');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS customer_notifications;
DROP TABLE IF EXISTS owner_announcements;
-- +goose StatementEnd
