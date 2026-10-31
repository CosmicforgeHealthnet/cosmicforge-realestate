package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/approvals"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/organisation"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type InventoryMove struct {
	TreeVersion               int64                 `json:"tree_version"`
	ParentVersion             int64                 `json:"parent_version,omitempty"`
	ClearOrganisationalScopes bool                  `json:"clear_organisational_scopes,omitempty"`
	Version                   int64                 `json:"version"`
	ParentID                  ids.ID                `json:"parent_id,omitempty"`
	Detach                    bool                  `json:"detach"`
	BranchID                  ids.ID                `json:"branch_id,omitempty"`
	Attributes                *portfolio.Attributes `json:"attributes,omitempty"`
	RoomTypeID                *ids.ID               `json:"room_type_id,omitempty"`
	Timezone                  string                `json:"timezone,omitempty"`
	Currency                  string                `json:"currency,omitempty"`
	PolicyCode                string                `json:"policy_code,omitempty"`
	PolicyVersion             int64                 `json:"policy_version,omitempty"`
	CapacityCode              *string               `json:"capacity_code,omitempty"`
	Reason                    string                `json:"reason"`
	ApprovalID                ids.ID                `json:"approval_id"`
}
type InventoryLifecycle struct {
	TreeVersion int64  `json:"tree_version"`
	Version     int64  `json:"version"`
	State       string `json:"state"`
	Cascade     bool   `json:"cascade"`
	Reason      string `json:"reason"`
	ApprovalID  ids.ID `json:"approval_id,omitempty"`
}
type InventoryMembership struct {
	PreviousParentID      ids.ID `json:"previous_parent_id,omitempty"`
	PreviousParentVersion int64  `json:"previous_parent_version,omitempty"`
	TreeVersion           int64  `json:"tree_version"`
	ParentVersion         int64  `json:"parent_version"`
	Version               int64  `json:"version"`
	ParentID              ids.ID `json:"parent_id"`
	Relation              string `json:"relation"`
	Remove                bool   `json:"remove"`
	Reason                string `json:"reason"`
	ApprovalID            ids.ID `json:"approval_id"`
}

func (h *Handler) registerInventoryChanges(r chi.Router) {
	r.Post("/inventory-nodes/{id}/move", h.inventoryMove)
	r.Post("/inventory-nodes/{id}/lifecycle", h.inventoryLifecycle)
	r.Post("/inventory-nodes/{id}/relationships", h.inventoryMembership)
}

