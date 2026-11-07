-- +goose Up
-- Section 9 quantity-level fragmentation and automatic waitlist offers.
-- Accepted reservation lines stay immutable: a quantity change supersedes the
-- source line and appends exact replacement lines, per-day stock provenance and
-- an immutable conservation ledger (original = remaining + transferred + released).
-- Database-generated UUIDv7 identities for trigger-written evidence rows.
-- +goose StatementBegin
CREATE FUNCTION business_uuidv7() RETURNS uuid LANGUAGE plpgsql VOLATILE AS $$
DECLARE t text;r text;
BEGIN t=lpad(to_hex(floor(extract(epoch FROM clock_timestamp())*1000)::bigint),12,'0');r=replace(gen_random_uuid()::text,'-','');RETURN (substring(t,1,8)||'-'||substring(t,9,4)||'-7'||substring(r,1,3)||'-'||substr('89ab',1+(get_byte(decode(substring(r,4,2),'hex'),0)%4),1)||substring(r,6,3)||'-'||substring(r,9,12))::uuid;END;$$;
-- +goose StatementEnd
CREATE TABLE reservation_quantity_changes (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,
 reservation_id uuid NOT NULL,target_reservation_id uuid NOT NULL,action text NOT NULL CHECK(action IN ('PARTIAL_CANCEL','SPLIT','MERGE')),
 source_room_ids uuid[] NOT NULL CHECK(cardinality(source_room_ids) BETWEEN 1 AND 2),family_root_room_id uuid NOT NULL,
 remaining_room_id uuid NOT NULL,transferred_room_id uuid,cancelled_room_id uuid,
 original_quantity integer NOT NULL CHECK(original_quantity>0),remaining_quantity integer NOT NULL CHECK(remaining_quantity>0),transferred_quantity integer NOT NULL CHECK(transferred_quantity>=0),released_quantity integer NOT NULL CHECK(released_quantity>=0),
 original_amount_minor bigint NOT NULL CHECK(original_amount_minor>=0),remaining_amount_minor bigint NOT NULL CHECK(remaining_amount_minor>=0),transferred_amount_minor bigint NOT NULL CHECK(transferred_amount_minor>=0),released_amount_minor bigint NOT NULL CHECK(released_amount_minor>=0),
 original_accommodation_minor bigint,remaining_accommodation_minor bigint,transferred_accommodation_minor bigint,released_accommodation_minor bigint,
 currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),source_version bigint NOT NULL CHECK(source_version>1),target_version bigint NOT NULL CHECK(target_version>0),
 approval_id uuid,request_digest text NOT NULL CHECK(request_digest~'^[a-f0-9]{64}$'),reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),
 created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(original_quantity=remaining_quantity+transferred_quantity+released_quantity),
 CHECK(original_amount_minor=remaining_amount_minor+transferred_amount_minor+released_amount_minor),
 CHECK((original_accommodation_minor IS NULL AND remaining_accommodation_minor IS NULL AND transferred_accommodation_minor IS NULL AND released_accommodation_minor IS NULL) OR (original_accommodation_minor IS NOT NULL AND remaining_accommodation_minor IS NOT NULL AND transferred_accommodation_minor IS NOT NULL AND released_accommodation_minor IS NOT NULL AND original_accommodation_minor=remaining_accommodation_minor+transferred_accommodation_minor+released_accommodation_minor)),
 CHECK(action<>'PARTIAL_CANCEL' OR (released_quantity>0 AND transferred_quantity=0 AND cancelled_room_id IS NOT NULL AND transferred_room_id IS NULL AND cardinality(source_room_ids)=1 AND target_reservation_id=reservation_id)),
 CHECK(action<>'SPLIT' OR (transferred_quantity>0 AND released_quantity=0 AND transferred_room_id IS NOT NULL AND cancelled_room_id IS NULL AND cardinality(source_room_ids)=1 AND approval_id IS NOT NULL)),
 CHECK(action<>'MERGE' OR (transferred_quantity=0 AND released_quantity=0 AND transferred_room_id IS NULL AND cancelled_room_id IS NULL AND cardinality(source_room_ids)=2 AND approval_id IS NOT NULL)),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),
 FOREIGN KEY(organisation_id,property_id,reservation_id) REFERENCES reservations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,property_id,target_reservation_id) REFERENCES reservations(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,family_root_room_id) REFERENCES reservation_rooms(organisation_id,id),
 FOREIGN KEY(organisation_id,remaining_room_id) REFERENCES reservation_rooms(organisation_id,id),
 FOREIGN KEY(organisation_id,transferred_room_id) REFERENCES reservation_rooms(organisation_id,id),
 FOREIGN KEY(organisation_id,cancelled_room_id) REFERENCES reservation_rooms(organisation_id,id),
 FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(reservation_id,source_version)
);
CREATE INDEX reservation_quantity_changes_scope ON reservation_quantity_changes(organisation_id,property_id,created_at,id);
CREATE INDEX reservation_quantity_changes_reservation ON reservation_quantity_changes(organisation_id,reservation_id,created_at);
CREATE INDEX reservation_quantity_changes_target ON reservation_quantity_changes(organisation_id,target_reservation_id,created_at);
CREATE TRIGGER reservation_quantity_changes_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_quantity_changes FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
CREATE TABLE reservation_quantity_change_days (
 organisation_id uuid NOT NULL,property_id uuid NOT NULL,change_id uuid NOT NULL,pool_id uuid NOT NULL,day date NOT NULL,
 original_quantity integer NOT NULL CHECK(original_quantity>0),remaining_quantity integer NOT NULL CHECK(remaining_quantity>=0),transferred_quantity integer NOT NULL CHECK(transferred_quantity>=0),released_quantity integer NOT NULL CHECK(released_quantity>=0),
 original_allotted integer NOT NULL DEFAULT 0 CHECK(original_allotted>=0),remaining_allotted integer NOT NULL DEFAULT 0 CHECK(remaining_allotted>=0),transferred_allotted integer NOT NULL DEFAULT 0 CHECK(transferred_allotted>=0),released_allotted integer NOT NULL DEFAULT 0 CHECK(released_allotted>=0),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(change_id,day),
 CHECK(original_quantity=remaining_quantity+transferred_quantity+released_quantity),
 CHECK(original_allotted=remaining_allotted+transferred_allotted+released_allotted AND remaining_allotted<=remaining_quantity AND transferred_allotted<=transferred_quantity AND released_allotted<=released_quantity),
 FOREIGN KEY(organisation_id,property_id,change_id) REFERENCES reservation_quantity_changes(organisation_id,property_id,id),
 FOREIGN KEY(organisation_id,pool_id,day) REFERENCES inventory_calendar(organisation_id,pool_id,day)
);
CREATE TRIGGER reservation_quantity_change_days_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_quantity_change_days FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- The ledger must describe the committed provenance that actually exists at
-- commit time: sources released, replacement lines backed by exactly the
-- recorded remaining/transferred quantities. A ledger row cannot be forged.
-- +goose StatementBegin
CREATE FUNCTION business_quantity_change_day_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c reservation_quantity_changes;
BEGIN
 SELECT * INTO STRICT c FROM reservation_quantity_changes WHERE organisation_id=NEW.organisation_id AND id=NEW.change_id;
 IF (SELECT coalesce(sum(d.quantity),0) FROM reservation_inventory_days d WHERE d.organisation_id=NEW.organisation_id AND d.room_id=ANY(c.source_room_ids) AND d.day=NEW.day)<>NEW.original_quantity
  OR EXISTS(SELECT 1 FROM reservation_inventory_days d WHERE d.organisation_id=NEW.organisation_id AND d.room_id=ANY(c.source_room_ids) AND d.day=NEW.day AND d.released_at IS NULL)
  OR NOT EXISTS(SELECT 1 FROM reservation_inventory_days d WHERE d.organisation_id=NEW.organisation_id AND d.room_id=c.remaining_room_id AND d.day=NEW.day AND d.quantity=NEW.remaining_quantity AND d.allotted_quantity=NEW.remaining_allotted AND d.pool_id=NEW.pool_id)
  OR (NEW.transferred_quantity>0 AND NOT EXISTS(SELECT 1 FROM reservation_inventory_days d WHERE d.organisation_id=NEW.organisation_id AND d.room_id=c.transferred_room_id AND d.day=NEW.day AND d.quantity=NEW.transferred_quantity AND d.allotted_quantity=NEW.transferred_allotted AND d.pool_id=NEW.pool_id))
  OR (NEW.transferred_quantity=0 AND c.transferred_room_id IS NOT NULL)
 THEN RAISE EXCEPTION 'Quantity change ledger must match exact committed reservation provenance' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER reservation_quantity_change_day_integrity AFTER INSERT ON reservation_quantity_change_days DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_quantity_change_day_guard();

