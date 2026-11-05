-- +goose Up
-- Physical occupancy is a projection of canonical, committed reservation stock.
CREATE TABLE room_status_current (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,room_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('VACANT_CLEAN','VACANT_DIRTY','INSPECTED','OCCUPIED','BLOCKED','OUT_OF_ORDER')),
 evidence_reference text NOT NULL,reason text NOT NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid NOT NULL REFERENCES business_accounts(id),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,room_id) REFERENCES inventory_nodes(organisation_id,id),UNIQUE(organisation_id,room_id),UNIQUE(organisation_id,property_id,room_id)
);
CREATE TABLE room_assignments (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,reservation_id uuid NOT NULL,reservation_room_id uuid NOT NULL,room_id uuid NOT NULL,slot integer NOT NULL CHECK(slot BETWEEN 1 AND 1000),
 date_from date NOT NULL,date_to date NOT NULL CHECK(date_to>date_from),state text NOT NULL CHECK(state IN ('ASSIGNED','OCCUPIED','RELEASED')),version bigint NOT NULL DEFAULT 1 CHECK(version>0),reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid NOT NULL REFERENCES business_accounts(id),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_room_id) REFERENCES reservation_rooms(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,room_id) REFERENCES room_status_current(organisation_id,property_id,room_id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE UNIQUE INDEX assignment_active_slot ON room_assignments(organisation_id,reservation_room_id,slot) WHERE state IN ('ASSIGNED','OCCUPIED');
CREATE UNIQUE INDEX assignment_occupied_room ON room_assignments(organisation_id,room_id) WHERE state='OCCUPIED';
CREATE INDEX assignment_property_dates ON room_assignments(organisation_id,property_id,date_from,date_to,state,id);
CREATE TABLE room_assignment_days (
 organisation_id uuid NOT NULL,assignment_id uuid NOT NULL,room_id uuid NOT NULL,day date NOT NULL,released_at timestamptz,
 FOREIGN KEY(organisation_id,assignment_id) REFERENCES room_assignments(organisation_id,id),FOREIGN KEY(organisation_id,room_id) REFERENCES inventory_nodes(organisation_id,id),PRIMARY KEY(assignment_id,day)
);
CREATE UNIQUE INDEX physical_room_night_exclusive ON room_assignment_days(organisation_id,room_id,day) WHERE released_at IS NULL;
CREATE TABLE stay_records (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,reservation_id uuid NOT NULL,assignment_id uuid NOT NULL,guest_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('ACTIVE','DEPARTED')),registration_reference text NOT NULL,checked_in_at timestamptz NOT NULL,checked_out_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK((state='ACTIVE' AND checked_out_at IS NULL) OR (state='DEPARTED' AND checked_out_at IS NOT NULL)),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,assignment_id) REFERENCES room_assignments(organisation_id,property_id,id),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(assignment_id)
);
CREATE INDEX stay_property_state ON stay_records(organisation_id,property_id,state,id);
CREATE TABLE folios (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,reservation_id uuid NOT NULL,stay_id uuid NOT NULL,
 state text NOT NULL CHECK(state IN ('OPEN','SETTLEMENT_PENDING','SETTLED','CLOSED','DISPUTED')),currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,stay_id) REFERENCES stay_records(organisation_id,property_id,id),UNIQUE(stay_id),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE folio_items (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,folio_id uuid NOT NULL,kind text NOT NULL CHECK(kind IN ('ACCOMMODATION','TAX','FEE','MEALS','MINIBAR','LAUNDRY','SPA','TRANSFER','EVENT','DAMAGE','OTHER','ADJUSTMENT')),
 source_reference text NOT NULL,quantity integer NOT NULL CHECK(quantity>0),amount_minor bigint NOT NULL CHECK(amount_minor<>0 OR kind='ACCOMMODATION'),currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),policy_snapshot jsonb NOT NULL,adjusts_item_id uuid,approval_id uuid,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(organisation_id,id),UNIQUE(folio_id,source_reference),FOREIGN KEY(organisation_id,adjusts_item_id) REFERENCES folio_items(organisation_id,id),CHECK((kind='ADJUSTMENT')=(adjusts_item_id IS NOT NULL)),CHECK(kind='ADJUSTMENT' OR amount_minor>=0)
);
CREATE TABLE hospitality_history (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,entity_id uuid NOT NULL,entity_type text NOT NULL,entity_version bigint NOT NULL CHECK(entity_version>0),action text NOT NULL,change_context jsonb NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),UNIQUE(organisation_id,entity_type,entity_id,entity_version)
);
CREATE TABLE folio_payment_references (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,folio_id uuid NOT NULL,financial_group_id uuid NOT NULL,intent_id uuid NOT NULL,payment_projection_id uuid NOT NULL,amount_minor bigint NOT NULL CHECK(amount_minor>0),currency text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,financial_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,payment_projection_id) REFERENCES reservation_payment_projections(organisation_id,property_id,id),UNIQUE(folio_id,financial_group_id)
);
CREATE TRIGGER folio_payment_references_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON folio_payment_references FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_payment_allocation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM id FROM reservation_financial_groups WHERE organisation_id=NEW.organisation_id AND id=NEW.financial_group_id FOR UPDATE;
 IF NOT EXISTS(SELECT 1 FROM reservation_payment_projections p JOIN reservation_payment_bindings b ON b.organisation_id=p.organisation_id AND b.financial_group_id=p.financial_group_id AND b.intent_id=p.intent_id WHERE p.organisation_id=NEW.organisation_id AND p.id=NEW.payment_projection_id AND p.financial_group_id=NEW.financial_group_id AND p.intent_id=NEW.intent_id AND p.currency=NEW.currency AND p.provider_state='VERIFIED_SUCCESS' AND p.settlement_state='SETTLED' AND p.funding_confirmed AND p.reversed_minor=0 AND p.remedy_version=1 AND NOT p.remedy_review_required AND p.expires_at>now() AND p.amount_minor>=(SELECT coalesce(sum(amount_minor),0) FROM folio_payment_references WHERE organisation_id=NEW.organisation_id AND financial_group_id=NEW.financial_group_id)+NEW.amount_minor) THEN RAISE EXCEPTION 'Bound authoritative settled Payment evidence and conserved allocation required' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER folio_payment_allocation BEFORE INSERT ON folio_payment_references FOR EACH ROW EXECUTE FUNCTION business_hospitality_payment_allocation();
