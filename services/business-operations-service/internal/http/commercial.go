package businesshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	businessEvents "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/events"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/database"
	apperrors "github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/errors"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) registerCommercial(r chi.Router) {
	r.Get("/inventory", h.productList)
	r.Put("/inventory", h.calendarSet)
	r.Get("/sellable-products", h.productList)
	r.Post("/sellable-products", h.productCreate)
	r.Get("/sellable-products/{id}", h.productGet)
	r.Patch("/sellable-products/{id}", h.productPatch)
	r.Get("/inventory-pools", h.poolList)
	r.Post("/inventory-pools", h.poolCreate)
	r.Get("/inventory-pools/{id}", h.poolGet)
	r.Patch("/inventory-pools/{id}", h.poolPatch)
	r.Get("/calendar", h.calendarGet)
	r.Get("/availability", h.availabilityGet)
	r.Post("/holds", h.holdCreate)
	r.Get("/holds", h.holdList)
	r.Get("/holds/{id}", h.holdGet)
	r.Delete("/holds/{id}", h.holdCancel)
	r.Post("/holds/{id}/extend", h.holdExtend)
	r.Post("/holds/{id}/handoff", h.holdHandoff)
	r.Get("/allotments", h.allotmentList)
	r.Post("/allotments", h.allotmentCreate)
	r.Post("/allotments/{id}/release", h.allotmentRelease)
	h.registerRates(r)
	h.registerCommercialContent(r)
	h.registerDistribution(r)
}
func pathCommercialID(r *http.Request) (ids.ID, error) {
	id, e := ids.ParseV7(chi.URLParam(r, "id"))
	if e != nil {
		return ids.Nil, apperrors.InvalidRequest("A canonical UUIDv7 identifier is required.")
	}
	return id, nil
}
func queryCommercialID(r *http.Request, key string) (ids.ID, error) {
	id, e := ids.ParseV7(r.URL.Query().Get(key))
	if e != nil {
		return ids.Nil, apperrors.InvalidRequest("A valid " + key + " query parameter is required.")
	}
	return id, nil
}
func (h *Handler) commercialScope(r *http.Request, capability string, property ids.ID, mfa bool) (tenant, error) {
	t, _, e := h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", property, mfa)
	return t, e
}
func (h *Handler) commercialGate(ctx context.Context, q database.Querier, t tenant, property ids.ID) error {
	n, e := portfolio.Get(ctx, q, t.Organisation, property)
	if e != nil {
		return e
	}
	if n.Kind != portfolio.Property || n.State != "ACTIVE" || t.State != "ACTIVE" {
		return apperrors.PermissionDenied("Current active business and property authority are required for commercial use.")
	}
	if h.CapacityAuthority == nil || !h.CapacityAuthority.SupportsCapacity(n.CapacityCode) {
		return apperrors.Unavailable("The owning verified operating-capacity assertion is unavailable.")
	}
	tx, ok := q.(pgx.Tx)
	if !ok {
		return apperrors.Unavailable("Commercial eligibility requires an owning snapshot transaction.")
	}
	if e = h.inventoryActivation(ctx, tx, t, n); e != nil {
		return e
	}
	var held bool
	e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM compliance_holds WHERE organisation_id=$1 AND property_id=$2 AND revoked_at IS NULL AND effective_at<=$3 AND (expires_at IS NULL OR expires_at>$3))`, t.Organisation, property, h.Auth.Service.Now().UTC()).Scan(&held)
	if e != nil {
		return e
	}
	if held {
		return apperrors.PermissionDenied("A hard compliance hold prohibits commercial use.")
	}
	return nil
}
func (h *Handler) commercialCommit(ctx context.Context, tx pgx.Tx, t tenant, property, target ids.ID, kind, operation, event string, version int64, snapshot any) error {
	return businessEvents.CommercialCommit(ctx, tx, h.Auth.Service.Outbox, t.Organisation, property, target, t.Account, kind, operation, event, version, snapshot)
}
func commercialTransition(from, to string) bool {
	return from == "DRAFT" && (to == "ACTIVE" || to == "ARCHIVED" || to == "RETIRED") || from == "ACTIVE" && (to == "SUSPENDED" || to == "ARCHIVED" || to == "RETIRED") || from == "SUSPENDED" && (to == "ACTIVE" || to == "ARCHIVED" || to == "RETIRED")
}
func commercialName(value string) bool {
	return len(strings.TrimSpace(value)) > 0 && len(value) <= 200 && !strings.ContainsRune(value, 0)
}
func commercialChannelList(channels []string) bool {
	if len(channels) > 50 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range channels {
		if c == "" || !inventory.ValidChannel(c) || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

// listCommercial is used only with server-owned, constant table identifiers.
// Actual scope checks happen before returning each typed/public projection.
func (h *Handler) listCommercial(w http.ResponseWriter, r *http.Request, table, capability string, projection func(context.Context, ids.ID, ids.ID) (any, error)) {
	allowed := map[string]bool{"reservation_policies": true, "reservation_groups": true, "reservation_conflicts": true, "waitlist_entries": true, "inventory_pools": true, "sellable_products": true, "inventory_holds": true, "inventory_allotments": true, "rate_plans": true, "rate_restrictions": true, "promotions": true, "packages": true, "add_ons": true, "listings": true, "tax_profiles": true, "compliance_holds": true, "channel_connections": true, "channel_sync_jobs": true, "channel_conflicts": true}
	if !allowed[table] {
		reply(w, r, 0, nil, apperrors.Unavailable("Unknown commercial collection."))
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	limit, offset, e := pagination(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	filter := r.URL.Query().Get("property_id")
	if filter != "" {
		if _, e = ids.ParseV7(filter); e != nil {
			reply(w, r, 0, nil, apperrors.InvalidRequest("Invalid property filter."))
			return
		}
	}
	orderColumn := "created_at"
	if table == "channel_conflicts" {
		orderColumn = "first_seen_at"
	}
	rows, e := h.Auth.Service.Pool.Query(r.Context(), `SELECT id,property_id FROM `+table+` WHERE organisation_id=$1 AND ($2='' OR property_id::text=$2) ORDER BY `+orderColumn+` DESC,id DESC LIMIT $3 OFFSET $4`, t.Organisation, filter, limit, offset)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	type reference struct{ id, property ids.ID }
	refs := []reference{}
	for rows.Next() {
		var item reference
		if e = rows.Scan(&item.id, &item.property); e != nil {
			break
		}
		refs = append(refs, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if e == nil {
		e = rowErr
	}
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	items := []any{}
	for _, ref := range refs {
		if _, _, e = h.authorize(r.Context(), h.Auth.Service.Pool, capability, "PROPERTY", ref.property, false); e != nil {
			continue
		}
		item, e := projection(r.Context(), t.Organisation, ref.id)
		if e != nil {
			reply(w, r, 0, nil, e)
			return
		}
		items = append(items, item)
	}
	reply(w, r, 200, map[string]any{"items": items, "limit": limit, "offset": offset, "next_offset": offset + len(refs)}, nil)
}
func (h *Handler) poolList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "inventory_pools", "pools:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return inventory.GetPool(ctx, h.Auth.Service.Pool, org, id)
	})
}
func (h *Handler) productList(w http.ResponseWriter, r *http.Request) {
	h.listCommercial(w, r, "sellable_products", "products:read", func(ctx context.Context, org, id ids.ID) (any, error) {
		return inventory.GetProduct(ctx, h.Auth.Service.Pool, org, id)
	})
}
func (h *Handler) poolGet(w http.ResponseWriter, r *http.Request) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	pool, e := inventory.GetPool(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		_, e = h.commercialScope(r, "pools:read", pool.PropertyID, false)
	}
	reply(w, r, 200, pool, e)
}
func (h *Handler) productGet(w http.ResponseWriter, r *http.Request) {
	_, p, e := h.productTarget(r, "products:read", false)
	reply(w, r, 200, p, e)
}
func (h *Handler) productTarget(r *http.Request, cap string, mfa bool) (tenant, inventory.Product, error) {
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		return t, inventory.Product{}, e
	}
	id, e := pathCommercialID(r)
	if e != nil {
		return t, inventory.Product{}, e
	}
	p, e := inventory.GetProduct(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e == nil {
		t, e = h.commercialScope(r, cap, p.PropertyID, mfa)
	}
	return t, p, e
}

type CommercialPoolCreate struct {
	PropertyID ids.ID   `json:"property_id"`
	NodeID     ids.ID   `json:"node_id"`
	Name       string   `json:"name"`
	MemberIDs  []ids.ID `json:"member_ids"`
	Capacity   int      `json:"capacity,omitempty"`
}

func (h *Handler) poolCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialPoolCreate
	if !decode(w, r, &in) {
		return
	}
	if in.PropertyID.ValidateV7() != nil || in.NodeID.ValidateV7() != nil || !commercialName(in.Name) || len(in.MemberIDs) > 1000 || in.Capacity < 0 {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid property/source, name and bounded physical membership are required."))
		return
	}
	h.execute(w, r, "commercial:pool:create", in, "pools:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pools:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		source, e := portfolio.Get(ctx, tx, t.Organisation, in.NodeID)
		if e != nil {
			return 0, nil, e
		}
		if source.PropertyID == nil || *source.PropertyID != in.PropertyID || !slices.Contains([]portfolio.Kind{portfolio.Unit, portfolio.Room, portfolio.RoomType, portfolio.Facility}, source.Kind) {
			return 0, nil, apperrors.InvalidRequest("Pool source must be a unit, room category, room or facility in this canonical property.")
		}
		memberIDs := in.MemberIDs
		if source.Kind != portfolio.RoomType {
			if len(memberIDs) > 0 && (len(memberIDs) != 1 || memberIDs[0] != source.ID) {
				return 0, nil, apperrors.InvalidRequest("A physical unit/room/facility pool uses exactly its own canonical source.")
			}
			memberIDs = []ids.ID{source.ID}
		}
		if len(memberIDs) == 0 {
			return 0, nil, apperrors.InvalidRequest("Room-category pools require explicit physical room IDs.")
		}
		members := []portfolio.Node{}
		seen := map[ids.ID]bool{}
		capacity := 0
		for _, id := range memberIDs {
			if id.ValidateV7() != nil || seen[id] {
				return 0, nil, apperrors.InvalidRequest("Physical stock IDs must be unique UUIDv7 values.")
			}
			seen[id] = true
			n, e := portfolio.Get(ctx, tx, t.Organisation, id)
			if e != nil {
				return 0, nil, e
			}
			if n.PropertyID == nil || *n.PropertyID != in.PropertyID || source.Kind == portfolio.RoomType && (n.Kind != portfolio.Room || n.RoomTypeID == nil || *n.RoomTypeID != source.ID) {
				return 0, nil, apperrors.InvalidRequest("Every pooled room must use this same-property room category.")
			}
			members = append(members, n)
			capacity++
		}
		if source.Kind == portfolio.Facility {
			if source.Attributes.Capacity == nil || *source.Attributes.Capacity < 1 {
				return 0, nil, apperrors.InvalidRequest("Facility capacity must be declared in the canonical physical graph.")
			}
			capacity = *source.Attributes.Capacity
			if in.Capacity > 0 {
				capacity = in.Capacity
			}
			if capacity > *source.Attributes.Capacity {
				return 0, nil, apperrors.PermissionDenied("Commercial capacity cannot exceed the canonical facility capacity.")
			}
		} else if in.Capacity > 0 && in.Capacity != capacity {
			return 0, nil, apperrors.InvalidRequest("Unit/room capacity is derived from distinct physical stock, not supplied quantity.")
		}
		pool := inventory.Pool{ID: ids.New(), OrganisationID: t.Organisation, PropertyID: in.PropertyID, NodeID: source.ID, Name: in.Name, Kind: string(source.Kind), Capacity: capacity, State: "DRAFT", Version: 1}
		if _, e = tx.Exec(ctx, `INSERT INTO inventory_pools(id,organisation_id,property_id,node_id,name,kind,capacity,created_by,source_node_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, pool.ID, t.Organisation, pool.PropertyID, pool.NodeID, pool.Name, pool.Kind, pool.Capacity, t.Account, source.Version); e != nil {
			return 0, nil, e
		}
		for _, n := range members {
			quantity := 1
			if source.Kind == portfolio.Facility {
				quantity = capacity
			}
			if _, e = tx.Exec(ctx, `INSERT INTO inventory_pool_members(organisation_id,pool_id,node_id,capacity,node_version) VALUES($1,$2,$3,$4,$5)`, t.Organisation, pool.ID, n.ID, quantity, n.Version); e != nil {
				return 0, nil, e
			}
		}
		pool, e = inventory.GetPool(ctx, tx, t.Organisation, pool.ID)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, pool.PropertyID, pool.ID, "inventory_pool", "POOL_CREATED", businessEvents.InventoryChanged, 1, map[string]any{"pool": pool, "physical_members": members})
		return 201, pool, e
	})
}

