-- +goose Up
-- Business obligations and payer allocations only. The existing authenticated
-- Payment queue, locked quotes, bindings and receipts still own money truth.
-- Each agreement owns exactly one dedicated financial group; accommodation
-- stays with the reservation's original group and is never collected here.
CREATE TABLE folio_collection_agreements (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,folio_id uuid NOT NULL,reservation_id uuid NOT NULL,financial_group_id uuid NOT NULL,
 request_reference uuid NOT NULL CHECK(substring(request_reference::text,15,1)='7'),folio_version bigint NOT NULL CHECK(folio_version>0),amount_minor bigint NOT NULL CHECK(amount_minor BETWEEN 1 AND 1000000000000),currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 payer_type text NOT NULL CHECK(payer_type IN ('GUEST','CORPORATE_BOOKER')),corporate_customer_id uuid,corporate_contract_id uuid,consent_reference text NOT NULL CHECK(length(consent_reference) BETWEEN 1 AND 300),effective_at timestamptz NOT NULL,replaces_agreement_id uuid,
 source_digest text NOT NULL CHECK(source_digest~'^[a-f0-9]{64}$'),command_digest text NOT NULL CHECK(command_digest~'^[a-f0-9]{64}$'),snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),accepted_policy jsonb NOT NULL CHECK(jsonb_typeof(accepted_policy)='object'),approval_id uuid,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,financial_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),FOREIGN KEY(organisation_id,corporate_customer_id) REFERENCES corporate_customer_profiles(organisation_id,id),FOREIGN KEY(organisation_id,corporate_contract_id) REFERENCES corporate_customer_contracts(organisation_id,id),FOREIGN KEY(organisation_id,property_id,replaces_agreement_id) REFERENCES folio_collection_agreements(organisation_id,property_id,id),
 CHECK((payer_type='CORPORATE_BOOKER')=(corporate_customer_id IS NOT NULL AND corporate_contract_id IS NOT NULL)),CHECK(payer_type='GUEST' OR approval_id IS NOT NULL),CHECK(replaces_agreement_id IS DISTINCT FROM id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(financial_group_id),UNIQUE(organisation_id,request_reference),UNIQUE(replaces_agreement_id)
);
CREATE INDEX folio_collection_scope ON folio_collection_agreements(organisation_id,property_id,folio_id,created_at DESC,id DESC);
CREATE TABLE folio_collection_allocations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,folio_id uuid NOT NULL,agreement_id uuid NOT NULL,folio_item_id uuid NOT NULL,amount_minor bigint NOT NULL CHECK(amount_minor>0),currency text NOT NULL,source_snapshot jsonb NOT NULL CHECK(jsonb_typeof(source_snapshot)='object'),
 FOREIGN KEY(organisation_id,property_id,agreement_id) REFERENCES folio_collection_agreements(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,folio_item_id) REFERENCES folio_items(organisation_id,id),UNIQUE(agreement_id,folio_item_id),UNIQUE(organisation_id,id)
);
CREATE INDEX folio_collection_source_budget ON folio_collection_allocations(organisation_id,folio_item_id,agreement_id);
CREATE TABLE folio_collection_cancellations (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,agreement_id uuid NOT NULL,disposition text NOT NULL CHECK(disposition IN ('NEVER_DISPATCHED','VERIFIED_FAILURE')),reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,agreement_id) REFERENCES folio_collection_agreements(organisation_id,property_id,id),UNIQUE(agreement_id),UNIQUE(organisation_id,id)
);
ALTER TABLE reservation_financial_requests ADD COLUMN folio_agreement_id uuid;
ALTER TABLE reservation_financial_requests ADD CONSTRAINT financial_folio_agreement_parent FOREIGN KEY(organisation_id,property_id,folio_agreement_id) REFERENCES folio_collection_agreements(organisation_id,property_id,id);
CREATE INDEX financial_folio_request_scope ON reservation_financial_requests(organisation_id,folio_agreement_id,created_at DESC,id DESC) WHERE folio_agreement_id IS NOT NULL;
CREATE TRIGGER folio_collection_agreement_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON folio_collection_agreements FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER folio_collection_allocation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON folio_collection_allocations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER folio_collection_cancellation_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON folio_collection_cancellations FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE CONSTRAINT TRIGGER folio_collection_agreement_evidence AFTER INSERT ON folio_collection_agreements DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('folio_collection_agreement');
CREATE CONSTRAINT TRIGGER folio_collection_cancellation_evidence AFTER INSERT ON folio_collection_cancellations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('folio_collection_cancellation');
-- +goose StatementBegin
CREATE FUNCTION business_folio_collection_source() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE available numeric;already numeric;BEGIN
 PERFORM id FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 IF NOT EXISTS(SELECT 1 FROM folio_collection_agreements a JOIN folio_items i ON i.organisation_id=a.organisation_id AND i.folio_id=a.folio_id WHERE a.organisation_id=NEW.organisation_id AND a.property_id=NEW.property_id AND a.folio_id=NEW.folio_id AND a.id=NEW.agreement_id AND i.id=NEW.folio_item_id AND i.currency=NEW.currency AND a.currency=NEW.currency AND i.kind NOT IN ('ACCOMMODATION','ADJUSTMENT')) THEN RAISE EXCEPTION 'Collection allocations must name actual same-folio ancillary obligations' USING ERRCODE='23514';END IF;
 SELECT i.amount_minor+coalesce((SELECT sum(c.amount_minor) FROM folio_items c WHERE c.organisation_id=i.organisation_id AND c.adjusts_item_id=i.id),0) INTO available FROM folio_items i WHERE i.organisation_id=NEW.organisation_id AND i.id=NEW.folio_item_id;
 SELECT coalesce(sum(a.amount_minor),0) INTO already FROM folio_collection_allocations a WHERE a.organisation_id=NEW.organisation_id AND a.folio_item_id=NEW.folio_item_id AND NOT EXISTS(SELECT 1 FROM folio_collection_cancellations c WHERE c.organisation_id=a.organisation_id AND c.agreement_id=a.agreement_id);
 IF already+NEW.amount_minor>available THEN RAISE EXCEPTION 'Accepted payer allocations cannot exceed their remaining original obligation' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER folio_collection_source_guard BEFORE INSERT ON folio_collection_allocations FOR EACH ROW EXECUTE FUNCTION business_folio_collection_source();