CREATE TABLE guest_requests (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,stay_id uuid NOT NULL,kind text NOT NULL CHECK(kind IN ('HOUSEKEEPING','MAINTENANCE','SERVICE','ACCESSIBILITY','OTHER')),state text NOT NULL CHECK(state IN ('OPEN','ACKNOWLEDGED','RESOLVED','CANCELLED')),evidence_reference text NOT NULL,reason text NOT NULL,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,stay_id) REFERENCES stay_records(organisation_id,property_id,id),UNIQUE(organisation_id,id)
);
CREATE TABLE access_grant_references (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,stay_id uuid NOT NULL,assignment_id uuid NOT NULL,operation text NOT NULL CHECK(operation IN ('ISSUE','REVOKE')),state text NOT NULL CHECK(state IN ('PENDING','UNKNOWN','CONFIRMED','FAILED')),provider_reference text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,stay_id) REFERENCES stay_records(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,assignment_id) REFERENCES room_assignments(organisation_id,property_id,id),UNIQUE(assignment_id,operation)
);
CREATE TRIGGER hospitality_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON hospitality_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER folio_items_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON folio_items FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER room_status_version BEFORE INSERT OR UPDATE ON room_status_current FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER assignments_version BEFORE INSERT OR UPDATE ON room_assignments FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER stays_version BEFORE INSERT OR UPDATE ON stay_records FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER folios_version BEFORE INSERT OR UPDATE ON folios FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
CREATE TRIGGER guest_requests_version BEFORE INSERT OR UPDATE ON guest_requests FOR EACH ROW EXECUTE FUNCTION business_commercial_version_guard();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_assignment_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='UPDATE' AND (NEW.organisation_id,NEW.property_id,NEW.reservation_id,NEW.reservation_room_id,NEW.room_id,NEW.slot,NEW.date_from,NEW.date_to) IS DISTINCT FROM (OLD.organisation_id,OLD.property_id,OLD.reservation_id,OLD.reservation_room_id,OLD.room_id,OLD.slot,OLD.date_from,OLD.date_to) THEN RAISE EXCEPTION 'Physical assignment identity is immutable; release and append a move' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM reservation_rooms r WHERE r.organisation_id=NEW.organisation_id AND r.id=NEW.reservation_room_id AND r.reservation_id=NEW.reservation_id AND r.property_id=NEW.property_id AND NEW.slot<=r.quantity AND NEW.date_from>=r.date_from AND NEW.date_to<=r.date_to) THEN RAISE EXCEPTION 'Assignment must fit canonical room quantity and dates' USING ERRCODE='23514';END IF;
 RETURN NEW; END $$;
