package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/portfolio"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func (c *commercialHarness) commercialNode(path string, body map[string]any) portfolio.Node {
	c.t.Helper()
	node := commercialData[portfolio.Node](c.t, c.must(c.owner, "POST", path, body, 201))
	if node.ID.ValidateV7() != nil || node.State != "DRAFT" || node.OrganisationID != c.owner.registered.OrganisationID || node.PropertyID == nil || *node.PropertyID != c.property.ID {
		c.t.Fatalf("Invalid canonical physical child %#v", node)
	}
	if node.Verification.Status == "VERIFIED" && (node.Verification.CaseID == nil || c.property.Verification.CaseID == nil || *node.Verification.CaseID != *c.property.Verification.CaseID) {
		c.t.Fatal("Physical child inherited an unrelated authority case")
	}
	return node
}
func (c *commercialHarness) activateNode(node portfolio.Node) portfolio.Node {
	c.t.Helper()
	current, e := portfolio.Get(c.t.Context(), c.db.Pool, c.owner.registered.OrganisationID, node.ID)
	if e != nil {
		c.t.Fatal(e)
	}
	c.must(c.owner, "POST", "/inventory-nodes/"+node.ID.String()+"/lifecycle", map[string]any{"version": current.Version, "tree_version": current.TreeVersion, "state": "ACTIVE", "reason": "Activate approved physical room stock"}, 200)
	current, e = portfolio.Get(c.t.Context(), c.db.Pool, c.owner.registered.OrganisationID, node.ID)
	if e != nil {
		c.t.Fatal(e)
	}
	return current
}
func (c *commercialHarness) activateProduct(product inventory.Product) inventory.Product {
	c.t.Helper()
	in := businesshttp.CommercialProductPatch{Version: product.Version, State: "ACTIVE", Reason: "Independently activate the priced physical stock product"}
	in.ApprovalID = c.approval("COMMERCIAL_PRODUCT_CHANGE", "pricing:approve", "", nil, "", map[string]any{"product_id": product.ID, "command": in})
	return commercialData[inventory.Product](c.t, c.must(c.owner, "PATCH", "/sellable-products/"+product.ID.String(), in, 200))
}
func TestBusinessCommercialRoomTypePoolUsesDistinctPhysicalRoomsAndNeverManufacturesStock(t *testing.T) {
	c := newCommercialHTTP(t)
	category := c.commercialNode("/room-types", map[string]any{"parent_id": c.property.ID, "code": "DELUXE", "name": "Deluxe category", "attributes": map[string]any{"capacity": 2, "beds": 1, "layout": "KING"}})
	rooms := []portfolio.Node{}
	for _, code := range []string{"DELUXE-1", "DELUXE-2"} {
		rooms = append(rooms, c.commercialNode("/rooms", map[string]any{"parent_id": c.property.ID, "room_type_id": category.ID, "code": code, "name": code, "attributes": map[string]any{"capacity": 2}}))
	}
	category = c.activateNode(category)
	for i := range rooms {
		rooms[i] = c.activateNode(rooms[i])
	}
	pool := commercialData[inventory.Pool](t, c.must(c.owner, "POST", "/inventory-pools", businesshttp.CommercialPoolCreate{PropertyID: c.property.ID, NodeID: category.ID, Name: "Two real deluxe rooms", MemberIDs: []ids.ID{rooms[0].ID, rooms[1].ID}}, 201))
	if pool.Capacity != 2 {
		t.Fatalf("Derived physical stock capacity %d", pool.Capacity)
	}
	c.must(c.owner, "POST", "/inventory-pools", businesshttp.CommercialPoolCreate{PropertyID: c.property.ID, NodeID: category.ID, Name: "Cannot double sell the same rooms", MemberIDs: []ids.ID{rooms[0].ID, rooms[1].ID}}, 409)
	pool = commercialData[inventory.Pool](t, c.must(c.owner, "PATCH", "/inventory-pools/"+pool.ID.String(), businesshttp.CommercialStateChange{Version: pool.Version, State: "ACTIVE", Reason: "Validate both real physical room identities"}, 200))
	product := commercialData[inventory.Product](t, c.must(c.owner, "POST", "/sellable-products", businesshttp.CommercialProductCreate{PropertyID: c.property.ID, NodeID: category.ID, PoolID: pool.ID, RatePlanID: &c.rate.ID, Name: "Deluxe room nights", MinGuests: 1, MaxGuests: 2, PolicyCode: "ROOM-NIGHTS", PolicyVersion: 1, Channels: []string{}}, 201))
	product = c.activateProduct(product)
	days, _ := c.dates.Days(c.property.Timezone)
	versions := map[string]int64{}
	for _, day := range days {
		versions[day] = 0
	}
	quantity := 2
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Capacity: &quantity, ExpectedVersions: versions, Reason: "Open exactly two physical rooms"}, 200)
	quote := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 2}, 201))
	if quote.AmountMinor != 40000 {
		t.Fatalf("Two-room quote %d", quote.AmountMinor)
	}
	c.must(c.owner, "POST", "/holds", inventory.HoldCommand{ProductID: product.ID, ProductVersion: product.Version, PoolVersion: pool.Version, QuoteID: quote.QuoteID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Quantity: 2, DurationSeconds: 120}, 201)
	c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}, 409)
	quantity = 3
	for _, day := range days {
		versions[day] = 2
	}
	c.must(c.owner, "PUT", "/inventory", businesshttp.CommercialCalendarCommand{PoolID: pool.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Capacity: &quantity, ExpectedVersions: versions, Reason: "Cannot manufacture a third physical room"}, 400)
	// The Section 4 API still requires real downstream safety assertions. A
	// disposable owning-projection fixture then proves a physical state change
	// invalidates the pool immediately even
	// before a maintenance tick or channel reconciliation.
	current, e := portfolio.Get(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, rooms[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/inventory-nodes/"+current.ID.String()+"/lifecycle", map[string]any{"version": current.Version, "tree_version": current.TreeVersion, "state": "INACTIVE", "reason": "Physical room temporarily unavailable"}, 503)
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE inventory_nodes SET state='INACTIVE',version=version+1,updated_at=now() WHERE organisation_id=$1 AND id=$2`, c.owner.registered.OrganisationID, current.ID); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}, 409)
}
func TestBusinessCommercialDerivedOccupancyLOSInheritedRestrictionsAndCycleRejection(t *testing.T) {
	c := newCommercialHTTP(t)
	versions := commercialData[struct {
		Items []revenue.RateVersion `json:"items"`
	}](t, c.must(c.owner, "GET", "/rate-plans/"+c.rate.ID.String()+"/versions", nil, 200))
	parent := versions.Items[0]
	derived := c.definition
	derived.ParentVersionID = &parent.ID
	derived.Adjustments = []revenue.Adjustment{{Kind: "PERCENT", BasisPoints: -1000}}
	derived.IncludedAdults = 1
	derived.ExtraAdultMinor = 1000
	derived.ChildMinor = 500
	derived.Rules = []revenue.Rule{{Priority: 10, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, MinNights: 2, Adjustment: revenue.Adjustment{Kind: "FIXED", AmountMinor: -500}}}
	created := commercialData[struct {
		Plan    revenue.Plan        `json:"rate_plan"`
		Version revenue.RateVersion `json:"rate_version"`
	}](t, c.must(c.owner, "POST", "/rate-plans", businesshttp.CommercialRateCreate{PropertyID: c.property.ID, Name: "Derived long stay occupancy rate", Kind: "LONG_STAY", DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Definition: derived, Reason: "Pinned parent, occupancy and LOS pricing"}, 201))
	activation := businesshttp.CommercialRatePatch{Version: 1, State: "ACTIVE", Reason: "Independent derived-rate activation"}
	activation.ApprovalID = c.approval("COMMERCIAL_RATE_ACTIVATION", "rates:approve", "", &derived.CeilingMinor, derived.Currency, map[string]any{"rate_plan_id": created.Plan.ID, "command": activation})
	c.must(c.owner, "PATCH", "/rate-plans/"+created.Plan.ID.String(), activation, 200)
	change := businesshttp.CommercialProductPatch{Version: c.product.Version, RatePlanID: &created.Plan.ID, Reason: "Bind the approved derived canonical rate"}
	change.ApprovalID = c.approval("COMMERCIAL_PRODUCT_CHANGE", "pricing:approve", "", nil, "", map[string]any{"product_id": c.product.ID, "command": change})
	c.product = commercialData[inventory.Product](t, c.must(c.owner, "PATCH", "/sellable-products/"+c.product.ID.String(), change, 200))
	quote := c.quote()
	if quote.AmountMinor != 19000 || len(quote.Nights[0].ParentVersionIDs) != 2 {
		t.Fatalf("Derived occupancy and LOS quote %#v", quote)
	}
	child := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 1, Children: 1, Quantity: 1}, 201))
	if child.AmountMinor != 18000 {
		t.Fatalf("Child occupancy calculation %d", child.AmountMinor)
	}
	cycle := c.definition
	cycle.ParentVersionID = &created.Version.ID
	patch := businesshttp.CommercialRatePatch{Version: c.rate.Version, Definition: &cycle, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Reason: "A derived rate must not point back to its ancestor"}
	patch.ApprovalID = c.approval("EXCEPTIONAL_RATE_CHANGE", "rates:approve", "", &cycle.CeilingMinor, cycle.Currency, map[string]any{"rate_plan_id": c.rate.ID, "command": patch})
	c.must(c.owner, "PATCH", "/rate-plans/"+c.rate.ID.String(), patch, 409)
	c.must(c.owner, "POST", "/restrictions", businesshttp.CommercialRestrictionCreate{PropertyID: c.property.ID, RatePlanID: &c.rate.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Definition: revenue.Restriction{StopSell: true}, Reason: "A parent hard stop sell must also close derived children"}, 201)
	c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1}, 409)
}
func (c *commercialHarness) activateOffer(id ids.ID, table string, version int64, amount int64, currency string) {
	c.t.Helper()
	command := businesshttp.CommercialStateChange{Version: version, State: "ACTIVE", Reason: "Independent approved offer activation"}
	command.ApprovalID = c.approval("COMMERCIAL_OFFER_ACTIVATION", "pricing:approve", "", &amount, currency, map[string]any{"offer_id": id, "offer_type": table, "command": command})
	path := map[string]string{"promotions": "promotions", "packages": "packages", "add_ons": "add-ons"}[table]
	c.must(c.owner, "PATCH", "/"+path+"/"+id.String(), command, 200)
}
func TestBusinessCommercialApprovedPromotionPackageAddOnPolicyPrecedence(t *testing.T) {
	c := newCommercialHTTP(t)
	now := time.Now().UTC()
	promotion := revenue.PromotionDefinition{Currency: "NGN", Discount: revenue.Adjustment{Kind: "PERCENT", BasisPoints: -1000}, MaxDiscountBPS: 2000, MaxDiscountMinor: 5000, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), MinNights: 1, MaxNights: 30, RatePlanIDs: []ids.ID{c.rate.ID}, Stackable: true, Priority: 10, PolicyReference: "APPROVED-PROMOTION:v1"}
	promo := commercialData[revenue.Promotion](t, c.must(c.owner, "POST", "/promotions", businesshttp.CommercialPromotionCreate{PropertyID: c.property.ID, Code: "APPROVED10", Definition: promotion, Reason: "Create a capped compatible ten percent promotion"}, 201))
	c.activateOffer(promo.ID, "promotions", promo.Version, promotion.MaxDiscountMinor, "NGN")
	addon := commercialData[revenue.AddOn](t, c.must(c.owner, "POST", "/add-ons", businesshttp.CommercialAddOnCreate{PropertyID: c.property.ID, Name: "Breakfast commercial reference", Definition: revenue.AddOnDefinition{Currency: "NGN", AmountMinor: 2000, MaxQuantity: 2, ExecutionReference: "BREAKFAST-OFFER:v1"}, Reason: "Create a priced add-on reference without executing hospitality"}, 201))
	c.activateOffer(addon.ID, "add_ons", addon.Version, 4000, "NGN")
	packDefinition := revenue.PackageDefinition{Currency: "NGN", Discount: revenue.Adjustment{Kind: "FIXED", AmountMinor: -1000}, MaxDiscountMinor: 2000, MaxDiscountBPS: 1000, AllowPromotions: true, MinNights: 1, MaxNights: 30, PolicyReference: "APPROVED-PACKAGE:v1"}
	pack := commercialData[struct {
		Package revenue.Package `json:"package"`
	}](t, c.must(c.owner, "POST", "/packages", businesshttp.CommercialPackageCreate{PropertyID: c.property.ID, ProductID: c.product.ID, Name: "Approved breakfast stay package", Definition: packDefinition, Components: []businesshttp.CommercialPackageComponent{{AddOnID: addon.ID, Quantity: 1}}, Reason: "Create a bounded compatible package"}, 201))
	c.activateOffer(pack.Package.ID, "packages", pack.Package.Version, packDefinition.MaxDiscountMinor, "NGN")
	in := businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adults: 2, Quantity: 1, PromotionIDs: []ids.ID{promo.ID}, PackageID: &pack.Package.ID}
	quote := commercialData[revenue.QuoteSnapshot](t, c.must(c.owner, "POST", "/rate-quotes", in, 201))
	if quote.BaseAmountMinor != 20000 || quote.DiscountMinor != 3000 || quote.AddOnsMinor != 2000 || quote.AmountMinor != 19000 || quote.PromotionVersions[promo.ID.String()] != 2 || quote.AddOnVersions[addon.ID.String()] != 2 {
		t.Fatalf("Promotion/package/add-on precedence %#v", quote)
	}
	hold := commercialData[inventory.Hold](t, c.must(c.owner, "POST", "/holds", inventory.HoldCommand{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: quote.QuoteID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Quantity: 1, DurationSeconds: 120}, 201))
	c.must(c.owner, "PATCH", "/promotions/"+promo.ID.String(), businesshttp.CommercialStateChange{Version: 2, State: "SUSPENDED", Reason: "Stop promotion without rewriting previous immutable prices"}, 200)
	c.must(c.owner, "POST", "/holds/"+hold.ID.String()+"/extend", businesshttp.CommercialHoldChange{Version: hold.Version, DurationSeconds: 300, Reason: "A suspended promotion cannot authorize an extension"}, 409)
	current := commercialData[inventory.Hold](t, c.must(c.owner, "GET", "/holds/"+hold.ID.String(), nil, 200))
	if current.State != "ACTIVE" || current.QuoteID != quote.QuoteID {
		t.Fatal("Offer suspension rewrote an existing held price")
	}
	c.must(c.owner, "POST", "/restrictions", businesshttp.CommercialRestrictionCreate{PropertyID: c.property.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Definition: revenue.Restriction{StopSell: true}, Reason: "Promotions cannot override a property hard stop sell"}, 201)
	c.must(c.owner, "POST", "/rate-quotes", in, 409)
}
