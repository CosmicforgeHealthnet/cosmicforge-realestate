package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"

	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/permissions"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type InventoryCreate struct {
	BranchID      ids.ID               `json:"branch_id,omitempty"`
	DepartmentID  *ids.ID              `json:"department_id,omitempty"`
	TeamID        *ids.ID              `json:"team_id,omitempty"`
	ParentID      ids.ID               `json:"parent_id,omitempty"`
	PortfolioIDs  []ids.ID             `json:"portfolio_ids,omitempty"`
	RoomTypeID    *ids.ID              `json:"room_type_id,omitempty"`
	Code          string               `json:"code"`
	Name          string               `json:"name"`
	Timezone      string               `json:"timezone,omitempty"`
	Currency      string               `json:"currency,omitempty"`
	PolicyCode    string               `json:"policy_code,omitempty"`
	PolicyVersion int64                `json:"policy_version,omitempty"`
	CapacityCode  string               `json:"capacity_code,omitempty"`
	Attributes    portfolio.Attributes `json:"attributes"`
}
type InventoryPatch struct {
	Version    int64                 `json:"version"`
	Name       *string               `json:"name,omitempty"`
	Code       *string               `json:"code,omitempty"`
	Attributes *portfolio.Attributes `json:"attributes,omitempty"`
	RoomTypeID *ids.ID               `json:"room_type_id,omitempty"`
	Reason     string                `json:"reason"`
}