-- +goose StatementEnd
CREATE TRIGGER physical_assignment_integrity BEFORE INSERT OR UPDATE ON room_assignments FOR EACH ROW EXECUTE FUNCTION business_hospitality_assignment_guard();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_reservation_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (NEW.state NOT IN ('CONFIRMED','CHECKED_IN','DISPUTED') OR NEW.allocation_state<>'ACTIVE') AND EXISTS(SELECT 1 FROM room_assignments a WHERE a.organisation_id=NEW.organisation_id AND a.reservation_id=NEW.id AND a.state IN ('ASSIGNED','OCCUPIED')) THEN RAISE EXCEPTION 'Release physical assignments before cancelling or restructuring a reservation' USING ERRCODE='23514'; END IF;
 RETURN NEW; END $$;
-- +goose StatementEnd
CREATE TRIGGER hospitality_reservation_protection BEFORE UPDATE ON reservations FOR EACH ROW EXECUTE FUNCTION business_hospitality_reservation_guard();
ALTER TABLE inventory_pools ADD COLUMN operational_hold boolean NOT NULL DEFAULT false;
-- A physical hazard closes affected pools conservatively, even when already
-- committed stock prevents lowering capacity. It never manufactures overbooking.
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_pool_hold() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 NEW.operational_hold:=EXISTS(SELECT 1 FROM inventory_pool_members m JOIN room_status_current s ON s.organisation_id=m.organisation_id AND s.room_id=m.node_id WHERE m.organisation_id=NEW.organisation_id AND m.pool_id=NEW.id AND s.state IN ('BLOCKED','OUT_OF_ORDER'));
 RETURN NEW; END $$;
-- +goose StatementEnd
CREATE TRIGGER operational_hold_guard BEFORE INSERT OR UPDATE ON inventory_pools FOR EACH ROW EXECUTE FUNCTION business_hospitality_pool_hold();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_propagate_hold() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 UPDATE inventory_pools p SET version=version+1,updated_at=now() WHERE p.organisation_id=NEW.organisation_id AND EXISTS(SELECT 1 FROM inventory_pool_members m WHERE m.organisation_id=p.organisation_id AND m.pool_id=p.id AND m.node_id=NEW.room_id) AND p.operational_hold IS DISTINCT FROM EXISTS(SELECT 1 FROM inventory_pool_members m JOIN room_status_current s ON s.organisation_id=m.organisation_id AND s.room_id=m.node_id WHERE m.organisation_id=p.organisation_id AND m.pool_id=p.id AND s.state IN ('BLOCKED','OUT_OF_ORDER'));
 RETURN NULL; END $$;
