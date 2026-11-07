package revenue

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/ids"
)

func automationPolicy(mode string) PricingPolicy {
	now := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	return PricingPolicy{Mode: mode, Currency: "NGN", FloorMinor: 8000, CeilingMinor: 12000, MaxDailyChangeBPS: 500, MaxCumulativeChangeBPS: 1000, CumulativeDays: 7, ApprovalThresholdBPS: 300, TargetOccupancyBPS: 7500, FeedbackGainBPS: 1000, MinimumHistoryCoverageBPS: 8000, MaxSourceAgeSeconds: 3600, RecommendationTTLSeconds: 3600, BlackoutDates: []string{"2027-01-10"}, EffectiveAt: now.Add(-time.Hour), ExpiresAt: now.Add(240 * time.Hour)}
}
func automationLimits() AutomationLimits {
	return AutomationLimits{FloorMinor: 9000, CeilingMinor: 11000, MaxDailyChangeBPS: 300, MaxCumulativeChangeBPS: 600, CumulativeDays: 7, ApprovalThresholdBPS: 200, BlackoutDates: []string{"2027-01-10", "2027-01-11"}, MinLeadHours: 0, RequiredCapacityCode: "MANAGEMENT", MaxSourceAgeSeconds: 1800, EligibleModels: []ModelRef{{Key: BaselineForecastModel, Version: 1}}, MinimumCoverageBPS: 8000, MinimumComparableDays: 3, MaxExposureMinor: 100000, HorizonOffsetDays: 0, HorizonDays: 1, HistoryDays: 56, RunIntervalSeconds: 3600, DriftThresholdBPS: 2000, DriftMinMeasurements: 1}
}
func automationInput(mode string, previous, proposed int64) AutomationInput {
	now := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	return AutomationInput{Mode: mode, Currency: "NGN", Model: ModelRef{Key: BaselineForecastModel, Version: 1}, CoverageBPS: 10000, SourceAsOf: now.Add(-10 * time.Minute), Now: now, Days: []AutomationDay{{Day: "2027-01-05", PreviousMinor: previous, ProposedMinor: proposed, PhysicalCapacity: 1, ComparableDays: 8, ArrivalAt: now.Add(96 * time.Hour), DailyBudgetMinor: 300, CumulativeBudgetMinor: 600}}}
}

func TestAutomationLimitsCannotWidenPolicy(t *testing.T) {
	p := automationPolicy("AUTOPILOT_WITH_LIMITS")
	if e := automationLimits().ValidateAgainst(p, "AUTOPILOT_WITH_LIMITS"); e != nil {
		t.Fatalf("valid limits rejected: %v", e)
	}
	for name, mutate := range map[string]func(*AutomationLimits){
		"floor":       func(l *AutomationLimits) { l.FloorMinor = 7000 },
		"ceiling":     func(l *AutomationLimits) { l.CeilingMinor = 13000 },
		"daily":       func(l *AutomationLimits) { l.MaxDailyChangeBPS = 600 },
		"cumulative":  func(l *AutomationLimits) { l.MaxCumulativeChangeBPS = 1100 },
		"threshold":   func(l *AutomationLimits) { l.ApprovalThresholdBPS = 400 },
		"source age":  func(l *AutomationLimits) { l.MaxSourceAgeSeconds = 7200 },
		"blackout":    func(l *AutomationLimits) { l.BlackoutDates = []string{"2027-01-11"} },
		"models":      func(l *AutomationLimits) { l.EligibleModels = nil },
		"coverage":    func(l *AutomationLimits) { l.MinimumCoverageBPS = 7000 },
		"compliance":  func(l *AutomationLimits) { l.ComplianceRestrictions = []string{"DECREASE_ONLY", "INCREASE_ONLY"} },
		"unknown":     func(l *AutomationLimits) { l.ComplianceRestrictions = []string{"ANYTHING"} },
		"horizon":     func(l *AutomationLimits) { l.HorizonOffsetDays, l.HorizonDays = 80, 20 },
		"interval":    func(l *AutomationLimits) { l.RunIntervalSeconds = 10 },
		"no capacity": func(l *AutomationLimits) { l.RequiredCapacityCode = "" },
	} {
		l := automationLimits()
		mutate(&l)
		if l.ValidateAgainst(p, "AUTOPILOT_WITH_LIMITS") == nil {
			t.Fatalf("%s: wider or invalid limits accepted", name)
		}
	}
	if automationLimits().ValidateAgainst(p, "APPROVAL_AUTOMATION") == nil {
		t.Fatal("limits accepted for a mode the policy does not authorize")
	}
	if automationLimits().ValidateAgainst(automationPolicy("RECOMMEND_ONLY"), "RECOMMEND_ONLY") == nil {
		t.Fatal("non-automatic mode accepted")
	}
}

