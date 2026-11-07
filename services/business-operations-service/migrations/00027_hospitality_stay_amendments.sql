-- +goose Up
-- Section 10 in-house amendments. Extensions add new canonical stock lines and
-- immutable quotes; early departures release unused unit nights through an
-- append-only ledger. Accepted prices, folio postings and stock provenance are
-- never rewritten. Payment/Admin remain the authority for refunds and money.
CREATE TABLE stay_extensions (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,reservation_id uuid NOT NULL,stay_id uuid NOT NULL,
 previous_assignment_id uuid NOT NULL,assignment_id uuid NOT NULL,source_room_id uuid NOT NULL,slot integer NOT NULL CHECK(slot BETWEEN 1 AND 1000),extension_room_id uuid NOT NULL,quote_id uuid NOT NULL,
 date_from date NOT NULL,date_to date NOT NULL CHECK(date_to>date_from),amount_minor bigint NOT NULL CHECK(amount_minor>=0),currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 payer_mode text NOT NULL CHECK(payer_mode IN ('RESERVATION_TERMS','CORPORATE','SPLIT')),folio_id uuid NOT NULL,folio_item_id uuid NOT NULL,approval_id uuid,reservation_version bigint NOT NULL CHECK(reservation_version>0),
 command_digest text NOT NULL CHECK(command_digest~'^[a-f0-9]{64}$'),reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,stay_id) REFERENCES stay_records(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,previous_assignment_id) REFERENCES room_assignments(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,assignment_id) REFERENCES room_assignments(organisation_id,property_id,id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(organisation_id,property_id,source_room_id) REFERENCES reservation_rooms(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,extension_room_id) REFERENCES reservation_rooms(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,quote_id) REFERENCES price_quotes(organisation_id,id),FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,folio_item_id) REFERENCES folio_items(organisation_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),CHECK(source_room_id<>extension_room_id),CHECK(previous_assignment_id<>assignment_id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(extension_room_id),UNIQUE(folio_item_id),UNIQUE(source_room_id,slot,date_from)
);
CREATE INDEX stay_extension_stay ON stay_extensions(organisation_id,stay_id,date_from,id);
CREATE TABLE stay_extension_payers (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,extension_id uuid NOT NULL,payer_kind text NOT NULL CHECK(payer_kind IN ('GUEST','CORPORATE')),
 corporate_customer_id uuid,corporate_contract_id uuid,financial_group_id uuid NOT NULL,amount_minor bigint NOT NULL CHECK(amount_minor>0),currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),responsibility text NOT NULL CHECK(responsibility IN ('PLATFORM_COLLECT','PAY_AT_PROPERTY','CORPORATE_CREDIT','CHANNEL_COLLECT')),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,extension_id) REFERENCES stay_extensions(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,financial_group_id) REFERENCES reservation_financial_groups(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,corporate_customer_id) REFERENCES corporate_customer_profiles(organisation_id,id),FOREIGN KEY(organisation_id,corporate_contract_id) REFERENCES corporate_customer_contracts(organisation_id,id),
 CHECK((payer_kind='CORPORATE')=(corporate_customer_id IS NOT NULL AND corporate_contract_id IS NOT NULL)),CHECK(payer_kind='GUEST' OR responsibility='CORPORATE_CREDIT'),
 UNIQUE(organisation_id,id),UNIQUE(financial_group_id),UNIQUE(extension_id,payer_kind)
);
CREATE TABLE reservation_unit_releases (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,reservation_id uuid NOT NULL,room_id uuid NOT NULL,slot integer NOT NULL CHECK(slot BETWEEN 1 AND 1000),stay_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('EARLY_DEPARTURE','PARTIAL_CHECKOUT')),date_from date NOT NULL,date_to date NOT NULL CHECK(date_to>date_from),nights integer NOT NULL CHECK(nights>0),reason text NOT NULL,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,room_id) REFERENCES reservation_rooms(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,stay_id) REFERENCES stay_records(organisation_id,property_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(room_id,slot)
);
-- One row per released unit night proves conservation for every date:
-- accepted quantity = remaining committed quantity + released unit nights.
CREATE TABLE reservation_unit_release_days (
 organisation_id uuid NOT NULL,property_id uuid NOT NULL,release_id uuid NOT NULL,room_id uuid NOT NULL,pool_id uuid NOT NULL,day date NOT NULL,slot integer NOT NULL CHECK(slot BETWEEN 1 AND 1000),
 quantity_before integer NOT NULL CHECK(quantity_before>0),quantity_after integer NOT NULL CHECK(quantity_after=quantity_before-1),created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(room_id,day,slot),FOREIGN KEY(organisation_id,property_id,release_id) REFERENCES reservation_unit_releases(organisation_id,property_id,id),FOREIGN KEY(room_id,day) REFERENCES reservation_inventory_days(room_id,day),
 FOREIGN KEY(organisation_id,pool_id,day) REFERENCES inventory_calendar(organisation_id,pool_id,day)
);
CREATE INDEX reservation_unit_release_pool_day ON reservation_unit_release_days(organisation_id,pool_id,day);
CREATE TABLE stay_early_departures (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,reservation_id uuid NOT NULL,stay_id uuid NOT NULL,folio_id uuid NOT NULL,
 departure_day date NOT NULL,departed_at timestamptz NOT NULL,unused_nights integer NOT NULL CHECK(unused_nights>0),currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 unused_minor bigint NOT NULL CHECK(unused_minor>=0),penalty_minor bigint NOT NULL CHECK(penalty_minor>=0 AND penalty_minor<=unused_minor),credit_minor bigint NOT NULL CHECK(credit_minor=unused_minor-penalty_minor),
 platform_collected_minor bigint CHECK(platform_collected_minor>=0),refundable_minor bigint CHECK(refundable_minor>=0 AND refundable_minor<=credit_minor),
 financial_state text NOT NULL CHECK(financial_state IN ('NO_CREDIT','FOLIO_CREDIT_ONLY','REFUND_PENDING_EXTERNAL','READBACK_REQUIRED','EXTERNAL_COLLECTION_UNKNOWN')),
 policy_snapshot jsonb NOT NULL CHECK(jsonb_typeof(policy_snapshot)='object'),line_effects jsonb NOT NULL CHECK(jsonb_typeof(line_effects)='array'),approval_id uuid NOT NULL,reservation_version bigint NOT NULL CHECK(reservation_version>0),
 reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),FOREIGN KEY(organisation_id,property_id,stay_id) REFERENCES stay_records(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,folio_id) REFERENCES folios(organisation_id,property_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 CHECK((financial_state='REFUND_PENDING_EXTERNAL')=(coalesce(refundable_minor,0)>0)),CHECK(financial_state NOT IN ('READBACK_REQUIRED','EXTERNAL_COLLECTION_UNKNOWN') OR refundable_minor IS NULL),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(stay_id)
);
CREATE INDEX stay_early_departure_scope ON stay_early_departures(organisation_id,property_id,financial_state,created_at,id);
CREATE TRIGGER stay_extensions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON stay_extensions FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER stay_extension_payers_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON stay_extension_payers FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER reservation_unit_releases_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_unit_releases FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER reservation_unit_release_days_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_unit_release_days FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER stay_early_departures_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON stay_early_departures FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE CONSTRAINT TRIGGER stay_extension_evidence AFTER INSERT ON stay_extensions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('stay_extension');
CREATE CONSTRAINT TRIGGER stay_early_departure_evidence AFTER INSERT ON stay_early_departures DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('stay_early_departure');
CREATE CONSTRAINT TRIGGER reservation_unit_release_evidence AFTER INSERT ON reservation_unit_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_hospitality_automation_evidence('reservation_unit_release');

