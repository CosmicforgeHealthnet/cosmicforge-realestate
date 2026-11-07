package revenue

import (
	"slices"
	"time"

	"github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/money"
)

// ModelRef names an exact registered forecast model version.
type ModelRef struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
}

// AutomationLimits are the independently approved execution bounds of one
// activation. They can only narrow the governing RMS policy, never widen it.
type AutomationLimits struct {
	FloorMinor             int64      `json:"floor_minor"`
	CeilingMinor           int64      `json:"ceiling_minor"`
	MaxDailyChangeBPS      int64      `json:"max_daily_change_bps"`
	MaxCumulativeChangeBPS int64      `json:"max_cumulative_change_bps"`
	CumulativeDays         int        `json:"cumulative_days"`
	ApprovalThresholdBPS   int64      `json:"approval_threshold_bps"`
	BlackoutDates          []string   `json:"blackout_dates"`
	ComplianceRestrictions []string   `json:"compliance_restrictions"`
	MinLeadHours           int        `json:"min_lead_hours"`
	RequiredCapacityCode   string     `json:"required_capacity_code"`
	MaxSourceAgeSeconds    int        `json:"max_source_age_seconds"`
	EligibleModels         []ModelRef `json:"eligible_models"`
	MinimumCoverageBPS     int64      `json:"minimum_coverage_bps"`
	MinimumComparableDays  int        `json:"minimum_comparable_days"`
	MaxExposureMinor       int64      `json:"max_exposure_minor"`
	HorizonOffsetDays      int        `json:"horizon_offset_days"`
	HorizonDays            int        `json:"horizon_days"`
	HistoryDays            int        `json:"history_days"`
	RunIntervalSeconds     int        `json:"run_interval_seconds"`
	ApplyDelaySeconds      int        `json:"apply_delay_seconds"`
	OutcomeDelaySeconds    int        `json:"outcome_delay_seconds"`
	DriftThresholdBPS      int64      `json:"drift_threshold_bps"`
	DriftMinMeasurements   int        `json:"drift_min_measurements"`
	AutoPauseOnDrift       bool       `json:"auto_pause_on_drift"`
}

var complianceRestrictions = []string{"DECREASE_ONLY", "INCREASE_ONLY"}

// ValidateAgainst rejects limits that are malformed or wider than the exact
// current policy version they are bound to.
func (l AutomationLimits) ValidateAgainst(p PricingPolicy, mode string) error {
	if p.Validate() != nil || p.Mode != mode || mode != "APPROVAL_AUTOMATION" && mode != "AUTOPILOT_WITH_LIMITS" {
		return ErrPricing
	}
	if l.FloorMinor < p.FloorMinor || l.CeilingMinor > p.CeilingMinor || l.CeilingMinor < l.FloorMinor || l.MaxDailyChangeBPS < 0 || l.MaxDailyChangeBPS > p.MaxDailyChangeBPS || l.MaxCumulativeChangeBPS < l.MaxDailyChangeBPS || l.MaxCumulativeChangeBPS > p.MaxCumulativeChangeBPS || l.CumulativeDays < 1 || l.CumulativeDays > p.CumulativeDays || l.ApprovalThresholdBPS < 0 || l.ApprovalThresholdBPS > l.MaxDailyChangeBPS || l.MinLeadHours < 0 || l.MinLeadHours > 720 || l.MaxSourceAgeSeconds < 60 || l.MaxSourceAgeSeconds > p.MaxSourceAgeSeconds || l.MinimumCoverageBPS < p.MinimumHistoryCoverageBPS || l.MinimumCoverageBPS > 10000 || l.MinimumComparableDays < 3 || l.MinimumComparableDays > 52 || l.MaxExposureMinor < 0 || l.MaxExposureMinor > 1000000000000000 || l.HorizonOffsetDays < 0 || l.HorizonOffsetDays > 89 || l.HorizonDays < 1 || l.HorizonOffsetDays+l.HorizonDays > 90 || l.HistoryDays < 21 || l.HistoryDays > 366 || l.RunIntervalSeconds < 300 || l.RunIntervalSeconds > 86400 || l.ApplyDelaySeconds < 0 || l.ApplyDelaySeconds > 86400 || l.OutcomeDelaySeconds < 0 || l.OutcomeDelaySeconds > 90*86400 || l.DriftThresholdBPS < 1 || l.DriftThresholdBPS > 10000 || l.DriftMinMeasurements < 1 || l.DriftMinMeasurements > 100 || len(l.BlackoutDates) > 366 || len(l.EligibleModels) < 1 || len(l.EligibleModels) > 8 || len(l.RequiredCapacityCode) < 1 || len(l.RequiredCapacityCode) > 80 {
		return ErrPricing
	}
	for _, day := range l.BlackoutDates {
		if _, e := time.Parse(time.DateOnly, day); e != nil {
			return ErrPricing
		}
	}
	// A policy blackout is always a limit blackout too.
	for _, day := range p.BlackoutDates {
		if !slices.Contains(l.BlackoutDates, day) {
			return ErrPricing
		}
	}
	seen := map[string]bool{}
	for _, r := range l.ComplianceRestrictions {
		if !slices.Contains(complianceRestrictions, r) || seen[r] {
			return ErrPricing
		}
		seen[r] = true
	}
	if seen["DECREASE_ONLY"] && seen["INCREASE_ONLY"] {
		return ErrPricing
	}
	models := map[ModelRef]bool{}
	for _, m := range l.EligibleModels {
		if m.Key == "" || len(m.Key) > 100 || m.Version < 1 || models[m] {
			return ErrPricing
		}
		models[m] = true
	}
	return nil
}