-- Optional, independently approved per-property automation. MANUAL versions
-- reduce authority (pause/kill switch) and need no approval. Latest version wins.
CREATE TABLE waitlist_automation_policies (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,version bigint NOT NULL CHECK(version>0),
 mode text NOT NULL CHECK(mode IN ('AUTOMATIC_OFFERS','MANUAL')),channel text NOT NULL DEFAULT '' CHECK(length(channel)<=40),
 offer_seconds integer NOT NULL CHECK(offer_seconds BETWEEN 30 AND 300),max_offers_per_entry integer NOT NULL CHECK(max_offers_per_entry BETWEEN 1 AND 10),max_offers_per_run integer NOT NULL CHECK(max_offers_per_run BETWEEN 1 AND 50),
 approval_id uuid,authorizing_membership_id uuid NOT NULL,authorizing_role_id uuid NOT NULL,effective_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 reason text NOT NULL CHECK(length(reason) BETWEEN 5 AND 1000),created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(expires_at>effective_at AND expires_at<=effective_at+interval '366 days'),CHECK(mode='MANUAL' OR approval_id IS NOT NULL),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,approval_id) REFERENCES approval_requests(organisation_id,id),
 FOREIGN KEY(authorizing_membership_id) REFERENCES staff_memberships(id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id),UNIQUE(organisation_id,property_id,version)
);
CREATE TRIGGER waitlist_automation_policies_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON waitlist_automation_policies FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();