type CommercialStateChange struct {
	Version    int64   `json:"version"`
	State      string  `json:"state,omitempty"`
	Name       *string `json:"name,omitempty"`
	Revalidate bool    `json:"revalidate,omitempty"`
	Reason     string  `json:"reason"`
	ApprovalID ids.ID  `json:"approval_id,omitempty"`
}

func (h *Handler) poolPatch(w http.ResponseWriter, r *http.Request) {
	var in CommercialStateChange
	if !decode(w, r, &in) {
		return
	}
	t, e := h.load(r.Context(), h.Auth.Service.Pool)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	id, e := pathCommercialID(r)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	p, e := inventory.GetPool(r.Context(), h.Auth.Service.Pool, t.Organisation, id)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || in.Name != nil && !commercialName(*in.Name) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current pool version, name and reason required."))
		return
	}
	h.execute(w, r, "commercial:pool:update:"+id.String(), in, "pools:write", "PROPERTY", p.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "pools:write", "PROPERTY", p.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := inventory.GetPool(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Pool version changed.")
		}
		if current.State == "RETIRED" {
			return 0, nil, apperrors.Conflict("POOL_RETIRED", "Retired physical pool definitions are immutable.")
		}
		sourceVersion := current.SourceNodeVersion
		previousMembers, e := commercialJSONRows(ctx, tx, `SELECT to_jsonb(m) FROM inventory_pool_members m WHERE organisation_id=$1 AND pool_id=$2 ORDER BY node_id`, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		if in.State != "" && !commercialTransition(current.State, in.State) {
			return 0, nil, apperrors.InvalidRequest("Invalid pool lifecycle transition.")
		}
		if in.State == "ACTIVE" || in.Revalidate {
			source, e := portfolio.Get(ctx, tx, t.Organisation, current.NodeID)
			if e != nil {
				return 0, nil, e
			}
			if source.State != "ACTIVE" || source.NonSellable || string(source.Kind) != current.Kind {
				return 0, nil, apperrors.PermissionDenied("The canonical pool source must be active and unchanged in kind.")
			}
			sourceVersion = source.Version
			if e = h.commercialGate(ctx, tx, t, p.PropertyID); e != nil {
				return 0, nil, e
			}
			var invalid bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_pool_members m JOIN inventory_nodes n ON n.id=m.node_id AND n.organisation_id=m.organisation_id WHERE m.organisation_id=$1 AND m.pool_id=$2 AND (n.state<>'ACTIVE' OR business_graph_property($1,n.id) IS DISTINCT FROM $3))`, t.Organisation, id, p.PropertyID).Scan(&invalid); e != nil {
				return 0, nil, e
			}
			if invalid {
				return 0, nil, apperrors.PermissionDenied("All physical stock must be active and belong to this property.")
			}
			if _, e = tx.Exec(ctx, `UPDATE inventory_pool_members m SET node_version=n.version FROM inventory_nodes n WHERE m.organisation_id=$1 AND m.pool_id=$2 AND n.organisation_id=m.organisation_id AND n.id=m.node_id`, t.Organisation, id); e != nil {
				return 0, nil, e
			}
		}
		if in.State == "RETIRED" {
			var active bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_holds WHERE organisation_id=$1 AND pool_id=$2 AND state='ACTIVE' AND expires_at>now()) OR EXISTS(SELECT 1 FROM inventory_calendar WHERE organisation_id=$1 AND pool_id=$2 AND committed>0) OR EXISTS(SELECT 1 FROM sellable_products WHERE organisation_id=$1 AND pool_id=$2 AND state='ACTIVE') OR EXISTS(SELECT 1 FROM inventory_allotments WHERE organisation_id=$1 AND pool_id=$2 AND state='ACTIVE' AND release_at>now())`, t.Organisation, id).Scan(&active); e != nil {
				return 0, nil, e
			}
			if active {
				return 0, nil, apperrors.Conflict("POOL_IN_USE", "Active products, commitments or holds prevent retirement.")
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_pools SET state=coalesce(nullif($3,''),state),name=coalesce($4,name),source_node_version=$5,version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, id, in.State, in.Name, sourceVersion); e != nil {
			return 0, nil, e
		}
		if in.State == "RETIRED" {
			if _, e = tx.Exec(ctx, `DELETE FROM inventory_pool_members WHERE organisation_id=$1 AND pool_id=$2`, t.Organisation, id); e != nil {
				return 0, nil, e
			}
		}
		updated, e := inventory.GetPool(ctx, tx, t.Organisation, id)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, p.PropertyID, id, "inventory_pool", "POOL_UPDATED", businessEvents.InventoryChanged, updated.Version, map[string]any{"pool": updated, "previous_physical_members": previousMembers, "reason": in.Reason})
		return 200, updated, e
	})
}

