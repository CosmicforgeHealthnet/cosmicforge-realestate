package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/repositories/db"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/outbox"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/requestcontext"
	"github.com/jackc/pgx/v5"
)

type Repository struct{ Outbox *outbox.Store }

func Get(ctx context.Context, q database.Querier, org, id ids.ID) (Node, error) {
	var n Node
	var raw []byte
	err := q.QueryRow(ctx, `SELECT jsonb_strip_nulls(to_jsonb(n)||jsonb_build_object('non_sellable',n.kind='COMMON_AREA','parents',coalesce((SELECT jsonb_agg(jsonb_build_object('edge_id',e.id,'parent_id',e.parent_id,'relation',e.relation) ORDER BY e.relation,e.parent_id) FROM inventory_edges e WHERE e.organisation_id=n.organisation_id AND e.child_id=n.id),'[]'::jsonb),'property_id',business_graph_property(n.organisation_id,n.id),'tree_version',(SELECT version FROM inventory_graph_versions WHERE node_id=n.id),'verification',coalesce((SELECT jsonb_build_object('case_id',v.case_id,'status',CASE WHEN v.node_version<>p.version THEN 'STALE' WHEN v.expires_at<=now() THEN 'EXPIRED' ELSE v.provider_status END,'node_version',v.node_version,'expires_at',v.expires_at,'checked_at',v.checked_at) FROM inventory_verification_links v JOIN inventory_nodes p ON p.id=v.node_id AND p.organisation_id=v.organisation_id WHERE v.organisation_id=n.organisation_id AND v.node_id=coalesce(business_graph_property(n.organisation_id,n.id),n.id)),jsonb_build_object('status','UNVERIFIED')))) FROM inventory_nodes n WHERE organisation_id=$1 AND id=$2`, org, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, apperrors.NotFound("INVENTORY_NOT_FOUND", "Inventory resource not found.")
	}
	if err != nil {
		return n, err
	}
	err = json.Unmarshal(raw, &n)
	return n, err
}
func Create(ctx context.Context, tx pgx.Tx, n Node, roomType *ids.ID, parent ids.ID, actor ids.ID) error {
	if n.Validate() != nil {
		return apperrors.InvalidRequest("Invalid inventory identity, attributes or operating context.")
	}
	attrs, err := json.Marshal(n.Attributes)
	if err != nil {
		return err
	}
	queries := &db.Queries{}
	if err = queries.CreateInventoryNode(ctx, tx, db.CreateInventoryNodeParams{ID: n.ID, OrganisationID: n.OrganisationID, BranchID: n.BranchID, DepartmentID: n.DepartmentID, TeamID: n.TeamID, Kind: string(n.Kind), Code: n.Code, Name: n.Name, Timezone: n.Timezone, Currency: n.Currency, PolicyCode: n.PolicyCode, PolicyVersion: n.PolicyVersion, CapacityCode: n.CapacityCode, Attributes: attrs, RoomTypeID: roomType, CreatedBy: actor}); err != nil {
		return err
	}
	if !parent.IsZero() {
		return AddEdge(ctx, tx, n.OrganisationID, parent, n.ID, "CONTAINS", actor)
	}
	if !n.Kind.RootAllowed() {
		return apperrors.InvalidRequest("This entity requires a physical parent.")
	}
	return nil
}
func AddEdge(ctx context.Context, tx pgx.Tx, org, parent, child ids.ID, relation string, actor ids.ID) error {
	id := ids.New()
	if _, err := tx.Exec(ctx, `INSERT INTO inventory_edges(id,organisation_id,parent_id,child_id,relation,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, org, parent, child, relation, actor); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO inventory_edge_history(id,organisation_id,edge_id,parent_id,child_id,relation,action,actor_id,correlation_id) VALUES($1,$2,$3,$4,$5,$6,'ADDED',$7,$8)`, ids.New(), org, id, parent, child, relation, actor, requestcontext.From(ctx).RequestID)
	return err
}
func RemoveEdges(ctx context.Context, tx pgx.Tx, org, parent, child ids.ID, relation string, actor ids.ID) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id,parent_id FROM inventory_edges WHERE organisation_id=$1 AND child_id=$2 AND relation=$3 AND ($4::uuid IS NULL OR parent_id=$4) ORDER BY id FOR UPDATE`, org, child, relation, parent)
	if err != nil {
		return 0, err
	}
	type edge struct{ id, parent ids.ID }
	edges := []edge{}
	for rows.Next() {
		var e edge
		if err = rows.Scan(&e.id, &e.parent); err != nil {
			break
		}
		edges = append(edges, e)
	}
	rowErr := rows.Err()
	rows.Close()
	if err == nil {
		err = rowErr
	}
	if err != nil {
		return 0, err
	}
	for _, e := range edges {
		if _, err = tx.Exec(ctx, `DELETE FROM inventory_edges WHERE organisation_id=$1 AND id=$2`, org, e.id); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO inventory_edge_history(id,organisation_id,edge_id,parent_id,child_id,relation,action,actor_id,correlation_id) VALUES($1,$2,$3,$4,$5,$6,'REMOVED',$7,$8)`, ids.New(), org, e.id, e.parent, child, relation, actor, requestcontext.From(ctx).RequestID); err != nil {
			return 0, err
		}
	}
	return len(edges), nil
}

