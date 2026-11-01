-- +goose Up
-- Additive exception accounting. Physical capacity is unchanged. Existing rows
-- retain overbooked=0 and therefore exactly their previous stock constraint.
-- Goose applies this replacement atomically; no table or data is discarded.
ALTER TABLE inventory_calendar ADD COLUMN overbooked integer NOT NULL DEFAULT 0 CHECK(overbooked>=0 AND overbooked<=committed);
-- +goose StatementBegin
DO $$ DECLARE old_name text; BEGIN
 SELECT conname INTO STRICT old_name FROM pg_constraint WHERE conrelid='inventory_calendar'::regclass AND contype='c' AND regexp_replace(pg_get_expr(conbin,conrelid),'[[:space:]()]','','g')='blocked+committed<=capacity';
 EXECUTE format('ALTER TABLE inventory_calendar DROP CONSTRAINT %I',old_name);
END $$;
-- +goose StatementEnd
ALTER TABLE inventory_calendar ADD CONSTRAINT inventory_calendar_physical_commitment CHECK(blocked+committed<=capacity+overbooked);
ALTER TABLE reservation_overbooking_policies ADD COLUMN pool_id uuid;
ALTER TABLE reservation_overbooking_policies ADD CONSTRAINT overbooking_policy_pool FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id);
CREATE UNIQUE INDEX overbooking_policy_active_pool ON reservation_overbooking_policies(organisation_id,pool_id) WHERE state='ACTIVE' AND pool_id IS NOT NULL;
ALTER TABLE reservation_overbooking_permits ADD COLUMN policy_version bigint NOT NULL DEFAULT 0 CHECK(policy_version>=0),ADD COLUMN policy_snapshot jsonb NOT NULL DEFAULT '{}';
ALTER TABLE reservation_inventory_days ADD COLUMN overbooked_quantity integer NOT NULL DEFAULT 0 CHECK(overbooked_quantity>=0 AND overbooked_quantity<=quantity),ADD COLUMN overbooking_permit_id uuid;
ALTER TABLE reservation_inventory_days ADD CONSTRAINT reservation_day_overbooking_permit FOREIGN KEY(organisation_id,property_id,overbooking_permit_id) REFERENCES reservation_overbooking_permits(organisation_id,property_id,id),ADD CONSTRAINT reservation_day_overbooking_basis CHECK(overbooked_quantity=0 OR overbooking_permit_id IS NOT NULL);
CREATE TABLE reservation_overbooking_releases (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'),organisation_id uuid NOT NULL,property_id uuid NOT NULL,room_id uuid NOT NULL,pool_id uuid NOT NULL,day date NOT NULL,quantity integer NOT NULL CHECK(quantity>0),kind text NOT NULL CHECK(kind IN ('CONVERTED','CANCELLED')),reason text NOT NULL,
 created_by uuid REFERENCES business_accounts(id),created_by_service text NOT NULL DEFAULT coalesce(nullif(current_setting('business.actor_service',true),''),''),created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(organisation_id,property_id,room_id) REFERENCES reservation_rooms(organisation_id,property_id,id),FOREIGN KEY(organisation_id,pool_id,day) REFERENCES inventory_calendar(organisation_id,pool_id,day),FOREIGN KEY(room_id,day) REFERENCES reservation_inventory_days(room_id,day),CHECK(created_by IS NOT NULL OR created_by_service='business-operations-service'),UNIQUE(organisation_id,id)
);
ALTER TABLE reservation_inventory_days ADD CONSTRAINT reservation_day_property_pool FOREIGN KEY(organisation_id,property_id,pool_id) REFERENCES inventory_pools(organisation_id,property_id,id);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION business_reservation_days_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM 1 FROM organisations WHERE id=coalesce(NEW.organisation_id,OLD.organisation_id) FOR UPDATE;
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Reservation inventory history is retained';END IF;
 IF TG_OP='INSERT' AND NOT EXISTS(SELECT 1 FROM reservation_rooms r WHERE r.organisation_id=NEW.organisation_id AND r.property_id=NEW.property_id AND r.id=NEW.room_id AND r.pool_id=NEW.pool_id AND r.state='ACTIVE' AND r.quantity=NEW.quantity AND NEW.day>=r.date_from AND NEW.day<r.date_to) THEN RAISE EXCEPTION 'Committed provenance must match its exact accepted room, pool, quantity and dates' USING ERRCODE='23514';END IF;
 IF TG_OP='UPDATE' AND (to_jsonb(NEW)-'released_at' IS DISTINCT FROM to_jsonb(OLD)-'released_at' OR OLD.released_at IS NOT NULL OR NEW.released_at IS NULL) THEN RAISE EXCEPTION 'Only one-way reservation inventory release is permitted' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE INDEX overbooking_release_day ON reservation_overbooking_releases(organisation_id,room_id,day);
