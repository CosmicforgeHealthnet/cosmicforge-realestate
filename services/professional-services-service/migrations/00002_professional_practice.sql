-- +goose Up
-- Professional-services-owned practice: the professional's verified
-- credential (Verification Service is the authority), appointments and
-- certification requests projected from Business, findings and reports.

CREATE TABLE professional_profiles (
    account_id        uuid PRIMARY KEY REFERENCES domain_accounts(id),
    id                uuid        NOT NULL UNIQUE CHECK (substring(id::text,15,1)='7'),
    profession        text        NOT NULL CHECK (profession IN ('VALUER','SURVEYOR','LAWYER','INSPECTOR','ARCHITECT','ENGINEER','AUDITOR','QUANTITY_SURVEYOR')),
    license_number    text        NOT NULL CHECK (length(license_number) BETWEEN 3 AND 100),
    state             text        NOT NULL CHECK (state IN ('UNVERIFIED','PENDING','VERIFIED','REJECTED','EXPIRED')),
    verification_id   uuid,
    verified_at       timestamptz,
    expires_at        timestamptz,
    version           bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CHECK (state <> 'VERIFIED' OR verification_id IS NOT NULL AND verified_at IS NOT NULL)
);
CREATE TRIGGER professional_profiles_guard BEFORE UPDATE OR DELETE ON professional_profiles FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('UNVERIFIED>PENDING,REJECTED>PENDING,EXPIRED>PENDING,PENDING>PENDING,PENDING>VERIFIED,PENDING>REJECTED,VERIFIED>EXPIRED,VERIFIED>PENDING');

CREATE TABLE appointments (
    id                       uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    business_appointment_id  uuid        NOT NULL UNIQUE,
    organisation_id          uuid        NOT NULL,
    property_id              uuid        NOT NULL,
    professional_account_id  uuid        NOT NULL REFERENCES domain_accounts(id),
    kind                     text        NOT NULL,
    title                    text        NOT NULL,
    purpose                  text        NOT NULL DEFAULT '',
    state                    text        NOT NULL CHECK (state IN ('OFFERED','ACCEPTED','IN_PROGRESS','REPORT_SUBMITTED','COMPLETED','CANCELLED')),
    business_state           text        NOT NULL,
    report_reference         uuid,
    version                  bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX appointments_professional ON appointments (professional_account_id, state, created_at DESC);
CREATE TRIGGER appointments_guard BEFORE UPDATE OR DELETE ON appointments FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('OFFERED>ACCEPTED,ACCEPTED>IN_PROGRESS,IN_PROGRESS>REPORT_SUBMITTED,REPORT_SUBMITTED>COMPLETED,OFFERED>CANCELLED,ACCEPTED>CANCELLED,IN_PROGRESS>CANCELLED');

CREATE TABLE findings (
    id                    uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    appointment_id        uuid        NOT NULL REFERENCES appointments(id),
    business_finding_id   uuid        NOT NULL UNIQUE,
    kind                  text        NOT NULL,
    title                 text        NOT NULL,
    evidence_refs         uuid[]      NOT NULL CHECK (cardinality(evidence_refs) BETWEEN 1 AND 20),
    created_by            uuid        NOT NULL REFERENCES domain_accounts(id),
    created_at            timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementBegin
CREATE FUNCTION findings_append_only() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
    RAISE EXCEPTION 'findings are append-only' USING ERRCODE='23514';
END;
$fn$;
-- +goose StatementEnd
CREATE TRIGGER findings_immutable BEFORE UPDATE OR DELETE ON findings FOR EACH ROW EXECUTE FUNCTION findings_append_only();

CREATE TABLE certification_requests (
    id                          uuid PRIMARY KEY CHECK (substring(id::text,15,1)='7'),
    business_certification_id   uuid        NOT NULL UNIQUE,
    organisation_id             uuid        NOT NULL,
    development_id              uuid        NOT NULL,
    professional_account_id     uuid        NOT NULL REFERENCES domain_accounts(id),
    milestone_id                text        NOT NULL DEFAULT '',
    title                       text        NOT NULL,
    state                       text        NOT NULL CHECK (state IN ('REQUESTED','IN_REVIEW','CERTIFIED','REJECTED','CANCELLED')),
    certificate_reference       text,
    version                     bigint      NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    CHECK (state <> 'CERTIFIED' OR certificate_reference IS NOT NULL)
);
CREATE TRIGGER certification_requests_guard BEFORE UPDATE OR DELETE ON certification_requests FOR EACH ROW
    EXECUTE FUNCTION domain_state_guard('REQUESTED>IN_REVIEW,IN_REVIEW>CERTIFIED,IN_REVIEW>REJECTED,REQUESTED>REJECTED,REQUESTED>CANCELLED,IN_REVIEW>CANCELLED');

-- +goose Down
SELECT 1;
