-- Section 6 contexts 28–30: rent financing / payment plans (§13.28),
-- insurance and property protection (§13.29) and smart access (§13.30).
--
-- Financing and insurance are partner-integrated: the owner domain keeps
-- partner references, consent and the owner's business choices, never the
-- applicant's financial details. Access grants are owner-domain authority:
-- device adapters only provision/revoke credentials, credential secrets are
-- never stored, and no grant outlives the business relationship it serves.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE finance_offers (
    id                      uuid PRIMARY KEY,
    account_id              uuid        NOT NULL REFERENCES owner_accounts (account_id),
    listing_id              uuid        REFERENCES listings (id),
    product_type            text        NOT NULL CHECK (product_type IN ('RENT_INSTALMENT','RENT_ADVANCE','MORTGAGE_BUYER','SELLER_PLAN')),
    partner_ref             text        NOT NULL CHECK (char_length(partner_ref) BETWEEN 1 AND 128),
    external_offer_ref      text        NOT NULL CHECK (char_length(external_offer_ref) BETWEEN 1 AND 128),
    status                  text        NOT NULL CHECK (status IN ('OFFERED','ACCEPTED','DECLINED','WITHDRAWN','EXPIRED')),
    owner_payout_preference text        CHECK (owner_payout_preference IN ('UPFRONT','AS_COLLECTED')),
    disclosure              jsonb       NOT NULL,
    consent_id              uuid        NOT NULL REFERENCES consents (id),
    expires_at              timestamptz,
    decided_at              timestamptz,
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL
);
CREATE UNIQUE INDEX finance_offers_external_uniq ON finance_offers (partner_ref, external_offer_ref);
CREATE INDEX finance_offers_account_idx ON finance_offers (account_id, created_at DESC);

CREATE TABLE insurance_policies (
    id                      uuid PRIMARY KEY,
    account_id              uuid        NOT NULL REFERENCES owner_accounts (account_id),
    property_id             uuid        NOT NULL REFERENCES properties (id),
    source                  text        NOT NULL CHECK (source IN ('PARTNER','OWNER_RECORDED')),
    product_type            text        NOT NULL CHECK (product_type IN ('BUILDING','CONTENTS','LANDLORD_LIABILITY','RENT_GUARANTEE','SHORTLET_COVER',
                                'WORKMANSHIP','FIDELITY','OTHER')),
    insurer_name            text        NOT NULL CHECK (char_length(insurer_name) BETWEEN 1 AND 120),
    policy_ref              text        NOT NULL CHECK (char_length(policy_ref) BETWEEN 1 AND 128),
    coverage_minor          bigint      CHECK (coverage_minor > 0),
    premium_minor           bigint      CHECK (premium_minor >= 0),
    currency                char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    starts_on               date        NOT NULL,
    ends_on                 date        NOT NULL,
    status                  text        NOT NULL CHECK (status IN ('ACTIVE','EXPIRING','EXPIRED','CANCELLED')),
    document_media_id       uuid,
    renewal_reminded_at     timestamptz,
    version                 bigint      NOT NULL DEFAULT 1,
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL,
    CONSTRAINT insurance_policies_range_chk CHECK (ends_on > starts_on),
    CONSTRAINT insurance_policies_evidence_chk CHECK (source = 'PARTNER' OR document_media_id IS NOT NULL)
);
CREATE UNIQUE INDEX insurance_policies_ref_uniq ON insurance_policies (property_id, insurer_name, policy_ref);
CREATE INDEX insurance_policies_due_idx ON insurance_policies (ends_on) WHERE status IN ('ACTIVE','EXPIRING');

CREATE TABLE insurance_claims (
    id                      uuid PRIMARY KEY,
    policy_id               uuid        NOT NULL REFERENCES insurance_policies (id),
    property_id             uuid        NOT NULL REFERENCES properties (id),
    incident_date           date        NOT NULL,
    description             text        NOT NULL CHECK (char_length(description) BETWEEN 1 AND 4000),
    claimed_minor           bigint      NOT NULL CHECK (claimed_minor > 0),
    currency                char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status                  text        NOT NULL CHECK (status IN ('SUBMITTED','UNDER_REVIEW','APPROVED','PARTIALLY_APPROVED','REJECTED','PAID','WITHDRAWN',
                                'COMPLAINT')),
    insurer_claim_ref       text        CHECK (char_length(insurer_claim_ref) <= 128),
    approved_minor          bigint      CHECK (approved_minor >= 0),
    paid_minor              bigint      NOT NULL DEFAULT 0,
    evidence_media_ids      uuid[]      NOT NULL DEFAULT '{}',
    related_ref_type        text        CHECK (related_ref_type IN ('maintenance_request','damage_claim','tenancy')),
    related_ref_id          uuid,
    status_note             text        CHECK (char_length(status_note) <= 2000),
    version                 bigint      NOT NULL DEFAULT 1,
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL,
    CONSTRAINT insurance_claims_media_chk CHECK (cardinality(evidence_media_ids) BETWEEN 1 AND 40)
);
CREATE INDEX insurance_claims_policy_idx ON insurance_claims (policy_id, created_at DESC);