func TestEvaluateAutomationBoundsAndModes(t *testing.T) {
	l := automationLimits()
	ok := EvaluateAutomation(l, automationInput("AUTOPILOT_WITH_LIMITS", 10000, 9850))
	if ok.Outcome != "AUTO_APPLY" || !ok.MayApplyByItself || ok.MaxChangeBPS != 150 || ok.ExposureMinor != 150 {
		t.Fatalf("within limits %+v", ok)
	}
	if d := EvaluateAutomation(l, automationInput("APPROVAL_AUTOMATION", 10000, 9850)); d.Outcome != "REQUIRES_APPROVAL" || d.MayApplyByItself || !d.RequiresHuman {
		t.Fatalf("approval automation must always need a human %+v", d)
	}
	if d := EvaluateAutomation(l, automationInput("AUTOPILOT_WITH_LIMITS", 10000, 9750)); d.Outcome != "REQUIRES_APPROVAL" {
		t.Fatalf("above threshold must escalate %+v", d)
	}
	if d := EvaluateAutomation(l, automationInput("AUTOPILOT_WITH_LIMITS", 10000, 10000)); d.Outcome != "NO_CHANGE" {
		t.Fatalf("no change %+v", d)
	}
	cases := map[string]func(*AutomationInput){
		"BELOW_FLOOR":                   func(in *AutomationInput) { in.Days[0].PreviousMinor, in.Days[0].ProposedMinor = 9100, 8950 },
		"ABOVE_CEILING":                 func(in *AutomationInput) { in.Days[0].PreviousMinor, in.Days[0].ProposedMinor = 10900, 11050 },
		"BLACKOUT_DATE":                 func(in *AutomationInput) { in.Days[0].Day = "2027-01-11" },
		"DAILY_LIMIT":                   func(in *AutomationInput) { in.Days[0].DailyBudgetMinor = 100 },
		"CUMULATIVE_LIMIT":              func(in *AutomationInput) { in.Days[0].CumulativeBudgetMinor = 100 },
		"SOURCE_STALE":                  func(in *AutomationInput) { in.SourceAsOf = in.Now.Add(-time.Hour) },
		"MODEL_INELIGIBLE":              func(in *AutomationInput) { in.Model.Version = 2 },
		"COVERAGE_BELOW_MINIMUM":        func(in *AutomationInput) { in.CoverageBPS = 7000 },
		"COMPARABLE_DAYS_BELOW_MINIMUM": func(in *AutomationInput) { in.Days[0].ComparableDays = 2 },
		"EXPOSURE_LIMIT":                func(in *AutomationInput) { in.Days[0].PhysicalCapacity = 1000 },
	}
	for code, mutate := range cases {
		in := automationInput("AUTOPILOT_WITH_LIMITS", 10000, 9850)
		mutate(&in)
		d := EvaluateAutomation(l, in)
		if d.Outcome != "BLOCKED" || !slices.Contains(d.Violations, code) {
			t.Fatalf("%s not enforced: %+v", code, d)
		}
		// Hard limits block approval automation too; no approval can bypass them.
		in.Mode = "APPROVAL_AUTOMATION"
		if d = EvaluateAutomation(l, in); d.Outcome != "BLOCKED" {
			t.Fatalf("%s bypassed in approval mode: %+v", code, d)
		}
	}
	l.ComplianceRestrictions = []string{"DECREASE_ONLY"}
	if d := EvaluateAutomation(l, automationInput("AUTOPILOT_WITH_LIMITS", 10000, 10100)); d.Outcome != "BLOCKED" || !slices.Contains(d.Violations, "COMPLIANCE_DIRECTION") {
		t.Fatalf("compliance direction %+v", d)
	}
	l = automationLimits()
	l.MinLeadHours = 120
	if d := EvaluateAutomation(l, automationInput("AUTOPILOT_WITH_LIMITS", 10000, 9850)); !slices.Contains(d.Violations, "WITHIN_MINIMUM_LEAD_TIME") {
		t.Fatalf("lead time %+v", d)
	}
	// Determinism: identical inputs give identical decisions.
	a, b := EvaluateAutomation(automationLimits(), automationInput("AUTOPILOT_WITH_LIMITS", 10000, 9850)), EvaluateAutomation(automationLimits(), automationInput("AUTOPILOT_WITH_LIMITS", 10000, 9850))
	if !reflect.DeepEqual(a, b) {
		t.Fatal("evaluation is not deterministic")
	}
}