-- Durable release signals are written by the database for every producer
-- (cancellation, partial cancellation, early departure, capacity increase,
-- hold cancellation/expiry), so a release can never be missed by a caller.
CREATE TABLE inventory_release_signals (
 id uuid PRIMARY KEY DEFAULT business_uuidv7() CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,pool_id uuid NOT NULL,day date NOT NULL,
 source text NOT NULL CHECK(source IN ('CALENDAR_RELEASE','HOLD_RELEASE','MANUAL')),observed_at timestamptz NOT NULL DEFAULT now(),processed_at timestamptz,run_id uuid,
 FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id),FOREIGN KEY(organisation_id,pool_id,day) REFERENCES inventory_calendar(organisation_id,pool_id,day),
 CHECK((processed_at IS NULL)=(run_id IS NULL)),UNIQUE(organisation_id,id)
);
CREATE UNIQUE INDEX inventory_release_signal_pending ON inventory_release_signals(organisation_id,pool_id,day) WHERE processed_at IS NULL;
CREATE INDEX inventory_release_signal_work ON inventory_release_signals(organisation_id,property_id,observed_at) WHERE processed_at IS NULL;
-- +goose StatementBegin
CREATE FUNCTION business_release_signal_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Inventory release signals are retained'; END IF;
 IF OLD.processed_at IS NOT NULL OR NEW.processed_at IS NULL OR to_jsonb(NEW)-'processed_at'-'run_id' IS DISTINCT FROM to_jsonb(OLD)-'processed_at'-'run_id' THEN RAISE EXCEPTION 'A release signal is processed exactly once' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER inventory_release_signal_guard BEFORE UPDATE OR DELETE ON inventory_release_signals FOR EACH ROW EXECUTE FUNCTION business_release_signal_guard();
-- +goose StatementBegin
CREATE FUNCTION business_calendar_release_signal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.capacity-NEW.blocked-NEW.committed>OLD.capacity-OLD.blocked-OLD.committed THEN
  INSERT INTO inventory_release_signals(organisation_id,property_id,pool_id,day,source)
  SELECT NEW.organisation_id,p.property_id,NEW.pool_id,NEW.day,'CALENDAR_RELEASE' FROM inventory_pools p WHERE p.organisation_id=NEW.organisation_id AND p.id=NEW.pool_id
  ON CONFLICT (organisation_id,pool_id,day) WHERE processed_at IS NULL DO NOTHING;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER inventory_calendar_release_signal AFTER UPDATE ON inventory_calendar FOR EACH ROW EXECUTE FUNCTION business_calendar_release_signal();
-- +goose StatementBegin
CREATE FUNCTION business_hold_release_signal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.state='ACTIVE' AND NEW.state IN ('CANCELLED','EXPIRED') THEN
  INSERT INTO inventory_release_signals(organisation_id,property_id,pool_id,day,source)
  SELECT d.organisation_id,NEW.property_id,d.pool_id,d.day,'HOLD_RELEASE' FROM inventory_hold_days d WHERE d.organisation_id=NEW.organisation_id AND d.hold_id=NEW.id
  ON CONFLICT (organisation_id,pool_id,day) WHERE processed_at IS NULL DO NOTHING;
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER inventory_hold_release_signal AFTER UPDATE ON inventory_holds FOR EACH ROW EXECUTE FUNCTION business_hold_release_signal();

-- One worker lease per property, fenced by a monotonically increasing
-- generation. A restarted or duplicate worker reclaims only expired leases and
-- every write re-checks the current owner/generation inside its transaction.
CREATE TABLE waitlist_automation_leases (
 organisation_id uuid NOT NULL,property_id uuid NOT NULL,lease_owner text,lease_generation bigint NOT NULL DEFAULT 0 CHECK(lease_generation>=0),lease_expires_at timestamptz,
 last_completed_at timestamptz,run_requested_at timestamptz,updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(organisation_id,property_id),FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),
 CHECK((lease_owner IS NULL)=(lease_expires_at IS NULL))
);
-- +goose StatementBegin
CREATE FUNCTION business_waitlist_lease_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Waitlist automation leases are retained'; END IF;
 IF TG_OP='UPDATE' AND (NEW.lease_generation<OLD.lease_generation OR NEW.lease_generation>OLD.lease_generation+1 OR (NEW.lease_generation=OLD.lease_generation+1 AND NEW.lease_owner IS NULL)) THEN RAISE EXCEPTION 'Lease generations advance monotonically by claim' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER waitlist_automation_lease_guard BEFORE UPDATE OR DELETE ON waitlist_automation_leases FOR EACH ROW EXECUTE FUNCTION business_waitlist_lease_guard();
