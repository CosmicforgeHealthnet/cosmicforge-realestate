package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/platform"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/serviceauth"
)

func TestBusinessPMSNightAuditSnapshotFenceSingleCloseAndImmutableReport(t *testing.T) {
	c := newCommercialHTTP(t)
	reviewer := c.user()
	c.secure(reviewer)
	c.join(reviewer, c.owner, []string{"approvals:decide", "night_audit:approve"})
	c.approver = reviewer
	loc, e := time.LoadLocation(c.property.Timezone)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	policy := businesshttp.PMSHospitalityPolicyCommand{PropertyID: c.property.ID, CutoffLocal: now.Add(time.Minute).In(loc).Format("15:04"), Reason: "Configure the next local property cutoff under independent operational approval"}
	op := policy
	policy.ApprovalID = c.approval("COMMERCIAL_PMS_BUSINESS_DAY_POLICY", "night_audit:approve", "", nil, "", map[string]any{"command": op})
	c.must(c.owner, "POST", "/hospitality-policies", policy, 201)
	command := businesshttp.PMSAuditCommand{PropertyID: c.property.ID, Reason: "Validate actual reservations, rooms, financial exceptions and posting sources"}
	type auditResult struct {
		ID             ids.ID                        `json:"id"`
		State          string                        `json:"state"`
		SnapshotDigest string                        `json:"snapshot_digest"`
		Snapshot       businesshttp.PMSAuditSnapshot `json:"snapshot"`
	}
	audit := commercialData[auditResult](t, c.must(c.owner, "POST", "/night-audit/validate", command, 201))
	if audit.State != "READY" {
		t.Fatalf("empty actual property not ready %+v", audit)
	}
	close := businesshttp.PMSDayCloseCommand{PropertyID: c.property.ID, BusinessDayID: audit.Snapshot.Day.ID, Version: audit.Snapshot.Day.Version, AuditID: audit.ID, SnapshotDigest: audit.SnapshotDigest, Reason: "Close only the actual validated property day after its configured civil cutoff"}
	c.must(c.owner, "POST", "/night-audit/close", close, 409)
	// A real operational change with unchanged exception counts still invalidates
	// the previous snapshot. The clock is a controlled domain test dependency.
	pmsReady(c)
	c.domain.Auth.Service.Now = func() time.Time { return now.Add(2 * time.Minute) }
	c.must(c.owner, "POST", "/night-audit/close", close, 409)
	audit = commercialData[auditResult](t, c.must(c.owner, "POST", "/night-audit/validate", command, 201))
	close.AuditID = audit.ID
	close.SnapshotDigest = audit.SnapshotDigest
	key := ids.New().String()
	code, raw := c.call(c.owner, "POST", "/night-audit/close", close, map[string]string{"Idempotency-Key": key})
	if code != 200 {
		t.Fatalf("close %d %s", code, raw)
	}
	result := commercialData[struct {
		ID   ids.ID                      `json:"id"`
		Next businesshttp.PMSBusinessDay `json:"next_business_day"`
	}](t, raw)
	first, _ := time.Parse(time.DateOnly, audit.Snapshot.Day.Day)
	if result.Next.Day != first.AddDate(0, 0, 1).Format(time.DateOnly) {
		t.Fatal("business-day rollover skipped or repeated a date")
	}
	code, raw = c.call(c.owner, "POST", "/night-audit/close", close, map[string]string{"Idempotency-Key": key})
	if code != 200 {
		t.Fatalf("close replay %d %s", code, raw)
	}
	c.must(c.owner, "POST", "/night-audit/close", close, 409)
	if _, e = c.db.Pool.Exec(t.Context(), `DELETE FROM business_day_close_reports WHERE close_id=$1`, result.ID); e == nil {
		t.Fatal("closing report could be erased")
	}
}

func TestBusinessPMSCashierSignedExpectedCountDistinctHandover(t *testing.T) {
	c := newCommercialHTTP(t)
	drawer := commercialData[struct {
		ID ids.ID `json:"id"`
	}](t, c.must(c.owner, "POST", "/cash-drawers", businesshttp.PMSDrawerCommand{PropertyID: c.property.ID, Code: "DISPOSABLE-DRAWER", Currency: c.property.Currency, Reason: "Assign a property-local drawer with the canonical operating currency"}, 201))
	open := businesshttp.PMSCashierOpenCommand{DrawerID: drawer.ID, OpeningMinor: 10000, Reason: "Record an actual opening declaration without inventing a Payment balance"}
	shift := commercialData[businesshttp.PMSCashierShift](t, c.must(c.owner, "POST", "/cashier-shifts/open", open, 201))
	c.must(c.owner, "POST", "/cashier-shifts/open", open, 409)
	close := businesshttp.PMSCashierCloseCommand{Version: shift.Version, ClosingMinor: 12500, Reason: "Close the actual cashier count using its signed owning financial reconciliation"}
	c.must(c.owner, "POST", "/cashier-shifts/"+shift.ID.String()+"/close", close, 403)
	p := c.supplySigner(platform.PaymentService)
	token, e := p.TokenFor(string(platform.BusinessOperationsService), serviceauth.Delegation{OrganizationID: c.owner.registered.OrganisationID})
	if e != nil {
		t.Fatal(e)
	}
	input := businesshttp.PMSCashAttestation{OrganisationID: c.owner.registered.OrganisationID, PropertyID: c.property.ID, ShiftID: shift.ID, SourceVersion: 1, ExpectedMovementMinor: 2500, Currency: c.property.Currency, SourceReference: "disposable-payment-cash-reconciliation", ObservedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	path := "/internal/v1/business-supply/cashier-attestations"
	if code, _ := c.rootCall(path, input, map[string]string{"Authorization": "Bearer " + c.owner.tokens.AccessToken}); code != 401 {
		t.Fatalf("browser forged cash truth %d", code)
	}
	header := map[string]string{"Authorization": "Bearer " + token}
	if code, raw := c.rootCall(path, input, header); code != 200 {
		t.Fatalf("signed cash proof %d %s", code, raw)
	}
	if code, raw := c.rootCall(path, input, header); code != 200 {
		t.Fatalf("cash source duplicate %d %s", code, raw)
	}
	finished := commercialData[businesshttp.PMSCashierShift](t, c.must(c.owner, "POST", "/cashier-shifts/"+shift.ID.String()+"/close", close, 200))
	if finished.State != "CLOSED" || finished.ExpectedMinor == nil || *finished.ExpectedMinor != 12500 || finished.VarianceMinor == nil || *finished.VarianceMinor != 0 {
		t.Fatalf("cash proof not reconciled %+v", finished)
	}
	handover := businesshttp.PMSCashierHandoverCommand{Version: finished.Version, DeclaredMinor: 12500, EvidenceReference: "disposable-dual-count-handover-evidence", Reason: "A distinct authorized receiver acknowledges the actual retained closing count"}
	c.must(c.owner, "POST", "/cashier-shifts/"+shift.ID.String()+"/handover", handover, 409)
	receiver := c.user()
	c.secure(receiver)
	c.join(receiver, c.owner, []string{"cashier:write"})
	c.must(receiver, "POST", "/cashier-shifts/"+shift.ID.String()+"/handover", handover, 201)
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE cashier_financial_attestations SET expected_movement_minor=0 WHERE shift_id=$1`, shift.ID); e == nil {
		t.Fatal("owning financial attestation was mutable")
	}
}