func TestDetectDriftDefinedConditions(t *testing.T) {
	if d := DetectDrift(nil, 1000, 1); d.Status != "INSUFFICIENT_DATA" || d.MeanAbsErrBPS != nil || d.Drift {
		t.Fatalf("empty drift %+v", d)
	}
	points := []OutcomePoint{{Day: "2027-01-01", PhysicalCapacity: 1, PredictedMilliNights: 500, ActualConfirmedNights: 0}, {Day: "2027-01-02", PhysicalCapacity: 1, PredictedMilliNights: 500, ActualConfirmedNights: 1}}
	d := DetectDrift(points, 4000, 2)
	if d.Status != "EVALUATED" || *d.MeanAbsErrBPS != 5000 || *d.MeanBiasBPS != 0 || !d.Drift {
		t.Fatalf("drift %+v", d)
	}
	if d = DetectDrift(points, 6000, 2); d.Drift {
		t.Fatal("drift below threshold reported")
	}
	if d = DetectDrift(points, 1000, 3); d.Drift || d.Status != "INSUFFICIENT_DATA" {
		t.Fatal("drift fired before minimum measurements")
	}
}

func analyticsFixture() AnalyticsInput {
	property, branch := ids.New(), ids.New()
	asOf := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	days := []DemandDay{}
	start := time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC)
	for d := start; d.Before(time.Date(2027, 3, 8, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
		booked := int64(0)
		if d.Weekday() == time.Friday || d.Weekday() == time.Saturday {
			booked = 1
		}
		amount := booked * 10000
		days = append(days, DemandDay{Day: d.Format(time.DateOnly), Capacity: 2, ConfirmedNights: booked, CalendarVersion: 1, AccommodationMinor: &amount})
	}
	confirmed := asOf.Add(-90 * 24 * time.Hour)
	bookings := []BookingFact{}
	for i, day := range []string{"2027-01-08", "2027-01-15", "2027-01-22", "2027-01-29", "2027-02-05", "2027-02-12"} {
		amount := int64(10000)
		booked, _ := time.Parse(time.DateOnly, day)
		booked = booked.AddDate(0, 0, -(i%3)*7-1)
		b := BookingFact{RoomID: ids.New(), PropertyID: property, ProductID: ids.New(), Channel: []string{"", "BOOKING_COM"}[i%2], Source: "DIRECT", State: "COMPLETED", BookedAt: booked, ConfirmedAt: &confirmed, DateFrom: day, DateTo: booked.AddDate(0, 0, (i%3)*7+2).Format(time.DateOnly), Quantity: 1, AccommodationMinor: &amount, Currency: "NGN"}
		if i == 5 {
			b.State, b.CancelledAt = "CANCELLED", &confirmed
		}
		bookings = append(bookings, b)
	}
	commission := int64(1500)
	return AnalyticsInput{ScopeKind: "PROPERTY", ScopeID: property, AsOf: asOf, Today: "2027-03-01", HistoryFrom: "2027-01-04", DateFrom: "2027-03-01", DateTo: "2027-03-08", Properties: []PropertyDemand{{PropertyID: property, BranchID: branch, Currency: "NGN", Timezone: "Africa/Lagos", Days: days}}, Bookings: bookings, MarketInputs: []MarketInput{{Kind: "CHANNEL_COMMISSION", Channel: "BOOKING_COM", DateFrom: "2027-01-01", DateTo: "2027-12-31", ValueBPS: &commission, Label: "contract", SourceReference: "signed-channel-contract"}}}
}

