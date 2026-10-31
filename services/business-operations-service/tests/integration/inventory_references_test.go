package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/media"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/workers"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

type inventoryMediaFake struct {
	metadata media.Metadata
	down     bool
	purpose  string
}

func (f *inventoryMediaFake) Get(context.Context, ids.ID) (media.Metadata, error) {
	if f.down {
		return media.Metadata{}, errors.New("offline")
	}
	return f.metadata, nil
}
func (f *inventoryMediaFake) DownloadURL(_ context.Context, _ ids.ID, purpose string) (media.Access, error) {
	f.purpose = purpose
	return media.Access{URL: "https://media.example.test/purpose-bound", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func TestBusinessInventoryMediaOwnershipAccessAndConfirmedSpaces(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	u := h.user()
	h.secure(u)
	branch := h.inventoryBranch(u)
	p := h.inventoryNode(u, "/properties", map[string]any{"branch_id": branch, "code": "P", "name": "Property", "attributes": map[string]any{}})
	unit := h.inventoryNode(u, "/units", map[string]any{"parent_id": p.ID, "code": "101", "name": "Apartment", "attributes": map[string]any{}})
	f := &inventoryMediaFake{metadata: media.Metadata{ID: ids.New(), OwnerDomain: string(platform.DomainBusinessOperations), OwnerType: "ORGANIZATION", OwnerID: ids.New(), Status: "READY", Classification: "PRIVATE"}}
	h.domain.Media = f
	path := "/inventory-nodes/" + p.ID.String()
	body := map[string]any{"version": p.Version, "media_id": f.metadata.ID, "purpose": "DIGITAL_TWIN", "reason": "Confirmed owning Media reference"}
	if status, _ := h.call(u, "POST", path+"/media", body, nil); status != 403 {
		t.Fatalf("foreign media %d", status)
	}
	f.metadata.OwnerID = u.registered.OrganisationID
	status, raw := h.call(u, "POST", path+"/media", body, nil)
	if status != 200 {
		t.Fatalf("media %d %s", status, raw)
	}
	var linked struct {
		Data struct {
			ID ids.ID `json:"media_link_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &linked)
	if status, raw = h.call(u, "POST", path+"/media/"+linked.Data.ID.String()+"/access", nil, nil); status != 200 || f.purpose != "BUSINESS_INVENTORY_DIGITAL_TWIN" {
		t.Fatalf("access %d %s %s", status, raw, f.purpose)
	}
	spaces := map[string]any{"version": p.Version, "media_link_id": linked.Data.ID, "coordinate_system": "LOCAL_METRES", "confirmed": true, "spaces": []map[string]any{{"node_id": unit.ID, "label": "Apartment", "points": [][]float64{{0, 0, 0}, {10, 0, 0}, {10, 8, 0}}}}, "reason": "Operator confirmed canonical spaces"}
	if status, raw = h.call(u, "POST", path+"/space-graphs", spaces, nil); status != 201 {
		t.Fatalf("spaces %d %s", status, raw)
	}
	other := h.inventoryNode(u, "/properties", map[string]any{"branch_id": branch, "code": "OTHER", "name": "Other", "attributes": map[string]any{}})
	spaces["spaces"] = []map[string]any{{"node_id": other.ID, "label": "Wrong property", "points": [][]float64{{0, 0, 0}}}}
	if status, _ := h.call(u, "POST", path+"/space-graphs", spaces, nil); status != 403 {
		t.Fatalf("foreign physical space %d", status)
	}
	f.metadata.Status = "DELETED"
	if status, _ := h.call(u, "POST", path+"/media/"+linked.Data.ID.String()+"/access", nil, nil); status != 403 {
		t.Fatalf("deleted media access %d", status)
	}
	tick := workers.InventoryReferences{Projection: portfolio.VerificationProjector{Pool: h.db.Pool, Outbox: h.service.Outbox}, Media: f}
	if err := tick.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	var projected string
	_ = h.db.Pool.QueryRow(t.Context(), `SELECT provider_status FROM inventory_media_links WHERE id=$1`, linked.Data.ID).Scan(&projected)
	if projected != "DELETED" {
		t.Fatalf("deleted projection %s", projected)
	}
	f.down = true
	if status, _ := h.call(u, "POST", path+"/media", body, nil); status != 503 {
		t.Fatalf("outage %d", status)
	}
	var twins int
	_ = h.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM digital_twin_refs`).Scan(&twins)
	if twins != 1 {
		t.Fatalf("reference count %d", twins)
	}
}

func TestBusinessInventoryAuthoritativeVerificationActivationAndExpiry(t *testing.T) {
	provider := &businessVerificationFake{}
	h := newBusinessHTTP(t, provider)
	u := h.user()
	h.secure(u)
	branch := h.inventoryBranch(u)
	org := u.registered.OrganisationID
	p := h.inventoryNode(u, "/properties", map[string]any{"branch_id": branch, "code": "HOTEL", "name": "Hotel", "capacity_code": "MANAGEMENT", "attributes": map[string]any{"classification": "HOTEL"}})
	unit := h.inventoryNode(u, "/units", map[string]any{"parent_id": p.ID, "code": "101", "name": "Apartment", "attributes": map[string]any{}})
	path := "/inventory-nodes/" + p.ID.String()
	expiry := time.Now().Add(time.Hour)
	c, err := provider.Create(t.Context(), verification.TypePropertyAuthority, verification.Subject{Type: verification.SubjectProperty, Domain: string(platform.DomainBusinessOperations), ID: p.ID}, "property", "property")
	if err != nil {
		t.Fatal(err)
	}
	provider.set(c.VerificationID, "VERIFIED", &expiry)
	if status, raw := h.call(u, "POST", path+"/verification", map[string]any{"version": p.Version, "verification_id": c.VerificationID}, nil); status != 200 {
		t.Fatalf("authority %d %s", status, raw)
	}
	// Capacity verification is an explicit provider assertion; the test does not
	// pretend the currently unavailable live capacity operation exists.
	capCase, capLink := ids.New(), ids.New()
	h.domain.CapacityAuthority = capacityAuthorityFake{verification.CapacityAssertion{Code: "MANAGEMENT"}}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at) VALUES($1,$2,$3,'KYB','VERIFIED',$4)`, []any{ids.New(), org, ids.New(), expiry}},
		{`INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at) VALUES($1,$2,$3,'CAPACITY:MANAGEMENT','VERIFIED',$4)`, []any{capLink, org, capCase, expiry}},
		{`INSERT INTO organisation_capacities(id,organisation_id,code,status,verification_link_id,effective_at,expires_at) VALUES($1,$2,'MANAGEMENT','ACTIVE',$3,now(),$4)`, []any{ids.New(), org, capLink, expiry}},
		{`UPDATE organisations SET state='ACTIVE',activated_at=now() WHERE id=$1`, []any{org}},
	} {
		if _, err = h.db.Pool.Exec(t.Context(), statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	activate := func(id ids.ID, want int) {
		t.Helper()
		n, e := portfolio.Get(t.Context(), h.db.Pool, org, id)
		if e != nil {
			t.Fatal(e)
		}
		if status, raw := h.call(u, "POST", "/inventory-nodes/"+id.String()+"/lifecycle", map[string]any{"version": n.Version, "tree_version": n.TreeVersion, "state": "ACTIVE", "reason": "Verified structural activation"}, nil); status != want {
			t.Fatalf("activate %d want %d %s", status, want, raw)
		}
	}
	activate(p.ID, 200)
	activate(unit.ID, 200)
	current, _ := portfolio.Get(t.Context(), h.db.Pool, org, p.ID)
	if status, _ := h.call(u, "POST", path+"/move", map[string]any{"version": current.Version, "tree_version": current.TreeVersion, "reason": "Unsafe active change"}, nil); status != 503 {
		t.Fatalf("missing future dependency assertions %d", status)
	}
	// Read-back refresh propagates restrictions; client-submitted hints cannot.
	provider.set(c.VerificationID, "RESTRICTED", &expiry)
	tick := workers.InventoryReferences{Projection: portfolio.VerificationProjector{Pool: h.db.Pool, Outbox: h.service.Outbox}, Verification: provider}
	if err = tick.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err := portfolio.Get(t.Context(), h.db.Pool, org, p.ID)
	if err != nil || n.Verification.Status != "RESTRICTED" {
		t.Fatalf("restriction %+v %v", n.Verification, err)
	}
	later := h.inventoryNode(u, "/units", map[string]any{"parent_id": p.ID, "code": "102", "name": "Later apartment", "attributes": map[string]any{}})
	activate(later.ID, 403)
	provider.down = true
	if status, _ := h.call(u, "POST", path+"/verification", map[string]any{"version": n.Version, "verification_id": c.VerificationID}, nil); status != 503 {
		t.Fatalf("provider outage %d", status)
	}
	provider.down = false
	forged, e := provider.Create(t.Context(), verification.TypePropertyAuthority, verification.Subject{Type: verification.SubjectProperty, Domain: string(platform.DomainBusinessOperations), ID: ids.New()}, "forged-subject", "forged-subject")
	if e != nil {
		t.Fatal(e)
	}
	if status, _ := h.call(u, "POST", path+"/verification", map[string]any{"version": n.Version, "verification_id": forged.VerificationID}, nil); status != 403 {
		t.Fatalf("forged subject %d", status)
	}
	past := time.Now().Add(-time.Second)
	provider.set(c.VerificationID, "VERIFIED", &past)
	tick = workers.InventoryReferences{Projection: portfolio.VerificationProjector{Pool: h.db.Pool, Outbox: h.service.Outbox}, Verification: provider}
	if err = tick.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err = portfolio.Get(t.Context(), h.db.Pool, org, p.ID)
	if err != nil || n.Verification.Status != "EXPIRED" {
		t.Fatalf("expiry %+v %v", n.Verification, err)
	}
}

func TestBusinessInventoryExternalMappingNamespacesAndCollision(t *testing.T) {
	h := newBusinessHTTP(t, nil)
	u := h.user()
	h.secure(u)
	branch := h.inventoryBranch(u)
	p := h.inventoryNode(u, "/properties", map[string]any{"branch_id": branch, "code": "P", "name": "Property", "attributes": map[string]any{}})
	q := h.inventoryNode(u, "/properties", map[string]any{"branch_id": branch, "code": "Q", "name": "Second property", "attributes": map[string]any{}})
	a := h.inventoryNode(u, "/assets", map[string]any{"parent_id": p.ID, "code": "A", "name": "Asset", "attributes": map[string]any{}})
	body := map[string]any{"provider": "legacy", "external_id": "42", "source_reference": "migration-1", "reason": "Reconcile exact source identity"}
	for _, n := range []portfolio.Node{p, a} {
		if status, raw := h.call(u, "POST", "/inventory-nodes/"+n.ID.String()+"/mappings", body, nil); status != 200 {
			t.Fatalf("typed namespace %d %s", status, raw)
		}
	}
	if status, _ := h.call(u, "POST", "/inventory-nodes/"+q.ID.String()+"/mappings", body, nil); status != 409 {
		t.Fatalf("collision %d", status)
	}
	body["provider"] = "other-provider"
	if status, _ := h.call(u, "POST", "/inventory-nodes/"+q.ID.String()+"/mappings", body, nil); status != 200 {
		t.Fatalf("provider namespace %d", status)
	}
	approver := h.user()
	h.secure(approver)
	h.join(approver, u, []string{"approvals:decide", "inventory:approve"})
	if status, raw := h.call(u, "POST", "/approval-policies", map[string]any{"action": "INVENTORY_MAPPING_CHANGE", "risk": "HIGH", "scope_kind": "COMPANY", "levels": []map[string]any{{"capability": "inventory:approve", "count": 1, "require_mfa": true}}}, nil); status != 201 {
		t.Fatalf("mapping policy %d %s", status, raw)
	}
	p, err := portfolio.Get(t.Context(), h.db.Pool, u.registered.OrganisationID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	q, err = portfolio.Get(t.Context(), h.db.Pool, u.registered.OrganisationID, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	remap := map[string]any{"provider": "legacy", "external_id": "42", "source_reference": "reviewed-correction", "expected_node_id": p.ID, "version": 1, "node_version": q.Version, "tree_version": q.TreeVersion, "previous_node_version": p.Version, "previous_tree_version": p.TreeVersion, "reason": "Review exact source and destination mapping identities"}
	if status, _ := h.call(u, "POST", "/inventory-nodes/"+q.ID.String()+"/mappings", remap, nil); status != 403 {
		t.Fatalf("unapproved remap %d", status)
	}
	approval := h.inventoryApproval(u, approver, q, remap, "INVENTORY_MAPPING_CHANGE")
	remap["approval_id"] = approval
	if status, raw := h.call(u, "POST", "/inventory-nodes/"+q.ID.String()+"/mappings", remap, nil); status != 200 {
		t.Fatalf("approved remap %d %s", status, raw)
	}
	var mapped ids.ID
	if err = h.db.Pool.QueryRow(t.Context(), `SELECT node_id FROM inventory_external_mappings WHERE organisation_id=$1 AND provider='legacy' AND object_type='PROPERTY' AND external_id='42'`, u.registered.OrganisationID).Scan(&mapped); err != nil || mapped != q.ID {
		t.Fatalf("remap %v %v", mapped, err)
	}
}
