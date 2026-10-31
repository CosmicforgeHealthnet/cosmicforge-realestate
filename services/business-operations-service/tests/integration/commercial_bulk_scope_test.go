package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/integrations/verification"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
)

func TestBusinessCommercialBranchBulkScopeChecksEveryPropertyAndSelectedProduct(t *testing.T) {
	c := newCommercialHTTP(t)
	other := c.inventoryNode(c.owner, "/properties", map[string]any{"branch_id": c.property.BranchID, "code": "SECOND-COMMERCIAL-HOTEL", "name": "Second eligible property", "capacity_code": "MANAGEMENT", "attributes": map[string]any{"classification": "HOTEL"}})
	provider := c.domain.Verification.(*businessVerificationFake)
	proof, e := provider.Create(t.Context(), verification.TypePropertyAuthority, verification.Subject{Type: verification.SubjectProperty, Domain: string(platform.DomainBusinessOperations), ID: other.ID}, "second-property-authority", "second-property-authority")
	if e != nil {
		t.Fatal(e)
	}
	expiry := time.Now().Add(time.Hour)
	provider.set(proof.VerificationID, "VERIFIED", &expiry)
	c.must(c.owner, "POST", "/inventory-nodes/"+other.ID.String()+"/verification", map[string]any{"version": other.Version, "verification_id": proof.VerificationID}, 200)
	other = c.activateNode(other)
	definition := c.definition
	definition.TaxProfileID = nil
	created := commercialData[struct {
		Plan revenue.Plan `json:"rate_plan"`
	}](t, c.must(c.owner, "POST", "/rate-plans", businesshttp.CommercialRateCreate{PropertyID: other.ID, Name: "Draft second-property rate", Kind: "STANDARD", DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Definition: definition, Reason: "Prepare canonical second-property pricing"}, 201))
	branch := c.property.BranchID
	command := businesshttp.CommercialBulkRateCommand{BranchID: &branch, PlanVersions: map[string]int64{c.rate.ID.String(): c.rate.Version, created.Plan.ID.String(): created.Plan.Version}, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adjustment: revenue.Adjustment{Kind: "PERCENT", BasisPoints: 500}, Reason: "Independently reviewed multi-property branch rate batch"}
	bad := command
	bad.ProductIDs = []ids.ID{c.product.ID}
	c.must(c.owner, "POST", "/rates/bulk-preview", bad, 400)
	preview := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "POST", "/rates/bulk-preview", command, 201))
	if len(preview.Snapshot.Segments) != 2 {
		t.Fatalf("Branch preview %#v", preview.Snapshot)
	}
	apply := businesshttp.CommercialBulkApply{PreviewID: preview.ID, Version: preview.Version, SnapshotDigest: preview.Snapshot.Digest, Reason: "Apply the complete reviewed branch snapshot"}
	apply.ApprovalID = c.approvalAt("COMMERCIAL_BULK_RATE_CHANGE", "rates:approve", "", &preview.Snapshot.ApprovalAmountMinor, preview.Snapshot.Currency, map[string]any{"command": apply}, "BRANCH", branch)
	c.must(c.owner, "POST", "/rates/bulk-apply", apply, 202)
	if e = c.domain.ProcessCommercialRates(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	view := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "GET", "/rates/bulk-commands/"+preview.ID.String(), nil, 200))
	if view.State != "APPLIED" || len(view.Snapshot.AppliedPlanVersions) != 2 {
		t.Fatalf("Branch batch not atomically applied %#v", view)
	}
	var scopeCount int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM bulk_rate_scope_links WHERE organisation_id=$1 AND command_id=$2`, c.owner.registered.OrganisationID, preview.ID).Scan(&scopeCount); e != nil || scopeCount != 2 {
		t.Fatalf("Normalized batch scope %d %v", scopeCount, e)
	}
	staff := c.user()
	member := c.join(staff, c.owner, nil)
	if _, e = c.db.Pool.Exec(t.Context(), `INSERT INTO staff_scope_grants(id,organisation_id,membership_id,capability,scope_kind,scope_id,inherit,effective_at) VALUES($1,$2,$3,'rates:read','PROPERTY',$4,true,now())`, ids.New(), c.owner.registered.OrganisationID, member, c.property.ID); e != nil {
		t.Fatal(e)
	}
	c.must(staff, "GET", "/rates/bulk-commands/"+preview.ID.String(), nil, 403)
}