// AutomationDay is one proposed canonical night change with its retained facts.
type AutomationDay struct {
	Day              string    `json:"day"`
	PreviousMinor    int64     `json:"previous_minor"`
	ProposedMinor    int64     `json:"proposed_minor"`
	PhysicalCapacity int64     `json:"physical_capacity"`
	ComparableDays   int       `json:"comparable_days"`
	ArrivalAt        time.Time `json:"arrival_at"`
	// Remaining movement budgets in minor units computed from canonical history.
	DailyBudgetMinor      int64 `json:"daily_budget_minor"`
	CumulativeBudgetMinor int64 `json:"cumulative_budget_minor"`
}

// AutomationInput carries everything the bounded decision depends on.
type AutomationInput struct {
	Mode        string
	Currency    string
	Model       ModelRef
	CoverageBPS int64
	SourceAsOf  time.Time
	Now         time.Time
	Days        []AutomationDay
}

// AutomationDecision is the auditable result of a bounded evaluation.
type AutomationDecision struct {
	Outcome          string   `json:"outcome"`
	Code             string   `json:"code"`
	Violations       []string `json:"violations"`
	MaxChangeBPS     int64    `json:"max_change_bps"`
	ExposureMinor    int64    `json:"exposure_minor"`
	ChangedNights    int      `json:"changed_nights"`
	EvaluatedAt      string   `json:"evaluated_at"`
	SourceAgeSecs    int64    `json:"source_age_seconds"`
	ThresholdBPS     int64    `json:"threshold_bps"`
	RequiresHuman    bool     `json:"requires_human_approval"`
	MayApplyByItself bool     `json:"may_apply_without_per_change_approval"`
}