func TestAnalyzeDemandIsReproducibleAndSourceBacked(t *testing.T) {
	in := analyticsFixture()
	a, e := AnalyzeDemand(in)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := AnalyzeDemand(in)
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	if string(ra) != string(rb) {
		t.Fatal("analytics is not reproducible for identical inputs")
	}
	if a.Model != AnalyticsModel || a.IsCalibratedProbability || a.Status != "BASELINE_READY" || len(a.Forecast) != 7 {
		t.Fatalf("analytics %+v", a)
	}
	if a.CancellationRateBPS == nil || a.LeadTime.MedianDays == nil || a.AverageLOSMilliNights == nil || a.Portfolio.History.OccupancyBPS == nil {
		t.Fatalf("source-backed features missing %+v", a)
	}
	var booking *SegmentAnalysis
	for i := range a.Channels {
		if a.Channels[i].Key == "BOOKING_COM" {
			booking = &a.Channels[i]
		}
	}
	if booking == nil || booking.CommissionBPS == nil || booking.NetMinor == nil {
		t.Fatalf("governed commission not applied %+v", a.Channels)
	}
	if a.ComparablePeriod.Status != "UNAVAILABLE" || a.ComparablePeriod.OccupancyBPS != nil || !slices.Contains(a.MissingInputs, "comparable_period_history") {
		t.Fatalf("comparable period was fabricated %+v", a.ComparablePeriod)
	}
	if !slices.Contains(a.MissingInputs, "verified_market_holiday_event_inputs") {
		t.Fatal("missing market inputs not reported")
	}
	for _, d := range a.Forecast {
		if d.CancellationAdjustedMilli == nil || *d.CancellationAdjustedMilli > d.PhysicalCapacity*1000 {
			t.Fatalf("forecast day %+v", d)
		}
	}
	// Without bookings the dependent outputs are null, never zero.
	in.Bookings = nil
	empty, e := AnalyzeDemand(in)
	if e != nil || empty.CancellationRateBPS != nil || empty.LeadTime.MedianDays != nil || empty.Status != "INSUFFICIENT_DATA" {
		t.Fatalf("absent sources fabricated %+v %v", empty, e)
	}
	// A governed demand index is the only external multiplier.
	index := int64(2000)
	in = analyticsFixture()
	in.MarketInputs = append(in.MarketInputs, MarketInput{Kind: "MARKET_DEMAND_INDEX", DateFrom: "2027-03-05", DateTo: "2027-03-06", ValueBPS: &index, Label: "festival", SourceReference: "licensed-index"})
	indexed, _ := AnalyzeDemand(in)
	if indexed.Forecast[4].MarketIndexBPS == nil || *indexed.Forecast[4].MarketIndexBPS != 2000 || indexed.Forecast[3].MarketIndexBPS != nil {
		t.Fatalf("market index %+v", indexed.Forecast)
	}
}

func TestSimulateScenariosKeepsUnknownCostsNull(t *testing.T) {
	cost := int64(2000)
	change := int64(-500)
	in := ScenarioInput{Currency: "NGN", Days: []ScenarioDay{{Day: "2027-03-01", PhysicalCapacity: 2, ProjectedMilliNights: 1000, CurrentAmountMinor: 10000}}, BaselineChannelMix: []ChannelShare{{Channel: "", ShareBPS: 5000}, {Channel: "BOOKING_COM", ShareBPS: 5000}}, CommissionBPS: map[string]int64{"BOOKING_COM": 1500}, Alternatives: []ScenarioAlternative{{Name: "current", ChangeBPS: new(int64)}, {Name: "discount", ChangeBPS: &change, PromotionDiscountBPS: 1000, PromotionShareBPS: 5000}}}
	out, e := SimulateScenarios(in)
	if e != nil {
		t.Fatal(e)
	}
	current := out.Results[0]
	if current.GrossMinor != 10000 || *current.OccupancyBPS != 5000 || *current.ADRMinor != 10000 || *current.RevPARMinor != 5000 {
		t.Fatalf("current %+v", current)
	}
	if current.ChannelCommissionMinor != nil || current.NetRevenueMinor != nil || current.ContributionMinor != nil {
		t.Fatalf("unknown direct commission fabricated %+v", current)
	}
	if out.Results[1].GrossMinor != 9500-475 || out.Results[1].PromotionCostMinor != 475 {
		t.Fatalf("promotion %+v", out.Results[1])
	}
	in.CommissionBPS[""] = 0
	in.OperatingCostPerNightMinor = &cost
	out, _ = SimulateScenarios(in)
	if r := out.Results[0]; r.ChannelCommissionMinor == nil || *r.ChannelCommissionMinor != 750 || *r.NetRevenueMinor != 9250 || *r.OperationalCostMinor != 2000 || *r.ContributionMinor != 7250 {
		t.Fatalf("known inputs %+v", r)
	}
	in.Alternatives = []ScenarioAlternative{{Name: "both", ChangeBPS: &change, AmountMinor: &cost}}
	if _, e = SimulateScenarios(in); e == nil {
		t.Fatal("ambiguous alternative accepted")
	}
}

