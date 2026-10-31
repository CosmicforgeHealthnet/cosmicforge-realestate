package integration

import (
	"sync"
	"testing"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
)

func TestBusinessCommercialBulkPreviewApprovedApplyConcurrentWorkersAndRollback(t *testing.T) {
	c := newCommercialHTTP(t)
	command := businesshttp.CommercialBulkRateCommand{PropertyID: c.property.ID, PlanVersions: map[string]int64{c.rate.ID.String(): c.rate.Version}, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adjustment: revenue.Adjustment{Kind: "PERCENT", BasisPoints: 500}, Reason: "Reviewed five percent seasonal increase"}
	preview := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "POST", "/rates/bulk-preview", command, 201))
	if preview.State != "PREVIEW" || len(preview.Snapshot.Segments) != 1 || len(preview.Snapshot.Conflicts) != 0 {
		t.Fatalf("preview %#v", preview)
	}
	if q := c.quote(); q.AmountMinor != 20000 {
		t.Fatal("Preview modified canonical prices")
	}
	apply := businesshttp.CommercialBulkApply{PreviewID: preview.ID, Version: preview.Version, SnapshotDigest: preview.Snapshot.Digest, Reason: "Apply the independently reviewed preview"}
	c.must(c.owner, "POST", "/rates/bulk-apply", apply, 403)
	apply.ApprovalID = c.approval("COMMERCIAL_BULK_RATE_CHANGE", "rates:approve", "", &preview.Snapshot.ApprovalAmountMinor, preview.Snapshot.Currency, map[string]any{"command": apply})
	queued := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "POST", "/rates/bulk-apply", apply, 202))
	if queued.State != "QUEUED" {
		t.Fatalf("queued %s", queued.State)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- c.domain.ProcessCommercialRates(t.Context(), 5) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	applied := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "GET", "/rates/bulk-commands/"+preview.ID.String(), nil, 200))
	if applied.State != "APPLIED" || applied.Snapshot.Segments[0].AppliedVersionID == nil {
		t.Fatalf("applied %#v", applied)
	}
	if q := c.quote(); q.AmountMinor != 21000 {
		t.Fatalf("Applied price %d", q.AmountMinor)
	}
	var versions int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, c.rate.ID).Scan(&versions); e != nil || versions != 2 {
		t.Fatalf("double execution rate versions=%d err=%v", versions, e)
	}
	rollback := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "POST", "/rates/bulk-rollback", businesshttp.CommercialBulkRollback{CommandID: applied.ID, Version: applied.Version, Reason: "Restore the independently reviewed previous price"}, 201))
	undo := businesshttp.CommercialBulkApply{PreviewID: rollback.ID, Version: rollback.Version, SnapshotDigest: rollback.Snapshot.Digest, Reason: "Approved rollback creates a new immutable price version"}
	undo.ApprovalID = c.approval("COMMERCIAL_BULK_RATE_ROLLBACK", "rates:approve", "", &rollback.Snapshot.ApprovalAmountMinor, rollback.Snapshot.Currency, map[string]any{"command": undo})
	c.must(c.owner, "POST", "/rates/bulk-apply", undo, 202)
	if e := c.domain.ProcessCommercialRates(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	if q := c.quote(); q.AmountMinor != 20000 {
		t.Fatalf("Rollback did not restore price %d", q.AmountMinor)
	}
	original := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "GET", "/rates/bulk-commands/"+applied.ID.String(), nil, 200))
	if original.State != "ROLLED_BACK" {
		t.Fatalf("Original command %s", original.State)
	}
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, c.rate.ID).Scan(&versions); e != nil || versions != 3 {
		t.Fatalf("Rollback rewrote history versions=%d err=%v", versions, e)
	}
	c.must(c.owner, "POST", "/rates/bulk-rollback", businesshttp.CommercialBulkRollback{CommandID: original.ID, Version: original.Version, Reason: "Must not roll back twice"}, 409)
}
func TestBusinessCommercialBulkStalePreviewAndRevokedActorQuarantineAtomically(t *testing.T) {
	c := newCommercialHTTP(t)
	preview := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "POST", "/rates/bulk-preview", businesshttp.CommercialBulkRateCommand{PropertyID: c.property.ID, PlanVersions: map[string]int64{c.rate.ID.String(): c.rate.Version}, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adjustment: revenue.Adjustment{Kind: "PERCENT", BasisPoints: 500}, Reason: "Rate change awaiting independent execution"}, 201))
	apply := businesshttp.CommercialBulkApply{PreviewID: preview.ID, Version: 1, SnapshotDigest: preview.Snapshot.Digest, Reason: "Queue the reviewed rate command"}
	apply.ApprovalID = c.approval("COMMERCIAL_BULK_RATE_CHANGE", "rates:approve", "", &preview.Snapshot.ApprovalAmountMinor, preview.Snapshot.Currency, map[string]any{"command": apply})
	c.must(c.owner, "POST", "/rates/bulk-apply", apply, 202)
	next := c.definition
	next.AmountMinor = 10900
	c.must(c.owner, "PATCH", "/rate-plans/"+c.rate.ID.String(), businesshttp.CommercialRatePatch{Version: c.rate.Version, Definition: &next, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Reason: "Intervening authorized canonical price update"}, 200)
	if e := c.domain.ProcessCommercialRates(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	view := commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "GET", "/rates/bulk-commands/"+preview.ID.String(), nil, 200))
	if view.State != "QUARANTINED" || view.LastError == nil || *view.LastError != "BULK_RATE_VERSION_CONFLICT" {
		t.Fatalf("Stale worker %#v", view)
	}
	if q := c.quote(); q.AmountMinor != 21800 {
		t.Fatalf("Stale bulk command overwrote a newer price %d", q.AmountMinor)
	}
	var audit int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM commercial_history WHERE entity_id=$1 AND operation='BULK_RATE_QUARANTINED'`, preview.ID).Scan(&audit); e != nil || audit != 1 {
		t.Fatalf("Quarantine audit %d %v", audit, e)
	}
	current, e := revenue.GetPlan(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.rate.ID)
	if e != nil {
		t.Fatal(e)
	}
	preview = commercialData[businesshttp.CommercialBulkView](t, c.must(c.owner, "POST", "/rates/bulk-preview", businesshttp.CommercialBulkRateCommand{PropertyID: c.property.ID, PlanVersions: map[string]int64{c.rate.ID.String(): current.Version}, DateFrom: c.dates.DateFrom, DateTo: c.dates.DateTo, Adjustment: revenue.Adjustment{Kind: "PERCENT", BasisPoints: 500}, Reason: "Independent queued command before session revocation"}, 201))
	apply = businesshttp.CommercialBulkApply{PreviewID: preview.ID, Version: 1, SnapshotDigest: preview.Snapshot.Digest, Reason: "Queue before revoking the initiating session"}
	apply.ApprovalID = c.approval("COMMERCIAL_BULK_RATE_CHANGE", "rates:approve", "", &preview.Snapshot.ApprovalAmountMinor, preview.Snapshot.Currency, map[string]any{"command": apply})
	c.must(c.owner, "POST", "/rates/bulk-apply", apply, 202)
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE business_sessions SET revoked_at=now() WHERE id=$1`, c.owner.tokens.SessionID); e != nil {
		t.Fatal(e)
	}
	if e = c.domain.ProcessCommercialRates(t.Context(), 5); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT state FROM bulk_rate_commands WHERE id=$1`, preview.ID).Scan(&state); e != nil || state != "QUARANTINED" {
		t.Fatalf("Revoked actor worker %s %v", state, e)
	}
}
