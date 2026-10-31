package integration

import (
	"encoding/json"
	"testing"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func (h *businessHarness) inventoryBranch(u *businessUser) ids.ID {
	h.t.Helper()
	status, raw := h.call(u, "POST", "/branches", map[string]any{"name": "Inventory branch " + ids.New().String(), "timezone": "Africa/Lagos", "currency": "NGN"}, nil)
	if status != 201 {
		h.t.Fatalf("branch %d %s", status, raw)
	}
	var result struct {
		Data struct {
			ID ids.ID `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		h.t.Fatal(err)
	}
	return result.Data.ID
}
func (h *businessHarness) inventoryNode(u *businessUser, path string, body map[string]any) portfolio.Node {
	h.t.Helper()
	status, raw := h.call(u, "POST", path, body, nil)
	if status != 201 {
		h.t.Fatalf("create %s %d %s", path, status, raw)
	}
	var result struct {
		Data portfolio.Node `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		h.t.Fatal(err)
	}
	if result.Data.ID.ValidateV7() != nil || result.Data.State != "DRAFT" || result.Data.Verification.Status == "VERIFIED" {
		h.t.Fatalf("invalid new identity %s", raw)
	}
	return result.Data
}
func TestBusinessEnterpriseCanonicalStructures(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	owner := h.user()
	h.secure(owner)
	branch := h.inventoryBranch(owner)
	create := func(path, code string, parent ids.ID, extra map[string]any) portfolio.Node {
		body := map[string]any{"name": code, "code": code, "branch_id": branch, "attributes": map[string]any{}}
		if !parent.IsZero() {
			body["parent_id"] = parent
		}
		for k, v := range extra {
			body[k] = v
		}
		return h.inventoryNode(owner, path, body)
	}
	collection := create("/portfolios", "REGION-FUND", ids.Nil, nil)
	development := create("/developments", "ESTATE", ids.Nil, map[string]any{"portfolio_ids": []ids.ID{collection.ID}, "attributes": map[string]any{"classification": "ESTATE"}})
	phase := create("/developments/"+development.ID.String()+"/phases", "PHASE-1", development.ID, map[string]any{"attributes": map[string]any{"ordering": 1}})
	parcel := create("/parcels", "LAND-1", phase.ID, map[string]any{"attributes": map[string]any{"classification": "RESIDENTIAL_PLOT", "area_square_metres": "1250.25", "survey_reference": "SURVEY-001", "geometry": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{3, 6}, {4, 6}, {4, 7}, {3, 7}, {3, 6}}}}}})
	property := create("/properties", "MIXED-1", parcel.ID, map[string]any{"attributes": map[string]any{"classification": "MIXED_USE", "address": "Test estate"}})
	building := create("/buildings", "BLDG-A", property.ID, nil)
	block := create("/blocks", "BLOCK-A", building.ID, nil)
	floor := create("/floors", "FLOOR-1", block.ID, map[string]any{"attributes": map[string]any{"floor_number": 1}})
	unit := create("/properties/"+property.ID.String()+"/units", "APT-101", floor.ID, map[string]any{"attributes": map[string]any{"classification": "APARTMENT", "area_square_metres": "85.5", "length_mm": 10000, "width_mm": 8550}})
	roomType := create("/properties/"+property.ID.String()+"/room-types", "DELUXE", property.ID, map[string]any{"attributes": map[string]any{"capacity": 2, "beds": 1, "layout": "KING"}})
	room := create("/properties/"+property.ID.String()+"/rooms", "ROOM-101", floor.ID, map[string]any{"room_type_id": roomType.ID, "attributes": map[string]any{"capacity": 2}})
	facility := create("/properties/"+property.ID.String()+"/facilities", "POOL", property.ID, map[string]any{"attributes": map[string]any{"classification": "SWIMMING_POOL", "capacity": 25}})
	common := create("/common-areas", "LOBBY", building.ID, map[string]any{"attributes": map[string]any{"classification": "NON_SELLABLE"}})
	asset := create("/assets", "AIRCON-101", room.ID, map[string]any{"attributes": map[string]any{"classification": "AIR_CONDITIONER", "manufacturer": "Test manufacturer", "model": "AC-1", "serial": "SERIAL-001"}})
	if !common.NonSellable || unit.NonSellable {
		t.Fatal("Canonical common areas must remain explicitly non-sellable independently of classification metadata")
	}
	for _, n := range []portfolio.Node{collection, development, phase, parcel, property, building, block, floor, unit, roomType, room, facility, common, asset} {
		if n.OrganisationID != owner.registered.OrganisationID || n.BranchID != branch || n.Currency != "NGN" || n.Timezone != "Africa/Lagos" {
			t.Fatalf("identity/context %v", n)
		}
	}
	status, raw := h.call(owner, "GET", "/inventory-nodes/"+collection.ID.String()+"/hierarchy", nil, nil)
	if status != 200 {
		t.Fatalf("tree %d %s", status, raw)
	}
	var tree struct {
		Data struct {
			Items []portfolio.Node `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil || len(tree.Data.Items) != 14 {
		t.Fatalf("tree %d %v %s", len(tree.Data.Items), err, raw)
	}
	// A standalone hotel and commercial property share the same canonical model.
	for _, category := range []string{"HOTEL", "COMMERCIAL_BUILDING", "STANDALONE_RESIDENCE"} {
		create("/properties", category, ids.Nil, map[string]any{"attributes": map[string]any{"classification": category}})
	}
	if status, _ := h.call(owner, "POST", "/units", map[string]any{"code": "APT-101", "name": "Duplicate", "parent_id": building.ID, "attributes": map[string]any{}}, nil); status != 409 {
		t.Fatalf("duplicate unit code %d", status)
	}
	if status, _ := h.call(owner, "POST", "/rooms", map[string]any{"code": "ORPHAN", "name": "Invalid", "branch_id": branch, "room_type_id": roomType.ID, "attributes": map[string]any{}}, nil); status != 400 {
		t.Fatalf("orphan room %d", status)
	}
	if status, _ := h.call(owner, "POST", "/floors", map[string]any{"code": "INVALID", "name": "Invalid", "parent_id": room.ID, "attributes": map[string]any{}}, nil); status != 400 {
		t.Fatalf("illegal parent %d", status)
	}
	currentProperty, e := portfolio.Get(t.Context(), h.db.Pool, owner.registered.OrganisationID, property.ID)
	if e != nil {
		t.Fatal(e)
	}
	if status, _ := h.call(owner, "POST", "/inventory-nodes/"+property.ID.String()+"/lifecycle", map[string]any{"version": currentProperty.Version, "tree_version": currentProperty.TreeVersion, "state": "ACTIVE", "reason": "Activation without proof"}, nil); status != 403 {
		t.Fatalf("unverified activation %d", status)
	}
	var nodes, anchors int
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM inventory_nodes),(SELECT count(*) FROM portfolios)+(SELECT count(*) FROM developments)+(SELECT count(*) FROM development_phases)+(SELECT count(*) FROM parcels)+(SELECT count(*) FROM properties)+(SELECT count(*) FROM buildings)+(SELECT count(*) FROM blocks)+(SELECT count(*) FROM floors)+(SELECT count(*) FROM units)+(SELECT count(*) FROM room_types)+(SELECT count(*) FROM rooms)+(SELECT count(*) FROM facilities)+(SELECT count(*) FROM common_areas)+(SELECT count(*) FROM assets)`).Scan(&nodes, &anchors); err != nil || nodes != anchors {
		t.Fatalf("canonical anchor integrity %d %d %v", nodes, anchors, err)
	}
}