func inventoryOperation(id ids.ID, command any) map[string]any {
	return map[string]any{"node_id": id, "command": command}
}
func (h *Handler) inventoryPreflight(r *http.Request, t tenant, n portfolio.Node, operation any) ([]portfolio.Node, error) {
	nodes, err := portfolio.PhysicalSubtree(r.Context(), h.Auth.Service.Pool, t.Organisation, n.ID)
	if err != nil {
		return nil, err
	}
	digest, err := operationDigest(approvals.ActionInventoryRestructure, operation)
	if err != nil {
		return nil, err
	}
	safetyNodes := nodes
	if n.Kind == portfolio.Portfolio {
		safetyNodes, err = portfolio.RelatedSubtree(r.Context(), h.Auth.Service.Pool, t.Organisation, n.ID)
		if err != nil {
			return nil, err
		}
	}
	err = portfolio.CheckSafety(r.Context(), h.InventoryDependencies, safetyNodes, digest, h.Auth.Service.Now().UTC())
	return nodes, err
}
func (h *Handler) inventorySnapshot(ctx context.Context, tx pgx.Tx, t tenant, expected []portfolio.Node) ([]portfolio.Node, error) {
	if len(expected) == 0 {
		return nil, apperrors.Conflict("GRAPH_CHANGED", "Inventory snapshot unavailable.")
	}
	nodes, err := portfolio.PhysicalSubtree(ctx, tx, t.Organisation, expected[0].ID)
	if err != nil {
		return nil, err
	}
	if len(nodes) != len(expected) {
		return nil, apperrors.Conflict("GRAPH_CHANGED", "Inventory subtree changed.")
	}
	// Both snapshots are sorted by canonical ID; the original root need not sort
	// first, so callers pass the actual root explicitly via the first entry.
	byID := map[ids.ID]int64{}
	for _, n := range expected {
		byID[n.ID] = n.Version
	}
	for _, n := range nodes {
		if byID[n.ID] != n.Version {
			return nil, apperrors.Conflict("VERSION_CONFLICT", "An affected inventory version changed.")
		}
		if _, _, err = h.authorize(ctx, tx, "inventory:restructure", string(n.Kind), n.ID, true); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}
func rootFirst(nodes []portfolio.Node, id ids.ID) []portfolio.Node {
	out := append([]portfolio.Node(nil), nodes...)
	i := slices.IndexFunc(out, func(n portfolio.Node) bool { return n.ID == id })
	if i > 0 {
		out[0], out[i] = out[i], out[0]
	}
	return out
}
func (h *Handler) inventoryMove(w http.ResponseWriter, r *http.Request) {
	var in InventoryMove
	if !decode(w, r, &in) {
		return
	}
	t, n, err := h.inventoryTarget(r, "inventory:restructure", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if in.Version < 1 || in.TreeVersion < 1 || !in.ParentID.IsZero() && in.ParentVersion < 1 || !validStaffReason(in.Reason) || in.Detach && !in.ParentID.IsZero() {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A current version, valid destination and reason are required."))
		return
	}
	operationCommand := in
	operationCommand.ApprovalID = ids.Nil
	operation := inventoryOperation(n.ID, operationCommand)
	snapshot, err := h.inventoryPreflight(r, t, n, operation)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	snapshot = rootFirst(snapshot, n.ID)
	h.execute(w, r, "inventory:move:"+n.ID.String(), in, "inventory:restructure", string(n.Kind), n.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "inventory:restructure", string(n.Kind), n.ID, true)
		if err != nil {
			return 0, nil, err
		}
		nodes, err := h.inventorySnapshot(ctx, tx, t, snapshot)
		if err != nil {
			return 0, nil, err
		}
		current, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		if err != nil {
			return 0, nil, err
		}
		if current.Version != in.Version || current.TreeVersion != in.TreeVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Inventory version changed.")
		}
		if err = h.requirePrivilegeChange(ctx, tx, t, approvals.ActionInventoryRestructure, target.Scope, in.ApprovalID, operation); err != nil {
			return 0, nil, err
		}
		branch := current.BranchID
		timezone, currency := current.Timezone, current.Currency
		var destinationDepartment, destinationTeam *ids.ID
		if !in.ParentID.IsZero() {
			parent, e := portfolio.Get(ctx, tx, t.Organisation, in.ParentID)
			if e != nil {
				return 0, nil, e
			}
			if parent.TreeVersion != in.ParentVersion {
				return 0, nil, apperrors.Conflict("DESTINATION_CHANGED", "Destination structure changed since approval.")
			}
			if !portfolio.LegalEdge(parent.Kind, n.Kind, "CONTAINS") {
				return 0, nil, apperrors.InvalidRequest("Invalid physical destination.")
			}
			if _, _, e = h.authorize(ctx, tx, "inventory:restructure", string(parent.Kind), parent.ID, true); e != nil {
				return 0, nil, e
			}
			branch = parent.BranchID
			timezone = parent.Timezone
			currency = parent.Currency
			destinationDepartment, destinationTeam = parent.DepartmentID, parent.TeamID
		}
		if !in.BranchID.IsZero() {
			if !in.ParentID.IsZero() && in.BranchID != branch {
				return 0, nil, apperrors.InvalidRequest("Destination branch conflicts with parent.")
			}
			branch = in.BranchID
			if _, _, err = h.authorize(ctx, tx, "inventory:restructure", "BRANCH", branch, true); err != nil {
				return 0, nil, err
			}
			if err = tx.QueryRow(ctx, `SELECT timezone,currency FROM branches WHERE organisation_id=$1 AND id=$2 AND status='ACTIVE'`, t.Organisation, branch).Scan(&timezone, &currency); err != nil {
				return 0, nil, err
			}
		}
		if branch != current.BranchID {
			for _, node := range nodes {
				if (node.DepartmentID != nil || node.TeamID != nil) && !in.ClearOrganisationalScopes {
					return 0, nil, apperrors.Conflict("SCOPED_MEMBERSHIP_REVIEW_REQUIRED", "Clear departmental/team associations through a reviewed migration before changing branch.")
				}
			}
		}
		if in.Detach || !in.ParentID.IsZero() {
			if in.Detach && !n.Kind.RootAllowed() {
				return 0, nil, apperrors.InvalidRequest("Required physical parent cannot be removed.")
			}
			if _, err = portfolio.RemoveEdges(ctx, tx, t.Organisation, ids.Nil, n.ID, "CONTAINS", t.Account); err != nil {
				return 0, nil, err
			}
		}

		changed := current
		if in.Timezone != "" {
			timezone = in.Timezone
		}
		if in.Currency != "" {
			currency = in.Currency
		}
		changed.Timezone, changed.Currency, changed.BranchID = timezone, currency, branch
		if in.PolicyCode != "" {
			changed.PolicyCode = in.PolicyCode
		}
		if in.PolicyVersion > 0 {
			changed.PolicyVersion = in.PolicyVersion
		}
		if in.CapacityCode != nil {
			changed.CapacityCode = *in.CapacityCode
		}
		if in.Attributes != nil {
			changed.Attributes = *in.Attributes
		}
		if changed.Validate() != nil {
			return 0, nil, apperrors.InvalidRequest("Invalid structural attributes.")
		}
		attrs, _ := json.Marshal(changed.Attributes)
		for _, node := range nodes {
			if node.ID == n.ID {
				_, err = tx.Exec(ctx, `UPDATE inventory_nodes SET branch_id=$3,timezone=$4,currency=$5,attributes=$6,room_type_id=coalesce($7,room_type_id),department_id=coalesce($13,CASE WHEN $9 THEN NULL ELSE department_id END),team_id=coalesce($14,CASE WHEN $9 THEN NULL ELSE team_id END),policy_code=$10,policy_version=$11,capacity_code=$12,version=version+1,updated_at=now(),updated_by=$8 WHERE organisation_id=$1 AND id=$2`, t.Organisation, node.ID, branch, timezone, currency, attrs, in.RoomTypeID, t.Account, in.ClearOrganisationalScopes, changed.PolicyCode, changed.PolicyVersion, changed.CapacityCode, destinationDepartment, destinationTeam)
			} else {
				_, err = tx.Exec(ctx, `UPDATE inventory_nodes SET branch_id=$3,timezone=$4,currency=$5,department_id=coalesce($8,CASE WHEN $7 THEN NULL ELSE department_id END),team_id=coalesce($9,CASE WHEN $7 THEN NULL ELSE team_id END),version=version+1,updated_at=now(),updated_by=$6 WHERE organisation_id=$1 AND id=$2`, t.Organisation, node.ID, branch, timezone, currency, t.Account, in.ClearOrganisationalScopes, destinationDepartment, destinationTeam)
			}
			if err != nil {
				return 0, nil, err
			}
		}
		if !in.ParentID.IsZero() {
			if err = portfolio.AddEdge(ctx, tx, t.Organisation, in.ParentID, n.ID, "CONTAINS", t.Account); err != nil {
				return 0, nil, err
			}
		}
		for _, node := range nodes {

			before := node
			if err = (portfolio.Repository{Outbox: h.Auth.Service.Outbox}).Commit(ctx, tx, t.Organisation, node.ID, t.Account, "RESTRUCTURED", &before); err != nil {
				return 0, nil, err
			}
		}
		if err = audit(ctx, tx, t, "INVENTORY_RESTRUCTURED", "inventory_node", n.ID, map[string]any{"reason": in.Reason, "operation": operation}); err != nil {
			return 0, nil, err
		}
		out, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		return 200, out, err
	})
}
func (h *Handler) inventoryLifecycle(w http.ResponseWriter, r *http.Request) {
	var in InventoryLifecycle
	if !decode(w, r, &in) {
		return
	}
	t, n, err := h.inventoryTarget(r, "inventory:restructure", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if in.Version < 1 || in.TreeVersion < 1 || !validStaffReason(in.Reason) || !portfolio.Transition(n.State, in.State) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid lifecycle transition, version or reason."))
		return
	}
	command := in
	command.ApprovalID = ids.Nil
	operation := inventoryOperation(n.ID, command)
	snapshot, err := portfolio.PhysicalSubtree(r.Context(), h.Auth.Service.Pool, t.Organisation, n.ID)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	safetyNodes := snapshot
	if n.Kind == portfolio.Portfolio {
		safetyNodes, err = portfolio.RelatedSubtree(r.Context(), h.Auth.Service.Pool, t.Organisation, n.ID)
		if err != nil {
			reply(w, r, 0, nil, err)
			return
		}
	}
	needsApproval := false
	for _, node := range safetyNodes {
		needsApproval = needsApproval || node.ActivatedAt != nil || node.State != "DRAFT"
	}
	if in.State != "ACTIVE" {
		if err = portfolio.CheckSafety(r.Context(), h.InventoryDependencies, safetyNodes, mustInventoryDigest(operation), h.Auth.Service.Now().UTC()); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
	}
	snapshot = rootFirst(snapshot, n.ID)
	h.execute(w, r, "inventory:lifecycle:"+n.ID.String(), in, "inventory:restructure", string(n.Kind), n.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "inventory:restructure", string(n.Kind), n.ID, true)
		if err != nil {
			return 0, nil, err
		}
		nodes, err := h.inventorySnapshot(ctx, tx, t, snapshot)
		if err != nil {
			return 0, nil, err
		}
		current, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		if err != nil {
			return 0, nil, err
		}
		if current.Version != in.Version || current.TreeVersion != in.TreeVersion {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Inventory version changed.")
		}
		if in.State == "ACTIVE" {
			if err = h.inventoryActivation(ctx, tx, t, current); err != nil {
				return 0, nil, err
			}
		} else if needsApproval {
			if err = h.requirePrivilegeChange(ctx, tx, t, approvals.ActionInventoryRestructure, target.Scope, in.ApprovalID, operation); err != nil {
				return 0, nil, err
			}
		}
		if len(nodes) > 1 && !in.Cascade && in.State != "ACTIVE" {
			return 0, nil, apperrors.Conflict("DEPENDENT_INVENTORY", "Explicit cascade is required for nonempty inventory.")
		}
		if in.State == "ACTIVE" && in.Cascade {
			return 0, nil, apperrors.InvalidRequest("Activate each structural identity with its own current prerequisites.")
		}
		for _, node := range nodes {
			if node.ID != n.ID && !in.Cascade {
				continue
			}
			if !portfolio.Transition(node.State, in.State) {
				return 0, nil, apperrors.Conflict("DESCENDANT_LIFECYCLE_CONFLICT", "A descendant cannot enter the requested state.")
			}
			if _, err = tx.Exec(ctx, `UPDATE inventory_nodes SET state=$3,activated_at=CASE WHEN $3='ACTIVE' THEN coalesce(activated_at,now()) ELSE activated_at END,version=version+1,updated_at=now(),updated_by=$4 WHERE organisation_id=$1 AND id=$2`, t.Organisation, node.ID, in.State, t.Account); err != nil {
				return 0, nil, err
			}
			if node.Kind == portfolio.Property {
				if _, err = tx.Exec(ctx, `UPDATE inventory_verification_links SET node_version=(SELECT version FROM inventory_nodes WHERE organisation_id=$1 AND id=$2) WHERE organisation_id=$1 AND node_id=$2 AND node_version=$3`, t.Organisation, node.ID, node.Version); err != nil {
					return 0, nil, err
				}
			}

			before := node
			if err = (portfolio.Repository{Outbox: h.Auth.Service.Outbox}).Commit(ctx, tx, t.Organisation, node.ID, t.Account, "LIFECYCLE_CHANGED", &before); err != nil {
				return 0, nil, err
			}
		}
		if err = audit(ctx, tx, t, "INVENTORY_LIFECYCLE_CHANGED", "inventory_node", n.ID, in); err != nil {
			return 0, nil, err
		}
		out, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		return 200, out, err
	})
}
func mustInventoryDigest(operation any) string {
	digest, _ := operationDigest(approvals.ActionInventoryRestructure, operation)
	return digest
}
func (h *Handler) inventoryActivation(ctx context.Context, tx pgx.Tx, t tenant, n portfolio.Node) error {
	if t.State != "ACTIVE" || n.CapacityCode == "" || h.CapacityAuthority == nil || !h.CapacityAuthority.SupportsCapacity(n.CapacityCode) {
		return apperrors.PermissionDenied("Active KYB and a supported verified operating capacity are required.")
	}
	var c organisation.Capacity
	err := tx.QueryRow(ctx, `SELECT c.code,c.status,c.effective_at,c.expires_at,c.restricted_at,v.case_id,v.provider_status,v.expires_at FROM organisation_capacities c JOIN verification_links v ON v.organisation_id=c.organisation_id AND v.id=c.verification_link_id AND v.purpose='CAPACITY:'||c.code WHERE c.organisation_id=$1 AND c.code=$2`, t.Organisation, n.CapacityCode).Scan(&c.Code, &c.Status, &c.EffectiveAt, &c.ExpiresAt, &c.RestrictedAt, &c.Verification.CaseID, &c.Verification.Status, &c.Verification.ExpiresAt)
	if err != nil || !c.Authorizes(n.CapacityCode, organisation.Active, h.Auth.Service.Now().UTC()) {
		return apperrors.PermissionDenied("Current verified operating capacity required.")
	}
	var parentInactive bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_edges e JOIN inventory_nodes p ON p.id=e.parent_id AND p.organisation_id=e.organisation_id WHERE e.organisation_id=$1 AND e.child_id=$2 AND e.relation='CONTAINS' AND p.state<>'ACTIVE')`, t.Organisation, n.ID).Scan(&parentInactive); err != nil {
		return err
	}
	if parentInactive {
		return apperrors.Conflict("PARENT_INACTIVE", "Activate the physical parent first.")
	}
	var authorityID *ids.ID
	if err = tx.QueryRow(ctx, `SELECT business_graph_property($1,$2)`, t.Organisation, n.ID).Scan(&authorityID); err != nil {
		return err
	}
	if n.Kind == portfolio.Parcel {
		authorityID = &n.ID
	}
	if authorityID != nil {
		var verified bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_verification_links v JOIN inventory_nodes p ON p.organisation_id=v.organisation_id AND p.id=v.node_id WHERE v.organisation_id=$1 AND v.node_id=$2 AND v.provider_status='VERIFIED' AND v.node_version=p.version AND v.expires_at>$3)`, t.Organisation, *authorityID, h.Auth.Service.Now().UTC()).Scan(&verified)
		if err != nil {
			return err
		}
		if !verified {
			return apperrors.PermissionDenied("Current authoritative property verification is required.")
		}
	}
	return nil
}
func (h *Handler) inventoryMembership(w http.ResponseWriter, r *http.Request) {
	var in InventoryMembership
	if !decode(w, r, &in) {
		return
	}
	t, n, err := h.inventoryTarget(r, "inventory:restructure", true)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if in.ParentID.ValidateV7() != nil || in.Version < 1 || in.TreeVersion < 1 || in.ParentVersion < 1 || !validStaffReason(in.Reason) || in.Relation == "CONTAINS" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Use a valid grouping/association relationship and current version."))
		return
	}
	if !in.PreviousParentID.IsZero() && (in.PreviousParentID.ValidateV7() != nil || in.PreviousParentVersion < 1 || in.Remove || in.Relation != "MEMBERSHIP" || in.PreviousParentID == in.ParentID) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("A portfolio transfer requires distinct current source/destination portfolios and their subtree versions."))
		return
	}
	command := in
	command.ApprovalID = ids.Nil
	operation := inventoryOperation(n.ID, command)
	snapshot, err := h.inventoryPreflight(r, t, n, operation)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	snapshot = rootFirst(snapshot, n.ID)
	h.execute(w, r, "inventory:relationship:"+n.ID.String(), in, "inventory:restructure", string(n.Kind), n.ID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, err := h.authorize(ctx, tx, "inventory:restructure", string(n.Kind), n.ID, true)
		if err != nil {
			return 0, nil, err
		}
		if _, err = h.inventorySnapshot(ctx, tx, t, snapshot); err != nil {
			return 0, nil, err
		}
		parent, err := portfolio.Get(ctx, tx, t.Organisation, in.ParentID)
		if err != nil {
			return 0, nil, err
		}
		if _, _, err = h.authorize(ctx, tx, "inventory:restructure", string(parent.Kind), parent.ID, true); err != nil {
			return 0, nil, err
		}
		if parent.TreeVersion != in.ParentVersion {
			return 0, nil, apperrors.Conflict("DESTINATION_CHANGED", "Destination structure changed.")
		}
		if !portfolio.LegalEdge(parent.Kind, n.Kind, in.Relation) {
			return 0, nil, apperrors.InvalidRequest("Invalid grouping/association types.")
		}
		if !in.PreviousParentID.IsZero() {
			previous, e := portfolio.Get(ctx, tx, t.Organisation, in.PreviousParentID)
			if e != nil {
				return 0, nil, e
			}
			if previous.Kind != portfolio.Portfolio || previous.TreeVersion != in.PreviousParentVersion {
				return 0, nil, apperrors.Conflict("SOURCE_COLLECTION_CHANGED", "Source portfolio changed since review.")
			}
			if _, _, e = h.authorize(ctx, tx, "inventory:restructure", string(previous.Kind), previous.ID, true); e != nil {
				return 0, nil, e
			}
		}
		current, e := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.TreeVersion != in.TreeVersion {
			return 0, nil, apperrors.Conflict("GRAPH_CHANGED", "Source structure changed.")
		}
		if err = h.requirePrivilegeChange(ctx, tx, t, approvals.ActionInventoryRestructure, target.Scope, in.ApprovalID, operation); err != nil {
			return 0, nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE inventory_nodes SET version=version+1,updated_at=now(),updated_by=$4 WHERE organisation_id=$1 AND id=$2 AND version=$3`, t.Organisation, n.ID, in.Version, t.Account)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Inventory version changed.")
		}
		if !in.PreviousParentID.IsZero() {
			count, e := portfolio.RemoveEdges(ctx, tx, t.Organisation, in.PreviousParentID, n.ID, "MEMBERSHIP", t.Account)
			if e != nil {
				return 0, nil, e
			}
			if count != 1 {
				return 0, nil, apperrors.Conflict("SOURCE_MEMBERSHIP_CHANGED", "Source portfolio no longer contains this identity.")
			}
		}
		if in.Remove {
			count, removeErr := portfolio.RemoveEdges(ctx, tx, t.Organisation, in.ParentID, n.ID, in.Relation, t.Account)
			err = removeErr
			if err == nil && count != 1 {
				return 0, nil, apperrors.NotFound("RELATIONSHIP_NOT_FOUND", "Relationship not found.")
			}

		} else {
			err = portfolio.AddEdge(ctx, tx, t.Organisation, in.ParentID, n.ID, in.Relation, t.Account)
		}
		if err != nil {
			return 0, nil, err
		}
		if err = (portfolio.Repository{Outbox: h.Auth.Service.Outbox}).Commit(ctx, tx, t.Organisation, n.ID, t.Account, "GROUP_MEMBERSHIP_CHANGED", &n); err != nil {
			return 0, nil, err
		}
		if err = audit(ctx, tx, t, "INVENTORY_RELATIONSHIP_CHANGED", "inventory_node", n.ID, in); err != nil {
			return 0, nil, err
		}
		out, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		return 200, out, err
	})
}
