package integration

import (
	"testing"
	"time"

	businesshttp "github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/http"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/services/business-operations-service/internal/revenue"
	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func (x *rmsAutomationFixture) analytics(kind string, scope ids.ID) businesshttp.RMSAnalyticsSnapshot {
	x.t.Helper()
	end, _ := time.Parse(time.DateOnly, x.day)
	return commercialData[businesshttp.RMSAnalyticsSnapshot](x.t, x.must(x.owner, "POST", "/revenue/analytics", businesshttp.RMSAnalyticsCommand{ScopeKind: kind, ScopeID: scope, DateFrom: x.day, DateTo: end.AddDate(0, 0, 1).Format(time.DateOnly), HistoryDays: 56}, 201))
}
func (x *rmsAutomationFixture) advanced(p businesshttp.RMSPolicy, kind string, analytics *ids.ID, status int) businesshttp.RMSAdvancedRecommendation {
	x.t.Helper()
	raw := x.must(x.owner, "POST", "/revenue/advanced-recommendations", businesshttp.RMSAdvancedCommand{PolicyID: p.ID, ForecastID: x.forecast.ID, AnalyticsID: analytics, Kind: kind, Reason: "Derive a source-backed advanced recommendation"}, status)
	if status != 201 {
		return businesshttp.RMSAdvancedRecommendation{}
	}
	return commercialData[businesshttp.RMSAdvancedRecommendation](x.t, raw)
}
func (x *rmsAutomationFixture) advancedDecide(v businesshttp.RMSAdvancedRecommendation, action string, approval ids.ID, status int) businesshttp.RMSAdvancedRecommendation {
	x.t.Helper()
	raw := x.must(x.owner, "POST", "/revenue/advanced-recommendations/"+v.ID.String()+"/decision", businesshttp.RMSAdvancedDecisionCommand{Version: v.Version, Action: action, ApprovalID: approval, Reason: "Independent reviewed advanced decision"}, status)
	if status != 200 {
		return v
	}
	return commercialData[businesshttp.RMSAdvancedRecommendation](x.t, raw)
}
func (x *rmsAutomationFixture) advancedApproval(v businesshttp.RMSAdvancedRecommendation) ids.ID {
	x.t.Helper()
	var amount *int64
	currency := ""
	if p := v.Proposal.Recommendation; p.Rate != nil {
		amount, currency = &p.Rate.CeilingMinor, p.Rate.Currency
	} else if p.Promotion != nil {
		amount, currency = &p.Promotion.MaxDiscountMinor, p.Promotion.Currency
	}
	return x.approval("COMMERCIAL_RMS_ADVANCED_CHANGE", "pricing:approve", "", amount, currency, map[string]any{"advanced_recommendation_id": v.ID, "change_digest": v.ChangeDigest, "kind": v.Kind, "application": v.Application, "target_plan_version": v.TargetPlanVersion})
}

func TestBusinessRMSAnalyticsAggregationReproducibilityAndNonMutatingSimulation(t *testing.T) {
	x := newRMSAutomationFixture(t)
	org := x.owner.registered.OrganisationID
	bad := businesshttp.RMSMarketInputCommand{PropertyID: x.property.ID, Kind: "CHANNEL_COMMISSION", Channel: "BOOKING_COM", DateFrom: "2026-01-01", DateTo: "2026-12-31", Label: "Contracted commission", SourceReference: "signed-channel-contract-reference", EvidenceDigest: "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9a", ObservedAt: x.service.Now().Add(-time.Hour)}
	commission := int64(1500)
	bad.ValueBPS = &commission
	x.must(x.owner, "POST", "/revenue/market-inputs", bad, 400) // no licence reference
	good := bad
	good.LicenceReference = "contractual-data-use-licence-reference"
	day, _ := time.Parse(time.DateOnly, x.day)
	good.DateFrom, good.DateTo = day.AddDate(0, 0, -120).Format(time.DateOnly), day.AddDate(0, 0, 30).Format(time.DateOnly)
	x.must(x.owner, "POST", "/revenue/market-inputs", good, 201)
	first := x.analytics("PROPERTY", x.property.ID)
	second := x.analytics("PROPERTY", x.property.ID)
	if first.ID == second.ID || first.InputDigest != second.InputDigest || first.ResultDigest != second.ResultDigest {
		t.Fatalf("analytics not reproducible: %s/%s vs %s/%s", first.InputDigest, first.ResultDigest, second.InputDigest, second.ResultDigest)
	}
	r := first.Result
	if r.Model != revenue.AnalyticsModel || r.IsCalibratedProbability || r.LeadTime.Bookings != 4 || r.LeadTime.MedianDays == nil || r.CancellationRateBPS == nil || *r.CancellationRateBPS != 0 || len(r.Forecast) != 1 || len(r.Properties) != 1 {
		t.Fatalf("source-backed analytics %+v", r)
	}
	featured := false
	for _, f := range r.Features {
		featured = featured || f == "governed_channel_commission"
	}
	if !featured || r.ComparablePeriod.Status != "UNAVAILABLE" || r.ComparablePeriod.OccupancyBPS != nil {
		t.Fatalf("governed inputs / missing comparable handling %+v", r)
	}
	branch := x.analytics("BRANCH", x.property.BranchID)
	portfolio := x.analytics("PORTFOLIO", org)
	if len(branch.Result.Branches) != 1 || branch.Result.Branches[0].ScopeID != x.property.BranchID || portfolio.Result.Portfolio.History.ConfirmedRoomNights != r.Portfolio.History.ConfirmedRoomNights || len(portfolio.PropertyIDs) != 1 {
		t.Fatalf("aggregation branch=%+v portfolio=%+v", branch.Result.Branches, portfolio.Result.Portfolio)
	}
	x.must(x.owner, "GET", "/revenue/analytics/"+first.ID.String(), nil, 200)
	clock := x.service.Now
	x.service.Now = time.Now
	outsider := x.user()
	x.service.Now = clock
	x.must(outsider, "GET", "/revenue/analytics/"+first.ID.String(), nil, 404)
	// Simulation is a pure projection: no rates, promotions, inventory or events change.
	counts := func() [4]int {
		return [4]int{x.count(`SELECT count(*) FROM rate_plan_versions WHERE organisation_id=$1`, org), x.count(`SELECT count(*) FROM promotions WHERE organisation_id=$1`, org), x.count(`SELECT count(*) FROM outbox_events`), x.count(`SELECT coalesce(sum(version),0) FROM inventory_calendar WHERE organisation_id=$1`, org)}
	}
	before := counts()
	hold, cut := int64(0), int64(-500)
	raw := x.must(x.owner, "POST", "/revenue/scenario-simulations", businesshttp.RMSScenarioCommand{ForecastID: x.forecast.ID, RatePlanID: x.rate.ID, AnalyticsID: &first.ID, Alternatives: []revenue.ScenarioAlternative{{Name: "hold", ChangeBPS: &hold}, {Name: "cut", ChangeBPS: &cut, PromotionDiscountBPS: 1000, PromotionShareBPS: 5000}}}, 200)
	sim := commercialData[businesshttp.RMSScenarioResult](t, raw)
	if sim.Mutated || len(sim.Simulation.Results) != 2 || sim.Simulation.Results[0].GrossMinor != 5000 || sim.Simulation.Results[0].OccupancyBPS == nil || *sim.Simulation.Results[0].OccupancyBPS != 5000 {
		t.Fatalf("simulation %+v", sim)
	}
	// The DIRECT channel has no governed commission, so net/contribution stay unknown.
	if sim.Simulation.Results[0].NetRevenueMinor != nil || sim.Simulation.Results[0].ContributionMinor != nil {
		t.Fatalf("unknown commission fabricated %+v", sim.Simulation.Results[0])
	}
	if after := counts(); after != before {
		t.Fatalf("simulation mutated state %v -> %v", before, after)
	}
	models := commercialData[struct {
		Items []map[string]any `json:"items"`
	}](t, x.must(x.owner, "GET", "/revenue/models", nil, 200))
	if len(models.Items) < 2 {
		t.Fatalf("model registry %+v", models)
	}
}

func TestBusinessRMSAdvancedRecommendationsApplyCanonicallyOrStayAdvisory(t *testing.T) {
	x := newRMSAutomationFixture(t)
	p := x.policy("RECOMMEND_ONLY", nil)
	snapshot := x.analytics("PROPERTY", x.property.ID)
	// Rate range: canonical rate version through the existing rate engine.
	rng := x.advanced(p, "RATE_RANGE", nil, 201)
	if rng.Application != "CANONICAL_RATE_VERSION" || rng.Proposal.Recommendation.Rate == nil || rng.Proposal.Recommendation.Rate.FloorMinor != 9500 || rng.Proposal.Recommendation.Explanation == "" {
		t.Fatalf("rate range %+v", rng)
	}
	x.advancedDecide(rng, "APPLY", ids.Nil, 409)
	rng = x.advancedDecide(rng, "SUBMIT", ids.Nil, 200)
	x.advancedDecide(rng, "APPROVE", ids.Nil, 403)
	rng = x.advancedDecide(rng, "APPROVE", x.advancedApproval(rng), 200)
	x.must(x.approver, "POST", "/revenue/advanced-recommendations/"+rng.ID.String()+"/decision", businesshttp.RMSAdvancedDecisionCommand{Version: rng.Version, Action: "APPLY", Reason: "Another actor cannot borrow consumed authority"}, 403)
	rng = x.advancedDecide(rng, "APPLY", ids.Nil, 200)
	current, e := revenue.VersionForDay(t.Context(), x.db.Pool, x.owner.registered.OrganisationID, x.rate.ID, x.day)
	if e != nil || rng.State != "APPLIED" || current.Definition.FloorMinor != 9500 || current.AmountMinor != 10000 || rng.AppliedReference["rate_version_id"] != current.ID.String() {
		t.Fatalf("applied range %+v %+v %v", rng, current, e)
	}
	// Last-minute promotion: canonical promotion scoped to the rate plan.
	promo := x.advanced(p, "LAST_MINUTE", nil, 201)
	if promo.Application != "CANONICAL_PROMOTION" || promo.Proposal.Recommendation.Promotion == nil || len(promo.Proposal.Recommendation.Promotion.RatePlanIDs) != 1 {
		t.Fatalf("last minute %+v", promo)
	}
	promo = x.advancedDecide(promo, "SUBMIT", ids.Nil, 200)
	promo = x.advancedDecide(promo, "APPROVE", x.advancedApproval(promo), 200)
	promo = x.advancedDecide(promo, "APPLY", ids.Nil, 200)
	if x.count(`SELECT count(*) FROM promotions WHERE organisation_id=$1 AND state='ACTIVE' AND approval_id=$2 AND code=$3`, x.owner.registered.OrganisationID, *promo.ApprovalID, promo.Proposal.Recommendation.PromotionCode) != 1 {
		t.Fatal("canonical promotion not created")
	}
	// Advisory kinds are retained and acknowledged, never applied.
	allocation := x.advanced(p, "CHANNEL_ALLOCATION", &snapshot.ID, 201)
	overbooking := x.advanced(p, "OVERBOOKING", &snapshot.ID, 201)
	for _, v := range []businesshttp.RMSAdvancedRecommendation{allocation, overbooking} {
		if v.Application != "ADVISORY" || v.AdvisoryReason == "" {
			t.Fatalf("advisory %+v", v)
		}
		x.advancedDecide(v, "SUBMIT", ids.Nil, 409)
		x.advancedDecide(v, "APPLY", ids.Nil, 409)
		if got := x.advancedDecide(v, "ACKNOWLEDGE", ids.Nil, 200); got.State != "ACKNOWLEDGED" {
			t.Fatalf("acknowledge %+v", got)
		}
	}
	// Unsupported conditions do not invent a recommendation.
	x.advanced(p, "MIN_LOS", nil, 409)
	x.advanced(p, "BOOKING_WINDOW", nil, 409)
	// A proposal derived from a superseded rate cannot be approved.
	long := x.advanced(p, "LONG_STAY", &snapshot.ID, 201)
	long = x.advancedDecide(long, "SUBMIT", ids.Nil, 200)
	approval := x.advancedApproval(long)
	if _, e = x.db.Pool.Exec(t.Context(), `UPDATE rate_plans SET version=version+1 WHERE id=$1`, x.rate.ID); e != nil {
		t.Fatal(e)
	}
	x.advancedDecide(long, "APPROVE", approval, 409)
	if _, e = x.db.Pool.Exec(t.Context(), `UPDATE rms_advanced_recommendations SET proposal='{}',version=version+1 WHERE id=$1`, long.ID); e == nil {
		t.Fatal("advanced proposal mutated")
	}
	if x.count(`SELECT count(*) FROM rms_advanced_decisions WHERE recommendation_id=$1`, rng.ID) != 3 {
		t.Fatal("advanced decision history incomplete")
	}
}