// EvaluateAutomation is deterministic. Any hard violation blocks execution in
// every mode. APPROVAL_AUTOMATION always requires an independent human
// approval; AUTOPILOT_WITH_LIMITS applies only within its threshold.
func EvaluateAutomation(l AutomationLimits, in AutomationInput) AutomationDecision {
	out := AutomationDecision{Violations: []string{}, EvaluatedAt: in.Now.UTC().Format(time.RFC3339), ThresholdBPS: l.ApprovalThresholdBPS}
	add := func(code string) {
		if !slices.Contains(out.Violations, code) {
			out.Violations = append(out.Violations, code)
		}
	}
	if _, e := money.ParseCurrency(in.Currency); e != nil {
		add("CURRENCY_INVALID")
	}
	if !slices.Contains(l.EligibleModels, in.Model) {
		add("MODEL_INELIGIBLE")
	}
	if in.CoverageBPS < l.MinimumCoverageBPS {
		add("COVERAGE_BELOW_MINIMUM")
	}
	if in.SourceAsOf.IsZero() || in.SourceAsOf.After(in.Now) {
		add("SOURCE_TIME_INVALID")
	} else {
		out.SourceAgeSecs = int64(in.Now.Sub(in.SourceAsOf) / time.Second)
		if out.SourceAgeSecs > int64(l.MaxSourceAgeSeconds) {
			add("SOURCE_STALE")
		}
	}
	if len(in.Days) == 0 {
		add("NO_SEGMENTS")
	}
	for _, d := range in.Days {
		if d.ProposedMinor == d.PreviousMinor {
			continue
		}
		out.ChangedNights++
		if slices.Contains(l.BlackoutDates, d.Day) {
			add("BLACKOUT_DATE")
		}
		if d.ProposedMinor < l.FloorMinor {
			add("BELOW_FLOOR")
		}
		if d.ProposedMinor > l.CeilingMinor {
			add("ABOVE_CEILING")
		}
		if d.ComparableDays < l.MinimumComparableDays {
			add("COMPARABLE_DAYS_BELOW_MINIMUM")
		}
		if l.MinLeadHours > 0 && (d.ArrivalAt.IsZero() || d.ArrivalAt.Sub(in.Now) < time.Duration(l.MinLeadHours)*time.Hour) {
			add("WITHIN_MINIMUM_LEAD_TIME")
		}
		delta := d.ProposedMinor - d.PreviousMinor
		if delta > 0 && slices.Contains(l.ComplianceRestrictions, "DECREASE_ONLY") || delta < 0 && slices.Contains(l.ComplianceRestrictions, "INCREASE_ONLY") {
			add("COMPLIANCE_DIRECTION")
		}
		abs := delta
		if abs < 0 {
			abs = -abs
		}
		if d.PreviousMinor <= 0 {
			add("PREVIOUS_RATE_UNKNOWN")
			continue
		}
		bps, e := Ratio(abs, 10000, d.PreviousMinor)
		if e != nil {
			add("ARITHMETIC_OVERFLOW")
			continue
		}
		out.MaxChangeBPS = max(out.MaxChangeBPS, bps)
		if bps > l.MaxDailyChangeBPS || abs > d.DailyBudgetMinor {
			add("DAILY_LIMIT")
		}
		if abs > d.CumulativeBudgetMinor {
			add("CUMULATIVE_LIMIT")
		}
		if d.PhysicalCapacity < 0 {
			add("CAPACITY_UNKNOWN")
			continue
		}
		exposure, e := Ratio(abs, d.PhysicalCapacity, 1)
		if e != nil {
			add("ARITHMETIC_OVERFLOW")
			continue
		}
		if next, e := checkedAdd(out.ExposureMinor, exposure); e != nil {
			add("ARITHMETIC_OVERFLOW")
		} else {
			out.ExposureMinor = next
		}
	}
	if out.ExposureMinor > l.MaxExposureMinor {
		add("EXPOSURE_LIMIT")
	}
	slices.Sort(out.Violations)
	switch {
	case len(out.Violations) > 0:
		out.Outcome, out.Code = "BLOCKED", out.Violations[0]
	case out.ChangedNights == 0:
		out.Outcome, out.Code = "NO_CHANGE", "NO_CHANGE"
	case in.Mode == "AUTOPILOT_WITH_LIMITS" && out.MaxChangeBPS <= l.ApprovalThresholdBPS:
		out.Outcome, out.Code, out.MayApplyByItself = "AUTO_APPLY", "WITHIN_AUTOPILOT_LIMITS", true
	default:
		out.Outcome, out.Code, out.RequiresHuman = "REQUIRES_APPROVAL", "INDEPENDENT_APPROVAL_REQUIRED", true
	}
	return out
}

// OutcomePoint is one measured night: forecast versus later observed facts.
type OutcomePoint struct {
	Day                   string `json:"day"`
	PhysicalCapacity      int64  `json:"physical_capacity"`
	PredictedMilliNights  int64  `json:"predicted_milli_nights"`
	ActualConfirmedNights int64  `json:"actual_confirmed_nights"`
}

// DriftResult reports mean absolute and signed forecast error in basis points
// of physical capacity. Unknown is reported, never zero-filled.
type DriftResult struct {
	Measurements  int    `json:"measurements"`
	MeanAbsErrBPS *int64 `json:"mean_absolute_error_bps"`
	MeanBiasBPS   *int64 `json:"mean_bias_bps"`
	ThresholdBPS  int64  `json:"threshold_bps"`
	Drift         bool   `json:"drift"`
	Status        string `json:"status"`
	Definition    string `json:"definition"`
}

// DetectDrift fires when at least minimum measured nights exist and their mean
// absolute error exceeds the configured threshold.
func DetectDrift(points []OutcomePoint, thresholdBPS int64, minimum int) DriftResult {
	out := DriftResult{ThresholdBPS: thresholdBPS, Status: "INSUFFICIENT_DATA", Definition: "Mean |actual confirmed nights - predicted nights| / physical capacity over measured nights, in basis points. Bias keeps the sign (positive = under-forecast)."}
	var absSum, biasSum int64
	for _, p := range points {
		if p.PhysicalCapacity <= 0 {
			continue
		}
		diff := p.ActualConfirmedNights*1000 - p.PredictedMilliNights
		abs := diff
		if abs < 0 {
			abs = -abs
		}
		a, e := Ratio(abs, 10000, p.PhysicalCapacity*1000)
		if e != nil {
			continue
		}
		b, e := Ratio(diff, 10000, p.PhysicalCapacity*1000)
		if e != nil {
			continue
		}
		absSum += a
		biasSum += b
		out.Measurements++
	}
	if out.Measurements == 0 {
		return out
	}
	mean, _ := Ratio(absSum, 1, int64(out.Measurements))
	bias, _ := Ratio(biasSum, 1, int64(out.Measurements))
	out.MeanAbsErrBPS, out.MeanBiasBPS = &mean, &bias
	if out.Measurements >= minimum {
		out.Status = "EVALUATED"
		out.Drift = mean > thresholdBPS
	}
	return out
}