-- +goose StatementBegin
CREATE FUNCTION business_folio_collection_agreement_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM folios f JOIN reservation_financial_groups g ON g.organisation_id=f.organisation_id AND g.property_id=f.property_id AND g.reservation_id=f.reservation_id WHERE f.organisation_id=NEW.organisation_id AND f.property_id=NEW.property_id AND f.id=NEW.folio_id AND f.reservation_id=NEW.reservation_id AND g.id=NEW.financial_group_id AND g.amount_minor=NEW.amount_minor AND g.required_amount_minor=NEW.amount_minor AND g.currency=NEW.currency AND f.currency=NEW.currency AND g.responsibility='PLATFORM_COLLECT' AND NEW.amount_minor=(SELECT coalesce(sum(a.amount_minor),0) FROM folio_collection_allocations a WHERE a.organisation_id=NEW.organisation_id AND a.agreement_id=NEW.id)) THEN RAISE EXCEPTION 'Accepted folio agreement, financial obligation and exact source allocations must conserve amounts' USING ERRCODE='23514';END IF;
 IF EXISTS(SELECT 1 FROM reservation_rooms r WHERE r.organisation_id=NEW.organisation_id AND r.financial_group_id=NEW.financial_group_id) THEN RAISE EXCEPTION 'A folio agreement cannot reuse an accommodation financial group' USING ERRCODE='23514';END IF;
 IF NEW.replaces_agreement_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM folio_collection_agreements p JOIN folio_collection_cancellations c ON c.organisation_id=p.organisation_id AND c.agreement_id=p.id WHERE p.organisation_id=NEW.organisation_id AND p.property_id=NEW.property_id AND p.id=NEW.replaces_agreement_id AND p.folio_id=NEW.folio_id) THEN RAISE EXCEPTION 'A replacement must name a released agreement on the same folio' USING ERRCODE='23514';END IF;
 IF NOT EXISTS(SELECT 1 FROM reservation_financial_requests r WHERE r.organisation_id=NEW.organisation_id AND r.folio_agreement_id=NEW.id AND r.financial_group_id=NEW.financial_group_id AND r.kind='COLLECTION') THEN RAISE EXCEPTION 'An accepted folio agreement must queue its durable Payment collection request atomically' USING ERRCODE='23514';END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER folio_collection_agreement_guard AFTER INSERT ON folio_collection_agreements DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_folio_collection_agreement_guard();