// Commit records state, immutable history and the existing transactional outbox
// together. property.* envelopes use the business transport namespace because
// the existing Individual Owner stream already owns root property.* subjects.
func (r Repository) Commit(ctx context.Context, tx pgx.Tx, org, id, actor ids.ID, operation string, before *Node) error {
	n, err := Get(ctx, tx, org, id)
	if err != nil {
		return err
	}
	after, err := json.Marshal(n)
	if err != nil {
		return err
	}
	var old any
	if before != nil {
		raw, e := json.Marshal(before)
		if e != nil {
			return e
		}
		old = raw
	}
	if _, err = tx.Exec(ctx, `INSERT INTO inventory_history(id,organisation_id,node_id,actor_id,operation,before_snapshot,after_snapshot,correlation_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ids.New(), org, id, actor, operation, old, after, requestcontext.From(ctx).RequestID); err != nil {
		return err
	}
	eventType := "business.structure.changed.v1"
	if n.Kind == Property {
		if operation == "CREATED" {
			eventType = "property.created.v1"
		} else {
			eventType = "property.updated.v1"
		}
	}
	payload := map[string]any{"organisation_id": org, "branch_id": n.BranchID, "target_id": id, "node_id": id, "kind": n.Kind, "state": n.State, "version": n.Version, "operation": operation}
	aggregate := "inventory_node"
	if n.Kind == Property {
		aggregate = "property"
	}
	e, err := events.New(ctx, platform.BusinessOperationsService, eventType, aggregate, id, payload)
	if err != nil {
		return err
	}
	if err = r.Outbox.Add(ctx, tx, e); err != nil {
		return err
	}
	if n.Kind == Property {
		_, err = tx.Exec(ctx, `UPDATE outbox_events SET subject=left(subject,length(subject)-length(event_type))||'business.'||event_type WHERE event_id=$1`, e.EventID)
	}
	return err
}
func PhysicalSubtree(ctx context.Context, q database.Querier, org, id ids.ID) ([]Node, error) {
	rows, err := q.Query(ctx, `WITH RECURSIVE d(id) AS(SELECT $2::uuid UNION SELECT e.child_id FROM inventory_edges e JOIN d ON e.parent_id=d.id WHERE e.organisation_id=$1 AND e.relation='CONTAINS') SELECT to_jsonb(n) FROM inventory_nodes n JOIN d ON d.id=n.id WHERE n.organisation_id=$1 ORDER BY n.id LIMIT 10001`, org, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []Node{}
	for rows.Next() {
		var raw []byte
		var n Node
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &n); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	if len(nodes) > 10000 {
		return nil, apperrors.Conflict("SUBTREE_TOO_LARGE", "Restructuring requires a bounded reviewed migration.")
	}
	return nodes, rows.Err()
}

// RelatedSubtree includes collection memberships for dependency safety and
// summaries; it must never be used to physically move a portfolio's properties.
func RelatedSubtree(ctx context.Context, q database.Querier, org, id ids.ID) ([]Node, error) {
	rows, err := q.Query(ctx, `WITH RECURSIVE d(id) AS(SELECT $2::uuid UNION SELECT e.child_id FROM inventory_edges e JOIN d ON e.parent_id=d.id WHERE e.organisation_id=$1 AND e.relation IN ('CONTAINS','MEMBERSHIP')) SELECT n.id FROM inventory_nodes n JOIN d ON d.id=n.id WHERE n.organisation_id=$1 ORDER BY n.id LIMIT 10001`, org, id)
	if err != nil {
		return nil, err
	}
	nodeIDs := []ids.ID{}
	for rows.Next() {
		var id ids.ID
		if err = rows.Scan(&id); err != nil {
			break
		}
		nodeIDs = append(nodeIDs, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err == nil {
		err = rowErr
	}
	if err != nil {
		return nil, err
	}
	if len(nodeIDs) > 10000 {
		return nil, apperrors.Conflict("SUBTREE_TOO_LARGE", "Collection safety requires a bounded reviewed migration.")
	}
	nodes := []Node{}
	for _, id := range nodeIDs {
		n, e := Get(ctx, q, org, id)
		if e != nil {
			return nil, e
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}
func CheckSafety(ctx context.Context, guard DependencyGuard, nodes []Node, digest string, now time.Time) error {
	if len(nodes) == 0 {
		return apperrors.NotFound("INVENTORY_NOT_FOUND", "Inventory resource not found.")
	}
	safe := true
	req := SafetyRequest{OrganisationID: nodes[0].OrganisationID, OperationDigest: digest}
	for _, n := range nodes {
		req.NodeIDs = append(req.NodeIDs, n.ID)
		req.Versions = append(req.Versions, n.Version)
		req.TreeVersions = append(req.TreeVersions, n.TreeVersion)
		safe = safe && n.State == "DRAFT" && n.ActivatedAt == nil
	}
	if safe {
		return nil
	}
	if guard == nil {
		return apperrors.Unavailable("Reservation, lease and sale dependency assertions are unavailable; activated inventory cannot be destructively changed.")
	}
	a, err := guard.Check(ctx, req)
	if err != nil || !a.Complete || !a.Safe || a.OrganisationID != req.OrganisationID || a.OperationDigest != digest || a.Reference == "" || !now.Before(a.ExpiresAt) {
		return apperrors.Conflict("DEPENDENCY_SAFETY_UNPROVEN", "Complete current dependency safety could not be established.")
	}
	return nil
}
