-- +goose Up
-- Group blocks reference canonical held/committed room lines. Their quantity
-- never creates a second availability calendar or an invented channel allotment.
CREATE TABLE group_blocks (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,group_id uuid NOT NULL,reservation_id uuid NOT NULL,reservation_room_id uuid NOT NULL,
 quantity integer NOT NULL CHECK(quantity>0),date_from date NOT NULL,date_to date NOT NULL CHECK(date_to>date_from),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,group_id) REFERENCES reservation_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_room_id) REFERENCES reservation_rooms(organisation_id,property_id,id),UNIQUE(reservation_room_id),UNIQUE(organisation_id,id)
);
CREATE TABLE rooming_lists (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,group_id uuid NOT NULL,reservation_id uuid NOT NULL,reservation_room_id uuid NOT NULL,slot integer NOT NULL CHECK(slot>0),guest_id uuid NOT NULL,state text NOT NULL CHECK(state IN ('ACTIVE','REVOKED')),version bigint NOT NULL CHECK(version>0),approval_id uuid,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,group_id) REFERENCES reservation_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_room_id) REFERENCES reservation_rooms(organisation_id,property_id,id),FOREIGN KEY(organisation_id,guest_id,property_id) REFERENCES guest_property_scopes(organisation_id,guest_id,property_id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),UNIQUE(group_id,reservation_room_id,slot,version),CHECK(state<>'ACTIVE' OR approval_id IS NOT NULL)
);
CREATE INDEX rooming_current ON rooming_lists(organisation_id,property_id,group_id,reservation_room_id,slot,version DESC);
CREATE TABLE master_folios (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,group_id uuid NOT NULL,currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),billing_reference text NOT NULL,corporate_id uuid,corporate_contract_id uuid,approval_id uuid NOT NULL,reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,group_id) REFERENCES reservation_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,corporate_id,corporate_contract_id) REFERENCES corporate_customer_contracts(organisation_id,property_id,corporate_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),CHECK((corporate_id IS NULL)=(corporate_contract_id IS NULL)),UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE TABLE master_folio_members (
 organisation_id uuid NOT NULL,property_id uuid NOT NULL,master_folio_id uuid NOT NULL,folio_id uuid NOT NULL,folio_version bigint NOT NULL CHECK(folio_version>0),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,master_folio_id) REFERENCES master_folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),PRIMARY KEY(master_folio_id,folio_id),UNIQUE(folio_id)
);
CREATE TABLE event_bookings (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,group_id uuid NOT NULL,reservation_id uuid NOT NULL,facility_id uuid NOT NULL,source_reference text NOT NULL,conversation_reference text NOT NULL DEFAULT '',created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,group_id) REFERENCES reservation_groups(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,facility_id) REFERENCES facilities(organisation_id,id),UNIQUE(reservation_id),UNIQUE(organisation_id,id)
);
CREATE TRIGGER group_block_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON group_blocks FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER rooming_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON rooming_lists FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER master_folio_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON master_folios FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER master_member_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON master_folio_members FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER event_booking_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON event_bookings FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_group_binding_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM reservations r WHERE r.organisation_id=NEW.organisation_id AND r.property_id=NEW.property_id AND r.id=NEW.reservation_id AND r.group_id=NEW.group_id) THEN RAISE EXCEPTION 'Exact canonical reservation/group binding required' USING ERRCODE='23514';END IF;
 IF TG_TABLE_NAME IN ('group_blocks','rooming_lists') AND NOT EXISTS(SELECT 1 FROM reservation_rooms rr WHERE rr.organisation_id=NEW.organisation_id AND rr.id=(to_jsonb(NEW)->>'reservation_room_id')::uuid AND rr.reservation_id=NEW.reservation_id AND rr.state='ACTIVE' AND (TG_TABLE_NAME<>'rooming_lists' OR (to_jsonb(NEW)->>'slot')::integer<=rr.quantity) AND (TG_TABLE_NAME<>'group_blocks' OR (rr.quantity=(to_jsonb(NEW)->>'quantity')::integer AND rr.date_from=(to_jsonb(NEW)->>'date_from')::date AND rr.date_to=(to_jsonb(NEW)->>'date_to')::date))) THEN RAISE EXCEPTION 'Group scope must fit its immutable canonical room line' USING ERRCODE='23514';END IF;
 RETURN NEW; END $$;
-- +goose StatementEnd
CREATE TRIGGER group_block_binding BEFORE INSERT ON group_blocks FOR EACH ROW EXECUTE FUNCTION business_group_binding_guard();
CREATE TRIGGER rooming_binding BEFORE INSERT ON rooming_lists FOR EACH ROW EXECUTE FUNCTION business_group_binding_guard();
CREATE TRIGGER event_group_binding BEFORE INSERT ON event_bookings FOR EACH ROW EXECUTE FUNCTION business_group_binding_guard();
-- +goose StatementBegin
CREATE FUNCTION business_group_master_member() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM master_folios m JOIN folios f ON f.organisation_id=m.organisation_id AND f.property_id=m.property_id JOIN reservations r ON r.organisation_id=f.organisation_id AND r.id=f.reservation_id WHERE m.organisation_id=NEW.organisation_id AND m.id=NEW.master_folio_id AND f.id=NEW.folio_id AND f.currency=m.currency AND f.version=NEW.folio_version AND r.group_id=m.group_id AND (m.corporate_id IS NULL OR r.corporate_id=m.corporate_id AND r.corporate_contract_id=m.corporate_contract_id)) THEN RAISE EXCEPTION 'Master folio binds exact current same-group/currency/payer obligations only' USING ERRCODE='23514';END IF;RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER master_member_binding BEFORE INSERT ON master_folio_members FOR EACH ROW EXECUTE FUNCTION business_group_master_member();
-- +goose StatementBegin
CREATE FUNCTION business_group_evidence_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM commercial_history h WHERE h.organisation_id=NEW.organisation_id AND h.property_id=NEW.property_id AND h.entity_id=NEW.id AND h.entity_type='reservation_group' AND h.version=NEW.version) THEN RAISE EXCEPTION 'Group transition requires its immutable commercial audit in the owning transaction' USING ERRCODE='23514';END IF;RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER group_transition_evidence AFTER INSERT OR UPDATE ON reservation_groups DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_group_evidence_guard();
CREATE TRIGGER group_identity BEFORE UPDATE ON reservation_groups FOR EACH ROW EXECUTE FUNCTION business_hospitality_identity_guard();
CREATE TRIGGER group_retention BEFORE DELETE OR TRUNCATE ON reservation_groups FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose Down
SELECT 1;
