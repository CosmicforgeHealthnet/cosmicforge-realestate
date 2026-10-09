-- Section 13 closure: short-let stay extensions (§13.20), statutory/tax
-- calendar and property valuations for yield (§13.25), portfolio insight
-- types (§13.25) and attributed assisted use (§13.27).

-- +goose Up
-- +goose StatementBegin

-- §13.20 Stay extensions of a checked-in short-let booking. The booking's
-- accepted price snapshot never changes: each extension carries its own
-- immutable price snapshot for the added nights, computed by the same cost
-- engine, and its own payment (payment-service) when collected on platform.
-- The added nights are claimed on the calendar under the extension's id
-- until it is applied (then they belong to the booking) or released.
CREATE TABLE shortlet_booking_extensions (
    id                    uuid PRIMARY KEY,
    booking_id            uuid        NOT NULL REFERENCES shortlet_bookings (id),
    listing_id            uuid        NOT NULL REFERENCES listings (id),
    property_id           uuid        NOT NULL REFERENCES properties (id),
    requested_by          text        NOT NULL CHECK (requested_by IN ('GUEST','OWNER')),
    awaiting              text        CHECK (awaiting IN ('GUEST','OWNER')),
    status                text        NOT NULL CHECK (status IN ('REQUESTED','PAYMENT_PENDING','APPLIED','DECLINED','EXPIRED','CANCELLED')),
    previous_check_out    date        NOT NULL,
    new_check_out         date        NOT NULL,
    buffer_end            date        NOT NULL,
    previous_buffer_end   date        NOT NULL,
    pricing_version_id    uuid        NOT NULL REFERENCES pricing_versions (id),
    cost_snapshot         jsonb       NOT NULL,
    amount_minor          bigint      NOT NULL CHECK (amount_minor >= 0),
    currency              char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    payment_collection    text        NOT NULL CHECK (payment_collection IN ('PLATFORM','OFF_PLATFORM')),
    payment_intent_ref    text        CHECK (char_length(payment_intent_ref) <= 200),
    paid_minor            bigint      NOT NULL DEFAULT 0 CHECK (paid_minor >= 0),
    last_payment_status   text,
    last_checked_at       timestamptz,
    respond_by            timestamptz NOT NULL,
    decline_reason        text        CHECK (decline_reason ~ '^[A-Z][A-Z0-9_]{1,63}$'),
    applied_at            timestamptz,
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT shortlet_booking_extensions_dates_chk CHECK (new_check_out > previous_check_out AND buffer_end >= previous_check_out)
);
CREATE UNIQUE INDEX shortlet_booking_extensions_open_uniq ON shortlet_booking_extensions (booking_id)
    WHERE status IN ('REQUESTED','PAYMENT_PENDING');
CREATE INDEX shortlet_booking_extensions_booking_idx ON shortlet_booking_extensions (booking_id, created_at);
CREATE UNIQUE INDEX shortlet_booking_extensions_intent_uniq ON shortlet_booking_extensions (payment_intent_ref) WHERE payment_intent_ref IS NOT NULL;

-- §13.25 Tax/statutory calendar: owner-maintained obligations (filings,
-- property rates, ground rent, licence renewals). Jurisdiction rules are not
-- hard-coded; amounts are the owner's informational estimates, never a tax
-- computation, and nothing here is paid through the platform.
CREATE TABLE statutory_obligations (
    id                    uuid PRIMARY KEY,
    account_id            uuid        NOT NULL REFERENCES owner_accounts (account_id),
    property_id           uuid        REFERENCES properties (id),
    obligation_type       text        NOT NULL CHECK (obligation_type IN ('INCOME_TAX_FILING','RENTAL_INCOME_TAX','WITHHOLDING_TAX_REMITTANCE',
                              'PROPERTY_TAX','LAND_USE_CHARGE','GROUND_RENT','TENEMENT_RATE','SERVICE_LICENCE','STATUTORY_INSURANCE','OTHER')),
    title                 text        NOT NULL CHECK (char_length(title) BETWEEN 3 AND 160),
    jurisdiction          char(2)     NOT NULL CHECK (jurisdiction ~ '^[A-Z]{2}$'),
    authority             text        CHECK (char_length(authority) <= 160),
    reference             text        CHECK (char_length(reference) <= 120),
    due_on                date        NOT NULL,
    recurrence            text        NOT NULL CHECK (recurrence IN ('NONE','MONTHLY','QUARTERLY','ANNUAL')),
    estimated_amount_minor bigint     CHECK (estimated_amount_minor >= 0),
    currency              char(3)     CHECK (currency ~ '^[A-Z]{3}$'),
    reminder_days         integer     NOT NULL DEFAULT 14 CHECK (reminder_days BETWEEN 0 AND 90),
    status                text        NOT NULL CHECK (status IN ('UPCOMING','COMPLETED','CANCELLED')),
    reminded_at           timestamptz,
    completed_at          timestamptz,
    evidence_media_ids    uuid[]      NOT NULL DEFAULT '{}',
    notes                 text        CHECK (char_length(notes) <= 2000),
    previous_id           uuid        REFERENCES statutory_obligations (id),
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    CONSTRAINT statutory_obligations_amount_chk CHECK ((estimated_amount_minor IS NULL) = (currency IS NULL)),
    CONSTRAINT statutory_obligations_media_chk CHECK (cardinality(evidence_media_ids) <= 10)
);
CREATE INDEX statutory_obligations_account_idx ON statutory_obligations (account_id, due_on);
CREATE INDEX statutory_obligations_due_idx ON statutory_obligations (due_on) WHERE status = 'UPCOMING';
CREATE UNIQUE INDEX statutory_obligations_next_uniq ON statutory_obligations (previous_id) WHERE previous_id IS NOT NULL;