CREATE TABLE access_devices (
    id                      uuid PRIMARY KEY,
    property_id             uuid        NOT NULL REFERENCES properties (id),
    unit_id                 uuid        REFERENCES property_units (id),
    device_type             text        NOT NULL CHECK (device_type IN ('SMART_LOCK','KEYPAD','LOCKBOX','PHYSICAL_KEY','KEY_CARD')),
    label                   text        NOT NULL CHECK (char_length(label) BETWEEN 1 AND 120),
    provider_device_ref     text        CHECK (char_length(provider_device_ref) <= 128),
    status                  text        NOT NULL CHECK (status IN ('ACTIVE','RETIRED')),
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL,
    CONSTRAINT access_devices_provider_chk CHECK (device_type NOT IN ('SMART_LOCK','KEYPAD') OR provider_device_ref IS NOT NULL)
);
CREATE INDEX access_devices_property_idx ON access_devices (property_id) WHERE status = 'ACTIVE';

CREATE TABLE access_grants (
    id                      uuid PRIMARY KEY,
    property_id             uuid        NOT NULL REFERENCES properties (id),
    device_id               uuid        NOT NULL REFERENCES access_devices (id),
    purpose                 text        NOT NULL CHECK (purpose IN ('VIEWING','TENANT','GUEST','MANAGER','PROVIDER')),
    relation_type           text        NOT NULL CHECK (relation_type IN ('viewing_request','tenancy','shortlet_booking','manager_assignment','maintenance_job')),
    relation_id             uuid        NOT NULL,
    principal_domain        text        NOT NULL,
    principal_ref           uuid        NOT NULL,
    principal_label         text        NOT NULL CHECK (char_length(principal_label) BETWEEN 1 AND 120),
    starts_at               timestamptz NOT NULL,
    ends_at                 timestamptz NOT NULL,
    status                  text        NOT NULL CHECK (status IN ('REQUESTED','APPROVED','ISSUED','ACTIVE','EXPIRED','REVOKED','COMPROMISED')),
    requested_by            text        NOT NULL CHECK (requested_by IN ('OWNER','MANAGER')),
    credential_ref          text        CHECK (char_length(credential_ref) <= 200),
    credential_revoked_at   timestamptz,
    end_reason              text        CHECK (end_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    version                 bigint      NOT NULL DEFAULT 1,
    created_at              timestamptz NOT NULL,
    updated_at              timestamptz NOT NULL,
    CONSTRAINT access_grants_window_chk CHECK (ends_at > starts_at)
);
CREATE INDEX access_grants_property_idx ON access_grants (property_id, created_at DESC);
CREATE INDEX access_grants_relation_idx ON access_grants (relation_type, relation_id) WHERE status IN ('REQUESTED','APPROVED','ISSUED','ACTIVE');
CREATE INDEX access_grants_due_idx ON access_grants (ends_at) WHERE status IN ('APPROVED','ISSUED','ACTIVE');
CREATE INDEX access_grants_unrevoked_idx ON access_grants (updated_at)
    WHERE status IN ('EXPIRED','REVOKED','COMPROMISED') AND credential_ref IS NOT NULL AND credential_revoked_at IS NULL;

-- Grant and device history, append-only.
CREATE TABLE access_events (
    id                      uuid PRIMARY KEY,
    grant_id                uuid        REFERENCES access_grants (id),
    device_id               uuid        NOT NULL REFERENCES access_devices (id),
    property_id             uuid        NOT NULL REFERENCES properties (id),
    event_type              text        NOT NULL CHECK (event_type IN ('REQUESTED','APPROVED','ISSUED','ACTIVATED','EXPIRED','REVOKED','COMPROMISED',
                                'CREDENTIAL_REVOKED','KEY_HANDED_OVER','KEY_RETURNED')),
    actor                   text        NOT NULL,
    note                    text        CHECK (char_length(note) <= 500),
    occurred_at             timestamptz NOT NULL
);
CREATE INDEX access_events_property_idx ON access_events (property_id, occurred_at DESC);
CREATE TRIGGER access_events_append_only BEFORE UPDATE OR DELETE ON access_events
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS access_events;
DROP TABLE IF EXISTS access_grants;
DROP TABLE IF EXISTS access_devices;
DROP TABLE IF EXISTS insurance_claims;
DROP TABLE IF EXISTS insurance_policies;
DROP TABLE IF EXISTS finance_offers;
-- +goose StatementEnd