CREATE TRIGGER overbooking_release_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON reservation_overbooking_releases FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
-- +goose StatementBegin
CREATE FUNCTION business_overbooking_origin_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p reservation_overbooking_permits;r reservation_rooms;c inventory_calendar;
BEGIN
 IF NEW.overbooking_permit_id IS NULL THEN RETURN NEW;END IF;
 SELECT * INTO STRICT p FROM reservation_overbooking_permits WHERE organisation_id=NEW.organisation_id AND property_id=NEW.property_id AND id=NEW.overbooking_permit_id;
 SELECT * INTO STRICT r FROM reservation_rooms WHERE organisation_id=NEW.organisation_id AND id=NEW.room_id;
 SELECT * INTO STRICT c FROM inventory_calendar WHERE organisation_id=NEW.organisation_id AND pool_id=NEW.pool_id AND day=NEW.day FOR UPDATE;
 IF p.policy_version<1 OR p.expires_at<=now() OR p.reservation_id<>r.reservation_id OR NOT EXISTS(SELECT 1 FROM reservation_overbooking_policies policy WHERE policy.organisation_id=p.organisation_id AND policy.id=p.policy_id AND policy.pool_id=NEW.pool_id AND policy.version=p.policy_version AND policy.definition=p.policy_snapshot AND policy.state='ACTIVE') OR NOT EXISTS(SELECT 1 FROM approval_requests a WHERE a.organisation_id=p.organisation_id AND a.id=p.approval_id AND a.state='APPROVED' AND a.consumed_at IS NOT NULL AND a.consumed_by_account_id=p.created_by AND a.scope_kind='PROPERTY' AND a.scope_id=p.property_id AND a.action='COMMERCIAL_RESERVATION_OVERBOOKING') OR NEW.overbooked_quantity>p.quantity OR p.quantity>least((p.policy_snapshot->>'max_quantity')::integer,(c.capacity::bigint*(p.policy_snapshot->>'max_capacity_bps')::bigint/10000)::integer) OR c.overbooked>least((p.policy_snapshot->>'max_quantity')::integer,(c.capacity::bigint*(p.policy_snapshot->>'max_capacity_bps')::bigint/10000)::integer) THEN RAISE EXCEPTION 'Approved exact overbooking provenance required' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER overbooking_origin_guard BEFORE INSERT ON reservation_inventory_days FOR EACH ROW EXECUTE FUNCTION business_overbooking_origin_guard();
-- +goose StatementBegin
CREATE FUNCTION business_overbooking_release_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE original integer;used bigint;
BEGIN
 PERFORM 1 FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 SELECT overbooked_quantity INTO STRICT original FROM reservation_inventory_days WHERE organisation_id=NEW.organisation_id AND property_id=NEW.property_id AND room_id=NEW.room_id AND pool_id=NEW.pool_id AND day=NEW.day;
 SELECT coalesce(sum(quantity),0) INTO used FROM reservation_overbooking_releases WHERE organisation_id=NEW.organisation_id AND room_id=NEW.room_id AND day=NEW.day;
 IF used>original THEN RAISE EXCEPTION 'Overbooking release cannot manufacture physical backing' USING ERRCODE='23514';END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER overbooking_release_guard AFTER INSERT ON reservation_overbooking_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_overbooking_release_guard();
