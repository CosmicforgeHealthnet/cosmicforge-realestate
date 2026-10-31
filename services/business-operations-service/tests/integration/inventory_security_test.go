package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestBusinessInventoryTrustedScopeRevocationAndBranchIsolation(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, staff, outsider := h.user(), h.user(), h.user()
	h.secure(owner)
	a, b := h.inventoryBranch(owner), h.inventoryBranch(owner)
	p := h.inventoryNode(owner, "/properties", map[string]any{"branch_id": a, "code": "PROPERTY-A", "name": "Property A", "attributes": map[string]any{}})
	q := h.inventoryNode(owner, "/properties", map[string]any{"branch_id": b, "code": "PROPERTY-B", "name": "Property B", "attributes": map[string]any{}})
	unit := h.inventoryNode(owner, "/units", map[string]any{"parent_id": p.ID, "code": "A-101", "name": "Apartment A", "attributes": map[string]any{}})
	collection := h.inventoryNode(owner, "/portfolios", map[string]any{"branch_id": a, "code": "GROUP-A", "name": "Scoped collection", "attributes": map[string]any{}})
	grouped := h.inventoryNode(owner, "/properties", map[string]any{"portfolio_ids": []ids.ID{collection.ID}, "code": "GROUPED", "name": "Grouped property", "attributes": map[string]any{}})
	member := h.join(staff, owner, nil)
	org := owner.registered.OrganisationID
	grant := func(capability, kind string, id ids.ID) {
		t.Helper()
		if _, err := h.db.Pool.Exec(t.Context(), `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES($1,$2,$3,$4,$5,$6,true,now())`, ids.New(), org, member, capability, kind, id); err != nil {
			t.Fatal(err)
		}
	}
	grant("property:read", "PROPERTY", p.ID)
	grant("structure:read", "PROPERTY", p.ID)
	grant("property:write", "BRANCH", a)
	for _, test := range []struct {
		u    *businessUser
		path string
		want int
	}{{staff, "/properties/" + p.ID.String(), 200}, {staff, "/units/" + unit.ID.String(), 200}, {staff, "/properties/" + q.ID.String(), 403}, {outsider, "/properties/" + p.ID.String(), 404}} {
		if status, raw := h.call(test.u, "GET", test.path, nil, map[string]string{"X-Organisation-ID": org.String(), "X-Resource-Ancestors": p.ID.String()}); status != test.want {
			t.Fatalf("%s %d want %d %s", test.path, status, test.want, raw)
		}
	}
	if status, _ := h.call(staff, "POST", "/properties", map[string]any{"branch_id": b, "code": "ESCAPE", "name": "Forbidden branch", "attributes": map[string]any{}}, nil); status != 403 {
		t.Fatalf("cross-branch create %d", status)
	}
	if status, raw := h.call(staff, "POST", "/properties", map[string]any{"branch_id": a, "code": "ALLOWED", "name": "Permitted branch", "attributes": map[string]any{}}, nil); status != 201 {
		t.Fatalf("branch create %d %s", status, raw)
	}
	if _, err := h.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE membership_id=$1 AND capability='property:read'`, member); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.call(staff, "GET", "/properties/"+p.ID.String(), nil, nil); status != 403 {
		t.Fatalf("revoked grant retained access %d", status)
	}
	grant("property:read", "PORTFOLIO", collection.ID)
	if status, _ := h.call(staff, "GET", "/properties/"+grouped.ID.String(), nil, nil); status != 200 {
		t.Fatalf("trusted portfolio ancestry %d", status)
	}
	if status, _ := h.call(staff, "GET", "/properties/"+p.ID.String(), nil, map[string]string{"X-Portfolio-ID": collection.ID.String()}); status != 403 {
		t.Fatalf("forged group ancestry %d", status)
	}
	grant("property:write", "PORTFOLIO", collection.ID)
	if status, _ := h.call(staff, "POST", "/properties", map[string]any{"branch_id": b, "portfolio_ids": []ids.ID{collection.ID}, "code": "FORGED-BRANCH", "name": "Cannot use grouping as branch authority", "attributes": map[string]any{}}, nil); status != 403 {
		t.Fatalf("portfolio self-attachment branch escalation %d", status)
	}
	// Scoped delegation uses the same resolved graph, current source authority,
	// explicit role ceiling and effective dates as Section 3.
	h.secure(staff)
	expires := time.Now().UTC().Add(time.Hour)
	status, raw := h.call(owner, "POST", "/delegations", map[string]any{"delegate_membership_id": member, "grant": map[string]any{"capability": "property:read", "scope_kind": "PROPERTY", "scope_id": q.ID, "inherit": true, "expires_at": expires}, "reason": "Temporary property cover"}, nil)
	if status != 201 {
		t.Fatalf("delegate %d %s", status, raw)
	}
	var delegated struct {
		Data struct {
			ID ids.ID `json:"delegation_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &delegated)
	if status, _ := h.call(staff, "GET", "/properties/"+q.ID.String(), nil, nil); status != 200 {
		t.Fatalf("delegated property read %d", status)
	}
	if status, _ := h.call(owner, "DELETE", "/delegations/"+delegated.Data.ID.String(), map[string]any{"reason": "Cover ended"}, nil); status != 200 {
		t.Fatalf("revoke delegation %d", status)
	}
	if status, _ := h.call(staff, "GET", "/properties/"+q.ID.String(), nil, nil); status != 403 {
		t.Fatalf("delegation revocation ignored %d", status)
	}
}
func (h *businessHarness) inventoryApproval(owner, approver *businessUser, node portfolio.Node, command any, action string) ids.ID {
	h.t.Helper()
	operation := map[string]any{"node_id": node.ID, "command": command}
	status, raw := h.call(owner, "POST", "/approvals", map[string]any{"action": action, "scope_kind": string(node.Kind), "scope_id": node.ID, "operation": operation, "reason": "Independent inventory safety review"}, nil)
	if status != 201 {
		h.t.Fatalf("approval request %d %s", status, raw)
	}
	var created struct {
		Data struct {
			Approval struct {
				ID ids.ID `json:"id"`
			} `json:"approval"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		h.t.Fatal(err)
	}
	id := created.Data.Approval.ID
	if status, _ := h.call(owner, "POST", "/approvals/"+id.String()+"/decision", map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Requester must not self-approve"}, nil); status != 403 {
		h.t.Fatalf("self approval %d", status)
	}
	if status, raw = h.call(approver, "POST", "/approvals/"+id.String()+"/decision", map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Evidence and destination checked independently"}, nil); status != 200 {
		h.t.Fatalf("approve %d %s", status, raw)
	}
	return id
}
func TestBusinessInventoryReviewedMoveVersionsAndImmutableHistory(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, approver := h.user(), h.user()
	h.secure(owner)
	h.secure(approver)
	h.join(approver, owner, []string{"approvals:decide", "inventory:approve"})
	branch := h.inventoryBranch(owner)
	p := h.inventoryNode(owner, "/properties", map[string]any{"branch_id": branch, "code": "MOVE-PROPERTY", "name": "Property", "attributes": map[string]any{}})
	first := h.inventoryNode(owner, "/floors", map[string]any{"parent_id": p.ID, "code": "FIRST", "name": "First floor", "attributes": map[string]any{}})
	second := h.inventoryNode(owner, "/floors", map[string]any{"parent_id": p.ID, "code": "SECOND", "name": "Second floor", "attributes": map[string]any{}})
	unit := h.inventoryNode(owner, "/units", map[string]any{"parent_id": first.ID, "code": "101", "name": "Apartment", "attributes": map[string]any{}})
	policy := map[string]any{"action": "INVENTORY_RESTRUCTURE", "risk": "HIGH", "scope_kind": "COMPANY", "levels": []map[string]any{{"capability": "inventory:approve", "count": 1, "require_mfa": true}}}
	if status, raw := h.call(owner, "POST", "/approval-policies", policy, nil); status != 201 {
		t.Fatalf("policy %d %s", status, raw)
	}
	current, err := portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	command := map[string]any{"version": current.Version, "tree_version": current.TreeVersion, "parent_id": second.ID, "parent_version": destination.TreeVersion, "reason": "Transfer unused draft unit to its correct physical floor"}
	if status, _ := h.call(owner, "POST", "/inventory-nodes/"+unit.ID.String()+"/move", command, nil); status != 403 {
		t.Fatalf("unapproved move %d", status)
	}
	approval := h.inventoryApproval(owner, approver, current, command, "INVENTORY_RESTRUCTURE")
	command["approval_id"] = approval
	status, raw := h.call(owner, "POST", "/inventory-nodes/"+unit.ID.String()+"/move", command, map[string]string{"Idempotency-Key": "reviewed-unit-move"})
	if status != 200 {
		t.Fatalf("move %d %s", status, raw)
	}
	if status, again := h.call(owner, "POST", "/inventory-nodes/"+unit.ID.String()+"/move", command, map[string]string{"Idempotency-Key": "reviewed-unit-move"}); status != 200 || string(again) != string(raw) {
		t.Fatalf("move retry %d %s", status, again)
	}
	moved, err := portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, unit.ID)
	if err != nil || moved.Version != current.Version+1 || len(moved.Parents) != 1 || moved.Parents[0].ParentID != second.ID {
		t.Fatalf("move state %+v %v", moved, err)
	}
	if status, _ := h.call(owner, "POST", "/inventory-nodes/"+unit.ID.String()+"/move", command, nil); status != 409 {
		t.Fatalf("stale concurrent move %d", status)
	}
	var transitions int
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_edge_history WHERE organisation_id=$1 AND child_id=$2`, owner.registered.OrganisationID, unit.ID).Scan(&transitions); err != nil || transitions != 3 {
		t.Fatalf("edge history %d %v", transitions, err)
	}
	if _, err = h.db.Pool.Exec(t.Context(), `UPDATE inventory_edge_history SET action='REMOVED' WHERE child_id=$1`, unit.ID); err == nil {
		t.Fatal("relationship history mutable")
	}
	// An approval also freezes subtree and destination revisions, not only the
	// root metadata version. Adding a child after review must invalidate it.
	destination, err = portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleCommand := map[string]any{"version": moved.Version, "tree_version": moved.TreeVersion, "parent_id": first.ID, "parent_version": destination.TreeVersion, "reason": "Reviewed move with a frozen descendant snapshot"}
	staleApproval := h.inventoryApproval(owner, approver, moved, staleCommand, "INVENTORY_RESTRUCTURE")
	h.inventoryNode(owner, "/assets", map[string]any{"parent_id": unit.ID, "code": "NEW-CHILD", "name": "Acquired equipment", "attributes": map[string]any{}})
	staleCommand["approval_id"] = staleApproval
	if status, raw := h.call(owner, "POST", "/inventory-nodes/"+unit.ID.String()+"/move", staleCommand, nil); status != 409 {
		t.Fatalf("descendant revision bypass %d %s", status, raw)
	}
}

func TestBusinessInventoryAtomicReviewedPortfolioTransfer(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner, approver := h.user(), h.user()
	h.secure(owner)
	h.secure(approver)
	h.join(approver, owner, []string{"approvals:decide", "inventory:approve"})
	branch := h.inventoryBranch(owner)
	groups := []portfolio.Node{}
	for _, code := range []string{"SOURCE", "EXISTING", "DESTINATION"} {
		groups = append(groups, h.inventoryNode(owner, "/portfolios", map[string]any{"branch_id": branch, "code": code, "name": code, "attributes": map[string]any{}}))
	}
	p := h.inventoryNode(owner, "/properties", map[string]any{"branch_id": branch, "portfolio_ids": []ids.ID{groups[0].ID, groups[1].ID}, "code": "TRANSFER", "name": "Transferred property", "attributes": map[string]any{}})
	if status, raw := h.call(owner, "POST", "/approval-policies", map[string]any{"action": "INVENTORY_RESTRUCTURE", "risk": "HIGH", "scope_kind": "COMPANY", "levels": []map[string]any{{"capability": "inventory:approve", "count": 1, "require_mfa": true}}}, nil); status != 201 {
		t.Fatalf("policy %d %s", status, raw)
	}
	for _, destination := range []int{1, 2} {
		p, e := portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, p.ID)
		if e != nil {
			t.Fatal(e)
		}
		source, e := portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, groups[0].ID)
		if e != nil {
			t.Fatal(e)
		}
		target, e := portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, groups[destination].ID)
		if e != nil {
			t.Fatal(e)
		}
		command := map[string]any{"version": p.Version, "tree_version": p.TreeVersion, "parent_id": target.ID, "parent_version": target.TreeVersion, "previous_parent_id": source.ID, "previous_parent_version": source.TreeVersion, "relation": "MEMBERSHIP", "remove": false, "reason": "Atomic reviewed transfer retaining independent memberships"}
		approval := h.inventoryApproval(owner, approver, p, command, "INVENTORY_RESTRUCTURE")
		command["approval_id"] = approval
		status, raw := h.call(owner, "POST", "/inventory-nodes/"+p.ID.String()+"/relationships", command, nil)
		want := 200
		if destination == 1 {
			want = 409
		}
		if status != want {
			t.Fatalf("transfer %d want %d %s", status, want, raw)
		}
		var oldCount, newCount, total int
		if e = h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE parent_id=$3),count(*) FILTER(WHERE parent_id=$4),count(*) FROM inventory_edges WHERE organisation_id=$1 AND child_id=$2 AND relation='MEMBERSHIP'`, owner.registered.OrganisationID, p.ID, source.ID, target.ID).Scan(&oldCount, &newCount, &total); e != nil {
			t.Fatal(e)
		}
		if total != 2 || newCount != 1 || (destination == 1 && oldCount != 1) || (destination == 2 && oldCount != 0) {
			t.Fatalf("non-atomic transfer old=%d new=%d total=%d", oldCount, newCount, total)
		}
	}
}