type CommercialProductCreate struct {
	PropertyID    ids.ID   `json:"property_id"`
	NodeID        ids.ID   `json:"node_id"`
	PoolID        ids.ID   `json:"pool_id"`
	RatePlanID    *ids.ID  `json:"rate_plan_id,omitempty"`
	Name          string   `json:"name"`
	MinGuests     int      `json:"min_guests"`
	MaxGuests     int      `json:"max_guests"`
	PolicyCode    string   `json:"policy_code"`
	PolicyVersion int64    `json:"policy_version"`
	Channels      []string `json:"channels"`
}

func (h *Handler) productCreate(w http.ResponseWriter, r *http.Request) {
	var in CommercialProductCreate
	if !decode(w, r, &in) {
		return
	}
	if in.Channels == nil {
		in.Channels = []string{}
	}
	if in.PropertyID.ValidateV7() != nil || in.NodeID.ValidateV7() != nil || in.PoolID.ValidateV7() != nil || !commercialName(in.Name) || in.MinGuests < 1 || in.MaxGuests < in.MinGuests || in.MaxGuests > 100000 || in.PolicyCode == "" || len(in.PolicyCode) > 80 || in.PolicyVersion < 1 || !commercialChannelList(in.Channels) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Valid canonical references, occupancy, policy and channel eligibility are required."))
		return
	}
	h.execute(w, r, "commercial:product:create", in, "products:write", "PROPERTY", in.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, _, e := h.authorize(ctx, tx, "products:write", "PROPERTY", in.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		pool, e := inventory.GetPool(ctx, tx, t.Organisation, in.PoolID)
		if e != nil {
			return 0, nil, e
		}
		if pool.PropertyID != in.PropertyID || pool.NodeID != in.NodeID || pool.State == "RETIRED" {
			return 0, nil, apperrors.PermissionDenied("The product must reference this property's canonical pool source.")
		}
		n, e := portfolio.Get(ctx, tx, t.Organisation, in.NodeID)
		if e != nil {
			return 0, nil, e
		}
		if n.NonSellable || n.Attributes.Capacity == nil || in.MaxGuests > *n.Attributes.Capacity {
			return 0, nil, apperrors.PermissionDenied("Declared product occupancy cannot exceed the canonical physical capacity.")
		}
		if in.RatePlanID != nil {
			if e = h.validateProductRate(ctx, tx, t.Organisation, in.PropertyID, *in.RatePlanID); e != nil {
				return 0, nil, e
			}
		}
		product := inventory.Product{ID: ids.New(), OrganisationID: t.Organisation, PropertyID: in.PropertyID, NodeID: in.NodeID, PoolID: in.PoolID, RatePlanID: in.RatePlanID, Name: in.Name, State: "DRAFT", MinGuests: in.MinGuests, MaxGuests: in.MaxGuests, PolicyCode: in.PolicyCode, PolicyVersion: in.PolicyVersion, Channels: in.Channels, Version: 1}
		if _, e = tx.Exec(ctx, `INSERT INTO sellable_products(id,organisation_id,property_id,node_id,pool_id,rate_plan_id,name,min_guests,max_guests,policy_code,policy_version,channels,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, product.ID, t.Organisation, in.PropertyID, in.NodeID, in.PoolID, in.RatePlanID, in.Name, in.MinGuests, in.MaxGuests, in.PolicyCode, in.PolicyVersion, in.Channels, t.Account); e != nil {
			return 0, nil, e
		}
		product, e = inventory.GetProduct(ctx, tx, t.Organisation, product.ID)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, in.PropertyID, product.ID, "sellable_product", "PRODUCT_CREATED", businessEvents.InventoryChanged, 1, product)
		return 201, product, e
	})
}

type CommercialProductPatch struct {
	Version    int64     `json:"version"`
	State      string    `json:"state,omitempty"`
	Name       *string   `json:"name,omitempty"`
	RatePlanID *ids.ID   `json:"rate_plan_id,omitempty"`
	Channels   *[]string `json:"channels,omitempty"`
	Reason     string    `json:"reason"`
	ApprovalID ids.ID    `json:"approval_id,omitempty"`
}

func (h *Handler) productPatch(w http.ResponseWriter, r *http.Request) {
	var in CommercialProductPatch
	if !decode(w, r, &in) {
		return
	}
	_, p, e := h.productTarget(r, "products:write", true)
	if e != nil {
		reply(w, r, 0, nil, e)
		return
	}
	if in.Version < 1 || !validStaffReason(in.Reason) || in.Name != nil && !commercialName(*in.Name) || in.Channels != nil && !commercialChannelList(*in.Channels) {
		reply(w, r, 0, nil, apperrors.InvalidRequest("Current version, valid metadata and reason are required."))
		return
	}
	h.execute(w, r, "commercial:product:update:"+p.ID.String(), in, "products:write", "PROPERTY", p.PropertyID, true, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		t, target, e := h.authorize(ctx, tx, "products:write", "PROPERTY", p.PropertyID, true)
		if e != nil {
			return 0, nil, e
		}
		current, e := inventory.GetProduct(ctx, tx, t.Organisation, p.ID)
		if e != nil {
			return 0, nil, e
		}
		if current.Version != in.Version {
			return 0, nil, apperrors.Conflict("VERSION_CONFLICT", "Product version changed.")
		}
		if in.State != "" && !commercialTransition(current.State, in.State) {
			return 0, nil, apperrors.InvalidRequest("Invalid product lifecycle transition.")
		}
		if current.State == "RETIRED" {
			return 0, nil, apperrors.Conflict("PRODUCT_RETIRED", "Retired product definitions are immutable.")
		}
		if in.RatePlanID != nil {
			if e = h.validateProductRate(ctx, tx, t.Organisation, p.PropertyID, *in.RatePlanID); e != nil {
				return 0, nil, e
			}
		}
		if in.State == "ACTIVE" {
			if e = h.commercialGate(ctx, tx, t, p.PropertyID); e != nil {
				return 0, nil, e
			}
			pool, e := inventory.GetPool(ctx, tx, t.Organisation, p.PoolID)
			if e != nil {
				return 0, nil, e
			}
			if e = inventory.CheckStockAuthority(ctx, tx, pool); e != nil {
				return 0, nil, e
			}
			plan := current.RatePlanID
			if in.RatePlanID != nil {
				plan = in.RatePlanID
			}
			if plan == nil {
				return 0, nil, apperrors.Conflict("RATE_REQUIRED", "An active product requires a canonical rate plan.")
			}
			if e = h.validateActiveRate(ctx, tx, t.Organisation, *plan); e != nil {
				return 0, nil, e
			}
		}
		if in.State == "ACTIVE" || current.State == "ACTIVE" && (in.RatePlanID != nil || in.Channels != nil) {
			command := in
			command.ApprovalID = ids.Nil
			if e = h.requireCommercialApproval(ctx, tx, t, "COMMERCIAL_PRODUCT_CHANGE", target.Scope, in.ApprovalID, map[string]any{"product_id": p.ID, "command": command}, "", nil, ""); e != nil {
				return 0, nil, e
			}
		}
		var channels any
		if in.Channels != nil {
			channels = *in.Channels
		}
		if _, e = tx.Exec(ctx, `UPDATE sellable_products SET state=coalesce(nullif($3,''),state),name=coalesce($4,name),rate_plan_id=coalesce($5,rate_plan_id),channels=coalesce($6,channels),version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, t.Organisation, p.ID, in.State, in.Name, in.RatePlanID, channels); e != nil {
			return 0, nil, e
		}
		updated, e := inventory.GetProduct(ctx, tx, t.Organisation, p.ID)
		if e != nil {
			return 0, nil, e
		}
		e = h.commercialCommit(ctx, tx, t, p.PropertyID, p.ID, "sellable_product", "PRODUCT_UPDATED", businessEvents.InventoryChanged, updated.Version, updated)
		return 200, updated, e
	})
}

// typedJSON decodes a server-owned SELECT projection, never arbitrary identifiers.
func typedJSON[T any](ctx context.Context, q database.Querier, query string, args ...any) (T, error) {
	var value T
	var raw []byte
	e := q.QueryRow(ctx, query, args...).Scan(&raw)
	if e != nil {
		return value, e
	}
	e = json.Unmarshal(raw, &value)
	return value, e
}