CREATE CONSTRAINT TRIGGER overbooking_release_stock AFTER INSERT ON reservation_overbooking_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_stock_integrity();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION business_stock_integrity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE org uuid;pool uuid;
BEGIN
 org=coalesce(NEW.organisation_id,OLD.organisation_id);pool=coalesce(NEW.pool_id,OLD.pool_id);
 IF EXISTS(SELECT 1 FROM inventory_calendar c JOIN inventory_pools p ON p.id=c.pool_id AND p.organisation_id=c.organisation_id WHERE c.organisation_id=org AND c.pool_id=pool AND (c.committed<(SELECT coalesce(sum(d.quantity),0) FROM reservation_inventory_days d WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND d.released_at IS NULL) OR c.capacity>p.capacity OR c.blocked+c.committed+
  (SELECT coalesce(sum(d.quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND h.state='ACTIVE' AND h.expires_at>now())+
  (SELECT coalesce(sum(d.quantity),0) FROM inventory_allotment_days d JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>now())-
  (SELECT coalesce(sum(d.allotted_quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND h.state='ACTIVE' AND h.expires_at>now() AND a.state='ACTIVE' AND a.release_at>now())-
  (SELECT coalesce(sum(d.allotted_quantity),0) FROM reservation_inventory_days d JOIN inventory_allotments a ON a.id=d.allotment_id AND a.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND d.released_at IS NULL AND a.state='ACTIVE' AND a.release_at>now())>c.capacity+c.overbooked)) THEN RAISE EXCEPTION 'Canonical stock cannot be oversold or manufactured by an allotment' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM inventory_allotment_days a WHERE a.organisation_id=org AND a.pool_id=pool AND a.quantity <
 (SELECT coalesce(sum(d.allotted_quantity),0) FROM reservation_inventory_days d WHERE d.organisation_id=org AND d.allotment_id=a.allotment_id AND d.day=a.day AND d.released_at IS NULL)+
 (SELECT coalesce(sum(d.allotted_quantity),0) FROM inventory_hold_days d JOIN inventory_holds h ON h.id=d.hold_id AND h.organisation_id=d.organisation_id WHERE d.organisation_id=org AND d.allotment_id=a.allotment_id AND d.day=a.day AND h.state='ACTIVE' AND h.expires_at>now())) THEN RAISE EXCEPTION 'Held and committed channel allotment provenance exceeds its original quota' USING ERRCODE='23514';END IF;
 IF EXISTS(SELECT 1 FROM reservation_inventory_days d JOIN reservation_overbooking_permits p ON p.organisation_id=d.organisation_id AND p.id=d.overbooking_permit_id WHERE d.organisation_id=org AND d.pool_id=pool GROUP BY p.id,d.day HAVING sum(d.overbooked_quantity)>max(p.quantity)) THEN RAISE EXCEPTION 'Overbooking permit day quota exceeded' USING ERRCODE='23514';END IF;
 IF EXISTS(SELECT 1 FROM inventory_calendar c WHERE c.organisation_id=org AND c.pool_id=pool AND c.overbooked<>(SELECT coalesce(sum(d.overbooked_quantity-coalesce((SELECT sum(j.quantity) FROM reservation_overbooking_releases j WHERE j.organisation_id=d.organisation_id AND j.room_id=d.room_id AND j.day=d.day),0)),0) FROM reservation_inventory_days d WHERE d.organisation_id=org AND d.pool_id=pool AND d.day=c.day AND d.released_at IS NULL)) THEN RAISE EXCEPTION 'Overbooked demand must match its approved outstanding provenance' USING ERRCODE='23514';END IF;
 RETURN NULL;
END; $$;
-- +goose StatementEnd

-- +goose Down
-- Accepted exception history and physical stock provenance are retained.
SELECT 1;