-- +goose StatementBegin
-- Allocation release is allowed only when no money can be in flight: either
-- the original request never reached Payment, or Payment verified failure.
CREATE FUNCTION business_folio_collection_cancel_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM id FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 IF NEW.disposition='NEVER_DISPATCHED' AND NOT EXISTS(SELECT 1 FROM reservation_financial_requests r WHERE r.organisation_id=NEW.organisation_id AND r.folio_agreement_id=NEW.agreement_id AND r.kind='COLLECTION' AND r.attempts=0 AND r.state='QUEUED' AND r.provider_reference='' AND NOT EXISTS(SELECT 1 FROM reservation_payment_bindings b WHERE b.organisation_id=r.organisation_id AND b.financial_group_id=r.financial_group_id)) THEN RAISE EXCEPTION 'Only an original never-dispatched agreement can release its payer allocation; reconcile uncertain money first' USING ERRCODE='23514';END IF;
 IF NEW.disposition='VERIFIED_FAILURE' AND NOT EXISTS(SELECT 1 FROM folio_collection_agreements a JOIN reservation_financial_groups g ON g.organisation_id=a.organisation_id AND g.id=a.financial_group_id JOIN LATERAL(SELECT p.* FROM reservation_payment_projections p WHERE p.organisation_id=a.organisation_id AND p.financial_group_id=a.financial_group_id ORDER BY p.observed_at DESC,p.id DESC LIMIT 1) p ON true WHERE a.organisation_id=NEW.organisation_id AND a.id=NEW.agreement_id AND g.state='FAILED' AND p.provider_state='VERIFIED_FAILURE' AND p.reversed_minor=0) THEN RAISE EXCEPTION 'A dispatched collection releases its allocation only after authoritative verified failure' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER folio_collection_cancel_guard BEFORE INSERT ON folio_collection_cancellations FOR EACH ROW EXECUTE FUNCTION business_folio_collection_cancel_guard();
-- +goose StatementBegin
CREATE FUNCTION business_financial_folio_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='UPDATE' AND NEW.folio_agreement_id IS DISTINCT FROM OLD.folio_agreement_id THEN RAISE EXCEPTION 'Original financial source ownership is immutable' USING ERRCODE='23514';END IF;
 IF TG_OP='INSERT' AND NEW.folio_agreement_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM folio_collection_agreements a WHERE a.organisation_id=NEW.organisation_id AND a.property_id=NEW.property_id AND a.id=NEW.folio_agreement_id AND a.reservation_id=NEW.reservation_id AND a.financial_group_id=NEW.financial_group_id) THEN RAISE EXCEPTION 'Financial request must retain its exact folio agreement and original obligation' USING ERRCODE='23514';END IF;
 IF TG_OP='INSERT' AND NEW.folio_agreement_id IS NULL AND NEW.kind='COLLECTION' AND EXISTS(SELECT 1 FROM folio_collection_agreements a WHERE a.organisation_id=NEW.organisation_id AND a.financial_group_id=NEW.financial_group_id) THEN RAISE EXCEPTION 'A folio payer obligation is collected only through its own agreement' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER financial_folio_source BEFORE INSERT OR UPDATE OF folio_agreement_id ON reservation_financial_requests FOR EACH ROW EXECUTE FUNCTION business_financial_folio_source();
-- +goose StatementBegin
CREATE FUNCTION business_folio_collection_payment_target() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF EXISTS(SELECT 1 FROM folio_collection_agreements a WHERE a.organisation_id=NEW.organisation_id AND a.financial_group_id=NEW.financial_group_id) AND NOT EXISTS(SELECT 1 FROM folio_collection_agreements a WHERE a.organisation_id=NEW.organisation_id AND a.property_id=NEW.property_id AND a.financial_group_id=NEW.financial_group_id AND a.folio_id=NEW.folio_id AND NEW.amount_minor<=a.amount_minor AND NOT EXISTS(SELECT 1 FROM folio_collection_cancellations c WHERE c.organisation_id=a.organisation_id AND c.agreement_id=a.id)) THEN RAISE EXCEPTION 'A folio payer receipt cannot settle another folio or a cancelled agreement' USING ERRCODE='23514';END IF;RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER folio_collection_payment_target BEFORE INSERT ON folio_payment_references FOR EACH ROW EXECUTE FUNCTION business_folio_collection_payment_target();
-- +goose Down
SELECT 1;