func (h *Handler) registerInventory(r chi.Router) {
	for path, kind := range portfolio.Collections {
		r.Get("/"+path, func(w http.ResponseWriter, r *http.Request) { h.inventoryList(w, r, kind, false) })
		r.Post("/"+path, func(w http.ResponseWriter, r *http.Request) { h.inventoryCreate(w, r, kind, false) })
		r.Get("/"+path+"/{id}", func(w http.ResponseWriter, r *http.Request) { h.inventoryGet(w, r, kind) })
		r.Patch("/"+path+"/{id}", func(w http.ResponseWriter, r *http.Request) { h.inventoryUpdate(w, r, kind) })
	}
	for path, kind := range map[string]portfolio.Kind{"units": portfolio.Unit, "room-types": portfolio.RoomType, "rooms": portfolio.Room, "facilities": portfolio.Facility} {
		r.Get("/properties/{id}/"+path, func(w http.ResponseWriter, r *http.Request) { h.inventoryList(w, r, kind, true) })
		r.Post("/properties/{id}/"+path, func(w http.ResponseWriter, r *http.Request) { h.inventoryCreate(w, r, kind, true) })
	}
	r.Get("/developments/{id}/phases", func(w http.ResponseWriter, r *http.Request) { h.inventoryList(w, r, portfolio.Phase, true) })
	r.Post("/developments/{id}/phases", func(w http.ResponseWriter, r *http.Request) { h.inventoryCreate(w, r, portfolio.Phase, true) })
	r.Get("/inventory-nodes/{id}/hierarchy", h.inventoryHierarchy)
	r.Get("/inventory-nodes/{id}/summary", h.inventorySummary)
	r.Get("/inventory-nodes/{id}/history", h.inventoryHistory)
	h.registerInventoryChanges(r)
	h.registerInventoryReferences(r)
	h.registerImports(r)
}
func (h *Handler) inventoryGet(w http.ResponseWriter, r *http.Request, kind portfolio.Kind) {
	t, n, err := h.inventoryTarget(r, portfolio.Capability(kind, false), false)
	if err == nil && n.Kind != kind {
		err = apperrors.NotFound("INVENTORY_NOT_FOUND", "Inventory resource not found.")
	}
	_ = t
	reply(w, r, 200, n, err)
}
func (h *Handler) inventoryTarget(r *http.Request, capability string, mfa bool) (tenant, portfolio.Node, error) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		return t, portfolio.Node{}, err
	}
	id, err := ids.ParseV7(chi.URLParam(r, "id"))
	if err != nil {
		return t, portfolio.Node{}, apperrors.InvalidRequest("Invalid inventory identifier.")
	}
	n, err := portfolio.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if err != nil {
		return t, n, err
	}
	t, _, err = h.authorize(r.Context(), h.Auth.Service.Pool, capability, string(n.Kind), n.ID, mfa)
	if err == nil && capability == "structure:read" && portfolio.Capability(n.Kind, false) != capability {
		t, _, err = h.authorize(r.Context(), h.Auth.Service.Pool, portfolio.Capability(n.Kind, false), string(n.Kind), n.ID, mfa)
	}
	return t, n, err
}
func (h *Handler) inventoryList(w http.ResponseWriter, r *http.Request, kind portfolio.Kind, nested bool) {
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	cap := portfolio.Capability(kind, false)
	if !t.Membership.RoleCapabilities.Has(permissions.Permission(cap)) {
		reply(w, r, 0, nil, apperrors.PermissionDenied("Inventory read capability required."))
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	var parent ids.ID
	if nested {
		parent, err = ids.ParseV7(chi.URLParam(r, "id"))
		if err == nil {
			var p portfolio.Node
			p, err = portfolio.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, parent)
			if err == nil && ((kind == portfolio.Phase && p.Kind != portfolio.Development) || (kind != portfolio.Phase && p.Kind != portfolio.Property)) {
				err = apperrors.NotFound("INVENTORY_NOT_FOUND", "Inventory parent not found.")
			}
			if err == nil {
				_, _, err = h.authorize(r.Context(), h.Auth.Service.Pool, cap, string(p.Kind), parent, false)
			}
		}
		if err != nil {
			reply(w, r, 0, nil, err)
			return
		}
	}
	branch := r.URL.Query().Get("branch_id")
	if branch != "" {
		if _, err = ids.ParseV7(branch); err != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid branch filter."))
			return
		}
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != "DRAFT" && state != "ACTIVE" && state != "INACTIVE" && state != "ARCHIVED" && state != "RETIRED" {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid inventory state filter."))
		return
	}
	search := r.URL.Query().Get("q")
	if len(search) > 200 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Search exceeds limit."))
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT n.id FROM inventory_nodes n WHERE organisation_id=$1 AND kind=$2 AND ($3='' OR branch_id::text=$3) AND ($4='' OR state=$4) AND ($5='' OR position(lower($5) in lower(name||' '||code))>0) AND ($6::uuid IS NULL OR business_graph_property($1,n.id)=$6 OR EXISTS(SELECT 1 FROM inventory_edges e WHERE e.organisation_id=$1 AND e.parent_id=$6 AND e.child_id=n.id AND e.relation='CONTAINS')) ORDER BY created_at DESC,id DESC LIMIT $7 OFFSET $8`, t.Organisation, string(kind), branch, state, search, parent, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
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
		reply(w, r, 0, nil, err)
		return
	}
	items := []portfolio.Node{}
	for _, id := range nodeIDs {
		if _, _, err = h.authorize(r.Context(), h.Auth.Service.Pool, cap, string(kind), id, false); err != nil {
			continue
		}
		n, e := portfolio.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		items = append(items, n)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, nil)
}
func (h *Handler) inventoryCreate(w http.ResponseWriter, r *http.Request, kind portfolio.Kind, nested bool) {
	var in InventoryCreate
	if !decode(w, r, &in) {
		return
	}
	var nestedID ids.ID
	var err error
	if nested {
		nestedID, err = ids.ParseV7(chi.URLParam(r, "id"))
		if err != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid parent identifier."))
			return
		}
		if in.ParentID.IsZero() {
			in.ParentID = nestedID
		}
	}
	t, err := h.load(r.Context(), h.Auth.Service.Pool)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	scopeKind, scopeID := "BRANCH", in.BranchID
	if !in.ParentID.IsZero() {
		p, e := portfolio.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, in.ParentID)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		scopeKind, scopeID = string(p.Kind), p.ID
	} else if len(in.PortfolioIDs) > 0 {
		scopeKind, scopeID = "PORTFOLIO", in.PortfolioIDs[0]
	}
	if scopeID.ValidateV7() != nil {
		reply(w, r, 0, nil, apperrors.InvalidRequest("An operational branch or authorised parent is required."))
		return
	}
	h.execute(w, r, "inventory:create:"+string(kind), in, portfolio.Capability(kind, true), scopeKind, scopeID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, portfolio.Capability(kind, true), scopeKind, scopeID, false)
		if err != nil {
			return 0, nil, err
		}
		n, err := h.createInventory(ctx, tx, t, kind, ids.New(), in)
		if err != nil {
			return 0, nil, err
		}
		if nested {
			var actual ids.ID
			if kind == portfolio.Phase {
				actual = in.ParentID
			} else {
				if err = tx.QueryRow(ctx, `SELECT business_graph_property($1,$2)`, t.Organisation, n.ID).Scan(&actual); err != nil {
					return 0, nil, err
				}
			}
			if actual != nestedID {
				return 0, nil, apperrors.PermissionDenied("Physical parent is outside the requested property/development.")
			}
		}
		return 201, n, nil
	})
}
func (h *Handler) createInventory(ctx context.Context, tx pgx.Tx, t tenant, kind portfolio.Kind, id ids.ID, in InventoryCreate) (portfolio.Node, error) {
	n := portfolio.Node{ID: id, OrganisationID: t.Organisation, BranchID: in.BranchID, DepartmentID: in.DepartmentID, TeamID: in.TeamID, Kind: kind, Code: in.Code, Name: in.Name, State: "DRAFT", Timezone: in.Timezone, Currency: in.Currency, PolicyCode: in.PolicyCode, PolicyVersion: in.PolicyVersion, CapacityCode: in.CapacityCode, Attributes: in.Attributes}
	if len(in.PortfolioIDs) > 20 {
		return n, apperrors.InvalidRequest("Portfolio membership limit exceeded.")
	}
	if !in.ParentID.IsZero() {
		p, err := portfolio.Get(ctx, tx, t.Organisation, in.ParentID)
		if err != nil {
			return n, err
		}
		if _, _, err = h.authorize(ctx, tx, portfolio.Capability(kind, true), string(p.Kind), p.ID, false); err != nil {
			return n, err
		}
		if !portfolio.LegalEdge(p.Kind, kind, "CONTAINS") || p.State == "ARCHIVED" || p.State == "RETIRED" {
			return n, apperrors.InvalidRequest("Invalid physical containment.")
		}
		if !n.BranchID.IsZero() && n.BranchID != p.BranchID {
			return n, apperrors.PermissionDenied("Physical children cannot cross branch boundaries.")
		}
		if p.DepartmentID != nil {
			if n.DepartmentID != nil && *n.DepartmentID != *p.DepartmentID {
				return n, apperrors.PermissionDenied("Physical department context cannot be overridden.")
			}
			n.DepartmentID = p.DepartmentID
		}
		if p.TeamID != nil {
			if n.TeamID != nil && *n.TeamID != *p.TeamID {
				return n, apperrors.PermissionDenied("Physical team context cannot be overridden.")
			}
			n.TeamID = p.TeamID
		}
		if n.CapacityCode == "" {
			n.CapacityCode = p.CapacityCode
		}

		n.BranchID = p.BranchID
		if n.Timezone != "" && n.Timezone != p.Timezone || n.Currency != "" && n.Currency != p.Currency {
			return n, apperrors.InvalidRequest("Physical child context must match its parent.")
		}
		n.Timezone = p.Timezone
		n.Currency = p.Currency
		if n.PolicyCode == "" {
			n.PolicyCode = p.PolicyCode
			n.PolicyVersion = p.PolicyVersion
		}
	}
	if n.BranchID.IsZero() && len(in.PortfolioIDs) > 0 {
		p, err := portfolio.Get(ctx, tx, t.Organisation, in.PortfolioIDs[0])
		if err != nil {
			return n, err
		}
		n.BranchID = p.BranchID
	}
	if in.ParentID.IsZero() && len(in.PortfolioIDs) > 0 {
		p, e := portfolio.Get(ctx, tx, t.Organisation, in.PortfolioIDs[0])
		if e != nil {
			return n, e
		}
		if n.BranchID != p.BranchID {
			if _, _, e = h.authorize(ctx, tx, portfolio.Capability(kind, true), "BRANCH", n.BranchID, false); e != nil {
				return n, e
			}
		}
	}

	if n.BranchID.ValidateV7() != nil {
		return n, apperrors.InvalidRequest("A valid operational branch is required.")
	}
	// A physical parent supplies branch authority; standalone creation requires
	// direct branch authority unless it is inside an authorised portfolio.
	if in.ParentID.IsZero() && len(in.PortfolioIDs) == 0 {
		if _, _, err := h.authorize(ctx, tx, portfolio.Capability(kind, true), "BRANCH", n.BranchID, false); err != nil {
			return n, err
		}
	}
	var timezone, currency string
	if err := tx.QueryRow(ctx, `SELECT timezone,currency FROM branches WHERE organisation_id=$1 AND id=$2 AND status='ACTIVE'`, t.Organisation, n.BranchID).Scan(&timezone, &currency); err != nil {
		return n, apperrors.PermissionDenied("Active owned branch required.")
	}
	if n.Timezone == "" {
		n.Timezone = timezone
	}
	if n.Currency == "" {
		n.Currency = currency
	}
	if n.PolicyCode == "" {
		n.PolicyCode = "STANDARD"
	}
	if n.PolicyVersion == 0 {
		n.PolicyVersion = 1
	}
	if n.DepartmentID != nil && in.DepartmentID != nil {
		if _, _, err := h.authorize(ctx, tx, portfolio.Capability(kind, true), "DEPARTMENT", *n.DepartmentID, false); err != nil {
			return n, err
		}
	}
	if n.TeamID != nil && in.TeamID != nil {
		if _, _, err := h.authorize(ctx, tx, portfolio.Capability(kind, true), "TEAM", *n.TeamID, false); err != nil {
			return n, err
		}
	}
	if err := portfolio.Create(ctx, tx, n, in.RoomTypeID, in.ParentID, t.Account); err != nil {
		return n, err
	}
	for _, p := range in.PortfolioIDs {
		if _, _, err := h.authorize(ctx, tx, portfolio.Capability(kind, true), "PORTFOLIO", p, false); err != nil {
			return n, err
		}
		if err := portfolio.AddEdge(ctx, tx, t.Organisation, p, n.ID, "MEMBERSHIP", t.Account); err != nil {
			return n, err
		}
	}
	if err := (portfolio.Repository{Outbox: h.Auth.Service.Outbox}).Commit(ctx, tx, t.Organisation, n.ID, t.Account, "CREATED", nil); err != nil {
		return n, err
	}
	if err := audit(ctx, tx, t, "INVENTORY_CREATED", "inventory_node", n.ID, map[string]any{"kind": kind, "branch_id": n.BranchID}); err != nil {
		return n, err
	}
	return portfolio.Get(ctx, tx, t.Organisation, n.ID)
}
func (h *Handler) inventoryUpdate(w http.ResponseWriter, r *http.Request, kind portfolio.Kind) {
	var in InventoryPatch
	if !decode(w, r, &in) {
		return
	}
	_, n, err := h.inventoryTarget(r, portfolio.Capability(kind, true), false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	if n.Kind != kind || in.Version < 1 || !validStaffReason(in.Reason) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current inventory version and reason required."))
		return
	}
	if in.RoomTypeID != nil && n.State != "DRAFT" {
		reply(w, r, 0, nil, apperrors.Conflict("RESTRUCTURE_REQUIRED", "Active room reassignment requires the controlled restructure workflow."))
		return
	}
	h.execute(w, r, "inventory:update:"+n.ID.String(), in, portfolio.Capability(kind, true), string(n.Kind), n.ID, false, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, err := h.authorize(ctx, tx, portfolio.Capability(kind, true), string(n.Kind), n.ID, false)
		if err != nil {
			return 0, nil, err
		}
		current, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		if err != nil {
			return 0, nil, err
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Inventory version changed.")
		}
		if current.State == "ARCHIVED" || current.State == "RETIRED" {
			return 0, nil, apperrors.Conflict("INVENTORY_IMMUTABLE", "Retired inventory cannot be edited.")
		}
		changed := current
		if in.Name != nil {
			changed.Name = *in.Name
		}
		if in.Code != nil {
			changed.Code = *in.Code
		}
		if in.Attributes != nil {
			changed.Attributes = *in.Attributes
		}
		if changed.Validate() != nil {
			return 0, nil, apperrors.InvalidRequest("Invalid inventory update.")
		}
		if in.Attributes != nil && current.ActivatedAt != nil {
			return 0, nil, apperrors.Conflict("RESTRUCTURE_REQUIRED", "Previously activated structural attributes require a reviewed restructure.")
		}
		attrs, _ := json.Marshal(changed.Attributes)
		tag, err := tx.Exec(ctx, `UPDATE inventory_nodes SET name=$3,code=$4,attributes=$5,room_type_id=coalesce($6,room_type_id),version=version+1,updated_at=now(),updated_by=$7 WHERE organisation_id=$1 AND id=$2 AND version=$8`, t.Organisation, n.ID, changed.Name, changed.Code, attrs, in.RoomTypeID, t.Account, in.Version)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Inventory version changed.")
		}
		if err = (portfolio.Repository{Outbox: h.Auth.Service.Outbox}).Commit(ctx, tx, t.Organisation, n.ID, t.Account, "UPDATED", &current); err != nil {
			return 0, nil, err
		}
		if err = audit(ctx, tx, t, "INVENTORY_UPDATED", "inventory_node", n.ID, map[string]any{"reason": in.Reason, "previous_version": in.Version}); err != nil {
			return 0, nil, err
		}
		out, err := portfolio.Get(ctx, tx, t.Organisation, n.ID)
		return 200, out, err
	})
}
func (h *Handler) inventoryHierarchy(w http.ResponseWriter, r *http.Request) {
	t, n, err := h.inventoryTarget(r, "structure:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `WITH RECURSIVE d(id) AS(SELECT $2::uuid UNION SELECT e.child_id FROM inventory_edges e JOIN d ON e.parent_id=d.id WHERE e.organisation_id=$1 AND e.relation IN ('CONTAINS','MEMBERSHIP')) SELECT n.id,n.kind FROM inventory_nodes n JOIN d ON d.id=n.id WHERE n.organisation_id=$1 ORDER BY n.id LIMIT $3 OFFSET $4`, t.Organisation, n.ID, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	refs := []struct {
		id   ids.ID
		kind string
	}{}
	for rows.Next() {
		var ref struct {
			id   ids.ID
			kind string
		}
		if err = rows.Scan(&ref.id, &ref.kind); err != nil {
			break
		}
		refs = append(refs, ref)
	}
	rowErr := rows.Err()
	rows.Close()
	if err == nil {
		err = rowErr
	}
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	items := []portfolio.Node{}
	counts := map[string]int{}
	for _, ref := range refs {
		if _, _, err = h.authorize(r.Context(), h.Auth.Service.Pool, "structure:read", ref.kind, ref.id, false); err != nil {
			continue
		}
		if _, _, err = h.authorize(r.Context(), h.Auth.Service.Pool, portfolio.Capability(portfolio.Kind(ref.kind), false), ref.kind, ref.id, false); err != nil {
			continue
		}
		item, e := portfolio.Get(r.Context(), h.Auth.Service.Pool, t.Organisation, ref.id)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		items = append(items, item)
		if item.State == "ACTIVE" {
			counts[ref.kind]++
		}
	}
	reply(w, r, 200, map[string]any{"root_id": n.ID, "items": items, "active_counts_on_page": counts, "limit": limit, "offset": offset}, nil)
}

// Summary counts only currently authorised identities. The same 10,000-node
// safety bound as restructuring prevents unbounded interactive graph scans.
func (h *Handler) inventorySummary(w http.ResponseWriter, r *http.Request) {
	t, n, err := h.inventoryTarget(r, "structure:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	nodes, err := portfolio.RelatedSubtree(r.Context(), h.Auth.Service.Pool, t.Organisation, n.ID)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	counts, active, states := map[string]int{}, map[string]int{}, map[string]int{}
	visible := 0
	for _, item := range nodes {
		if _, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, "structure:read", string(item.Kind), item.ID, false); e != nil {
			continue
		}
		if _, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, portfolio.Capability(item.Kind, false), string(item.Kind), item.ID, false); e != nil {
			continue
		}
		counts[string(item.Kind)]++
		states[item.State]++
		visible++
		if item.State == "ACTIVE" {
			active[string(item.Kind)]++
		}
	}
	reply(w, r, 200, map[string]any{"root_id": n.ID, "tree_version": n.TreeVersion, "visible_count": visible, "counts_by_kind": counts, "active_counts_by_kind": active, "counts_by_state": states}, nil)
}
func (h *Handler) inventoryHistory(w http.ResponseWriter, r *http.Request) {
	t, n, err := h.inventoryTarget(r, "structure:read", false)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	rows, err := h.Auth.Service.Pool.Query(r.Context(), `SELECT jsonb_build_object('id',id,'actor_id',actor_id,'operation',operation,'before',before_snapshot,'after',after_snapshot,'created_at',created_at,'correlation_id',correlation_id) FROM inventory_history WHERE organisation_id=$1 AND node_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, n.ID, limit, offset)
	if err != nil {
		reply(w, r, 0, nil, err)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			reply(w, r, 0, nil, err)
			return
		}
		items = append(items, raw)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset}, rows.Err())
}

func inventoryProvider(provider string) bool {
	return len(provider) > 0 && len(provider) <= 80 && strings.TrimSpace(provider) == provider && !strings.ContainsAny(provider, " /\\\n\r\t")
}
