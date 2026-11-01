package revenue

import (
	"math"
	"testing"
	"time"
)

func rmsTestPolicy() PricingPolicy {
	now := time.Now().UTC()
	return PricingPolicy{Mode: "RECOMMEND_ONLY", Currency: "NGN", FloorMinor: 9000, CeilingMinor: 11000, MaxDailyChangeBPS: 500, MaxCumulativeChangeBPS: 1000, CumulativeDays: 7, ApprovalThresholdBPS: 100, TargetOccupancyBPS: 7500, FeedbackGainBPS: 1000, MinimumHistoryCoverageBPS: 8000, MaxSourceAgeSeconds: 3600, RecommendationTTLSeconds: 3600, EffectiveAt: now, ExpiresAt: now.Add(24 * time.Hour)}
}
func rmsTestRate() RateDefinition {
	return RateDefinition{Currency: "NGN", AmountMinor: 10000, IncludedAdults: 2, MinGuests: 1, MaxGuests: 2, MinNights: 1, MaxNights: 30, CancellationReference: "terms:v1", PrepaymentReference: "terms:v1", PolicyCode: "BASE", PolicyVersion: 1, FloorMinor: 1000, CeilingMinor: 20000, MaxChangeBPS: 1000}
}
func TestRMSRecommendationBoundsAndExplicitFeedback(t *testing.T) {
	p := rmsTestPolicy()
	sold := int64(500)
	day := ForecastDay{Day: "2027-01-01", PhysicalCapacity: 1, ProjectedSoldMilliNights: &sold, Status: "BASELINE_READY"}
	amount, e := RecommendAmount(p, rmsTestRate(), day)
	if e != nil || amount != 9750 {
		t.Fatalf("feedback amount=%d err=%v", amount, e)
	}
	p.FloorMinor = 9900
	amount, e = RecommendAmount(p, rmsTestRate(), day)
	if e != nil || amount != 9900 {
		t.Fatalf("floor amount=%d err=%v", amount, e)
	}
	p.BlackoutDates = []string{day.Day}
	if _, e = RecommendAmount(p, rmsTestRate(), day); e == nil {
		t.Fatal("blackout authorized")
	}
	p = rmsTestPolicy()
	p.Currency = "USD"
	if _, e = RecommendAmount(p, rmsTestRate(), day); e == nil {
		t.Fatal("currency mismatch authorized")
	}
	p = rmsTestPolicy()
	p.Channels = []string{"MARKETPLACE"}
	if _, e = RecommendAmount(p, rmsTestRate(), day); e == nil {
		t.Fatal("channel scope expanded")
	}
}
func TestRMSSimulationPreservesFractionalDemandAndUnknownCosts(t *testing.T) {
	sold := int64(500)
	f := DemandForecast{Status: "BASELINE_READY", Days: []ForecastDay{{Day: "2027-01-01", Status: "BASELINE_READY", PhysicalCapacity: 1, ProjectedSoldMilliNights: &sold}}}
	r, e := SimulatePricing(f, []PricingSegment{{DateFrom: "2027-01-01", DateTo: "2027-01-02", Proposed: rmsTestRate()}})
	if e != nil || r.ProjectedGrossAccommodationMinor != 5000 || r.ProjectedADRMinor == nil || *r.ProjectedADRMinor != 10000 || r.ProjectedOccupancyBPS == nil || *r.ProjectedOccupancyBPS != 5000 || r.ProjectedNetContributionMinor != nil || r.ProjectedChannelCommissionMinor != nil {
		t.Fatalf("scenario %+v err=%v", r, e)
	}
	f.Status = "INSUFFICIENT_DATA"
	if _, e = SimulatePricing(f, []PricingSegment{{Proposed: rmsTestRate()}}); e == nil {
		t.Fatal("unknown demand treated as zero")
	}
	if _, e = checkedAdd(math.MaxInt64, 1); e == nil {
		t.Fatal("overflow accepted")
	}
}
