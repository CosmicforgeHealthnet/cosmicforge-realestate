-- +goose Up
-- Marketplace customer side. Marketplace owns customer accounts (00002),
-- its checkout journeys, verified-stay reviews and funnel observations. It
-- is never a price, inventory, reservation or payment authority: quotes,
-- holds, reservations and confirmation are Business decisions read back from
-- Business; money moves only in the Payment Service. Columns that mirror an
-- owning service are labelled business_* / payment references.

CREATE TABLE checkouts (
    id                      uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    account_id              uuid        NOT NULL REFERENCES domain_accounts(id),
    connection_id           uuid        NOT NULL REFERENCES supply_connections(id),
    organisation_id         uuid        NOT NULL,
    property_id             uuid        NOT NULL,
    product_id              uuid        NOT NULL,
    date_from               date        NOT NULL,
    date_to                 date        NOT NULL CHECK (date_to > date_from),
    adults                  integer     NOT NULL CHECK (adults BETWEEN 1 AND 50),
    children                integer     NOT NULL DEFAULT 0 CHECK (children BETWEEN 0 AND 50),
    quantity                integer     NOT NULL CHECK (quantity BETWEEN 1 AND 20),
    business_quote_id       uuid        NOT NULL UNIQUE,
    product_version         bigint      NOT NULL,
    pool_version            bigint      NOT NULL,
    amount_minor            bigint      NOT NULL CHECK (amount_minor >= 0),
    currency                text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    quote_expires_at        timestamptz NOT NULL,
    state                   text        NOT NULL CHECK (state IN ('QUOTED','HELD','PAYMENT_PENDING','CONFIRMED','PAYMENT_FAILED','EXPIRED','CANCELLED','COMPLETED')),
    business_reservation_id uuid        UNIQUE,
    business_version        bigint,
    business_state          text,
    financial_state         text,
    hold_expires_at         timestamptz,
    wallet_id               uuid,
    collection_request_id   uuid,
    intent_id               uuid,
    checkout_url            text,
    last_synced_at          timestamptz,
    version                 bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    CHECK (state IN ('QUOTED','EXPIRED') OR business_reservation_id IS NOT NULL OR state = 'CANCELLED')
);
CREATE INDEX checkouts_account ON checkouts (account_id, created_at DESC);
CREATE INDEX checkouts_active ON checkouts (state, updated_at) WHERE state IN ('QUOTED','HELD','PAYMENT_PENDING','CONFIRMED');
CREATE TRIGGER checkouts_guard BEFORE UPDATE OR DELETE ON checkouts FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('QUOTED>HELD,QUOTED>EXPIRED,QUOTED>CANCELLED,HELD>PAYMENT_PENDING,HELD>CONFIRMED,HELD>EXPIRED,HELD>CANCELLED,PAYMENT_PENDING>CONFIRMED,PAYMENT_PENDING>PAYMENT_FAILED,PAYMENT_PENDING>EXPIRED,PAYMENT_PENDING>CANCELLED,CONFIRMED>CANCELLED,CONFIRMED>COMPLETED');

-- A verified-stay review: one per completed checkout, accepted by Business
-- reputation before it is stored here.
CREATE TABLE stay_reviews (
    id                 uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    checkout_id        uuid        NOT NULL UNIQUE REFERENCES checkouts(id),
    account_id         uuid        NOT NULL REFERENCES domain_accounts(id),
    rating             integer     NOT NULL CHECK (rating BETWEEN 1 AND 5),
    body               text        NOT NULL DEFAULT '' CHECK (length(body) <= 5000),
    business_review    jsonb       NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementBegin
CREATE FUNCTION stay_reviews_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION 'stay_reviews is append-only' USING ERRCODE='23514';
END;
$fn$;
-- +goose StatementEnd
CREATE TRIGGER stay_reviews_immutable BEFORE UPDATE OR DELETE ON stay_reviews FOR EACH ROW EXECUTE FUNCTION stay_reviews_append_only();

-- Funnel observations produced by Marketplace and delivered to Business
-- (deduplicated there by source_reference).
CREATE TABLE funnel_events (
    id                uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    connection_id     uuid        NOT NULL REFERENCES supply_connections(id),
    organisation_id   uuid        NOT NULL,
    property_id       uuid        NOT NULL,
    listing_id        uuid,
    stage             text        NOT NULL CHECK (stage IN ('IMPRESSION','LISTING_VIEW','INQUIRY')),
    source_reference  text        NOT NULL UNIQUE CHECK (length(source_reference) BETWEEN 3 AND 300),
    quantity          integer     NOT NULL DEFAULT 1 CHECK (quantity BETWEEN 1 AND 10000),
    observed_at       timestamptz NOT NULL,
    delivered_at      timestamptz,
    attempts          integer     NOT NULL DEFAULT 0,
    last_error        text,
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX funnel_events_pending ON funnel_events (created_at) WHERE delivered_at IS NULL;

-- +goose Down
SELECT 1;
