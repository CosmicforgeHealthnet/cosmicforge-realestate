package integration

import (
	"sync"
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/inventory"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func TestBusinessCommercialQuoteAtomicHoldRaceReleaseAndTenantIsolation(t *testing.T) {
	c := newCommercialHTTP(t)
	q := c.quote()
	if q.IsReservation || q.AmountMinor != 20000 || q.Currency != "NGN" || len(q.Nights) != 2 || q.Quantity != 1 || len(q.TaxProfileIDs) != 1 {
		t.Fatalf("immutable quote %#v", q)
	}
	command := inventory.HoldCommand{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: q.QuoteID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Quantity: 1, DurationSeconds: 120}
	var wg sync.WaitGroup
	results := make(chan struct {
		status int
		raw    []byte
	}, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, raw := c.call(c.owner, "POST", "/holds", command, nil)
			results <- struct {
				status int
				raw    []byte
			}{status, raw}
		}()
	}
	wg.Wait()
	close(results)
	wins, losses := 0, 0
	var hold inventory.Hold
	for r := range results {
		switch r.status {
		case 201:
			wins++
			hold = commercialData[inventory.Hold](t, r.raw)
		case 409:
			losses++
		default:
			t.Fatalf("hold race %d %s", r.status, r.raw)
		}
	}
	if wins != 1 || losses != 1 {
		t.Fatalf("oversell fence wins=%d losses=%d", wins, losses)
	}
	var holds, days, events int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM inventory_holds WHERE organisation_id=$1),(SELECT count(*) FROM inventory_hold_days WHERE organisation_id=$1),(SELECT count(*) FROM commercial_history WHERE organisation_id=$1 AND operation='INVENTORY_HOLD_CREATED')`, c.owner.registered.OrganisationID).Scan(&holds, &days, &events); e != nil {
		t.Fatal(e)
	}
	if holds != 1 || days != 2 || events != 1 {
		t.Fatalf("partial/duplicate commit holds=%d days=%d events=%d", holds, days, events)
	}
	outsider := c.user()
	if status, _ := c.call(outsider, "GET", "/sellable-products/"+c.product.ID.String(), nil, map[string]string{"X-Organisation-ID": c.owner.registered.OrganisationID.String()}); status != 404 {
		t.Fatalf("tenant override accepted %d", status)
	}
	c.must(c.owner, "POST", "/holds/"+hold.ID.String()+"/handoff", map[string]any{}, 503)
	c.must(c.owner, "DELETE", "/holds/"+hold.ID.String(), businesshttp.CommercialHoldChange{Version: hold.Version, Reason: "Customer abandoned checkout"}, 200)
	retry := commercialData[inventory.Hold](t, c.must(c.owner, "POST", "/holds", command, 201))
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE inventory_holds SET expires_at=now()-interval '1 second',version=version+1 WHERE id=$1`, retry.ID); e != nil {
		t.Fatal(e)
	}
	// Expired holds no longer consume availability even before worker cleanup.
	c.quote()
	if e := c.domain.ProcessCommercialMaintenance(t.Context(), 10); e != nil {
		t.Fatal(e)
	}
	var state string
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state FROM inventory_holds WHERE id=$1`, retry.ID).Scan(&state); e != nil || state != "EXPIRED" {
		t.Fatalf("expiry %s %v", state, e)
	}
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE price_quotes SET amount_minor=0 WHERE id=$1`, q.QuoteID); e == nil {
		t.Fatal("immutable quote could be rewritten")
	}
}
func TestBusinessCommercialQuoteRechecksRateStopSellAndCapacityAuthority(t *testing.T) {
	c := newCommercialHTTP(t)
	q := c.quote()
	next := c.definition
	next.AmountMinor = 11000
	command := businesshttp.CommercialRatePatch{Version: c.rate.Version, Definition: &next, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Reason: "Within approved ten percent rate band"}
	raw := c.must(c.owner, "PATCH", "/rate-plans/"+c.rate.ID.String(), command, 200)
	updated := commercialData[struct {
		Plan revenue.Plan `json:"rate_plan"`
	}](t, raw)
	c.rate = updated.Plan
	hold := inventory.HoldCommand{ProductID: c.product.ID, ProductVersion: c.product.Version, PoolVersion: c.pool.Version, QuoteID: q.QuoteID, DateFrom: q.DateFrom, DateTo: q.DateTo, Quantity: 1, DurationSeconds: 120}
	c.must(c.owner, "POST", "/holds", hold, 409)
	q = c.quote()
	if q.AmountMinor != 22000 {
		t.Fatalf("price after immutable rate revision %d", q.AmountMinor)
	}
	hold.QuoteID = q.QuoteID
	restriction := businesshttp.CommercialRestrictionCreate{PropertyID: c.property.ID, ProductID: &c.product.ID, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Definition: revenue.Restriction{StopSell: true}, Reason: "Safety stop sell after price quote"}
	c.must(c.owner, "POST", "/restrictions", restriction, 201)
	c.must(c.owner, "POST", "/holds", hold, 409)
	c.must(c.owner, "POST", "/rate-quotes", businesshttp.CommercialQuoteCommand{ProductID: c.product.ID, DateFrom: q.DateFrom, DateTo: q.DateTo, Adults: 2, Quantity: 1}, 409)
	var count int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM inventory_holds WHERE organisation_id=$1`, c.owner.registered.OrganisationID).Scan(&count); e != nil || count != 0 {
		t.Fatalf("denied hold persisted %d %v", count, e)
	}
	// Losing the live capacity contract cannot be worked around by an owner role.
	c.domain.CapacityAuthority = nil
	c.must(c.owner, "GET", "/availability?product_id="+c.product.ID.String()+"&date_from="+q.DateFrom+"&date_to="+q.DateTo, nil, 503)
}
func TestBusinessCommercialApprovalAmountBindingAndNoPrivilegeEscalation(t *testing.T) {
	c := newCommercialHTTP(t)
	next := c.definition
	next.AmountMinor = 15000
	command := businesshttp.CommercialRatePatch{Version: c.rate.Version, Definition: &next, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Reason: "Independent exceptional rate policy change"}
	operation := map[string]any{"rate_plan_id": c.rate.ID, "command": command}
	wrong := int64(1)
	command.ApprovalID = c.approval("EXCEPTIONAL_RATE_CHANGE", "rates:approve", "", &wrong, "NGN", operation)
	c.must(c.owner, "PATCH", "/rate-plans/"+c.rate.ID.String(), command, 403)
	var consumed *time.Time
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT consumed_at FROM approval_requests WHERE id=$1`, command.ApprovalID).Scan(&consumed); e != nil || consumed != nil {
		t.Fatalf("invalid amount consumed authorization %v %v", consumed, e)
	}
	command.ApprovalID = ids.Nil
	operation["command"] = command
	command.ApprovalID = c.approval("EXCEPTIONAL_RATE_CHANGE", "rates:approve", "", &next.CeilingMinor, "NGN", operation)
	c.must(c.owner, "PATCH", "/rate-plans/"+c.rate.ID.String(), command, 200)
	c.must(c.owner, "POST", "/approval-policies", map[string]any{"action": "COMMERCIAL_CHANNEL_CHANGE", "risk": "HIGH", "scope_kind": "COMPANY", "levels": []map[string]any{{"capability": "organisation:read", "count": 1, "require_mfa": true}}}, 400)
	staff := c.user()
	membership := c.join(staff, c.owner, []string{"products:read"})
	status, raw := c.call(staff, "GET", "/sellable-products/"+c.product.ID.String(), nil, nil)
	if status != 200 {
		t.Fatalf("explicit scoped read %d %s", status, raw)
	}
	c.must(staff, "POST", "/rate-plans", businesshttp.CommercialRateCreate{PropertyID: c.property.ID, Name: "Escalation", Kind: "STANDARD", DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Definition: c.definition, Reason: "Must not inherit role name privileges"}, 403)
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE staff_scope_grants SET revoked_at=now() WHERE membership_id=$1 AND capability='products:read'`, membership); e != nil {
		t.Fatal(e)
	}
	c.must(staff, "GET", "/sellable-products/"+c.product.ID.String(), nil, 403)
}
