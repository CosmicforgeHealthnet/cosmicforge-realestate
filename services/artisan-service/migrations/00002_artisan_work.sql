-- +goose Up
-- Artisan-owned work: provider work requests and quote requests projected
-- from Business events (Business owns the work order; the artisan owns the
-- execution, evidence and warranty). Business ids are references only.

CREATE TABLE work_requests (
    id                      uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    business_assignment_id  uuid        NOT NULL UNIQUE,
    organisation_id         uuid        NOT NULL,
    property_id             uuid        NOT NULL,
    work_order_id           uuid        NOT NULL,
    artisan_account_id      uuid        NOT NULL REFERENCES domain_accounts(id),
    kind                    text        NOT NULL CHECK (kind IN ('STANDARD','REWORK')),
    rework_of               uuid        REFERENCES work_requests(id),
    title                   text        NOT NULL CHECK (length(title) BETWEEN 3 AND 200),
    scope_of_work           text        NOT NULL DEFAULT '' CHECK (length(scope_of_work) <= 2000),
    state                   text        NOT NULL CHECK (state IN ('OFFERED','ACCEPTED','DECLINED','ON_SITE','COMPLETED','CANCELLED')),
    business_state          text        NOT NULL DEFAULT 'REQUESTED',
    business_version        bigint      NOT NULL DEFAULT 1,
    warranty_days           integer     CHECK (warranty_days IS NULL OR warranty_days BETWEEN 0 AND 3650),
    version                 bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX work_requests_artisan ON work_requests (artisan_account_id, state, created_at DESC);
CREATE TRIGGER work_requests_guard BEFORE UPDATE OR DELETE ON work_requests FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('OFFERED>ACCEPTED,OFFERED>DECLINED,ACCEPTED>ON_SITE,ON_SITE>COMPLETED,OFFERED>CANCELLED,ACCEPTED>CANCELLED,ON_SITE>CANCELLED');

CREATE TABLE work_evidence (
    id                uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    work_request_id   uuid        NOT NULL REFERENCES work_requests(id),
    stage             text        NOT NULL CHECK (stage IN ('BEFORE','DURING','AFTER')),
    media_reference   uuid        NOT NULL,
    note              text        NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    created_by        uuid        NOT NULL REFERENCES domain_accounts(id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (work_request_id, media_reference)
);
-- +goose StatementBegin
CREATE FUNCTION work_evidence_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION 'work_evidence is append-only' USING ERRCODE='23514';
END;
$fn$;
-- +goose StatementEnd
CREATE TRIGGER work_evidence_immutable BEFORE UPDATE OR DELETE ON work_evidence FOR EACH ROW EXECUTE FUNCTION work_evidence_append_only();

CREATE TABLE quote_requests (
    id                  uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    business_quote_id   uuid        NOT NULL UNIQUE,
    organisation_id     uuid        NOT NULL,
    property_id         uuid        NOT NULL,
    work_order_id       uuid        NOT NULL,
    artisan_account_id  uuid        NOT NULL REFERENCES domain_accounts(id),
    title               text        NOT NULL,
    scope_of_work       text        NOT NULL DEFAULT '',
    state               text        NOT NULL CHECK (state IN ('REQUESTED','SUBMITTED','ACCEPTED','REJECTED','WITHDRAWN')),
    amount_minor        bigint      CHECK (amount_minor IS NULL OR amount_minor > 0),
    currency            text        CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
    version             bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK ((amount_minor IS NULL) = (currency IS NULL)),
    CHECK (state <> 'SUBMITTED' OR amount_minor IS NOT NULL)
);
CREATE INDEX quote_requests_artisan ON quote_requests (artisan_account_id, state, created_at DESC);
CREATE TRIGGER quote_requests_guard BEFORE UPDATE OR DELETE ON quote_requests FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('REQUESTED>SUBMITTED,REQUESTED>WITHDRAWN,SUBMITTED>ACCEPTED,SUBMITTED>REJECTED,SUBMITTED>WITHDRAWN');

CREATE TABLE warranties (
    id                uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    work_request_id   uuid        NOT NULL UNIQUE REFERENCES work_requests(id),
    artisan_account_id uuid       NOT NULL REFERENCES domain_accounts(id),
    reference         text        NOT NULL UNIQUE,
    days              integer     NOT NULL CHECK (days BETWEEN 1 AND 3650),
    starts_at         timestamptz NOT NULL,
    expires_at        timestamptz NOT NULL CHECK (expires_at > starts_at),
    state             text        NOT NULL CHECK (state IN ('ACTIVE','CLAIMED','EXPIRED')),
    version           bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER warranties_guard BEFORE UPDATE OR DELETE ON warranties FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('ACTIVE>CLAIMED,CLAIMED>ACTIVE,ACTIVE>EXPIRED,CLAIMED>EXPIRED');

-- +goose Down
SELECT 1;
