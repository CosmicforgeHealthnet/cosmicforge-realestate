-- +goose Up
CREATE TABLE inventory_nodes (
 id uuid PRIMARY KEY CHECK(substring(id::text,15,1)='7'), organisation_id uuid NOT NULL REFERENCES organisations(id),
 branch_id uuid NOT NULL,department_id uuid,team_id uuid,kind text NOT NULL CHECK(kind IN ('PORTFOLIO','DEVELOPMENT','DEVELOPMENT_PHASE','PARCEL','PROPERTY','BUILDING','BLOCK','FLOOR','UNIT','ROOM_TYPE','ROOM','FACILITY','COMMON_AREA','ASSET')),
 code text NOT NULL CHECK(code ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$'),name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 200),
 state text NOT NULL DEFAULT 'DRAFT' CHECK(state IN ('DRAFT','ACTIVE','INACTIVE','ARCHIVED','RETIRED')),
 timezone text NOT NULL,currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),policy_code text NOT NULL DEFAULT 'STANDARD',policy_version bigint NOT NULL DEFAULT 1 CHECK(policy_version>0),capacity_code text NOT NULL DEFAULT '',
 attributes jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(attributes)='object' AND octet_length(attributes::text)<=131072),
 room_type_id uuid,version bigint NOT NULL DEFAULT 1 CHECK(version>0),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),activated_at timestamptz,
 created_by uuid NOT NULL REFERENCES business_accounts(id),updated_by uuid NOT NULL REFERENCES business_accounts(id),
 UNIQUE(organisation_id,id),UNIQUE(organisation_id,id,kind),FOREIGN KEY(organisation_id,branch_id) REFERENCES branches(organisation_id,id),
 FOREIGN KEY(organisation_id,department_id) REFERENCES departments(organisation_id,id),FOREIGN KEY(organisation_id,team_id) REFERENCES teams(organisation_id,id),
 CHECK(kind='ROOM' OR room_type_id IS NULL)
);
CREATE INDEX inventory_nodes_org_branch_idx ON inventory_nodes(organisation_id,branch_id,kind,state,id);
CREATE INDEX inventory_nodes_code_idx ON inventory_nodes(organisation_id,kind,lower(code));
CREATE TABLE inventory_edge_rules(parent_kind text NOT NULL,child_kind text NOT NULL,relation text NOT NULL,PRIMARY KEY(parent_kind,child_kind,relation));
INSERT INTO inventory_edge_rules VALUES ('DEVELOPMENT','DEVELOPMENT_PHASE','CONTAINS'),('DEVELOPMENT','PARCEL','CONTAINS'),('DEVELOPMENT','PROPERTY','CONTAINS'),('DEVELOPMENT_PHASE','PARCEL','CONTAINS'),('DEVELOPMENT_PHASE','PROPERTY','CONTAINS'),('PARCEL','PROPERTY','CONTAINS'),('PROPERTY','BUILDING','CONTAINS'),('PROPERTY','BLOCK','CONTAINS'),('PROPERTY','FLOOR','CONTAINS'),('PROPERTY','UNIT','CONTAINS'),('PROPERTY','ROOM_TYPE','CONTAINS'),('PROPERTY','ROOM','CONTAINS'),('PROPERTY','FACILITY','CONTAINS'),('PROPERTY','COMMON_AREA','CONTAINS'),('PROPERTY','ASSET','CONTAINS'),('BUILDING','BLOCK','CONTAINS'),('BUILDING','FLOOR','CONTAINS'),('BUILDING','UNIT','CONTAINS'),('BUILDING','ROOM','CONTAINS'),('BUILDING','FACILITY','CONTAINS'),('BUILDING','COMMON_AREA','CONTAINS'),('BUILDING','ASSET','CONTAINS'),('BLOCK','FLOOR','CONTAINS'),('BLOCK','UNIT','CONTAINS'),('BLOCK','ROOM','CONTAINS'),('BLOCK','FACILITY','CONTAINS'),('BLOCK','COMMON_AREA','CONTAINS'),('BLOCK','ASSET','CONTAINS'),('FLOOR','UNIT','CONTAINS'),('FLOOR','ROOM','CONTAINS'),('FLOOR','FACILITY','CONTAINS'),('FLOOR','COMMON_AREA','CONTAINS'),('FLOOR','ASSET','CONTAINS'),('UNIT','ROOM','CONTAINS'),('UNIT','ASSET','CONTAINS'),('ROOM','ASSET','CONTAINS'),('FACILITY','ASSET','CONTAINS'),('COMMON_AREA','ASSET','CONTAINS'),('PORTFOLIO','DEVELOPMENT','MEMBERSHIP'),('PORTFOLIO','PARCEL','MEMBERSHIP'),('PORTFOLIO','PROPERTY','MEMBERSHIP'),('PARCEL','PROPERTY','ASSOCIATES');
-- Equipment/component associations may attach to any canonical node, including
-- another asset. UUID-level cycles and the single physical parent still apply.
INSERT INTO inventory_edge_rules SELECT k,'ASSET','CONTAINS' FROM unnest(ARRAY['PORTFOLIO','DEVELOPMENT','DEVELOPMENT_PHASE','PARCEL','PROPERTY','BUILDING','BLOCK','FLOOR','UNIT','ROOM_TYPE','ROOM','FACILITY','COMMON_AREA','ASSET']) k ON CONFLICT DO NOTHING;
CREATE TABLE inventory_edges(
 id uuid PRIMARY KEY,organisation_id uuid NOT NULL,parent_id uuid NOT NULL,child_id uuid NOT NULL,relation text NOT NULL CHECK(relation IN ('CONTAINS','MEMBERSHIP','ASSOCIATES')),
 created_at timestamptz NOT NULL DEFAULT now(),created_by uuid NOT NULL REFERENCES business_accounts(id),
 FOREIGN KEY(organisation_id,parent_id) REFERENCES inventory_nodes(organisation_id,id),FOREIGN KEY(organisation_id,child_id) REFERENCES inventory_nodes(organisation_id,id),
 CHECK(parent_id<>child_id),UNIQUE(organisation_id,parent_id,child_id,relation)
);
CREATE UNIQUE INDEX inventory_one_physical_parent ON inventory_edges(organisation_id,child_id) WHERE relation='CONTAINS';
CREATE INDEX inventory_edges_children ON inventory_edges(organisation_id,parent_id,child_id);
CREATE INDEX inventory_edges_ancestors ON inventory_edges(organisation_id,child_id,parent_id);
CREATE TABLE portfolios(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'PORTFOLIO' CHECK(kind='PORTFOLIO'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE developments(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'DEVELOPMENT' CHECK(kind='DEVELOPMENT'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE development_phases(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'DEVELOPMENT_PHASE' CHECK(kind='DEVELOPMENT_PHASE'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE parcels(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'PARCEL' CHECK(kind='PARCEL'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE properties(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'PROPERTY' CHECK(kind='PROPERTY'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE buildings(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'BUILDING' CHECK(kind='BUILDING'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE blocks(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'BLOCK' CHECK(kind='BLOCK'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE floors(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'FLOOR' CHECK(kind='FLOOR'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE units(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'UNIT' CHECK(kind='UNIT'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE room_types(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'ROOM_TYPE' CHECK(kind='ROOM_TYPE'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE rooms(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'ROOM' CHECK(kind='ROOM'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE facilities(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'FACILITY' CHECK(kind='FACILITY'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE common_areas(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'COMMON_AREA' CHECK(kind='COMMON_AREA'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
CREATE TABLE assets(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,kind text NOT NULL DEFAULT 'ASSET' CHECK(kind='ASSET'),UNIQUE(organisation_id,id),FOREIGN KEY(organisation_id,id,kind) REFERENCES inventory_nodes(organisation_id,id,kind) ON DELETE CASCADE);
ALTER TABLE inventory_nodes ADD CONSTRAINT inventory_room_type_fk FOREIGN KEY(organisation_id,room_type_id) REFERENCES room_types(organisation_id,id);
-- Typed anchors share the canonical ID; they contain no second identity/state.
-- +goose StatementBegin
CREATE FUNCTION business_inventory_anchor() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.kind='PORTFOLIO' THEN INSERT INTO portfolios(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='DEVELOPMENT' THEN INSERT INTO developments(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='DEVELOPMENT_PHASE' THEN INSERT INTO development_phases(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='PARCEL' THEN INSERT INTO parcels(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='PROPERTY' THEN INSERT INTO properties(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='BUILDING' THEN INSERT INTO buildings(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='BLOCK' THEN INSERT INTO blocks(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='FLOOR' THEN INSERT INTO floors(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='UNIT' THEN INSERT INTO units(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='ROOM_TYPE' THEN INSERT INTO room_types(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='ROOM' THEN INSERT INTO rooms(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='FACILITY' THEN INSERT INTO facilities(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='COMMON_AREA' THEN INSERT INTO common_areas(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 IF NEW.kind='ASSET' THEN INSERT INTO assets(id,organisation_id) VALUES(NEW.id,NEW.organisation_id); END IF;
 RETURN NEW; END; $$;
-- +goose StatementEnd
CREATE TRIGGER inventory_typed_anchor AFTER INSERT ON inventory_nodes FOR EACH ROW EXECUTE FUNCTION business_inventory_anchor();
-- +goose StatementBegin
CREATE FUNCTION business_graph_property(org uuid,node uuid) RETURNS uuid LANGUAGE sql STABLE AS $$
 WITH RECURSIVE ancestors(id) AS (SELECT node UNION SELECT e.parent_id FROM inventory_edges e JOIN ancestors a ON a.id=e.child_id WHERE e.organisation_id=org AND e.relation='CONTAINS')
 SELECT n.id FROM ancestors a JOIN inventory_nodes n ON n.id=a.id AND n.organisation_id=org WHERE n.kind='PROPERTY' LIMIT 1;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION business_inventory_edge_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p inventory_nodes; c inventory_nodes; cyclic boolean; above integer; below integer;
BEGIN
 PERFORM 1 FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 SELECT * INTO STRICT p FROM inventory_nodes WHERE organisation_id=NEW.organisation_id AND id=NEW.parent_id;
 SELECT * INTO STRICT c FROM inventory_nodes WHERE organisation_id=NEW.organisation_id AND id=NEW.child_id;
 IF NOT EXISTS(SELECT 1 FROM inventory_edge_rules WHERE parent_kind=p.kind AND child_kind=c.kind AND relation=NEW.relation)
 OR p.state IN ('ARCHIVED','RETIRED') OR c.state IN ('ARCHIVED','RETIRED')
 OR NEW.relation<>'MEMBERSHIP' AND p.branch_id<>c.branch_id
 THEN RAISE EXCEPTION 'Invalid typed/branch/lifecycle inventory edge' USING ERRCODE='23514'; END IF;
 WITH RECURSIVE reach(id) AS (SELECT NEW.child_id UNION SELECT e.child_id FROM inventory_edges e JOIN reach r ON e.parent_id=r.id WHERE e.organisation_id=NEW.organisation_id AND e.id<>NEW.id)
 SELECT EXISTS(SELECT 1 FROM reach WHERE id=NEW.parent_id) INTO cyclic;
 IF cyclic THEN RAISE EXCEPTION 'Inventory cycle rejected' USING ERRCODE='23514'; END IF;
 WITH RECURSIVE a(id,depth) AS(SELECT NEW.parent_id,1 UNION ALL SELECT e.parent_id,a.depth+1 FROM inventory_edges e JOIN a ON e.child_id=a.id WHERE e.organisation_id=NEW.organisation_id AND a.depth<65) SELECT coalesce(max(depth),1) INTO above FROM a;
 WITH RECURSIVE d(id,depth) AS(SELECT NEW.child_id,1 UNION ALL SELECT e.child_id,d.depth+1 FROM inventory_edges e JOIN d ON e.parent_id=d.id WHERE e.organisation_id=NEW.organisation_id AND d.depth<65) SELECT coalesce(max(depth),1) INTO below FROM d;
 IF above+below>64 THEN RAISE EXCEPTION 'Inventory hierarchy exceeds depth bound' USING ERRCODE='23514'; END IF;

 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER inventory_edge_guard BEFORE INSERT OR UPDATE ON inventory_edges FOR EACH ROW EXECUTE FUNCTION business_inventory_edge_check();
-- +goose StatementBegin
CREATE FUNCTION business_inventory_node_check() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM 1 FROM organisations WHERE id=NEW.organisation_id FOR UPDATE;
 IF TG_OP='UPDATE' THEN
 IF (NEW.id,NEW.organisation_id,NEW.kind,NEW.created_at,NEW.created_by) IS DISTINCT FROM (OLD.id,OLD.organisation_id,OLD.kind,OLD.created_at,OLD.created_by)
 OR NEW.version<>OLD.version+1 OR OLD.activated_at IS NOT NULL AND NEW.activated_at IS DISTINCT FROM OLD.activated_at
 THEN RAISE EXCEPTION 'Immutable identity or invalid inventory version' USING ERRCODE='23514'; END IF;
 IF NEW.state<>OLD.state AND NOT ((OLD.state='DRAFT' AND NEW.state IN ('ACTIVE','ARCHIVED')) OR (OLD.state='ACTIVE' AND NEW.state IN ('INACTIVE','RETIRED')) OR (OLD.state='INACTIVE' AND NEW.state IN ('ACTIVE','RETIRED','ARCHIVED')))
 THEN RAISE EXCEPTION 'Invalid inventory lifecycle transition' USING ERRCODE='23514'; END IF;
 END IF;
 IF NOT EXISTS(SELECT 1 FROM branches WHERE organisation_id=NEW.organisation_id AND id=NEW.branch_id AND status='ACTIVE')
 OR NOT EXISTS(SELECT 1 FROM pg_timezone_names WHERE name=NEW.timezone)
 OR NEW.department_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM departments WHERE organisation_id=NEW.organisation_id AND id=NEW.department_id AND branch_id=NEW.branch_id AND status='ACTIVE')
 OR NEW.team_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM teams WHERE organisation_id=NEW.organisation_id AND id=NEW.team_id AND department_id=NEW.department_id AND status='ACTIVE')
 THEN RAISE EXCEPTION 'Invalid inventory operating context' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER inventory_node_guard BEFORE INSERT OR UPDATE ON inventory_nodes FOR EACH ROW EXECUTE FUNCTION business_inventory_node_check();
-- Deferred checks permit atomic node/edge creation and safe edge replacement.
-- +goose StatementBegin
CREATE FUNCTION business_inventory_integrity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE org uuid;
BEGIN
 org=coalesce(NEW.organisation_id,OLD.organisation_id);
 IF EXISTS(SELECT 1 FROM inventory_edges e JOIN inventory_nodes p ON p.id=e.parent_id AND p.organisation_id=e.organisation_id JOIN inventory_nodes c ON c.id=e.child_id AND c.organisation_id=e.organisation_id WHERE e.organisation_id=org AND e.relation='CONTAINS' AND (p.branch_id<>c.branch_id OR p.timezone<>c.timezone OR p.currency<>c.currency OR p.department_id IS NOT NULL AND c.department_id IS DISTINCT FROM p.department_id OR p.team_id IS NOT NULL AND c.team_id IS DISTINCT FROM p.team_id OR c.state='ACTIVE' AND p.state<>'ACTIVE')) THEN RAISE EXCEPTION 'Physical operating context and lifecycle must remain consistent' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM inventory_nodes n JOIN inventory_nodes t ON t.organisation_id=n.organisation_id AND t.id=n.room_type_id WHERE n.organisation_id=org AND n.kind='ROOM' AND n.state='ACTIVE' AND t.state<>'ACTIVE') THEN RAISE EXCEPTION 'Active rooms require an active same-property category' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM inventory_edges e JOIN inventory_nodes p ON p.id=e.parent_id AND p.organisation_id=e.organisation_id JOIN inventory_nodes c ON c.id=e.child_id AND c.organisation_id=e.organisation_id WHERE e.organisation_id=org AND e.relation='CONTAINS' AND p.state IN ('ARCHIVED','RETIRED') AND c.state NOT IN ('ARCHIVED','RETIRED')) THEN RAISE EXCEPTION 'Live children require a live physical parent' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM inventory_nodes n WHERE n.organisation_id=org AND n.kind NOT IN ('PORTFOLIO','DEVELOPMENT','PARCEL','PROPERTY') AND NOT EXISTS(SELECT 1 FROM inventory_edges e WHERE e.organisation_id=org AND e.child_id=n.id AND e.relation='CONTAINS'))
 THEN RAISE EXCEPTION 'Required physical parent missing' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM inventory_nodes n JOIN inventory_nodes t ON t.organisation_id=n.organisation_id AND t.id=n.room_type_id WHERE n.organisation_id=org AND n.kind='ROOM' AND business_graph_property(org,n.id) IS DISTINCT FROM business_graph_property(org,t.id))
 OR EXISTS(SELECT 1 FROM inventory_nodes n WHERE n.organisation_id=org AND n.kind='ROOM' AND n.room_type_id IS NULL)
 THEN RAISE EXCEPTION 'Room type must belong to the same physical property' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM inventory_nodes n LEFT JOIN inventory_edges e ON e.organisation_id=org AND e.child_id=n.id AND e.relation='CONTAINS' WHERE n.organisation_id=org GROUP BY n.kind,lower(n.code),CASE WHEN n.kind IN ('UNIT','ROOM','ROOM_TYPE') THEN business_graph_property(org,n.id) ELSE coalesce(e.parent_id,n.branch_id) END HAVING count(*)>1)
 THEN RAISE EXCEPTION 'Duplicate inventory code in physical scope' USING ERRCODE='23505'; END IF;
 RETURN NULL;
END; $$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER inventory_node_integrity AFTER INSERT OR UPDATE ON inventory_nodes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_inventory_integrity();
CREATE CONSTRAINT TRIGGER inventory_edge_integrity AFTER INSERT OR UPDATE OR DELETE ON inventory_edges DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION business_inventory_integrity();
CREATE TABLE inventory_history(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,node_id uuid NOT NULL,actor_id uuid REFERENCES business_accounts(id),operation text NOT NULL,before_snapshot jsonb,after_snapshot jsonb NOT NULL,correlation_id text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id));
CREATE TABLE inventory_external_mappings(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,node_id uuid NOT NULL,provider text NOT NULL CHECK(length(provider) BETWEEN 1 AND 80),external_id text NOT NULL CHECK(length(external_id) BETWEEN 1 AND 200),source_reference text NOT NULL DEFAULT '',version bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),created_by uuid NOT NULL REFERENCES business_accounts(id),FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id),CONSTRAINT external_mapping_legacy_identity UNIQUE(organisation_id,provider,external_id));
CREATE INDEX inventory_mapping_node_idx ON inventory_external_mappings(organisation_id,node_id);
CREATE TABLE inventory_verification_links(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,node_id uuid NOT NULL,case_id uuid NOT NULL,provider_status text NOT NULL,node_version bigint NOT NULL,provider_updated_at timestamptz NOT NULL,expires_at timestamptz,checked_at timestamptz NOT NULL DEFAULT now(),version bigint NOT NULL DEFAULT 1,FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id),UNIQUE(organisation_id,node_id),UNIQUE(case_id));
CREATE TABLE inventory_media_links(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,node_id uuid NOT NULL,media_id uuid NOT NULL,purpose text NOT NULL CHECK(purpose IN ('PHOTO','DOCUMENT','SURVEY','FLOOR_PLAN','DIGITAL_TWIN','WARRANTY')),provider_status text NOT NULL,access_classification text NOT NULL,provider_checked_at timestamptz NOT NULL DEFAULT now(),version bigint NOT NULL DEFAULT 1,created_by uuid NOT NULL REFERENCES business_accounts(id),created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id),UNIQUE(organisation_id,node_id,media_id,purpose));
CREATE TABLE space_graphs(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,node_id uuid NOT NULL,media_link_id uuid NOT NULL REFERENCES inventory_media_links(id),coordinate_system text NOT NULL CHECK(coordinate_system IN ('LOCAL_METRES','WGS84')),spaces jsonb NOT NULL CHECK(jsonb_typeof(spaces)='array' AND octet_length(spaces::text)<=65536),confirmed_by uuid NOT NULL REFERENCES business_accounts(id),version bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id));
CREATE TABLE digital_twin_refs(id uuid PRIMARY KEY REFERENCES space_graphs(id),organisation_id uuid NOT NULL,node_id uuid NOT NULL,FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id));
CREATE TABLE inventory_import_jobs(id uuid PRIMARY KEY,organisation_id uuid NOT NULL REFERENCES organisations(id),actor_id uuid NOT NULL REFERENCES business_accounts(id),membership_id uuid NOT NULL REFERENCES staff_memberships(id),session_id uuid REFERENCES business_sessions(id) ON DELETE SET NULL,branch_id uuid NOT NULL,provider text NOT NULL,format text NOT NULL CHECK(format IN ('JSON','CSV')),status text NOT NULL DEFAULT 'QUEUED' CHECK(status IN ('QUEUED','VALIDATING','PREVIEW_READY','RUNNING','PAUSED','COMPLETED','COMPLETED_WITH_ERRORS','FAILED','CANCELLED')),record_count integer NOT NULL CHECK(record_count BETWEEN 1 AND 1000),checkpoint integer NOT NULL DEFAULT 0,version bigint NOT NULL DEFAULT 1,execute_requested boolean NOT NULL DEFAULT false,lease_owner uuid,lease_expires_at timestamptz,attempts integer NOT NULL DEFAULT 0,last_error text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,branch_id) REFERENCES branches(organisation_id,id));
CREATE INDEX inventory_import_work_idx ON inventory_import_jobs(status,lease_expires_at,id);
CREATE TABLE inventory_import_records(job_id uuid NOT NULL REFERENCES inventory_import_jobs(id),row_number integer NOT NULL,source_key text NOT NULL,node_id uuid NOT NULL,command jsonb NOT NULL,parent_key text,action text NOT NULL DEFAULT 'CREATE' CHECK(action IN ('CREATE','UPDATE','SKIP','RECONCILE')),status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','VALID','INVALID','APPLIED','SKIPPED','QUARANTINED')),validation_errors jsonb NOT NULL DEFAULT '[]',before_snapshot jsonb,after_snapshot jsonb,applied_version bigint,error_code text NOT NULL DEFAULT '',PRIMARY KEY(job_id,row_number),UNIQUE(job_id,source_key));
CREATE TABLE inventory_import_reconciliation(id uuid PRIMARY KEY,job_id uuid NOT NULL REFERENCES inventory_import_jobs(id),node_id uuid NOT NULL,action text NOT NULL,expected_version bigint,reason text NOT NULL,status text NOT NULL DEFAULT 'PENDING',created_at timestamptz NOT NULL DEFAULT now());
-- Reuse existing immutable record trigger for append-only history.
CREATE TRIGGER inventory_history_immutable BEFORE UPDATE OR DELETE ON inventory_history FOR EACH ROW EXECUTE FUNCTION business_immutable_history();
CREATE TRIGGER inventory_history_no_truncate BEFORE TRUNCATE ON inventory_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
ALTER TABLE staff_scope_grants DROP CONSTRAINT staff_scope_grants_scope_kind_check;
ALTER TABLE staff_scope_grants ADD CONSTRAINT staff_scope_grants_scope_kind_check CHECK(scope_kind IN ('COMPANY','REGION','BRANCH','DEPARTMENT','TEAM','PORTFOLIO','PROPERTY','DEVELOPMENT','DEVELOPMENT_PHASE','PARCEL','BUILDING','BLOCK','FLOOR','UNIT','ROOM_TYPE','ROOM','FACILITY','COMMON_AREA','ASSET'));

INSERT INTO permissions(code,description) VALUES ('portfolio:read','Section 4 canonical inventory capability'),('portfolio:write','Section 4 canonical inventory capability'),('development:read','Section 4 canonical inventory capability'),('development:write','Section 4 canonical inventory capability'),('parcel:read','Section 4 canonical inventory capability'),('parcel:write','Section 4 canonical inventory capability'),('property:read','Section 4 canonical inventory capability'),('property:write','Section 4 canonical inventory capability'),('structure:read','Section 4 canonical inventory capability'),('structure:write','Section 4 canonical inventory capability'),('assets:read','Section 4 canonical inventory capability'),('assets:write','Section 4 canonical inventory capability'),('imports:read','Section 4 canonical inventory capability'),('imports:write','Section 4 canonical inventory capability'),('inventory:restructure','Section 4 canonical inventory capability'),('inventory:mappings','Section 4 canonical inventory capability'),('inventory:verify','Section 4 canonical inventory capability'),('inventory:media','Section 4 canonical inventory capability'),('inventory:approve','Section 4 canonical inventory capability') ON CONFLICT(code) DO NOTHING;
ALTER TABLE inventory_media_links ADD COLUMN owner_type text NOT NULL;
ALTER TABLE inventory_media_links ADD COLUMN owner_id uuid NOT NULL;
CREATE UNIQUE INDEX space_graph_versions ON space_graphs(organisation_id,node_id,version);
CREATE TRIGGER space_graph_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON space_graphs FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
ALTER TABLE inventory_import_jobs ADD COLUMN allow_partial boolean NOT NULL DEFAULT false;
ALTER TABLE inventory_import_jobs ADD COLUMN recovery_state text NOT NULL DEFAULT 'NONE' CHECK(recovery_state IN ('NONE','RUNNING','APPLIED','PARTIAL'));
ALTER TABLE inventory_import_records ADD COLUMN source_row_number integer NOT NULL;
ALTER TABLE inventory_import_records ADD COLUMN mapping_id uuid REFERENCES inventory_external_mappings(id);
ALTER TABLE inventory_import_records ADD COLUMN mapping_created boolean NOT NULL DEFAULT false;
ALTER TABLE inventory_import_records ADD COLUMN recovery_status text NOT NULL DEFAULT 'PENDING' CHECK(recovery_status IN ('PENDING','RESTORED','ARCHIVED','RECONCILE'));
ALTER TABLE inventory_import_reconciliation ADD CONSTRAINT import_reconciliation_unique UNIQUE(job_id,node_id,action);
-- Aggregate structural revisions bind reviewed subtree operations without
-- conflating a legal property authority version with child room lifecycle.
CREATE TABLE inventory_graph_versions(organisation_id uuid NOT NULL,node_id uuid PRIMARY KEY,version bigint NOT NULL DEFAULT 1,FOREIGN KEY(organisation_id,node_id) REFERENCES inventory_nodes(organisation_id,id));
-- +goose StatementBegin
CREATE FUNCTION business_inventory_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE org uuid;first_node uuid;second_node uuid;
BEGIN
 org=coalesce(NEW.organisation_id,OLD.organisation_id);
 IF TG_TABLE_NAME='inventory_nodes' AND TG_OP='INSERT' THEN INSERT INTO inventory_graph_versions(organisation_id,node_id) VALUES(org,NEW.id);RETURN NULL;END IF;
 IF TG_TABLE_NAME='inventory_nodes' THEN first_node=NEW.id;
 ELSIF TG_TABLE_NAME='inventory_edges' THEN first_node=coalesce(NEW.parent_id,OLD.parent_id);second_node=coalesce(NEW.child_id,OLD.child_id);
 ELSE first_node=coalesce(NEW.node_id,OLD.node_id); END IF;
 WITH RECURSIVE a(id) AS(SELECT first_node UNION SELECT second_node UNION SELECT e.parent_id FROM inventory_edges e JOIN a ON e.child_id=a.id WHERE e.organisation_id=org AND e.relation IN ('CONTAINS','MEMBERSHIP')) UPDATE inventory_graph_versions v SET version=version+1 FROM a WHERE v.organisation_id=org AND v.node_id=a.id;
 RETURN NULL;
END;$$;
-- +goose StatementEnd
CREATE TRIGGER inventory_revision_node AFTER INSERT OR UPDATE ON inventory_nodes FOR EACH ROW EXECUTE FUNCTION business_inventory_revision();
CREATE TRIGGER inventory_revision_edge AFTER INSERT OR UPDATE OR DELETE ON inventory_edges FOR EACH ROW EXECUTE FUNCTION business_inventory_revision();
CREATE TRIGGER inventory_revision_media AFTER INSERT OR DELETE ON inventory_media_links FOR EACH ROW EXECUTE FUNCTION business_inventory_revision();
CREATE TRIGGER inventory_revision_verification AFTER INSERT OR DELETE ON inventory_verification_links FOR EACH ROW EXECUTE FUNCTION business_inventory_revision();
CREATE TABLE inventory_edge_history(id uuid PRIMARY KEY,organisation_id uuid NOT NULL,edge_id uuid NOT NULL,parent_id uuid NOT NULL,child_id uuid NOT NULL,relation text NOT NULL,action text NOT NULL CHECK(action IN ('ADDED','REMOVED')),actor_id uuid NOT NULL REFERENCES business_accounts(id),correlation_id text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(organisation_id,parent_id) REFERENCES inventory_nodes(organisation_id,id),FOREIGN KEY(organisation_id,child_id) REFERENCES inventory_nodes(organisation_id,id));
CREATE INDEX inventory_edge_history_child ON inventory_edge_history(organisation_id,child_id,created_at,id);
CREATE TRIGGER inventory_edge_history_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON inventory_edge_history FOR EACH STATEMENT EXECUTE FUNCTION business_immutable_history();
ALTER TABLE inventory_external_mappings ADD COLUMN object_type text NOT NULL;
ALTER TABLE inventory_external_mappings DROP CONSTRAINT external_mapping_legacy_identity;
ALTER TABLE inventory_external_mappings ADD CONSTRAINT external_mapping_typed_identity UNIQUE(organisation_id,provider,object_type,external_id);
ALTER TABLE inventory_external_mappings ADD CONSTRAINT external_mapping_node_type FOREIGN KEY(organisation_id,node_id,object_type) REFERENCES inventory_nodes(organisation_id,id,kind);
ALTER TABLE inventory_media_links ADD CONSTRAINT inventory_media_composite_identity UNIQUE(organisation_id,node_id,id);
ALTER TABLE space_graphs ADD CONSTRAINT space_graph_tenant_media FOREIGN KEY(organisation_id,node_id,media_link_id) REFERENCES inventory_media_links(organisation_id,node_id,id);
ALTER TABLE space_graphs ADD CONSTRAINT space_graph_composite_identity UNIQUE(organisation_id,node_id,id);
ALTER TABLE digital_twin_refs ADD CONSTRAINT digital_twin_tenant_space FOREIGN KEY(organisation_id,node_id,id) REFERENCES space_graphs(organisation_id,node_id,id);
ALTER TABLE inventory_import_jobs ADD CONSTRAINT import_job_tenant_membership FOREIGN KEY(organisation_id,membership_id) REFERENCES staff_memberships(organisation_id,id);
ALTER TABLE inventory_import_records ADD COLUMN mapping_version bigint;
-- The approved Section 4 capability extension creates an immutable new Owner
-- role version only for an unrestricted company-level initial representative.
-- Custom/limited staff roles and their assigned versions are never expanded.
-- +goose StatementBegin
CREATE FUNCTION business_graph_uuidv7_seed() RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE t text;r text;
BEGIN t=lpad(to_hex(floor(extract(epoch FROM clock_timestamp())*1000)::bigint),12,'0');r=replace(gen_random_uuid()::text,'-','');RETURN (substring(t,1,8)||'-'||substring(t,9,4)||'-7'||substring(r,1,3)||'-8'||substring(r,4,3)||'-'||substring(r,7,12))::uuid;END;$$;
-- +goose StatementEnd
-- +goose StatementBegin
DO $$ DECLARE item record;next_role uuid;next_version bigint;permission_code text;
BEGIN
 FOR item IN SELECT m.id member_id,m.organisation_id,m.account_id,r.* FROM staff_memberships m JOIN organisations o ON o.id=m.organisation_id JOIN role_templates r ON r.id=m.role_id AND r.organisation_id=m.organisation_id WHERE m.kind='OWNER' AND m.status='ACTIVE' AND m.scope_kind='COMPANY' AND m.scope_id=m.organisation_id AND o.representative_account_id=m.account_id AND r.active AND r.name='Owner' AND m.effective_at<=now() AND (m.expires_at IS NULL OR m.expires_at>now()) AND EXISTS(SELECT 1 FROM role_permissions rp WHERE rp.role_id=r.id AND rp.permission='roles:write') AND NOT EXISTS(SELECT 1 FROM staff_scope_grants denied WHERE denied.membership_id=m.id AND denied.capability='roles:write' AND denied.deny AND denied.revoked_at IS NULL AND denied.effective_at<=now() AND (denied.expires_at IS NULL OR denied.expires_at>now())) AND EXISTS(SELECT 1 FROM staff_scope_grants g WHERE g.membership_id=m.id AND g.capability='roles:write' AND g.scope_kind='COMPANY' AND g.scope_id=m.organisation_id AND g.inherit AND NOT g.deny AND g.revoked_at IS NULL AND g.expires_at IS NULL AND g.effective_at<=now())
 LOOP
 PERFORM 1 FROM organisations WHERE id=item.organisation_id FOR UPDATE;
 next_role=business_graph_uuidv7_seed();SELECT max(version)+1 INTO next_version FROM role_templates WHERE organisation_id=item.organisation_id AND role_key=item.role_key;
 INSERT INTO role_templates(id,organisation_id,role_key,name,version,active,senior_emergency_eligible) VALUES(next_role,item.organisation_id,item.role_key,item.name,next_version,true,item.senior_emergency_eligible);
 INSERT INTO role_permissions(role_id,permission) SELECT next_role,permission FROM role_permissions WHERE role_id=item.id;
 FOR permission_code IN SELECT unnest(ARRAY['portfolio:read','portfolio:write','development:read','development:write','parcel:read','parcel:write','property:read','property:write','structure:read','structure:write','assets:read','assets:write','imports:read','imports:write','inventory:restructure','inventory:mappings','inventory:verify','inventory:media','inventory:approve']::text[]) LOOP
 INSERT INTO role_permissions(role_id,permission) VALUES(next_role,permission_code) ON CONFLICT DO NOTHING;
 INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES(business_graph_uuidv7_seed(),item.organisation_id,item.member_id,permission_code,'COMPANY',item.organisation_id,true,now());
 END LOOP;
 UPDATE staff_memberships SET role_id=next_role,version=version+1,updated_at=now() WHERE id=item.member_id;
 INSERT INTO audit_events(id,organisation_id,action,target_type,target_id,change_context) VALUES(business_graph_uuidv7_seed(),item.organisation_id,'SECTION4_OWNER_CAPABILITY_MIGRATION','staff_membership',item.member_id,jsonb_build_object('from_role',item.id,'to_role',next_role,'version',next_version));
 END LOOP;
END;$$;
-- +goose StatementEnd
DROP FUNCTION business_graph_uuidv7_seed();
-- +goose Down
-- Forward-only: use a reviewed forward recovery plan, never discard canonical IDs/history.
SELECT 1;