-- §13.25 Property valuations (append-only) so rental yield is computed from
-- a disclosed basis instead of being omitted or invented.
CREATE TABLE property_valuations (
    id                         uuid PRIMARY KEY,
    property_id                uuid        NOT NULL REFERENCES properties (id),
    value_minor                bigint      NOT NULL CHECK (value_minor > 0),
    currency                   char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    valued_on                  date        NOT NULL,
    basis                      text        NOT NULL CHECK (basis IN ('OWNER_ESTIMATE','PURCHASE_PRICE','PROFESSIONAL_VALUATION')),
    professional_appointment_id uuid       REFERENCES professional_appointments (id),
    evidence_media_ids         uuid[]      NOT NULL DEFAULT '{}',
    note                       text        CHECK (char_length(note) <= 1000),
    recorded_by_account        uuid        NOT NULL REFERENCES owner_accounts (account_id),
    created_at                 timestamptz NOT NULL,
    CONSTRAINT property_valuations_professional_chk CHECK (basis <> 'PROFESSIONAL_VALUATION' OR professional_appointment_id IS NOT NULL),
    CONSTRAINT property_valuations_media_chk CHECK (cardinality(evidence_media_ids) <= 10)
);
CREATE INDEX property_valuations_property_idx ON property_valuations (property_id, valued_on DESC, created_at DESC);
CREATE TRIGGER property_valuations_append_only BEFORE UPDATE OR DELETE ON property_valuations
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- §13.25 Portfolio insight types (advisory, with provenance).
ALTER TABLE market_insights DROP CONSTRAINT market_insights_insight_type_check;
ALTER TABLE market_insights ADD CONSTRAINT market_insights_insight_type_check
    CHECK (insight_type IN ('PRICE_RECOMMENDATION','MARKET_TREND','VACANCY_RISK','RENEWAL_OUTLOOK','MAINTENANCE_PATTERN'));

-- §13.27 Assisted use. The owner authorizes support for a purpose, scope
-- and period; support staff act through admin-service as attributable admin
-- actors (never a shared account); every assisted action is recorded here
-- (append-only) and in the audit trail, and the owner is notified.
CREATE TABLE assistance_authorizations (
    id                    uuid PRIMARY KEY,
    account_id            uuid        NOT NULL REFERENCES owner_accounts (account_id),
    scopes                text[]      NOT NULL CHECK (cardinality(scopes) BETWEEN 1 AND 5
                              AND scopes <@ ARRAY['ONBOARDING','PROPERTY_RECORDS','MEDIA_CAPTURE','DOCUMENT_SCANNING','ACCESSIBILITY_SUPPORT']::text[]),
    property_ids          uuid[]      NOT NULL DEFAULT '{}' CHECK (cardinality(property_ids) <= 50),
    channel               text        NOT NULL CHECK (channel IN ('IN_APP','PHONE','IN_PERSON','VIDEO')),
    reason                text        CHECK (char_length(reason) <= 500),
    status                text        NOT NULL CHECK (status IN ('ACTIVE','REVOKED','EXPIRED')),
    expires_at            timestamptz NOT NULL,
    revoked_at            timestamptz,
    revoke_reason         text        CHECK (char_length(revoke_reason) <= 500),
    version               bigint      NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL
);
CREATE INDEX assistance_authorizations_account_idx ON assistance_authorizations (account_id, created_at DESC);
CREATE INDEX assistance_authorizations_expiry_idx ON assistance_authorizations (expires_at) WHERE status = 'ACTIVE';

CREATE TABLE assisted_activities (
    id                    uuid PRIMARY KEY,
    account_id            uuid        NOT NULL REFERENCES owner_accounts (account_id),
    authorization_id      uuid        NOT NULL REFERENCES assistance_authorizations (id),
    support_service       text        NOT NULL,
    support_actor_domain  text        NOT NULL,
    support_actor_id      uuid        NOT NULL,
    activity_type         text        NOT NULL CHECK (activity_type IN ('ONBOARDING_GUIDANCE','PROPERTY_DRAFT_CREATED','PHOTOGRAPHY_CAPTURED',
                              'DOCUMENT_SCANNED','REMOTE_HELP','ACCESSIBILITY_ASSISTANCE')),
    scope                 text        NOT NULL,
    property_id           uuid        REFERENCES properties (id),
    media_ids             uuid[]      NOT NULL DEFAULT '{}' CHECK (cardinality(media_ids) <= 20),
    note                  text        NOT NULL CHECK (char_length(note) BETWEEN 3 AND 2000),
    created_at            timestamptz NOT NULL
);
CREATE INDEX assisted_activities_account_idx ON assisted_activities (account_id, created_at DESC);
CREATE TRIGGER assisted_activities_append_only BEFORE UPDATE OR DELETE ON assisted_activities
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS assisted_activities;
DROP TABLE IF EXISTS assistance_authorizations;
DELETE FROM market_insights WHERE insight_type IN ('RENEWAL_OUTLOOK','MAINTENANCE_PATTERN');
ALTER TABLE market_insights DROP CONSTRAINT market_insights_insight_type_check;
ALTER TABLE market_insights ADD CONSTRAINT market_insights_insight_type_check
    CHECK (insight_type IN ('PRICE_RECOMMENDATION','MARKET_TREND','VACANCY_RISK'));
DROP TABLE IF EXISTS property_valuations;
DROP TABLE IF EXISTS statutory_obligations;
DROP TABLE IF EXISTS shortlet_booking_extensions;
-- +goose StatementEnd