func TestRecommendAdvancedKindsUseCanonicalOrAdvisory(t *testing.T) {
	policy := automationPolicy("RECOMMEND_ONLY")
	policy.BlackoutDates = nil
	sold := int64(900)
	low := int64(200)
	forecast := func(milli int64) DemandForecast {
		return DemandForecast{Model: BaselineForecastModel, AlgorithmVersion: 1, Status: "BASELINE_READY", ConfidenceMethod: "X", Days: []ForecastDay{{Day: "2027-03-02", PhysicalCapacity: 1, CurrentConfirmedNights: 0, ComparableDays: 8, ProjectedSoldMilliNights: &milli, Status: "BASELINE_READY"}}}
	}
	current := RateDefinition{Currency: "NGN", AmountMinor: 10000, IncludedAdults: 2, MinGuests: 1, MaxGuests: 2, MinNights: 1, MaxNights: 30, CancellationReference: "C", PrepaymentReference: "P", PolicyCode: "X", PolicyVersion: 1, FloorMinor: 1000, CeilingMinor: 20000, MaxChangeBPS: 1000}
	analytics, e := AnalyzeDemand(analyticsFixture())
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	ctx := func(kind string, milli int64) AdvancedContext {
		return AdvancedContext{Kind: kind, Policy: policy, Forecast: forecast(milli), Analytics: &analytics, Current: current, Today: "2027-03-01", Now: now, Parameters: AdvancedParameters{Channel: "BOOKING_COM"}}
	}
	expect := map[string][2]any{
		"RATE_RANGE":         {sold, "CANONICAL_RATE_VERSION"},
		"MIN_LOS":            {sold, "CANONICAL_RESTRICTION"},
		"MAX_LOS":            {sold, "CANONICAL_RESTRICTION"},
		"BOOKING_WINDOW":     {sold, "CANONICAL_RESTRICTION"},
		"LAST_MINUTE":        {low, "CANONICAL_PROMOTION"},
		"LONG_STAY":          {low, "CANONICAL_PROMOTION"},
		"CHANNEL_PRICING":    {low, "CANONICAL_PROMOTION"},
		"CHANNEL_ALLOCATION": {low, "ADVISORY"},
		"OVERBOOKING":        {sold, "ADVISORY"},
		"DERIVED_RATE":       {sold, "ADVISORY"},
	}
	for kind, want := range expect {
		p, e := RecommendAdvanced(ctx(kind, want[0].(int64)))
		if e != nil || p.Application != want[1] {
			t.Fatalf("%s: %+v %v", kind, p, e)
		}
		if p.Application == "ADVISORY" && p.AdvisoryReason == "" || p.Explanation == "" || p.ModelProvenance.Key != BaselineForecastModel {
			t.Fatalf("%s lacks explanation/provenance %+v", kind, p)
		}
		if p.Promotion != nil && (p.Promotion.Validate() != nil || p.Promotion.Discount.BasisPoints >= 0) {
			t.Fatalf("%s promotion invalid %+v", kind, p.Promotion)
		}
	}
	if _, e = RecommendAdvanced(ctx("MIN_LOS", low)); e != ErrNoRecommendation {
		t.Fatalf("unsupported condition produced a recommendation %v", e)
	}
	derived := current
	parent := ids.New()
	derived.ParentVersionID = &parent
	derived.Adjustments = []Adjustment{{Kind: "PERCENT", BasisPoints: -1000}}
	c := ctx("DERIVED_RATE", sold)
	c.Current = derived
	if p, e := RecommendAdvanced(c); e != nil || p.Application != "CANONICAL_RATE_VERSION" || p.Rate.Adjustments[0].BasisPoints == -1000 {
		t.Fatalf("derived %+v %v", p, e)
	}
	noAnalytics := ctx("BOOKING_WINDOW", sold)
	noAnalytics.Analytics = nil
	if _, e = RecommendAdvanced(noAnalytics); e != ErrNoRecommendation {
		t.Fatal("lead-time recommendation fabricated without analytics")
	}
}