-- +goose StatementBegin
-- Checked at commit: an extension continues one exact unit slot with a contiguous, same-pool,
-- single-unit canonical line of the same reservation.
CREATE FUNCTION business_stay_extension_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source reservation_rooms;extension reservation_rooms;chain_end date;BEGIN
 PERFORM id FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 SELECT * INTO source FROM reservation_rooms WHERE organisation_id=NEW.organisation_id AND id=NEW.source_room_id;
 SELECT * INTO extension FROM reservation_rooms WHERE organisation_id=NEW.organisation_id AND id=NEW.extension_room_id;
 SELECT coalesce(max(x.date_to),source.date_to) INTO chain_end FROM stay_extensions x WHERE x.organisation_id=NEW.organisation_id AND x.source_room_id=NEW.source_room_id AND x.slot=NEW.slot AND x.id<>NEW.id;
 IF source.reservation_id<>NEW.reservation_id OR extension.reservation_id<>NEW.reservation_id OR NEW.slot>source.quantity OR extension.quantity<>1 OR extension.pool_id<>source.pool_id OR extension.state<>'ACTIVE' OR extension.date_from<>NEW.date_from OR extension.date_to<>NEW.date_to OR extension.amount_minor<>NEW.amount_minor OR extension.currency<>NEW.currency OR extension.quote_id<>NEW.quote_id OR NEW.date_from<>chain_end
  OR NOT EXISTS(SELECT 1 FROM stay_records s WHERE s.organisation_id=NEW.organisation_id AND s.id=NEW.stay_id AND s.reservation_id=NEW.reservation_id AND s.assignment_id=NEW.assignment_id)
  OR NOT EXISTS(SELECT 1 FROM room_assignments a WHERE a.organisation_id=NEW.organisation_id AND a.id=NEW.assignment_id AND a.reservation_room_id=NEW.source_room_id AND a.slot=NEW.slot AND a.date_to=NEW.date_to)
  OR NOT EXISTS(SELECT 1 FROM folio_items i JOIN folios f ON f.organisation_id=i.organisation_id AND f.id=i.folio_id WHERE i.organisation_id=NEW.organisation_id AND i.id=NEW.folio_item_id AND f.id=NEW.folio_id AND f.stay_id=NEW.stay_id AND i.kind='ACCOMMODATION' AND i.amount_minor=NEW.amount_minor AND (i.policy_snapshot->>'reservation_room_id')::uuid=NEW.extension_room_id) THEN
  RAISE EXCEPTION 'An extension must continue its exact unit, stay, physical assignment and folio with contiguous same-pool canonical stock' USING ERRCODE='23514';END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER stay_extension_guard AFTER INSERT ON stay_extensions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stay_extension_guard();
