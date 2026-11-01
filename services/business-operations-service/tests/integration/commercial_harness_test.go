package integration

import (
	"encoding/json"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

type commercialHarness struct {
	*businessHarness
	owner, approver *businessUser
	property, unit  portfolio.Node
	pool            inventory.Pool
	product         inventory.Product
	rate            revenue.Plan
	definition      revenue.RateDefinition
	dates           inventory.DateRange
	policies        map[string]bool
}

func commercialData[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out struct {
		Data T `json:"data"`
	}
	if e := json.Unmarshal(raw, &out); e != nil {
		t.Fatal(e)
	}
	return out.Data
}
func (c *commercialHarness) must(u *businessUser, method, path string, body any, status int) []byte {
	c.t.Helper()
	got, raw := c.call(u, method, path, body, nil)
	if got != status {
		c.t.Fatalf("%s %s got %d want %d %s", method, path, got, status, raw)
	}
	return raw
}
func (c *commercialHarness) approval(action, capability, channel string, amount *int64, currency string, operation any) ids.ID {
	return c.approvalAt(action, capability, channel, amount, currency, operation, "PROPERTY", c.property.ID)
}
func (c *commercialHarness) approvalAt(action, capability, channel string, amount *int64, currency string, operation any, scopeKind string, scopeID ids.ID) ids.ID {
	c.t.Helper()
	key := action + ":" + channel
	if !c.policies[key] {
		c.must(c.owner, "POST", "/approval-policies", map[string]any{"action": action, "risk": "HIGH", "scope_kind": "COMPANY", "channel": channel, "levels": []map[string]any{{"capability": capability, "count": 1, "require_mfa": true}}}, 201)
		c.policies[key] = true
	}
	body := map[string]any{"action": action, "scope_kind": scopeKind, "scope_id": scopeID, "channel": channel, "operation": operation, "reason": "Independent commercial evidence review"}
	if amount != nil {
		body["amount_minor"] = *amount
		body["currency"] = currency
	}
	raw := c.must(c.owner, "POST", "/approvals", body, 201)
	created := commercialData[struct {
		Approval struct {
			ID ids.ID `json:"id"`
		} `json:"approval"`
	}](c.t, raw)
	id := created.Approval.ID
	if status, _ := c.call(c.owner, "POST", "/approvals/"+id.String()+"/decision", map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Requester cannot self approve"}, nil); status != 403 {
		c.t.Fatalf("commercial self approval got %d", status)
	}
	c.must(c.approver, "POST", "/approvals/"+id.String()+"/decision", map[string]any{"version": 1, "level": 0, "outcome": "APPROVED", "reason": "Independent source and scope checked"}, 200)
	return id
}
func newCommercialHTTP(t *testing.T) *commercialHarness {
	t.Helper()
	provider := &businessVerificationFake{}
	h := newBusinessHTTP(t, provider)
	c := &commercialHarness{businessHarness: h, owner: h.user(), approver: h.user(), policies: map[string]bool{}}
	h.secure(c.owner)
	h.secure(c.approver)
	h.join(c.approver, c.owner, []string{"approvals:decide", "rates:approve", "pricing:approve", "publication:approve", "channels:approve", "reservations:approve", "rms:approve"})
	org := c.owner.registered.OrganisationID
	branch := h.inventoryBranch(c.owner)
	c.property = h.inventoryNode(c.owner, "/properties", map[string]any{"branch_id": branch, "code": "COMMERCIAL-HOTEL", "name": "Commercial test hotel", "capacity_code": "MANAGEMENT", "attributes": map[string]any{"classification": "HOTEL"}})
	c.unit = h.inventoryNode(c.owner, "/units", map[string]any{"parent_id": c.property.ID, "code": "APARTMENT-101", "name": "Physical apartment", "attributes": map[string]any{"classification": "APARTMENT", "capacity": 2}})
	expiry := time.Now().UTC().Add(time.Hour)
	authority, e := provider.Create(t.Context(), verification.TypePropertyAuthority, verification.Subject{Type: verification.SubjectProperty, Domain: string(platform.DomainBusinessOperations), ID: c.property.ID}, "commercial-authority", "commercial-authority")
	if e != nil {
		t.Fatal(e)
	}
	provider.set(authority.VerificationID, "VERIFIED", &expiry)
	c.must(c.owner, "POST", "/inventory-nodes/"+c.property.ID.String()+"/verification", map[string]any{"version": c.property.Version, "verification_id": authority.VerificationID}, 200)
	// This is explicit disposable provider fixture data, not a live capacity
	// certification. Production still fails closed when that operation is absent.
	capLink := ids.New()
	h.domain.CapacityAuthority = capacityAuthorityFake{verification.CapacityAssertion{Code: "MANAGEMENT"}}
	for _, v := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at) VALUES($1,$2,$3,'KYB','VERIFIED',$4)`, []any{ids.New(), org, ids.New(), expiry}},
		{`INSERT INTO verification_links(id,organisation_id,case_id,purpose,provider_status,expires_at) VALUES($1,$2,$3,'CAPACITY:MANAGEMENT','VERIFIED',$4)`, []any{capLink, org, ids.New(), expiry}},
		{`INSERT INTO organisation_capacities(id,organisation_id,code,status,verification_link_id,effective_at,expires_at) VALUES($1,$2,'MANAGEMENT','ACTIVE',$3,now(),$4)`, []any{ids.New(), org, capLink, expiry}},
		{`UPDATE organisations SET state='ACTIVE',activated_at=now() WHERE id=$1`, []any{org}},
	} {
		if _, e = h.db.Pool.Exec(t.Context(), v.query, v.args...); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []ids.ID{c.property.ID, c.unit.ID} {
		node, e := portfolio.Get(t.Context(), h.db.Pool, org, id)
		if e != nil {
			t.Fatal(e)
		}
		c.must(c.owner, "POST", "/inventory-nodes/"+id.String()+"/lifecycle", map[string]any{"version": node.Version, "tree_version": node.TreeVersion, "state": "ACTIVE", "reason": "Activate verified physical inventory"}, 200)
	}
	c.property, e = portfolio.Get(t.Context(), h.db.Pool, org, c.property.ID)
	if e != nil {
		t.Fatal(e)
	}
	c.unit, e = portfolio.Get(t.Context(), h.db.Pool, org, c.unit.ID)
	if e != nil {
		t.Fatal(e)
	}
	c.pool = commercialData[inventory.Pool](t, c.must(c.owner, "POST", "/inventory-pools", businesshttp.CommercialPoolCreate{PropertyID: c.property.ID, NodeID: c.unit.ID, Name: "Apartment physical stock"}, 201))
	c.pool = commercialData[inventory.Pool](t, c.must(c.owner, "PATCH", "/inventory-pools/"+c.pool.ID.String(), businesshttp.CommercialStateChange{Version: c.pool.Version, State: "ACTIVE", Reason: "Canonical physical stock verified"}, 200))
	fiscal := businesshttp.CommercialTaxCreate{PropertyID: c.property.ID, Currency: "NGN", Definition: revenue.TaxPolicy{DisplayMode: "INCLUSIVE", FiscalReference: "test-only-explicit-zero-tax", PolicyVersion: 1, Taxes: []revenue.TaxComponent{}, Fees: []revenue.FeeComponent{}}, SourceReference: "disposable fiscal evidence", EffectiveAt: time.Now().UTC().Add(-time.Minute), Reason: "Approved explicit zero tax fixture"}
	fiscal.ApprovalID = c.approval("COMMERCIAL_FISCAL_POLICY_CHANGE", "pricing:approve", "", nil, "", map[string]any{"property_id": c.property.ID, "command": fiscal})
	tax := commercialData[businesshttp.CommercialTaxProfile](t, c.must(c.owner, "POST", "/tax-profiles", fiscal, 201))
	loc, _ := time.LoadLocation(c.property.Timezone)
	arrival := time.Now().In(loc).AddDate(0, 0, 2)
	c.dates = inventory.DateRange{DateFrom: arrival.Format(time.DateOnly), DateTo: arrival.AddDate(0, 0, 2).Format(time.DateOnly)}
	c.definition = revenue.RateDefinition{Currency: "NGN", AmountMinor: 10000, IncludedAdults: 2, MinGuests: 1, MaxGuests: 2, MinNights: 1, MaxNights: 30, CancellationReference: "FLEXIBLE-24H:v1", PrepaymentReference: "PAYMENT-SERVICE-REFERENCE:v1", PolicyCode: "COMMERCIAL-BASE", PolicyVersion: 1, FloorMinor: 1000, CeilingMinor: 20000, MaxChangeBPS: 1000, TaxProfileID: &tax.ID}
	created := commercialData[struct {
		Plan revenue.Plan `json:"rate_plan"`
	}](t, c.must(c.owner, "POST", "/rate-plans", businesshttp.CommercialRateCreate{PropertyID: c.property.ID, Name: "Flexible accommodation", Kind: "REFUNDABLE", DateFrom: c.dates.DateFrom, DateTo: arrival.AddDate(0, 0, 30).Format(time.DateOnly), Definition: c.definition, Reason: "Canonical approved pricing policy"}, 201))
	c.rate = created.Plan
	activate := businesshttp.CommercialRatePatch{Version: c.rate.Version, State: "ACTIVE", Reason: "Independent rate activation"}
	activate.ApprovalID = c.approval("COMMERCIAL_RATE_ACTIVATION", "rates:approve", "", &c.definition.CeilingMinor, "NGN", map[string]any{"rate_plan_id": c.rate.ID, "command": activate})
	activated := commercialData[struct {
		Plan revenue.Plan `json:"rate_plan"`
	}](t, c.must(c.owner, "PATCH", "/rate-plans/"+c.rate.ID.String(), activate, 200))
	c.rate = activated.Plan
	c.product = commercialData[inventory.Product](t, c.must(c.owner, "POST", "/sellable-products", businesshttp.CommercialProductCreate{PropertyID: c.property.ID, NodeID: c.unit.ID, PoolID: c.pool.ID, RatePlanID: &c.rate.ID, Name: "Apartment nightly stay", MinGuests: 1, MaxGuests: 2, PolicyCode: "ACCOMMODATION", PolicyVersion: 1, Channels: []string{}}, 201))
	productActivation := businesshttp.CommercialProductPatch{Version: c.product.Version, State: "ACTIVE", Reason: "Activate the approved stock product"}
	productActivation.ApprovalID = c.approval("COMMERCIAL_PRODUCT_CHANGE", "pricing:approve", "", nil, "", map[string]any{"product_id": c.product.ID, "command": productActivation})
	c.product = commercialData[inventory.Product](t, c.must(c.owner, "PATCH", "/sellable-products/"+c.product.ID.String(), productActivation, 200))
	days, _ := c.dates.Days(c.property.Timezone)
	versions := map[string]int64{}
	for _, day := range days {
		versions[day] = 0
	}
	capacity := 1
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: c.pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Capacity: &capacity, ExpectedVersions: versions, Reason: "Open existing physical stock"}, 200)
	return c
}
func (c *commercialHarness) quote() revenue.QuoteSnapshot {
	c.t.Helper()
	return commercialData[revenue.QuoteSnapshot](c.t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}, 201))
}
