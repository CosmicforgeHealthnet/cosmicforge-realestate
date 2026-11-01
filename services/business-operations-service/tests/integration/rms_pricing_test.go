package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func rmsPolicyFixture(c *commercialHarness, f businesshttp.RMSForecastRun) businesshttp.RMSPolicy {
	c.t.Helper()
	now := c.service.Now().UTC()
	if _, e := c.db.Pool.Exec(c.t.Context(), `UPDATE business_sessions SET mfa_at=$2,expires_at=$3 WHERE organisation_id=$1`, c.owner.registered.OrganisationID, now, now.Add(time.Hour)); e != nil {
		c.t.Fatal(e)
	}
	in := businesshttp.RMSPolicyCommand{PropertyID: c.property.ID, PoolID: c.pool.ID, RatePlanID: c.rate.ID, State: "ACTIVE", Definition: revenue.PricingPolicy{Mode: "RECOMMEND_ONLY", Currency: "NGN", FloorMinor: 9000, CeilingMinor: 11000, MaxDailyChangeBPS: 500, MaxCumulativeChangeBPS: 1000, CumulativeDays: 7, ApprovalThresholdBPS: 100, TargetOccupancyBPS: 7500, FeedbackGainBPS: 1000, MinimumHistoryCoverageBPS: 8000, MaxSourceAgeSeconds: 3600, RecommendationTTLSeconds: 3600, EffectiveAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}, Reason: "Independent bounded RMS policy for an actual canonical historical forecast"}
	op := in
	in.ApprovalID = c.approval("COMMERCIAL_RMS_POLICY", "rms:approve", "", nil, "", map[string]any{"command": op})
	return commercialData[businesshttp.RMSPolicy](c.t, c.must(c.owner, "POST", "/revenue/policies", in, 201))
}
func rmsRecommendationFixture(c *commercialHarness, p businesshttp.RMSPolicy, f businesshttp.RMSForecastRun) businesshttp.RMSRecommendation {
	c.t.Helper()
	return commercialData[businesshttp.RMSRecommendation](c.t, c.must(c.owner, "POST", "/revenue/recommendations", businesshttp.RMSRecommendationCommand{PolicyID: p.ID, ForecastID: f.ID, Reason: "Inspect bounded feedback against retained actual canonical demand"}, 201))
}
func rmsDecision(c *commercialHarness, v businesshttp.RMSRecommendation, action string, approval ids.ID) businesshttp.RMSRecommendation {
	c.t.Helper()
	return commercialData[businesshttp.RMSRecommendation](c.t, c.must(c.owner, "POST", "/revenue/recommendations/"+v.ID.String()+"/decision", businesshttp.RMSDecisionCommand{Version: v.Version, Action: action, ApprovalID: approval, Reason: "Apply independently reviewed exact recommendation with current guards"}, 200))
}
func rmsApproval(c *commercialHarness, v businesshttp.RMSRecommendation, rollback bool) ids.ID {
	c.t.Helper()
	action := "COMMERCIAL_RMS_RATE_CHANGE"
	if rollback {
		action = "COMMERCIAL_RMS_RATE_ROLLBACK"
	}
	exposure := c.definition.CeilingMinor
	return c.approval(action, "rates:approve", "", &exposure, "NGN", map[string]any{"recommendation_id": v.ID, "change_digest": v.ChangeDigest, "policy_id": v.PolicyID, "forecast_id": v.ForecastID, "target_plan_version": v.TargetPlanVersion, "rollback": rollback})
}
func TestBusinessRMSGovernedApplicationSimulationAndCompensatingRollback(t *testing.T) {
	c, f := rmsOwnedHistoryFixture(t)
	p := rmsPolicyFixture(c, f)
	v := rmsRecommendationFixture(c, p, f)
	if len(v.Snapshot.Segments) != 1 || v.Snapshot.Segments[0].Proposed.AmountMinor != 9750 {
		t.Fatalf("feedback %+v", v)
	}
	raw := c.must(c.owner, "POST", "/revenue/recommendations/"+v.ID.String()+"/simulate", businesshttp.RMSVersionCommand{Version: v.Version, Reason: "Inspect projected fixed demand with unknown cost inputs"}, 201)
	sim := commercialData[struct {
		Result revenue.PricingSimulation `json:"result"`
	}](t, raw)
	if sim.Result.ProjectedGrossAccommodationMinor != 4875 || sim.Result.ProjectedNetContributionMinor != nil {
		t.Fatalf("simulation %+v", sim)
	}
	before, e := revenue.VersionForDay(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.rate.ID, v.DateFrom)
	if e != nil || before.AmountMinor != 10000 {
		t.Fatalf("simulation mutated rates %v %+v", e, before)
	}
	c.must(c.owner, "POST", "/revenue/recommendations/"+v.ID.String()+"/decision", businesshttp.RMSDecisionCommand{Version: 1, Action: "APPLY", Reason: "Unapproved change must fail"}, 409)
	v = rmsDecision(c, v, "SUBMIT", ids.Nil)
	v = rmsDecision(c, v, "APPROVE", rmsApproval(c, v, false))
	c.must(c.approver, "POST", "/revenue/recommendations/"+v.ID.String()+"/decision", businesshttp.RMSDecisionCommand{Version: v.Version, Action: "APPLY", Reason: "Another actor cannot borrow consumed approval authority"}, 403)
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			status, _ := c.call(c.owner, "POST", "/revenue/recommendations/"+v.ID.String()+"/decision", businesshttp.RMSDecisionCommand{Version: v.Version, Action: "APPLY", Reason: "Concurrent execution must apply the exact proposal only once"}, nil)
			statuses <- status
		}()
	}
	first, second := <-statuses, <-statuses
	if !(first == 200 && second == 409 || first == 409 && second == 200) {
		t.Fatalf("concurrent application statuses %d/%d", first, second)
	}
	v = commercialData[businesshttp.RMSRecommendation](t, c.must(c.owner, "GET", "/revenue/recommendations/"+v.ID.String(), nil, 200))
	if v.State != "APPLIED" || len(v.AppliedVersions) != 1 {
		t.Fatalf("application %+v", v)
	}
	current, e := revenue.VersionForDay(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.rate.ID, v.DateFrom)
	if e != nil || current.AmountMinor != 9750 {
		t.Fatalf("canonical rate %+v %v", current, e)
	}
	c.must(c.owner, "POST", "/revenue/recommendations/"+v.ID.String()+"/decision", businesshttp.RMSDecisionCommand{Version: v.Version - 1, Action: "APPLY", Reason: "Duplicate submission must not double-apply"}, 409)
	v = rmsDecision(c, v, "ROLLBACK", rmsApproval(c, v, true))
	current, e = revenue.VersionForDay(t.Context(), c.db.Pool, c.owner.registered.OrganisationID, c.rate.ID, v.DateFrom)
	if e != nil || current.AmountMinor != 10000 || current.ID == before.ID || v.State != "ROLLED_BACK" {
		t.Fatalf("compensating rollback %+v %+v %v", v, current, e)
	}
	var decisions, versions int
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM pricing_decisions WHERE recommendation_id=$1`, v.ID).Scan(&decisions); e != nil {
		t.Fatal(e)
	}
	if e = c.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM rate_plan_versions WHERE rate_plan_id=$1`, c.rate.ID).Scan(&versions); e != nil {
		t.Fatal(e)
	}
	if decisions != 4 || versions != 3 {
		t.Fatalf("immutable lineage decisions=%d versions=%d", decisions, versions)
	}
	if _, e = c.db.Pool.Exec(t.Context(), `UPDATE pricing_recommendations SET snapshot='{}',version=version+1 WHERE id=$1`, v.ID); e == nil {
		t.Fatal("proposal mutated")
	}
	if _, e = c.db.Pool.Exec(t.Context(), `DELETE FROM pricing_decisions WHERE recommendation_id=$1`, v.ID); e == nil {
		t.Fatal("decision history deleted")
	}
}
func TestBusinessRMSPauseStaleRatesAndMandatoryUnknownInputsFailClosed(t *testing.T) {
	c, f := rmsOwnedHistoryFixture(t)
	p := rmsPolicyFixture(c, f)
	v := rmsRecommendationFixture(c, p, f)
	v = rmsDecision(c, v, "SUBMIT", ids.Nil)
	approval := rmsApproval(c, v, false)
	// An ordinary canonical update makes the retained recommendation stale.
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE rate_plans SET version=version+1 WHERE id=$1`, c.rate.ID); e != nil {
		t.Fatal(e)
	}
	c.must(c.owner, "POST", "/revenue/recommendations/"+v.ID.String()+"/decision", businesshttp.RMSDecisionCommand{Version: v.Version, Action: "APPROVE", ApprovalID: approval, Reason: "An old proposal cannot overwrite a newer canonical rate"}, 409)
	var consumed *time.Time
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT consumed_at FROM approval_requests WHERE id=$1`, approval).Scan(&consumed); e != nil || consumed != nil {
		t.Fatalf("failed change consumed authority %v %v", consumed, e)
	}
	pause := businesshttp.RMSPolicyCommand{PropertyID: p.PropertyID, PoolID: p.PoolID, RatePlanID: p.RatePlanID, ExpectedVersion: p.Version, State: "PAUSED", Definition: p.Definition, Reason: "Immediate kill switch without requiring a new approval"}
	paused := commercialData[businesshttp.RMSPolicy](t, c.must(c.owner, "POST", "/revenue/policies", pause, 201))
	if paused.Version != 2 {
		t.Fatalf("pause %+v", paused)
	}
	c.must(c.owner, "POST", "/revenue/recommendations", businesshttp.RMSRecommendationCommand{PolicyID: p.ID, ForecastID: f.ID, Reason: "Previously authorized policy is no longer current"}, 409)
	// No mode or model label may pretend an absent authoritative input exists.
	missing := pause
	missing.ExpectedVersion = 2
	missing.State = "ACTIVE"
	missing.Definition.MandatoryInputs = []string{"marketplace_conversion_attribution"}
	operation := missing
	missing.ApprovalID = c.approval("COMMERCIAL_RMS_POLICY", "rms:approve", "", nil, "", map[string]any{"command": operation})
	unusable := commercialData[businesshttp.RMSPolicy](t, c.must(c.owner, "POST", "/revenue/policies", missing, 201))
	c.must(c.owner, "POST", "/revenue/recommendations", businesshttp.RMSRecommendationCommand{PolicyID: unusable.ID, ForecastID: f.ID, Reason: "A mandatory input cannot be replaced by invented confidence"}, 409)
	c.service.Now = func() time.Time { return f.SourceAsOf.Add(2 * time.Hour) }
	if _, e := c.db.Pool.Exec(t.Context(), `UPDATE business_sessions SET mfa_at=$2,expires_at=$3 WHERE organisation_id=$1`, c.owner.registered.OrganisationID, c.service.Now(), c.service.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessRMSMaintenance(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	if e := c.domain.ProcessRMSMaintenance(t.Context(), 50); e != nil {
		t.Fatal(e)
	}
	var expired string
	var count int
	if e := c.db.Pool.QueryRow(t.Context(), `SELECT state,(SELECT count(*) FROM pricing_decisions WHERE recommendation_id=p.id AND state='EXPIRED') FROM pricing_recommendations p WHERE id=$1`, v.ID).Scan(&expired, &count); e != nil || expired != "EXPIRED" || count != 1 {
		t.Fatalf("expiry state=%s decisions=%d err=%v", expired, count, e)
	}
}