CREATE TABLE waitlist_automation_runs (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,policy_id uuid,policy_version bigint,
 lease_owner text NOT NULL,lease_generation bigint NOT NULL CHECK(lease_generation>0),trigger text NOT NULL CHECK(trigger IN ('RELEASE_SIGNAL','NEW_ENTRY','MANUAL','SWEEP')),
 state text NOT NULL CHECK(state IN ('OFFERED','NO_ELIGIBLE_CANDIDATE','POLICY_INACTIVE','AUTHORITY_REVOKED','PROPERTY_INELIGIBLE')),
 offers_created integer NOT NULL DEFAULT 0 CHECK(offers_created>=0),signals_processed integer NOT NULL DEFAULT 0 CHECK(signals_processed>=0),evaluations jsonb NOT NULL CHECK(jsonb_typeof(evaluations)='array' AND octet_length(evaluations::text)<=262144),
 created_by_service text NOT NULL CHECK(created_by_service='business-operations-service'),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id) REFERENCES properties(organisation_id,id),FOREIGN KEY(organisation_id,policy_id) REFERENCES waitlist_automation_policies(organisation_id,id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,property_id,id)
);
CREATE INDEX waitlist_automation_runs_scope ON waitlist_automation_runs(organisation_id,property_id,created_at,id);
CREATE TRIGGER waitlist_automation_runs_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON waitlist_automation_runs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
ALTER TABLE inventory_release_signals ADD CONSTRAINT inventory_release_signal_run FOREIGN KEY(organisation_id,run_id) REFERENCES waitlist_automation_runs(organisation_id,id);
-- Automatic offers are ordinary authoritative waitlist offers plus attribution.
-- attempt_number gives deterministic fairness: fewer prior automatic offers first.
CREATE TABLE waitlist_automation_offers (
 offer_id uuid PRIMARY KEY,organisation_id uuid NOT NULL,property_id uuid NOT NULL,entry_id uuid NOT NULL,run_id uuid NOT NULL,policy_id uuid NOT NULL,policy_version bigint NOT NULL CHECK(policy_version>0),
 attempt_number integer NOT NULL CHECK(attempt_number BETWEEN 1 AND 10),lease_generation bigint NOT NULL CHECK(lease_generation>0),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,offer_id) REFERENCES waitlist_offers(organisation_id,id),FOREIGN KEY(organisation_id,entry_id) REFERENCES waitlist_entries(organisation_id,id),
 FOREIGN KEY(organisation_id,run_id) REFERENCES waitlist_automation_runs(organisation_id,id),FOREIGN KEY(organisation_id,policy_id) REFERENCES waitlist_automation_policies(organisation_id,id),
 UNIQUE(organisation_id,entry_id,attempt_number)
);
CREATE TRIGGER waitlist_automation_offers_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON waitlist_automation_offers FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- At most one live offer per waitlist entry, whoever created it.
CREATE UNIQUE INDEX waitlist_offer_single_live ON waitlist_offers(organisation_id,entry_id) WHERE state='OFFERED';

-- +goose Down
DROP INDEX IF EXISTS waitlist_offer_single_live;
DROP TABLE IF EXISTS waitlist_automation_offers;
ALTER TABLE IF EXISTS inventory_release_signals DROP CONSTRAINT IF EXISTS inventory_release_signal_run;
DROP TABLE IF EXISTS waitlist_automation_runs;
DROP TABLE IF EXISTS waitlist_automation_leases;
DROP FUNCTION IF EXISTS business_waitlist_lease_guard();
DROP TRIGGER IF EXISTS inventory_hold_release_signal ON inventory_holds;
DROP FUNCTION IF EXISTS business_hold_release_signal();
DROP TRIGGER IF EXISTS inventory_calendar_release_signal ON inventory_calendar;
DROP FUNCTION IF EXISTS business_calendar_release_signal();
DROP TABLE IF EXISTS inventory_release_signals;
DROP FUNCTION IF EXISTS business_release_signal_guard();
DROP TABLE IF EXISTS waitlist_automation_policies;
DROP TABLE IF EXISTS reservation_quantity_change_days;
DROP FUNCTION IF EXISTS business_quantity_change_day_guard();
DROP TABLE IF EXISTS reservation_quantity_changes;
DROP FUNCTION IF EXISTS business_uuidv7();