-- +goose StatementBegin
CREATE FUNCTION business_stay_extension_payer_conservation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM stay_extensions x WHERE x.organisation_id=NEW.organisation_id AND x.id=NEW.extension_id AND x.amount_minor=(SELECT coalesce(sum(p.amount_minor),0) FROM stay_extension_payers p WHERE p.organisation_id=x.organisation_id AND p.extension_id=x.id))
  OR EXISTS(SELECT 1 FROM stay_extension_payers p JOIN reservation_financial_groups g ON g.organisation_id=p.organisation_id AND g.id=p.financial_group_id JOIN stay_extensions x ON x.organisation_id=p.organisation_id AND x.id=p.extension_id WHERE p.organisation_id=NEW.organisation_id AND p.extension_id=NEW.extension_id AND (g.amount_minor<>p.amount_minor OR g.currency<>p.currency OR g.reservation_id<>x.reservation_id OR g.responsibility<>p.responsibility)) THEN
  RAISE EXCEPTION 'Extension payer obligations must conserve the exact accepted extension amount' USING ERRCODE='23514';END IF;
 RETURN NULL;END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER stay_extension_payer_conservation AFTER INSERT ON stay_extension_payers DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stay_extension_payer_conservation();
-- +goose StatementBegin
CREATE FUNCTION business_unit_release_day_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r reservation_unit_releases;line reservation_rooms;released integer;BEGIN
 PERFORM id FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 SELECT * INTO r FROM reservation_unit_releases WHERE organisation_id=NEW.organisation_id AND id=NEW.release_id;
 SELECT * INTO line FROM reservation_rooms WHERE organisation_id=NEW.organisation_id AND id=NEW.room_id;
 SELECT count(*) INTO released FROM reservation_unit_release_days WHERE organisation_id=NEW.organisation_id AND room_id=NEW.room_id AND day=NEW.day;
 IF r.room_id<>NEW.room_id OR r.slot<>NEW.slot OR NEW.day<r.date_from OR NEW.day>=r.date_to OR line.pool_id<>NEW.pool_id OR NEW.slot>line.quantity OR NEW.quantity_before<>line.quantity-released THEN
  RAISE EXCEPTION 'A unit night release must name its exact accepted line, slot and current remaining quantity' USING ERRCODE='23514';END IF;
 RETURN NEW;END $$;
-- +goose StatementEnd
CREATE TRIGGER unit_release_day_guard BEFORE INSERT ON reservation_unit_release_days FOR EACH ROW EXECUTE FUNCTION business_unit_release_day_guard();

