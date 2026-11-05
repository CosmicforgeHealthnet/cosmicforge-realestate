-- +goose Up
CREATE TABLE hospitality_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,cutoff_local text NOT NULL CHECK(cutoff_local ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),version bigint NOT NULL CHECK(version>0),approval_id uuid NOT NULL,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,property_id,version)
);
CREATE TRIGGER hospitality_policies_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_policies FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE hospitality_business_days (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,day date NOT NULL,timezone text NOT NULL,cutoff_local text NOT NULL,cutoff_at timestamptz NOT NULL,state text NOT NULL CHECK(state IN ('OPEN','CLOSED')),version bigint NOT NULL DEFAULT 1 CHECK(version>0),policy_version bigint NOT NULL CHECK(policy_version>=0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid NOT NULL REFERENCES business_accounts(id),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,property_id,day),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE UNIQUE INDEX property_single_open_business_day ON hospitality_business_days(organisation_id,property_id) WHERE state='OPEN';
CREATE TRIGGER hospitality_days_version BEFORE INSERT OR UPDATE ON hospitality_business_days FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TABLE night_audits (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,business_day_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('READY','EXCEPTIONS')),snapshot jsonb NOT NULL,snapshot_digest text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,business_day_id) REFERENCES hospitality_business_days(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE night_audit_exceptions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,audit_id uuid NOT NULL,code text NOT NULL,hard_block boolean NOT NULL,quantity bigint NOT NULL CHECK(quantity>0),evidence jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,audit_id) REFERENCES night_audits(organisation_id,property_id,id),UNIQUE(audit_id,code)
);
CREATE TABLE business_day_closes (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,business_day_id uuid NOT NULL,audit_id uuid NOT NULL,approval_id uuid,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,business_day_id) REFERENCES hospitality_business_days(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,audit_id) REFERENCES night_audits(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(business_day_id),UNIQUE(organisation_id,id)
);
CREATE TABLE business_day_close_reports (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,close_id uuid NOT NULL,report jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,close_id) REFERENCES business_day_closes(organisation_id,id),UNIQUE(close_id)
);
CREATE TRIGGER night_audit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON night_audits FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER night_exceptions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON night_audit_exceptions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER day_close_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON business_day_closes FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER day_close_report_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON business_day_close_reports FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
ALTER TABLE folio_items ADD COLUMN business_day_id uuid;
ALTER TABLE folio_items ADD CONSTRAINT folio_posting_business_day FOREIGN KEY(organisation_id,property_id,business_day_id) REFERENCES hospitality_business_days(organisation_id,property_id,id);
-- Older postings retain their actual timestamps; no invented historical close.
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_posting_day_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.business_day_id IS NULL OR NOT EXISTS(SELECT 1 FROM hospitality_business_days d WHERE d.organisation_id=NEW.organisation_id AND d.property_id=NEW.property_id AND d.id=NEW.business_day_id AND d.state='OPEN' AND now()<d.cutoff_at) THEN RAISE EXCEPTION 'Posting requires the current open business day before its cutoff' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER folio_open_day BEFORE INSERT ON folio_items FOR EACH ROW EXECUTE FUNCTION business_hospitality_posting_day_guard();
CREATE TABLE cash_drawers (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,code text NOT NULL,currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),state text NOT NULL CHECK(state IN ('ACTIVE','SUSPENDED')),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,property_id,code),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE cashier_shifts (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,drawer_id uuid NOT NULL,business_day_id uuid NOT NULL,cashier_id uuid NOT NULL REFERENCES business_accounts(id),opening_minor bigint NOT NULL CHECK(opening_minor>=0),closing_minor bigint CHECK(closing_minor>=0),expected_minor bigint,variance_minor bigint,state text NOT NULL CHECK(state IN ('OPEN','CLOSED','RECONCILIATION_REQUIRED')),closed_at timestamptz,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,drawer_id) REFERENCES cash_drawers(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,business_day_id) REFERENCES hospitality_business_days(organisation_id,property_id,id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),CHECK((state='OPEN')=(closed_at IS NULL)),CHECK(expected_minor IS NULL OR variance_minor=closing_minor-expected_minor)
);
CREATE UNIQUE INDEX one_active_drawer_shift ON cashier_shifts(organisation_id,drawer_id) WHERE state IN ('OPEN','RECONCILIATION_REQUIRED');
CREATE TABLE cashier_financial_attestations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,shift_id uuid NOT NULL,source_version bigint NOT NULL CHECK(source_version>0),expected_movement_minor bigint NOT NULL,currency text NOT NULL,source_reference text NOT NULL,observed_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,payload_digest text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,shift_id) REFERENCES cashier_shifts(organisation_id,property_id,id),UNIQUE(shift_id,source_version),UNIQUE(organisation_id,source_reference)
);
CREATE TABLE cashier_variances (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,shift_id uuid NOT NULL,shift_version bigint NOT NULL,expected_minor bigint,closing_minor bigint NOT NULL,variance_minor bigint,approval_id uuid,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,shift_id) REFERENCES cashier_shifts(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(shift_id,shift_version)
);
CREATE TABLE cashier_handovers (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,shift_id uuid NOT NULL,from_account_id uuid NOT NULL REFERENCES business_accounts(id),to_account_id uuid NOT NULL REFERENCES business_accounts(id),declared_minor bigint NOT NULL CHECK(declared_minor>=0),evidence_reference text NOT NULL,reason text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),CHECK(from_account_id<>to_account_id),
 FOREIGN KEY(organisation_id,property_id,shift_id) REFERENCES cashier_shifts(organisation_id,property_id,id),UNIQUE(shift_id)
);
CREATE TRIGGER cashier_attestation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON cashier_financial_attestations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER cashier_variance_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON cashier_variances FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER cashier_handover_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON cashier_handovers FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER drawer_version BEFORE INSERT OR UPDATE ON cash_drawers FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER cashier_version BEFORE INSERT OR UPDATE ON cashier_shifts FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER business_day_identity BEFORE UPDATE ON hospitality_business_days FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER drawer_identity BEFORE UPDATE ON cash_drawers FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER cashier_identity BEFORE UPDATE ON cashier_shifts FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER business_day_retention BEFORE DELETE OR TRUNCATE ON hospitality_business_days FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER drawer_retention BEFORE DELETE OR TRUNCATE ON cash_drawers FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER cashier_retention BEFORE DELETE OR TRUNCATE ON cashier_shifts FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose Down
SELECT 1;