-- +goose StatementEnd
CREATE TRIGGER operational_hazard_hold AFTER INSERT OR UPDATE ON room_status_current FOR EACH ROW EXECUTE FUNCTION business_hospitality_propagate_hold();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE field text;BEGIN
 FOREACH field IN ARRAY ARRAY['id','organisation_id','property_id','created_at','created_by','room_id','reservation_id','reservation_room_id','slot','date_from','date_to','guest_id','stay_id','drawer_id','business_day_id','cashier_id','opening_minor','day','timezone','cutoff_local','cutoff_at','policy_version','currency','kind'] LOOP
 IF to_jsonb(NEW)->field IS DISTINCT FROM to_jsonb(OLD)->field THEN RAISE EXCEPTION 'Hospitality identity and accepted context are immutable' USING ERRCODE='23514';END IF;END LOOP;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER room_status_identity BEFORE UPDATE ON room_status_current FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER stay_identity BEFORE UPDATE ON stay_records FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER folio_identity BEFORE UPDATE ON folios FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER guest_request_identity BEFORE UPDATE ON guest_requests FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_day_integrity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='UPDATE' AND (NEW.organisation_id,NEW.assignment_id,NEW.room_id,NEW.day) IS DISTINCT FROM (OLD.organisation_id,OLD.assignment_id,OLD.room_id,OLD.day) OR TG_OP='UPDATE' AND OLD.released_at IS NOT NULL THEN RAISE EXCEPTION 'Physical assignment nights cannot be reassigned or revived' USING ERRCODE='23514';END IF;
 IF NOT EXISTS(SELECT 1 FROM room_assignments a WHERE a.organisation_id=NEW.organisation_id AND a.id=NEW.assignment_id AND a.room_id=NEW.room_id AND NEW.day>=a.date_from AND NEW.day<a.date_to) THEN RAISE EXCEPTION 'Night must belong to its exact bounded physical assignment' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER physical_night_identity BEFORE INSERT OR UPDATE ON room_assignment_days FOR EACH ROW EXECUTE FUNCTION business_hospitality_day_integrity();
CREATE TRIGGER physical_night_retention BEFORE DELETE OR TRUNCATE ON room_assignment_days FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER room_status_retention BEFORE DELETE OR TRUNCATE ON room_status_current FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER assignment_retention BEFORE DELETE OR TRUNCATE ON room_assignments FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER stay_retention BEFORE DELETE OR TRUNCATE ON stay_records FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER folio_retention BEFORE DELETE OR TRUNCATE ON folios FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER guest_request_retention BEFORE DELETE OR TRUNCATE ON guest_requests FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_hospitality_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE kind text;BEGIN
 kind:=CASE TG_TABLE_NAME WHEN 'room_status_current' THEN 'room_status' WHEN 'room_assignments' THEN 'room_assignment' WHEN 'stay_records' THEN 'stay' WHEN 'folios' THEN 'folio' WHEN 'guest_requests' THEN 'guest_request' END;
 IF NOT EXISTS(SELECT 1 FROM hospitality_history h WHERE h.organisation_id=NEW.organisation_id AND h.property_id=NEW.property_id AND h.entity_id=NEW.id AND h.entity_type=kind AND h.entity_version=NEW.version) THEN RAISE EXCEPTION 'Every hospitality transition requires its immutable evidence in the owning transaction' USING ERRCODE='23514'; END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER room_status_evidence AFTER INSERT OR UPDATE ON room_status_current DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_evidence_guard();
CREATE CONSTRAINT TRIGGER room_assignment_evidence AFTER INSERT OR UPDATE ON room_assignments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_evidence_guard();
CREATE CONSTRAINT TRIGGER stay_evidence AFTER INSERT OR UPDATE ON stay_records DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_evidence_guard();
CREATE CONSTRAINT TRIGGER folio_evidence AFTER INSERT OR UPDATE ON folios DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_evidence_guard();
CREATE CONSTRAINT TRIGGER guest_request_evidence AFTER INSERT OR UPDATE ON guest_requests DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_evidence_guard();
-- +goose Down
-- Retain guest, occupancy and financial history; rollback uses an application image.
SELECT 1;