-- +goose StatementBegin
-- Original guard plus a ledger-proved unit release: quantity (and any channel
-- allotment share) may only decrease to the accepted quantity minus released
-- unit nights. All other identity fields stay immutable and releases one-way.
CREATE OR REPLACE FUNCTION business_reservation_days_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM 1 FROM organisations WHERE id=coalesce(NEW.organisation_id,OLD.organisation_id) FOR UPDATE;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Reservation inventory history is retained';END IF;
 IF TG_OP='INSERT' AND NOT EXISTS(SELECT 1 FROM reservation_rooms r WHERE r.organisation_id=NEW.organisation_id AND r.property_id=NEW.property_id AND r.id=NEW.room_id AND r.pool_id=NEW.pool_id AND r.state='ACTIVE' AND r.quantity=NEW.quantity AND NEW.day>=r.date_from AND NEW.day<r.date_to) THEN RAISE EXCEPTION 'Committed provenance must match its exact accepted room, pool, quantity and dates' USING ERRCODE='23514';END IF;
 IF TG_OP='UPDATE' THEN
  IF OLD.released_at IS NOT NULL OR to_jsonb(NEW)-'released_at'-'quantity'-'allotted_quantity' IS DISTINCT FROM to_jsonb(OLD)-'released_at'-'quantity'-'allotted_quantity' THEN RAISE EXCEPTION 'Only one-way reservation inventory release is permitted' USING ERRCODE='23514';END IF;
  IF NEW.quantity<>OLD.quantity OR NEW.allotted_quantity<>OLD.allotted_quantity THEN
   IF NEW.released_at IS NOT NULL OR NEW.quantity>=OLD.quantity OR NEW.allotted_quantity<>least(OLD.allotted_quantity,NEW.quantity) OR NEW.quantity<>(SELECT r.quantity FROM reservation_rooms r WHERE r.organisation_id=NEW.organisation_id AND r.id=NEW.room_id)-(SELECT count(*) FROM reservation_unit_release_days u WHERE u.organisation_id=NEW.organisation_id AND u.room_id=NEW.room_id AND u.day=NEW.day) THEN
    RAISE EXCEPTION 'A unit release must be proved by its immutable release ledger' USING ERRCODE='23514';END IF;
  ELSIF NEW.released_at IS NULL THEN RAISE EXCEPTION 'Only one-way reservation inventory release is permitted' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
-- Original physical assignment guard; an assignment may also cover the exact
-- contiguous extension chain recorded for its unit slot.
CREATE OR REPLACE FUNCTION business_hospitality_assignment_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='UPDATE' AND (NEW.organisation_id,NEW.property_id,NEW.reservation_id,NEW.reservation_room_id,NEW.room_id,NEW.slot,NEW.date_from,NEW.date_to) IS DISTINCT FROM (OLD.organisation_id,OLD.property_id,OLD.reservation_id,OLD.reservation_room_id,OLD.room_id,OLD.slot,OLD.date_from,OLD.date_to) THEN RAISE EXCEPTION 'Physical assignment identity is immutable; release and append a move' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM reservation_rooms r WHERE r.organisation_id=NEW.organisation_id AND r.id=NEW.reservation_room_id AND r.reservation_id=NEW.reservation_id AND r.property_id=NEW.property_id AND NEW.slot<=r.quantity AND NEW.date_from>=r.date_from AND NEW.date_to<=greatest(r.date_to,coalesce((SELECT max(x.date_to) FROM stay_extensions x WHERE x.organisation_id=r.organisation_id AND x.source_room_id=r.id AND x.slot=NEW.slot),r.date_to))) THEN RAISE EXCEPTION 'Assignment must fit canonical room quantity and dates' USING ERRCODE='23514';END IF;
 RETURN NEW; END $$;
-- +goose StatementEnd

-- An in-house extension is a supported offline front-desk replay command.
ALTER TABLE offline_operation_receipts DROP CONSTRAINT IF EXISTS offline_operation_receipts_action_check;
ALTER TABLE offline_operation_receipts ADD CONSTRAINT offline_operation_receipts_action_check CHECK(action IN ('FOLIO_POST','ASSIGN_ROOM','CHECK_IN','EXTEND_STAY'));

-- +goose Down
-- Retain occupancy, stock and financial amendment history; rollback uses an
-- application image that still understands these records.
SELECT 1;
